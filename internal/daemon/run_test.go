package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
)

func TestReloadLatestModelConfigUsesFreshEnvFile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected probe path %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fresh-key" {
			t.Errorf("authorization = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer upstream.Close()

	path := filepath.Join(t.TempDir(), ".env")
	contents := fmt.Sprintf("CTF_MODELS=fresh\nCTF_DEFAULT_MODEL=fresh\nCTF_MODEL_FRESH_BASE_URL=%s/v1\nCTF_MODEL_FRESH_API_KEY=fresh-key\nCTF_MODEL_FRESH_ID=fresh-model\n", upstream.URL)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTF_AGENT_ENV_FILE", path)
	t.Setenv("CTF_MODELS", "old")
	t.Setenv("CTF_MODEL_OLD_BASE_URL", "http://127.0.0.1:1")

	gateway, err := modelgateway.NewLivePool(modelPoolConfigFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	status, err := reloadLatestModelConfig(context.Background(), gateway, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Available || status.CheckedAt == nil {
		t.Fatalf("unexpected fresh probe status %#v", status)
	}
	if profile, ok := gateway.Profile("fresh"); !ok || profile.ModelID != "fresh-model" {
		t.Fatalf("fresh profile was not published: %#v, %v", profile, ok)
	}
	if _, ok := gateway.Profile("old"); ok {
		t.Fatal("stale process-environment profile remained current after reload")
	}
}
