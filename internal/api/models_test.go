package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/envfile"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
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

func TestDeleteModelConfigBlocksUnfinishedTasksAndAllowsLastModelRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	contents := strings.Join([]string{
		"CTF_MODELS=deepseek,qwen",
		"CTF_DEFAULT_MODEL=deepseek",
		"CTF_MODEL_DEEPSEEK_BASE_URL=https://deepseek.example/v1",
		"CTF_MODEL_DEEPSEEK_API_KEY=deepseek-key",
		"CTF_MODEL_DEEPSEEK_ID=deepseek-chat",
		"CTF_MODEL_QWEN_BASE_URL=https://qwen.example/v1",
		"CTF_MODEL_QWEN_API_KEY=qwen-key",
		"CTF_MODEL_QWEN_ID=qwen-chat",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTF_AGENT_ENV_FILE", path)
	initialValues, err := envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	task := platform.Task{
		ID: "task_running_model", Title: "unfinished", Category: "web", Description: "test",
		ModelProfile: "deepseek", ModelID: "deepseek-chat", Status: platform.TaskRunning,
		Image: "test:latest", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	gateway, err := modelgateway.NewPool(modelgateway.PoolConfigFromLookup(func(key string) string { return initialValues[key] }))
	if err != nil {
		t.Fatal(err)
	}
	server := New("127.0.0.1:0", "test-token", store, nil, nil, nil, gateway)
	reloadedProfile := "not-called"
	server.SetModelConfigProbe(func(_ context.Context, profile string) (modelgateway.ProbeStatus, error) {
		reloadedProfile = profile
		return modelgateway.ProbeStatus{}, nil
	})

	response := deleteModelRequest(server, "deepseek")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "未结束题目") {
		t.Fatalf("running task delete status %d: %s", response.Code, response.Body.String())
	}
	values, err := envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["CTF_MODELS"] != "deepseek,qwen" || reloadedProfile != "not-called" {
		t.Fatalf("blocked deletion changed config or reloaded gateway: %#v, %q", values, reloadedProfile)
	}

	if err := store.UpdateTaskState(context.Background(), task.ID, platform.TaskSettled, "", "", ""); err != nil {
		t.Fatal(err)
	}
	response = deleteModelRequest(server, "deepseek")
	if response.Code != http.StatusOK {
		t.Fatalf("settled task delete status %d: %s", response.Code, response.Body.String())
	}
	values, err = envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["CTF_MODELS"] != "qwen" || values["CTF_DEFAULT_MODEL"] != "qwen" || values["CTF_MODEL_DEEPSEEK_API_KEY"] != "" || reloadedProfile != "qwen" {
		t.Fatalf("unexpected config after deleting deepseek: %#v, reload=%q", values, reloadedProfile)
	}

	legacyTask := platform.Task{
		ID: "task_legacy_model", Title: "legacy unfinished", Category: "web", Description: "test",
		ModelID: "qwen-chat", Status: platform.TaskRunning,
		Image: "test:latest", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	if err := store.CreateTask(context.Background(), legacyTask); err != nil {
		t.Fatal(err)
	}
	response = deleteModelRequest(server, "qwen")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), legacyTask.Title) {
		t.Fatalf("legacy running task delete status %d: %s", response.Code, response.Body.String())
	}
	if err := store.UpdateTaskState(context.Background(), legacyTask.ID, platform.TaskSettled, "", "", ""); err != nil {
		t.Fatal(err)
	}
	response = deleteModelRequest(server, "qwen")
	if response.Code != http.StatusOK {
		t.Fatalf("last model delete status %d: %s", response.Code, response.Body.String())
	}
	var list ModelConfigList
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 0 || list.DefaultModel != "" || reloadedProfile != "" {
		t.Fatalf("last deletion returned %#v, reload=%q", list, reloadedProfile)
	}
	values, err = envfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["CTF_MODELS"] != "" || values["CTF_MODEL_QWEN_API_KEY"] != "" {
		t.Fatalf("last model keys remained in config: %#v", values)
	}
}

func deleteModelRequest(server *Server, profile string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodDelete, "/api/models/config/"+profile, nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	return response
}
