package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/ctfagentpi/ctfagentpi/internal/agent"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
	"github.com/go-chi/chi/v5"
)

type Server struct {
	address   string
	token     string
	store     *storage.Store
	hub       *eventhub.Hub
	agents    *agent.Service
	sandboxes *sandbox.Manager
	gateway   *modelgateway.Gateway
	http      *http.Server
}

func New(address, token string, store *storage.Store, hub *eventhub.Hub, agents *agent.Service, sandboxes *sandbox.Manager, gateway *modelgateway.Gateway) *Server {
	server := &Server{address: address, token: token, store: store, hub: hub, agents: agents, sandboxes: sandboxes, gateway: gateway}
	router := chi.NewRouter()
	router.Use(server.cors)
	router.Get("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "time": time.Now()})
	})
	router.Handle("/model/*", gateway)
	router.Group(func(api chi.Router) {
		api.Use(server.authenticate)
		api.Get("/api/system", server.system)
		api.Get("/api/model-usage", server.modelUsage)
		api.Get("/api/tasks", server.listTasks)
		api.Post("/api/tasks", server.createTask)
		api.Get("/api/tasks/{taskID}", server.getTask)
		api.Get("/api/tasks/{taskID}/prompt", server.getTaskPrompt)
		api.Put("/api/tasks/{taskID}/prompt", server.updateTaskPrompt)
		api.Delete("/api/tasks/{taskID}", server.deleteTask)
		api.Post("/api/tasks/{taskID}/attachments", server.uploadAttachments)
		api.Post("/api/tasks/{taskID}/start", server.startTask)
		api.Post("/api/tasks/{taskID}/abort", server.abortTask)
		api.Post("/api/tasks/{taskID}/pause", server.pauseTask)
		api.Post("/api/tasks/{taskID}/resume", server.resumeTask)
		api.Post("/api/tasks/{taskID}/retry", server.retryTask)
		api.Post("/api/tasks/{taskID}/close-sandbox", server.closeSandbox)
		api.Get("/api/tasks/{taskID}/events", server.listEvents)
		api.Get("/api/tasks/{taskID}/files", server.listWorkspaceFiles)
		api.Get("/api/tasks/{taskID}/file", server.readWorkspaceFile)
		api.Get("/api/tasks/{taskID}/download", server.downloadWorkspaceFile)
		api.Get("/api/tasks/{taskID}/writeup", server.readWriteup)
		api.Post("/api/daemon/shutdown", server.shutdownDaemon)
		api.Get("/ws/tasks/{taskID}", server.taskEvents)
	})
	server.http = &http.Server{Addr: address, Handler: router, ReadHeaderTimeout: 10 * time.Second}
	return server
}

func (s *Server) ListenAndServe() error {
	slog.Info("CTF-BTFly daemon listening", "address", s.address)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		presented := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if presented == "" {
			presented = request.URL.Query().Get("token")
		}
		if len(presented) != len(s.token) || subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) != 1 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if request.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) system(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"daemon":       map[string]string{"address": s.address, "version": "0.1.0"},
		"docker":       s.sandboxes.Health(request.Context()),
		"modelGateway": map[string]any{"configured": s.gateway.Configured(), "model": s.gateway.ModelID()},
		"stack":        []string{"Wails v3", "React 19", "Tailwind CSS 4", "Go daemon", "SQLite", "Docker SDK", "Pi RPC"},
	})
}

func (s *Server) modelUsage(writer http.ResponseWriter, request *http.Request) {
	report, err := s.store.ModelUsageReport(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

func (s *Server) listTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.store.ListRootTasks(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, tasks)
}

func (s *Server) createTask(writer http.ResponseWriter, request *http.Request) {
	var input platform.CreateTask
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	task, err := s.agents.CreateTask(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, task)
}

func (s *Server) getTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.store.GetTask(request.Context(), chi.URLParam(request, "taskID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, task)
}

// taskPromptResponse separates the editable, task-specific direction from the
// generated system prompt. The latter is read-only and always comes from the
// daemon, so the renderer cannot weaken the execution policy.
type taskPromptResponse struct {
	Prompt       string `json:"prompt"`
	SystemPrompt string `json:"systemPrompt"`
	Editable     bool   `json:"editable"`
	Retryable    bool   `json:"retryable"`
	Resumable    bool   `json:"resumable"`
}

func (s *Server) getTaskPrompt(writer http.ResponseWriter, request *http.Request) {
	task, err := s.store.GetTask(request.Context(), chi.URLParam(request, "taskID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, taskPromptResponse{
		Prompt:       task.Prompt,
		SystemPrompt: agent.BuildPromptPreview(task),
		Editable:     task.Status != platform.TaskProvisioning && task.Status != platform.TaskRunning,
		Retryable:    task.Status == platform.TaskSettled || task.Status == platform.TaskFailed || task.Status == platform.TaskCancelled,
		Resumable:    task.Status == platform.TaskPaused,
	})
}

type updateTaskPromptRequest struct {
	Prompt string `json:"prompt"`
}

func (s *Server) updateTaskPrompt(writer http.ResponseWriter, request *http.Request) {
	var input updateTaskPromptRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	task, err := s.agents.UpdatePrompt(request.Context(), chi.URLParam(request, "taskID"), input.Prompt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if errors.Is(err, agent.ErrPromptLocked) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, task)
}

func (s *Server) deleteTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Delete(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
			return
		}
		if errors.Is(err, agent.ErrTaskNotDeletable) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) uploadAttachments(writer http.ResponseWriter, request *http.Request) {
	const maxUploadBytes int64 = 2 << 30 // 2 GiB per upload request
	request.Body = http.MaxBytesReader(writer, request.Body, maxUploadBytes)
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, fmt.Errorf("attachments exceed the 2 GiB upload limit"))
			return
		}
		writeError(writer, http.StatusBadRequest, fmt.Errorf("parse attachments: %w", err))
		return
	}
	defer request.MultipartForm.RemoveAll()
	files := request.MultipartForm.File["files"]
	if len(files) == 0 {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("at least one attachment is required"))
		return
	}
	var paths []string
	if err := json.Unmarshal([]byte(request.FormValue("paths")), &paths); err != nil || len(paths) != len(files) {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("attachment paths do not match uploaded files"))
		return
	}
	uploads := make([]agent.AttachmentUpload, 0, len(files))
	for index, header := range files {
		header := header
		uploads = append(uploads, agent.AttachmentUpload{
			Path: paths[index],
			Open: func() (io.ReadCloser, error) { return header.Open() },
		})
	}
	saved, err := s.agents.StoreAttachments(request.Context(), chi.URLParam(request, "taskID"), uploads)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if errors.Is(err, agent.ErrAttachmentsLocked) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"files": saved})
}

