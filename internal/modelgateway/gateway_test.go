package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

// recordingSink 是测试用内存账本，用于观察网关生成的 ModelUsage。
type recordingSink struct{ records []platform.ModelUsage }

// RecordModelUsage 实现 UsageRecorder，并保留每次调用供断言。
func (sink *recordingSink) RecordModelUsage(_ context.Context, usage platform.ModelUsage) error {
	sink.records = append(sink.records, usage)
	return nil
}

// TestGatewayReplacesTaskTokenWithUpstreamCredential 验证短期任务 Token
// 不会转发给模型供应商，且路径正确去除了 /model 前缀。
func TestGatewayReplacesTaskTokenWithUpstreamCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream path %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("unexpected upstream authorization %q", got)
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	gateway, err := New(Config{UpstreamBaseURL: upstream.URL + "/v1", UpstreamAPIKey: "upstream-secret", ModelID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := gateway.Issue("task_test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("unexpected status %d: %s", response.Code, body)
	}
}

// TestProbeUsesTheConfiguredModelEndpoint 验证启动前探测确实请求真实的
// OpenAI 兼容模型接口，而不是仅检查一个可能不存在的 /health 或 /models。
func TestProbeUsesTheConfiguredModelEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected probe path %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Fatalf("missing upstream authorization")
		}
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "probe-model" || body.Stream {
			t.Fatalf("unexpected probe body %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer upstream.Close()

	gateway, err := New(Config{UpstreamBaseURL: upstream.URL + "/v1", UpstreamAPIKey: "upstream-secret", ModelID: "probe-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Probe(context.Background()); err != nil {
		t.Fatalf("expected successful probe, got %v", err)
	}
	status := gateway.ProbeStatus()
	if !status.Configured || !status.Available || status.CheckedAt == nil || status.Error != "" {
		t.Fatalf("unexpected successful probe status %#v", status)
	}
}

func TestProbeReturnsActionableUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(522)
	}))
	defer upstream.Close()
	gateway, err := New(Config{UpstreamBaseURL: upstream.URL + "/v1", UpstreamAPIKey: "upstream-secret", ModelID: "probe-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 522") {
		t.Fatalf("expected 522 probe error, got %v", err)
	}
	status := gateway.ProbeStatus()
	if !status.Configured || status.Available || status.CheckedAt == nil || !strings.Contains(status.Error, "HTTP 522") {
		t.Fatalf("unexpected failed probe status %#v", status)
	}
}

// TestGatewayRecordsOpenAIUsage 验证普通 JSON 响应中的 Token 明细会完整记账。
func TestGatewayRecordsOpenAIUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}}}`))
	}))
	defer upstream.Close()
	gateway, err := New(Config{UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "upstream-secret", ModelID: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	gateway.SetUsageRecorder(sink)
	token, err := gateway.Issue("task_usage")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}
	if len(sink.records) != 1 {
		t.Fatalf("expected one usage record, got %#v", sink.records)
	}
	record := sink.records[0]
	if record.TaskID != "task_usage" || record.Model != "test-model" || !record.UsageReported || record.InputTokens != 12 || record.OutputTokens != 8 || record.TotalTokens != 20 || record.CachedInputTokens != 3 || record.ReasoningTokens != 4 {
		t.Fatalf("unexpected usage record %#v", record)
	}
}

// TestParseUsageFromOpenAICompatibleAndResponsesShapes 覆盖两类常见 usage 字段命名。
func TestParseUsageFromOpenAICompatibleAndResponsesShapes(t *testing.T) {
	for _, source := range []string{
		`{"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
		`{"usage":{"input_tokens":13,"output_tokens":9,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":5}}}`,
	} {
		usage, ok := parseUsage([]byte(source))
		if !ok || !usage.reported || usage.totalTokens != usage.inputTokens+usage.outputTokens {
			t.Fatalf("unexpected parsed usage %#v for %s", usage, source)
		}
	}
}

