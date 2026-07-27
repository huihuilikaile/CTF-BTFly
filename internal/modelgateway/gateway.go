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

// 两个上限分别约束普通响应的用量捕获和请求体重写，
// 防止为了 Token 统计而无限缓冲模型内容。
const maxJSONUsageCapture = 8 << 20
const maxJSONRequestRewrite = 4 << 20
const maxModelErrorMessageBytes = 4 << 10

// modelProbeTimeout 限制启动前连通性检查的等待时间。它只在真正准备创建
// 沙箱时调用，避免把不可用的上游模型错误伪装成 Pi 已正常结束一轮。
const modelProbeTimeout = 20 * time.Second

// Config 是 daemon 持有的真实上游配置，不会序列化给前端或容器。
type Config struct {
	UpstreamBaseURL string
	UpstreamAPIKey  string
	ModelID         string
	// IncludeStreamUsage 要求 OpenAI 兼容上游在 SSE 尾部返回 usage；
	// 若供应商拒绝 stream_options，可显式关闭。
	IncludeStreamUsage bool
	// SupportsImages 决定容器内 Provider 是否向上游发送图片内容块。默认 false，
	// 以兼容 DeepSeek 等只接受文本 content 的 OpenAI 兼容接口。
	SupportsImages bool
}

// UsageRecorder 由 SQLite Store 实现。通过小接口反转依赖，
// 让反向代理无需直接引用存储包，也便于单元测试替换记录器。
type UsageRecorder interface {
	RecordModelUsage(context.Context, platform.ModelUsage) error
}

// ErrorReporter 将模型上游失败转为所属任务的可持久化事件。
type ErrorReporter interface {
	RecordModelError(context.Context, string, int, string)
}

// Gateway 保存上游代理、题目短期 Token 映射和可选用量记录器。
type Gateway struct {
	config   Config
	proxy    *httputil.ReverseProxy
	mu       sync.RWMutex
	tokens   map[string]string
	recorder UsageRecorder
	errors   ErrorReporter
	probe    ProbeStatus
	// normalizeDeveloperRole 仅为 DeepSeek 上游启用 developer -> system
	// 兼容，避免改变支持 developer 角色的其他模型语义。
	normalizeDeveloperRole bool
}

