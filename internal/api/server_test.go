package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ctfagentpi/ctfagentpi/internal/agent"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

func TestTaskCreationAndEventReplayOverHTTP(t *testing.T) {
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
	agents := agent.NewService(store, hub, sandboxes, gateway, t.TempDir(), "http://host.docker.internal:17321")
	server := New("127.0.0.1:0", "test-token", store, hub, agents, sandboxes, gateway)
	transport := httptest.NewServer(server.http.Handler)
	defer transport.Close()

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
