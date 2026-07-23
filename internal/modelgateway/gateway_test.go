package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

type recordingSink struct{ records []platform.ModelUsage }

func (sink *recordingSink) RecordModelUsage(_ context.Context, usage platform.ModelUsage) error {
	sink.records = append(sink.records, usage)
	return nil
}

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
