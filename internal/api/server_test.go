package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/agent"
	"github.com/ctfagentpi/ctfagentpi/internal/buildinfo"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

// TestTaskCreationAndEventReplayOverHTTP 覆盖鉴权创建任务和 REST 事件重放主链路。
func TestTaskCreationAndEventReplayOverHTTP(t *testing.T) {
	// 使用临时 SQLite、内存 Hub 与 httptest 服务，测试不依赖真实监听端口。
	store, err := storage.Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sandboxes, err := sandbox.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sandboxes.Close()
	gateway, err := modelgateway.New(modelgateway.Config{})
	if err != nil {
		t.Fatal(err)
	}
	hub := eventhub.New()
	agents := agent.NewService(store, hub, sandboxes, gateway, t.TempDir(), "http://host.docker.internal:18731")
	server := New("127.0.0.1:0", "test-token", store, hub, agents, sandboxes, gateway)
	transport := httptest.NewServer(server.http.Handler)
	defer transport.Close()

	// 系统接口必须返回统一发布版本，供底栏和“关于”页面共同展示。
	systemRequest, _ := http.NewRequest(http.MethodGet, transport.URL+"/api/system", nil)
	systemRequest.Header.Set("Authorization", "Bearer test-token")
	systemResponse, err := http.DefaultClient.Do(systemRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer systemResponse.Body.Close()
	var systemStatus struct {
		Daemon struct {
			Version string `json:"version"`
		} `json:"daemon"`
	}
	if err := json.NewDecoder(systemResponse.Body).Decode(&systemStatus); err != nil {
		t.Fatal(err)
	}
	if systemStatus.Daemon.Version != buildinfo.Version {
		t.Fatalf("daemon version = %q, want %q", systemStatus.Daemon.Version, buildinfo.Version)
	}

	// 携带 daemon Bearer Token 创建一条合法 Crypto 任务。
	payload, _ := json.Marshal(platform.CreateTask{Title: "HTTP journal", Category: "crypto", Description: "test challenge"})
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, transport.URL+"/api/tasks", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("unexpected create status %d", response.StatusCode)
	}
	var task platform.Task
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Status != platform.TaskReady || task.Image == "" {
		t.Fatalf("unexpected task %#v", task)
	}

	// 创建动作应同步写入 sequence=1 的 task.created 事件，可从 REST 重放。
	eventRequest, _ := http.NewRequest(http.MethodGet, transport.URL+"/api/tasks/"+task.ID+"/events", nil)
	eventRequest.Header.Set("Authorization", "Bearer test-token")
	eventResponse, err := http.DefaultClient.Do(eventRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer eventResponse.Body.Close()
	var events []platform.Event
	if err := json.NewDecoder(eventResponse.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "task.created" || events[0].Sequence != 1 {
		t.Fatalf("unexpected events %#v", events)
	}
}

// TestDaemonShutdownRequestsLifecycleStop 确保关闭端点只发出生命周期退出请求，
// 避免 HTTP handler 在自身请求仍未完成时同步等待 Server.Shutdown。
func TestDaemonShutdownRequestsLifecycleStop(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sandboxes, err := sandbox.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sandboxes.Close()
	gateway, err := modelgateway.New(modelgateway.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server := New("127.0.0.1:0", "test-token", store, eventhub.New(), nil, sandboxes, gateway)
	stopped := make(chan struct{})
	server.SetShutdownRequest(func() { close(stopped) })

	request := httptest.NewRequest(http.MethodPost, "/api/daemon/shutdown", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected shutdown status %d", response.Code)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown lifecycle callback was not requested")
	}
}
