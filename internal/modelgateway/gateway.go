package modelgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

const maxJSONUsageCapture = 8 << 20
const maxJSONRequestRewrite = 4 << 20

type Config struct {
	UpstreamBaseURL string
	UpstreamAPIKey  string
	ModelID         string
	// IncludeStreamUsage asks OpenAI-compatible chat-completions upstreams to
	// put a final usage block into an SSE response. Set false for an upstream
	// that rejects stream_options.
	IncludeStreamUsage bool
}

// UsageRecorder is implemented by the SQLite store. Keeping the interface
// here prevents the reverse proxy from depending directly on storage.
type UsageRecorder interface {
	RecordModelUsage(context.Context, platform.ModelUsage) error
}

type Gateway struct {
	config   Config
	proxy    *httputil.ReverseProxy
	mu       sync.RWMutex
	tokens   map[string]string
	recorder UsageRecorder
}

type requestUsageMeta struct {
	taskID  string
	model   string
	started time.Time
	once    sync.Once
}

type requestUsageMetaKey struct{}

type upstreamUsage struct {
	inputTokens       int64
	cachedInputTokens int64
	outputTokens      int64
	reasoningTokens   int64
	totalTokens       int64
	reported          bool
}

func New(config Config) (*Gateway, error) {
	gateway := &Gateway{config: config, tokens: make(map[string]string)}
	if config.UpstreamBaseURL == "" || config.UpstreamAPIKey == "" || config.ModelID == "" {
		return gateway, nil
	}
	target, err := url.Parse(config.UpstreamBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse model upstream URL: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		request.URL.Path = strings.TrimPrefix(request.URL.Path, "/model")
		original(request)
		request.Host = target.Host
		request.Header.Set("Authorization", "Bearer "+config.UpstreamAPIKey)
	}
	proxy.ModifyResponse = gateway.captureResponseUsage
	proxy.ErrorHandler = gateway.handleProxyError
	gateway.proxy = proxy
	return gateway, nil
}

func (g *Gateway) Configured() bool {
	return g.proxy != nil
}

func (g *Gateway) ModelID() string { return g.config.ModelID }

// SetUsageRecorder attaches the daemon's persistent accounting journal. It is
// optional so the gateway remains usable in isolated tests and without SQLite.
func (g *Gateway) SetUsageRecorder(recorder UsageRecorder) {
	g.mu.Lock()
	g.recorder = recorder
	g.mu.Unlock()
}

func (g *Gateway) Issue(taskID string) (string, error) {
	if !g.Configured() {
		return "", fmt.Errorf("model gateway is not configured; set CTF_UPSTREAM_MODEL_BASE_URL, CTF_UPSTREAM_MODEL_API_KEY and CTF_MODEL_ID")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	g.mu.Lock()
	g.tokens[token] = taskID
	g.mu.Unlock()
	return token, nil
}

func (g *Gateway) Revoke(token string) {
	g.mu.Lock()
	delete(g.tokens, token)
	g.mu.Unlock()
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !g.Configured() {
		http.Error(writer, "model gateway is not configured", http.StatusServiceUnavailable)
		return
	}
	taskID, valid := g.taskForToken(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if !valid {
		http.Error(writer, "invalid task model token", http.StatusUnauthorized)
		return
	}
	if g.config.IncludeStreamUsage {
		ensureStreamUsage(request)
	}
	meta := &requestUsageMeta{taskID: taskID, model: g.config.ModelID, started: time.Now()}
	request = request.WithContext(context.WithValue(request.Context(), requestUsageMetaKey{}, meta))
	g.proxy.ServeHTTP(writer, request)
}

// ensureStreamUsage is deliberately best-effort: an invalid, non-JSON, or
// unusually large request is forwarded untouched. This leaves non-standard
// upstreams working while enabling accurate accounting for the usual OpenAI
// compatible Pi provider.
func ensureStreamUsage(request *http.Request) {
	if request.Body == nil || !strings.HasSuffix(strings.TrimSuffix(request.URL.Path, "/"), "/chat/completions") || !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		return
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxJSONRequestRewrite+1))
	if err != nil {
		return
	}
	if len(data) > maxJSONRequestRewrite {
		request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), request.Body))
		request.ContentLength = -1
		request.Header.Del("Content-Length")
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	var streaming bool
	if value, ok := body["stream"]; !ok || json.Unmarshal(value, &streaming) != nil || !streaming {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	streamOptions := map[string]any{}
	if value, ok := body["stream_options"]; ok {
		_ = json.Unmarshal(value, &streamOptions)
	}
	streamOptions["include_usage"] = true
	encodedOptions, err := json.Marshal(streamOptions)
	if err != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	body["stream_options"] = encodedOptions
	encoded, err := json.Marshal(body)
	if err != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
	request.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
}

func (g *Gateway) taskForToken(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for token, taskID := range g.tokens {
		if len(token) == len(presented) && subtle.ConstantTimeCompare([]byte(token), []byte(presented)) == 1 {
			return taskID, true
		}
	}
	return "", false
}

