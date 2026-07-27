package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivePoolReloadPublishesNewModelsAndPreservesIssuedTokens(t *testing.T) {
	oldCalls := 0
	oldUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		oldCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"old"}}]}`))
	}))
	defer oldUpstream.Close()

	newCalls := 0
	newUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		newCalls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"new"}}]}`))
	}))
	defer newUpstream.Close()

	live, err := NewLivePool(PoolConfig{
		Models:       []ModelConfig{{Name: "old", UpstreamBaseURL: oldUpstream.URL + "/v1", UpstreamAPIKey: "old-key", ModelID: "old-model"}},
		DefaultModel: "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := live.Issue("running-task", "old")
	if err != nil {
		t.Fatal(err)
	}

	if err := live.Reload(PoolConfig{
		Models:       []ModelConfig{{Name: "new", UpstreamBaseURL: newUpstream.URL + "/v1", UpstreamAPIKey: "new-key", ModelID: "new-model"}},
		DefaultModel: "new",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := live.Profile("new"); !ok {
		t.Fatal("new profile was not visible immediately after reload")
	}
	if _, ok := live.Profile("old"); ok {
		t.Fatal("retired profile leaked into the current selection list")
	}
	newToken, err := live.Issue("new-task", "new")
	if err != nil {
		t.Fatal(err)
	}

	proxyRequest(t, live, oldToken, http.StatusOK)
	proxyRequest(t, live, newToken, http.StatusOK)
	if oldCalls != 1 || newCalls != 1 {
		t.Fatalf("upstream calls old=%d new=%d, want 1 each", oldCalls, newCalls)
	}

	live.Revoke(oldToken)
	proxyRequest(t, live, oldToken, http.StatusUnauthorized)
}

func proxyRequest(t *testing.T, handler http.Handler, token string, wantStatus int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://gateway/model/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"test"}]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("proxy status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
}
