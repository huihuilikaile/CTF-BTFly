package modelgateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ModelConfig 是单个上游模型的私有配置；API Key 不会序列化到前端。
type ModelConfig struct {
	Name               string
	UpstreamBaseURL    string
	UpstreamAPIKey     string
	ModelID            string
	IncludeStreamUsage bool
	SupportsImages     bool
}

// ModelStatus 是可安全暴露给桌面端的模型摘要。
type ModelStatus struct {
	Name               string      `json:"name"`
	ModelID            string      `json:"modelId"`
	BaseURL            string      `json:"baseUrl"`
	Configured         bool        `json:"configured"`
	HasAPIKey          bool        `json:"hasApiKey"`
	SupportsImages     bool        `json:"supportsImages"`
	IncludeStreamUsage bool        `json:"includeStreamUsage"`
	Default            bool        `json:"default"`
	Probe              ProbeStatus `json:"probe"`
}

// PoolConfig 组合多个模型配置和默认选择。
type PoolConfig struct {
	Models       []ModelConfig
	DefaultModel string
}

// Pool 为每个配置维护独立 Gateway；短期 Token 只会被所属模型接收。
type Pool struct {
	models      map[string]*Gateway
	configs     map[string]ModelConfig
	order       []string
	defaultName string
}

// NewPool 创建模型池。单模型 Gateway 仍保留以兼容已有测试与内部职责。
func NewPool(config PoolConfig) (*Pool, error) {
	pool := &Pool{models: make(map[string]*Gateway), configs: make(map[string]ModelConfig)}
	for _, raw := range config.Models {
		name := strings.ToLower(strings.TrimSpace(raw.Name))
		if name == "" {
			return nil, fmt.Errorf("model profile name is required")
		}
		if _, exists := pool.models[name]; exists {
			return nil, fmt.Errorf("duplicate model profile %q", name)
		}
		raw.Name = name
		gateway, err := New(Config{
			UpstreamBaseURL: raw.UpstreamBaseURL, UpstreamAPIKey: raw.UpstreamAPIKey, ModelID: raw.ModelID,
			IncludeStreamUsage: raw.IncludeStreamUsage, SupportsImages: raw.SupportsImages,
		})
		if err != nil {
			return nil, fmt.Errorf("configure model %q: %w", name, err)
		}
		pool.models[name] = gateway
		pool.configs[name] = raw
		pool.order = append(pool.order, name)
	}
	requested := strings.ToLower(strings.TrimSpace(config.DefaultModel))
	if _, exists := pool.models[requested]; exists {
		pool.defaultName = requested
	} else if len(pool.order) > 0 {
		pool.defaultName = pool.order[0]
	}
	return pool, nil
}

// DefaultProfile 返回 daemon 在未指定配置时使用的名称。
func (p *Pool) DefaultProfile() string { return p.defaultName }

// Profiles 返回按 .env 声明顺序排列的安全状态摘要。
func (p *Pool) Profiles() []ModelStatus {
	result := make([]ModelStatus, 0, len(p.order))
	for _, name := range p.order {
		status := p.status(name)
		status.Default = name == p.defaultName
		result = append(result, status)
	}
	return result
}

// Profile 返回单个模型公开状态；空名称代表默认模型。
func (p *Pool) Profile(name string) (ModelStatus, bool) {
	name = p.resolve(name)
	if _, ok := p.models[name]; !ok {
		return ModelStatus{}, false
	}
	return p.status(name), true
}

func (p *Pool) status(name string) ModelStatus {
	config := p.configs[name]
	gateway := p.models[name]
	status := ModelStatus{Name: name, ModelID: config.ModelID, BaseURL: displayBaseURL(config.UpstreamBaseURL), Configured: strings.TrimSpace(config.UpstreamBaseURL) != "" && strings.TrimSpace(config.UpstreamAPIKey) != "" && strings.TrimSpace(config.ModelID) != "", HasAPIKey: strings.TrimSpace(config.UpstreamAPIKey) != "", SupportsImages: config.SupportsImages, IncludeStreamUsage: config.IncludeStreamUsage}
	if gateway != nil {
		status.Configured = gateway.Configured()
		status.Probe = gateway.ProbeStatus()
	}
	return status
}

func (p *Pool) resolve(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return p.defaultName
	}
	return name
}

func (p *Pool) gateway(name string) (*Gateway, error) {
	name = p.resolve(name)
	gateway, ok := p.models[name]
	if !ok {
		return nil, fmt.Errorf("unknown model profile %q", name)
	}
	if !gateway.Configured() {
		return nil, fmt.Errorf("model profile %q is not configured", name)
	}
	return gateway, nil
}

func (p *Pool) Configured() bool {
	gateway, err := p.gateway("")
	return err == nil && gateway.Configured()
}

func (p *Pool) ModelID(names ...string) string {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	status, ok := p.Profile(name)
	if !ok {
		return ""
	}
	return status.ModelID
}

func (p *Pool) SupportsImages(names ...string) bool {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	status, ok := p.Profile(name)
	return ok && status.SupportsImages
}

func (p *Pool) Probe(ctx context.Context, names ...string) error {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	gateway, err := p.gateway(name)
	if err != nil {
		return err
	}
	return gateway.Probe(ctx)
}

func (p *Pool) ProbeStatus(names ...string) ProbeStatus {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	gateway, err := p.gateway(name)
	if err != nil {
		return ProbeStatus{Configured: false, Error: err.Error()}
	}
	return gateway.ProbeStatus()
}

func (p *Pool) Issue(taskID string, names ...string) (string, error) {
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	gateway, err := p.gateway(name)
	if err != nil {
		return "", err
	}
	return gateway.Issue(taskID)
}

func (p *Pool) Revoke(token string) {
	for _, gateway := range p.models {
		gateway.Revoke(token)
	}
}

func (p *Pool) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if gateway := p.gatewayForToken(token); gateway != nil {
		gateway.ServeHTTP(writer, request)
		return
	}
	http.Error(writer, "invalid task model token", http.StatusUnauthorized)
}

func (p *Pool) gatewayForToken(token string) *Gateway {
	for _, gateway := range p.models {
		if _, valid := gateway.taskForToken(token); valid {
			return gateway
		}
	}
	return nil
}

func (p *Pool) hasActiveTokens() bool {
	for _, gateway := range p.models {
		if gateway.hasActiveTokens() {
			return true
		}
	}
	return false
}

func (p *Pool) SetUsageRecorder(recorder UsageRecorder) {
	for _, gateway := range p.models {
		gateway.SetUsageRecorder(recorder)
	}
}

func (p *Pool) SetErrorReporter(reporter ErrorReporter) {
	for _, gateway := range p.models {
		gateway.SetErrorReporter(reporter)
	}
}