// TestEnsureStreamUsageAddsOpenAICompatibleOption 验证流式请求会自动要求最终 usage 块。
func TestEnsureStreamUsageAddsOpenAICompatibleOption(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/v1/chat/completions", strings.NewReader(`{"model":"test","stream":true,"messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	ensureStreamUsage(request)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if !value.StreamOptions.IncludeUsage {
		t.Fatalf("stream usage option was not added: %s", body)
	}
}

// TestNormalizeRolesRewritesOnlyDeveloper 验证 DeepSeek 不支持的 developer
// 会降级为 system，同时不会改变其他 OpenAI 兼容角色和消息内容。
func TestNormalizeRolesRewritesOnlyDeveloper(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"question"},{"role":"assistant","content":"answer"},{"role":"tool","content":"result","tool_call_id":"call_1"}]}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	normalizeRoles(request)

	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Messages) != 4 {
		t.Fatalf("unexpected messages: %s", body)
	}
	if value.Messages[0].Role != "system" || value.Messages[0].Content != "rules" {
		t.Fatalf("developer role was not normalized: %s", body)
	}
	if value.Messages[1].Role != "user" || value.Messages[2].Role != "assistant" || value.Messages[3].Role != "tool" || value.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("non-developer messages were changed: %s", body)
	}
	if request.ContentLength != int64(len(body)) || request.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("request length was not updated: contentLength=%d header=%q body=%d", request.ContentLength, request.Header.Get("Content-Length"), len(body))
	}
}

// TestGatewayNormalizesRolesAndAddsStreamUsage 验证两个请求改写按预期组合，
// 防止后一次 JSON 重写覆盖前一次 DeepSeek 角色适配。
func TestGatewayNormalizesRolesAndAddsStreamUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "system" {
			t.Fatalf("developer role reached upstream: %#v", body.Messages)
		}
		if !body.StreamOptions.IncludeUsage {
			t.Fatalf("stream usage option was not preserved: %#v", body.StreamOptions)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	gateway, err := New(Config{
		UpstreamBaseURL:    upstream.URL,
		UpstreamAPIKey:     "upstream-secret",
		ModelID:            "deepseek-test",
		IncludeStreamUsage: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := gateway.Issue("task_deepseek")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/chat/completions", strings.NewReader(`{"model":"deepseek-test","stream":true,"messages":[{"role":"developer","content":"rules"}]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	gateway.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("unexpected status %d: %s", response.Code, body)
	}
}

// TestGatewayKeepsDeveloperRoleForOtherModels 确认 DeepSeek 适配不会改变
// 明确支持 developer 角色的其他 OpenAI 兼容模型。
func TestGatewayKeepsDeveloperRoleForOtherModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "developer" {
			t.Fatalf("non-DeepSeek role was changed: %#v", body.Messages)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	gateway, err := New(Config{UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "upstream-secret", ModelID: "openai-test"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := gateway.Issue("task_openai")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/chat/completions", strings.NewReader(`{"model":"openai-test","messages":[{"role":"developer","content":"rules"}]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	gateway.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("unexpected status %d: %s", response.Code, body)
	}
}

type errorSink struct {
	taskID     string
	statusCode int
	message    string
}

func (sink *errorSink) RecordModelError(_ context.Context, taskID string, statusCode int, message string) {
	sink.taskID, sink.statusCode, sink.message = taskID, statusCode, message
}

// TestGatewayReportsUpstreamModelErrors 验证供应商 4xx 会成为可显示的任务错误，
// 并保留 DeepSeek 返回的 image_url 兼容性说明。
func TestGatewayReportsUpstreamModelErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"unknown variant image_url"}}`))
	}))
	defer upstream.Close()
	gateway, err := New(Config{UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "upstream-secret", ModelID: "text-model"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &errorSink{}
	gateway.SetErrorReporter(sink)
	token, err := gateway.Issue("task_image_error")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d", response.Code)
	}
	if sink.taskID != "task_image_error" || sink.statusCode != http.StatusBadRequest {
		t.Fatalf("unexpected error event %#v", sink)
	}
	if !strings.Contains(sink.message, "image_url") {
		t.Fatalf("missing upstream error detail %q", sink.message)
	}
}