func (g *Gateway) captureResponseUsage(response *http.Response) error {
	meta, _ := response.Request.Context().Value(requestUsageMetaKey{}).(*requestUsageMeta)
	if meta == nil {
		return nil
	}
	response.Body = &usageCaptureBody{
		ReadCloser:  response.Body,
		contentType: response.Header.Get("Content-Type"),
		complete: func(usage upstreamUsage) {
			g.record(meta, usage, response.StatusCode, "completed")
		},
	}
	return nil
}

func (g *Gateway) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	if meta, _ := request.Context().Value(requestUsageMetaKey{}).(*requestUsageMeta); meta != nil {
		g.record(meta, upstreamUsage{}, 0, "transport_error")
	}
	http.Error(writer, "model upstream request failed", http.StatusBadGateway)
}

func (g *Gateway) record(meta *requestUsageMeta, usage upstreamUsage, statusCode int, status string) {
	meta.once.Do(func() {
		g.mu.RLock()
		recorder := g.recorder
		g.mu.RUnlock()
		if recorder == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = recorder.RecordModelUsage(ctx, platform.ModelUsage{
			ID:                platform.NewID("usage"),
			TaskID:            meta.taskID,
			Model:             meta.model,
			InputTokens:       usage.inputTokens,
			CachedInputTokens: usage.cachedInputTokens,
			OutputTokens:      usage.outputTokens,
			ReasoningTokens:   usage.reasoningTokens,
			TotalTokens:       usage.totalTokens,
			UsageReported:     usage.reported,
			LatencyMS:         time.Since(meta.started).Milliseconds(),
			StatusCode:        statusCode,
			Status:            status,
			CreatedAt:         time.Now(),
		})
	})
}

// usageCaptureBody watches a proxied response while it is copied to Pi. JSON
// responses are captured only up to a safe limit; SSE responses are parsed
// line-by-line so a long streamed answer does not need to be buffered.
type usageCaptureBody struct {
	io.ReadCloser
	contentType string
	jsonBody    bytes.Buffer
	ssePending  []byte
	usage       upstreamUsage
	complete    func(upstreamUsage)
	once        sync.Once
}

func (body *usageCaptureBody) Read(buffer []byte) (int, error) {
	count, err := body.ReadCloser.Read(buffer)
	if count > 0 {
		body.capture(buffer[:count])
	}
	if err == io.EOF {
		body.finish()
	}
	return count, err
}

func (body *usageCaptureBody) Close() error {
	body.finish()
	return body.ReadCloser.Close()
}

func (body *usageCaptureBody) capture(data []byte) {
	if strings.Contains(strings.ToLower(body.contentType), "text/event-stream") {
		body.ssePending = append(body.ssePending, data...)
		for {
			index := bytes.IndexByte(body.ssePending, '\n')
			if index < 0 {
				return
			}
			line := bytes.TrimSpace(body.ssePending[:index])
			body.ssePending = body.ssePending[index+1:]
			if bytes.HasPrefix(line, []byte("data:")) {
				body.mergeUsage(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
			}
		}
	}
	if body.jsonBody.Len()+len(data) <= maxJSONUsageCapture {
		_, _ = body.jsonBody.Write(data)
	}
}

func (body *usageCaptureBody) finish() {
	body.once.Do(func() {
		if strings.Contains(strings.ToLower(body.contentType), "text/event-stream") {
			if line := bytes.TrimSpace(body.ssePending); bytes.HasPrefix(line, []byte("data:")) {
				body.mergeUsage(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
			}
		} else {
			body.mergeUsage(body.jsonBody.Bytes())
		}
		body.complete(body.usage)
	})
}

func (body *usageCaptureBody) mergeUsage(data []byte) {
	if usage, ok := parseUsage(data); ok {
		body.usage = usage
	}
}

func parseUsage(data []byte) (upstreamUsage, bool) {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return upstreamUsage{}, false
	}
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Usage) == 0 || string(envelope.Usage) == "null" {
		return upstreamUsage{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Usage, &raw); err != nil {
		return upstreamUsage{}, false
	}
	usage := upstreamUsage{
		inputTokens:       firstUsageInt(raw, "prompt_tokens", "input_tokens"),
		outputTokens:      firstUsageInt(raw, "completion_tokens", "output_tokens"),
		totalTokens:       firstUsageInt(raw, "total_tokens"),
		cachedInputTokens: nestedUsageInt(raw, "prompt_tokens_details", "cached_tokens"),
		reasoningTokens:   nestedUsageInt(raw, "completion_tokens_details", "reasoning_tokens"),
		reported:          true,
	}
	if usage.cachedInputTokens == 0 {
		usage.cachedInputTokens = nestedUsageInt(raw, "input_tokens_details", "cached_tokens")
	}
	if usage.reasoningTokens == 0 {
		usage.reasoningTokens = nestedUsageInt(raw, "output_tokens_details", "reasoning_tokens")
	}
	if usage.totalTokens == 0 {
		usage.totalTokens = usage.inputTokens + usage.outputTokens
	}
	return usage, true
}

func firstUsageInt(values map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			var number int64
			if json.Unmarshal(value, &number) == nil {
				return number
			}
		}
	}
	return 0
}

func nestedUsageInt(values map[string]json.RawMessage, parent, key string) int64 {
	value, ok := values[parent]
	if !ok {
		return 0
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(value, &nested) != nil {
		return 0
	}
	return firstUsageInt(nested, key)
}