func (s *Server) startTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Start(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "starting"})
}

func (s *Server) abortTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Abort(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func (s *Server) pauseTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Pause(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
			return
		}
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "paused"})
}

func (s *Server) resumeTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Resume(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
			return
		}
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "resuming"})
}

func (s *Server) retryTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Retry(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
			return
		}
		if errors.Is(err, agent.ErrTaskNotRetryable) || errors.Is(err, agent.ErrSandboxNotClosable) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "retrying"})
}

func (s *Server) closeSandbox(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.CloseSandbox(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
			return
		}
		if errors.Is(err, agent.ErrSandboxNotClosable) {
			writeError(writer, http.StatusConflict, err)
			return
		}
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "sandbox_closed"})
}

func (s *Server) listEvents(writer http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	events, err := s.store.ListEvents(request.Context(), chi.URLParam(request, "taskID"), after, 5000)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, events)
}

func (s *Server) listWorkspaceFiles(writer http.ResponseWriter, request *http.Request) {
	files, err := s.agents.ListWorkspaceFiles(request.Context(), chi.URLParam(request, "taskID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, files)
}

func (s *Server) readWorkspaceFile(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("workspace file path is required"))
		return
	}
	file, err := s.agents.ReadWorkspaceFile(request.Context(), chi.URLParam(request, "taskID"), path)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, file)
}

// downloadWorkspaceFile serves a single task-local regular file. It delegates
// path and symlink checks to the same service method used by the preview API,
// then streams the resolved file as an attachment instead of exposing its host
// path to the browser.
func (s *Server) downloadWorkspaceFile(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("workspace file path is required"))
		return
	}
	file, err := s.agents.OpenWorkspaceFile(request.Context(), chi.URLParam(request, "taskID"), path)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	defer file.Close()
	name := filepath.Base(path)
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = io.Copy(writer, file)
}

func (s *Server) readWriteup(writer http.ResponseWriter, request *http.Request) {
	file, err := s.agents.ReadWorkspaceFile(request.Context(), chi.URLParam(request, "taskID"), "WRITEUP.md")
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusOK, map[string]any{"exists": false, "content": ""})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, fmt.Errorf("task not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"exists": true, "content": file.Content, "truncated": file.Truncated, "binary": file.Binary})
}

// shutdownDaemon deliberately refuses to stop while a sandbox is running.
// This keeps the desktop tray action from orphaning autonomous CTF tasks.
func (s *Server) shutdownDaemon(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.store.ListTasks(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	running := make([]map[string]string, 0)
	for _, task := range tasks {
		if task.Status == platform.TaskRunning || task.Status == platform.TaskProvisioning || task.Status == platform.TaskPaused {
			running = append(running, map[string]string{"id": task.ID, "title": task.Title, "status": string(task.Status)})
		}
	}
	if len(running) > 0 {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": "running tasks must be stopped before exit", "tasks": running})
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "shutting_down"})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
}

func (s *Server) taskEvents(writer http.ResponseWriter, request *http.Request) {
	taskID := chi.URLParam(request, "taskID")
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	stream, cancel := s.hub.Subscribe(taskID)
	defer cancel()
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	history, err := s.store.ListEvents(request.Context(), taskID, after, 5000)
	if err != nil {
		_ = connection.Close(websocket.StatusInternalError, err.Error())
		return
	}
	lastSequence := after
	for _, event := range history {
		if err := writeSocket(request.Context(), connection, event); err != nil {
			return
		}
		lastSequence = event.Sequence
	}
	for {
		select {
		case <-request.Context().Done():
			return
		case event, ok := <-stream:
			if !ok {
				return
			}
			if event.Sequence <= lastSequence {
				continue
			}
			if err := writeSocket(request.Context(), connection, event); err != nil {
				return
			}
			lastSequence = event.Sequence
		}
	}
}

func decodeJSON(request *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func writeSocket(ctx context.Context, connection *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, data)
}
