package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctfagentpi/ctfagentpi/internal/envfile"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
)

func TestSaveModelConfigWritesKeyWithoutReturningIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("CTF_AGENT_ENV_FILE", path)
	gateway, err := modelgateway.NewPool(modelgateway.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	server := New("127.0.0.1:0", "test-token", nil, nil, nil, nil, gateway)
	input := ModelConfigInput{
		Name: "deepseek", BaseURL: "https://api.deepseek.example/v1", APIKey: "super-secret-key",
		ModelID: "deepseek-chat", IncludeStreamUsage: true,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/models/config", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("save status %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), input.APIKey) {
		t.Fatal("model configuration response leaked API key")
	}
	var list ModelConfigList
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 1 || list.Models[0].Name != "deepseek" || !list.Models[0].HasAPIKey || !list.Models[0].Default {
		t.Fatalf("unexpected safe config list %#v", list)
	}
	values, err := envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["CTF_MODELS"] != "deepseek" || values["CTF_MODEL_DEEPSEEK_API_KEY"] != input.APIKey {
		t.Fatalf("configuration was not persisted correctly: %#v", values)
	}
}
