package modelgateway

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// LivePool allows the daemon to atomically load a newly saved model
// configuration. Pools that still own task-scoped tokens are retained so a
// configuration refresh cannot interrupt an already running task.
type LivePool struct {
	mu       sync.RWMutex
	current  *Pool
	retired  []*Pool
	recorder UsageRecorder
	errors   ErrorReporter
}

var _ Manager = (*LivePool)(nil)

// NewLivePool creates the long-lived model manager used by the daemon.
func NewLivePool(config PoolConfig) (*LivePool, error) {
	pool, err := NewPool(config)
	if err != nil {
		return nil, err
	}
	return &LivePool{current: pool}, nil
}

// Reload builds and validates the replacement before publishing it. The
// previous pool remains reachable only while it has issued task tokens.
func (p *LivePool) Reload(config PoolConfig) error {
	next, err := NewPool(config)
	if err != nil {
		return err
	}

	p.mu.Lock()
	if p.recorder != nil {
		next.SetUsageRecorder(p.recorder)
	}
	if p.errors != nil {
		next.SetErrorReporter(p.errors)
	}
	retired := p.retired[:0]
	for _, pool := range p.retired {
		if pool.hasActiveTokens() {
			retired = append(retired, pool)
		}
	}
	if p.current != nil && p.current.hasActiveTokens() {
		retired = append(retired, p.current)
	}
	p.current = next
	p.retired = retired
	p.mu.Unlock()
	return nil
}

func (p *LivePool) currentPool() *Pool {
	p.mu.RLock()
	current := p.current
	p.mu.RUnlock()
	return current
}

func (p *LivePool) Configured() bool {
	current := p.currentPool()
	return current != nil && current.Configured()
}

func (p *LivePool) DefaultProfile() string {
	current := p.currentPool()
	if current == nil {
		return ""
	}
	return current.DefaultProfile()
}

func (p *LivePool) Profiles() []ModelStatus {
	current := p.currentPool()
	if current == nil {
		return nil
	}
	return current.Profiles()
}

func (p *LivePool) Profile(name string) (ModelStatus, bool) {
	current := p.currentPool()
	if current == nil {
		return ModelStatus{}, false
	}
	return current.Profile(name)
}

func (p *LivePool) ModelID(names ...string) string {
	current := p.currentPool()
	if current == nil {
		return ""
	}
	return current.ModelID(names...)
}

func (p *LivePool) SupportsImages(names ...string) bool {
	current := p.currentPool()
	return current != nil && current.SupportsImages(names...)
}

func (p *LivePool) Probe(ctx context.Context, names ...string) error {
	current := p.currentPool()
	if current == nil {
		return nil
	}
	return current.Probe(ctx, names...)
}

func (p *LivePool) ProbeStatus(names ...string) ProbeStatus {
	current := p.currentPool()
	if current == nil {
		return ProbeStatus{}
	}
	return current.ProbeStatus(names...)
}

// Issue holds the read lock through token creation. A concurrent Reload will
// therefore see that token and retain its pool.
func (p *LivePool) Issue(taskID string, names ...string) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.current.Issue(taskID, names...)
}

func (p *LivePool) Revoke(token string) {
	p.mu.RLock()
	pools := append([]*Pool{p.current}, p.retired...)
	for _, pool := range pools {
		if pool != nil {
			pool.Revoke(token)
		}
	}
	p.mu.RUnlock()

	p.mu.Lock()
	retired := p.retired[:0]
	for _, pool := range p.retired {
		if pool.hasActiveTokens() {
			retired = append(retired, pool)
		}
	}
	p.retired = retired
	p.mu.Unlock()
}

func (p *LivePool) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	p.mu.RLock()
	pools := append([]*Pool{p.current}, p.retired...)
	p.mu.RUnlock()
	for _, pool := range pools {
		if pool == nil {
			continue
		}
		if gateway := pool.gatewayForToken(token); gateway != nil {
			gateway.ServeHTTP(writer, request)
			return
		}
	}
	http.Error(writer, "invalid task model token", http.StatusUnauthorized)
}

func (p *LivePool) SetUsageRecorder(recorder UsageRecorder) {
	p.mu.Lock()
	p.recorder = recorder
	if p.current != nil {
		p.current.SetUsageRecorder(recorder)
	}
	for _, pool := range p.retired {
		pool.SetUsageRecorder(recorder)
	}
	p.mu.Unlock()
}

func (p *LivePool) SetErrorReporter(reporter ErrorReporter) {
	p.mu.Lock()
	p.errors = reporter
	if p.current != nil {
		p.current.SetErrorReporter(reporter)
	}
	for _, pool := range p.retired {
		pool.SetErrorReporter(reporter)
	}
	p.mu.Unlock()
}