// ProbeStatus 是仅供本机控制平面展示的模型连通性摘要；不包含上游地址、
// API Key、请求内容或响应内容。
type ProbeStatus struct {
	Configured bool       `json:"configured"`
	Available  bool       `json:"available"`
	CheckedAt  *time.Time `json:"checkedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// requestUsageMeta 在单次 HTTP 请求的上下文中跟踪归属、模型与耗时。
type requestUsageMeta struct {
	taskID  string
	model   string
	started time.Time
	once    sync.Once
}

// requestUsageMetaKey 使用私有零大小类型，避免 Context 键与其他包冲突。
type requestUsageMetaKey struct{}

// upstreamUsage 是兼容多种 OpenAI 风格字段后的内部用量表示。
type upstreamUsage struct {
	inputTokens       int64
	cachedInputTokens int64
	outputTokens      int64
	reasoningTokens   int64
	totalTokens       int64
	reported          bool
}

// New 创建模型网关。配置不完整时返回“未配置”的可用对象，
// 使系统状态页仍可启动并明确提示缺少模型设置。
func New(config Config) (*Gateway, error) {
	gateway := &Gateway{config: config, tokens: make(map[string]string)}
	if config.UpstreamBaseURL == "" || config.UpstreamAPIKey == "" || config.ModelID == "" {
		return gateway, nil
	}
	target, err := url.Parse(config.UpstreamBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse model upstream URL: %w", err)
	}
	modelID := strings.ToLower(strings.TrimSpace(config.ModelID))
	gateway.normalizeDeveloperRole = strings.HasPrefix(modelID, "deepseek")

	// ReverseProxy 负责流式转发；Director 去掉本地 /model 前缀，
	// 并把容器短期 Token 替换为真实上游凭据。
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

// Configured 表示三项上游配置完整且代理已经创建。
func (g *Gateway) Configured() bool {
	return g.proxy != nil
}

// ModelID 返回 daemon 固定配置的模型标识。
func (g *Gateway) ModelID(_ ...string) string { return g.config.ModelID }

// SupportsImages 返回当前上游是否明确支持 OpenAI 风格图片内容块。
func (g *Gateway) SupportsImages(_ ...string) bool { return g.config.SupportsImages }

// Probe 以一次极小的 OpenAI 兼容 chat/completions 请求验证上游地址、网络、
// 鉴权和当前模型是否真的可用。相比仅访问 /models 或发 HEAD，它能提前发现
// 522、模型名错误、权限不足和不兼容网关；探测不经过本地 /model 代理，也不会
// 向任何容器暴露真实 API Key。
func (g *Gateway) Probe(ctx context.Context, _ ...string) (err error) {
	defer func() { g.recordProbeResult(err) }()
	if !g.Configured() {
		return fmt.Errorf("model gateway is not configured; set CTF_UPSTREAM_MODEL_BASE_URL, CTF_UPSTREAM_MODEL_API_KEY and CTF_MODEL_ID")
	}
	ctx, cancel := context.WithTimeout(ctx, modelProbeTimeout)
	defer cancel()

	endpoint := strings.TrimRight(g.config.UpstreamBaseURL, "/") + "/chat/completions"
	payload, err := json.Marshal(map[string]any{
		"model":       g.config.ModelID,
		"messages":    []map[string]string{{"role": "user", "content": "Reply with OK."}},
		"max_tokens":  1,
		"temperature": 0,
		"stream":      false,
	})
	if err != nil {
		return fmt.Errorf("encode model connection probe: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create model connection probe: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+g.config.UpstreamAPIKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return fmt.Errorf("model upstream connection probe failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	// 最多读取少量响应，方便指出供应商错误，同时不把长响应、HTML 页面或
	// 潜在敏感内容写进任务日志。
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	detail := strings.Join(strings.Fields(string(body)), " ")
	if detail != "" {
		return fmt.Errorf("model upstream returned HTTP %d: %s", response.StatusCode, detail)
	}
	return fmt.Errorf("model upstream returned HTTP %d (no response body)", response.StatusCode)
}

// ProbeStatus 返回最近一次显式检测的结果；尚未检测时 CheckedAt 为空，前端应
// 显示“未检测”而不是臆测模型可用。
func (g *Gateway) ProbeStatus(_ ...string) ProbeStatus {
	g.mu.RLock()
	status := g.probe
	g.mu.RUnlock()
	status.Configured = g.Configured()
	return status
}

func (g *Gateway) recordProbeResult(err error) {
	now := time.Now()
	status := ProbeStatus{Configured: g.Configured(), Available: err == nil && g.Configured(), CheckedAt: &now}
	if err != nil {
		status.Error = err.Error()
	}
	g.mu.Lock()
	g.probe = status
	g.mu.Unlock()
}

// SetUsageRecorder 连接持久化账本；它是可选项，便于隔离测试网关。

// SetErrorReporter 连接任务事件写入器，使上游模型失败可展示在前端时间线。
func (g *Gateway) SetErrorReporter(reporter ErrorReporter) {
	g.mu.Lock()
	g.errors = reporter
	g.mu.Unlock()
}
func (g *Gateway) SetUsageRecorder(recorder UsageRecorder) {
	g.mu.Lock()
	g.recorder = recorder
	g.mu.Unlock()
}

// Issue 为题目生成 256 位随机短期 Token，并建立 Token 到任务 ID 的映射。
func (g *Gateway) Issue(taskID string, _ ...string) (string, error) {
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

// Revoke 使一个题目短期 Token 立即失效。
func (g *Gateway) Revoke(token string) {
	g.mu.Lock()
	delete(g.tokens, token)
	g.mu.Unlock()
}

func (g *Gateway) hasActiveTokens() bool {
	g.mu.RLock()
	active := len(g.tokens) > 0
	g.mu.RUnlock()
	return active
}

// ServeHTTP 校验题目 Token、按需改写流式参数，并把请求转交上游。
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

	// 改写失败采用“原样转发”策略，兼容非标准 OpenAI 上游。
	if g.normalizeDeveloperRole {
		normalizeRoles(request)
	}
	if g.config.IncludeStreamUsage {
		ensureStreamUsage(request)
	}
	meta := &requestUsageMeta{taskID: taskID, model: g.config.ModelID, started: time.Now()}
	request = request.WithContext(context.WithValue(request.Context(), requestUsageMetaKey{}, meta))
	g.proxy.ServeHTTP(writer, request)
}

// normalizeRoles 将 chat/completions 请求中的 developer 角色改写为 system，
// 兼容 DeepSeek 等不识别 developer 角色的 OpenAI 兼容上游。
func normalizeRoles(request *http.Request) {
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
	messagesRaw, ok := body["messages"]
	if !ok {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(messagesRaw, &messages) != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	changed := false
	for index, message := range messages {
		role, ok := message["role"]
		if !ok {
			continue
		}
		var roleName string
		if json.Unmarshal(role, &roleName) == nil && roleName == "developer" {
			messages[index]["role"] = json.RawMessage(`"system"`)
			changed = true
		}
	}
	if !changed {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	encodedMessages, err := json.Marshal(messages)
	if err != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	body["messages"] = encodedMessages
	encoded, err := json.Marshal(body)
	if err != nil {
		request.Body = io.NopCloser(bytes.NewReader(data))
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
	request.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
}

// ensureStreamUsage 以尽力而为方式给流式 chat/completions 请求加入
// stream_options.include_usage=true。非 JSON、无效或过大的请求保持原样，
// 兼顾准确核算和非标准上游兼容性。
func ensureStreamUsage(request *http.Request) {
	// 仅处理具有 JSON 请求体的 chat/completions，其他模型接口不作假设。
	if request.Body == nil || !strings.HasSuffix(strings.TrimSuffix(request.URL.Path, "/"), "/chat/completions") || !strings.Contains(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		return
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxJSONRequestRewrite+1))
	if err != nil {
		return
	}

	// 超过 4 MiB 时把已读前缀与剩余 Body 拼回，取消 Content-Length 后原样转发。
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

	// 只有明确 stream=true 的请求需要 SSE usage 选项。
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

// taskForToken 使用恒定时间比较验证短期 Token，避免普通字符串比较泄漏前缀信息。
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

// captureResponseUsage 用观察型 ReadCloser 包装响应体，在不阻断转发的同时提取 usage。
func (g *Gateway) captureResponseUsage(response *http.Response) error {
	meta, _ := response.Request.Context().Value(requestUsageMetaKey{}).(*requestUsageMeta)
	if meta == nil {
		return nil
	}
	response.Body = &usageCaptureBody{
		ReadCloser:  response.Body,
		contentType: response.Header.Get("Content-Type"),
		statusCode:  response.StatusCode,
		reportError: func(message string) { g.reportError(meta.taskID, response.StatusCode, message) },
		complete: func(usage upstreamUsage) {
			g.record(meta, usage, response.StatusCode, "completed")
		},
	}
	return nil
}

// handleProxyError 记录网络层失败，并向容器返回不含上游细节的 502。
func (g *Gateway) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	if meta, _ := request.Context().Value(requestUsageMetaKey{}).(*requestUsageMeta); meta != nil {
		g.record(meta, upstreamUsage{}, 0, "transport_error")
		g.reportError(meta.taskID, 0, "model upstream request failed")
	}
	http.Error(writer, "model upstream request failed", http.StatusBadGateway)
}

// record 确保每个代理请求最多写入一次账本，并用独立短超时 Context
// 防止客户端取消导致用量记录一同丢失。
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

// reportError 将上游拒绝或传输失败转换为任务事件；错误文本已在调用点限制长度，
// 不携带 API Key、请求头或完整模型响应。
func (g *Gateway) reportError(taskID string, statusCode int, message string) {
	g.mu.RLock()
	reporter := g.errors
	g.mu.RUnlock()
	if reporter == nil || taskID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reporter.RecordModelError(ctx, taskID, statusCode, message)
}

// usageCaptureBody 在响应复制给 Pi 时旁路观察内容。普通 JSON 只缓冲到上限；
// SSE 按行解析，因此无需保存完整的长篇流式回答。
type usageCaptureBody struct {
	io.ReadCloser
	contentType string
	statusCode  int
	errorBody   bytes.Buffer
	reportError func(string)
	jsonBody    bytes.Buffer
	ssePending  []byte
	usage       upstreamUsage
	complete    func(upstreamUsage)
	once        sync.Once
}

// Read 将底层字节原样返回给 ReverseProxy，并把副本交给 usage 解析器。
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

// Close 即使调用方提前关闭响应体，也会触发一次最终账本记录。
func (body *usageCaptureBody) Close() error {
	body.finish()
	return body.ReadCloser.Close()
}

// capture 按 Content-Type 在 SSE 行解析与有界 JSON 缓冲之间选择。
func (body *usageCaptureBody) capture(data []byte) {
	if body.statusCode >= http.StatusBadRequest && body.errorBody.Len() < maxModelErrorMessageBytes {
		remaining := maxModelErrorMessageBytes - body.errorBody.Len()
		captured := data
		if len(captured) > remaining {
			captured = captured[:remaining]
		}
		_, _ = body.errorBody.Write(captured)
	}
	if strings.Contains(strings.ToLower(body.contentType), "text/event-stream") {
		body.ssePending = append(body.ssePending, data...)

		// SSE 数据可能跨多次 Read；保留最后一个不完整行等待后续字节。
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

// finish 合并最后一段数据，并借助 once 保证完整读取和 Close 不会重复记账。
func (body *usageCaptureBody) finish() {
	body.once.Do(func() {
		if strings.Contains(strings.ToLower(body.contentType), "text/event-stream") {
			if line := bytes.TrimSpace(body.ssePending); bytes.HasPrefix(line, []byte("data:")) {
				body.mergeUsage(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:"))))
			}
		} else {
			body.mergeUsage(body.jsonBody.Bytes())
		}
		if body.statusCode >= http.StatusBadRequest && body.reportError != nil {
			body.reportError(modelErrorMessage(body.statusCode, body.errorBody.Bytes()))
		}
		body.complete(body.usage)
	})
}

// modelErrorMessage 仅保留供应商错误对象中的 message，并截断为可安全展示的短文本。
func modelErrorMessage(statusCode int, body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		detail := strings.Join(strings.Fields(payload.Error.Message), " ")
		if len(detail) > maxModelErrorMessageBytes {
			detail = detail[:maxModelErrorMessageBytes] + "…"
		}
		return fmt.Sprintf("model upstream returned HTTP %d: %s", statusCode, detail)
	}
	return fmt.Sprintf("model upstream returned HTTP %d", statusCode)
}

// mergeUsage 只在当前片段包含合法 usage 时覆盖结果，
// 因而可自然跳过普通 token 增量和 [DONE] 标记。
func (body *usageCaptureBody) mergeUsage(data []byte) {
	if usage, ok := parseUsage(data); ok {
		body.usage = usage
	}
}

// parseUsage 同时兼容 chat/completions 的 prompt/completion 字段
// 与 responses 风格的 input/output 字段，并解析缓存及推理子字段。
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

	// 优先取标准字段；未提供 total_tokens 时按输入加输出计算。
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

// firstUsageInt 按候选键顺序返回第一个可解析的整数。
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

// nestedUsageInt 从 usage 的详情对象中读取一个整数子字段。
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
