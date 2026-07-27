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
	"github.com/ctfagentpi/ctfagentpi/internal/buildinfo"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
	"github.com/ctfagentpi/ctfagentpi/internal/systemstats"
	"github.com/go-chi/chi/v5"
)

// Server 聚合 HTTP 层依赖，并持有可优雅关闭的标准库 HTTP Server。
type Server struct {
	address          string
	token            string
	store            *storage.Store
	hub              *eventhub.Hub
	agents           *agent.Service
	sandboxes        *sandbox.Manager
	gateway          modelgateway.Manager
	resources        *systemstats.Sampler
	modelConfigProbe ModelConfigProbe
	http             *http.Server
	// requestShutdown 由 daemon 主循环提供。HTTP handler 只发出退出请求，
	// 实际的 Server.Shutdown 统一在主循环中执行，避免 handler 等待自身退出。
	requestShutdown func()
}

// New 注册健康检查、模型代理、受鉴权 REST API 与任务 WebSocket 路由。
func New(address, token string, store *storage.Store, hub *eventhub.Hub, agents *agent.Service, sandboxes *sandbox.Manager, gateway modelgateway.Manager) *Server {
	server := &Server{address: address, token: token, store: store, hub: hub, agents: agents, sandboxes: sandboxes, gateway: gateway, resources: systemstats.NewSampler()}
	router := chi.NewRouter()
	router.Use(server.cors)

	// 健康端点不返回敏感信息，供桌面端在尚未取得 Token 前探测 daemon。
	router.Get("/health", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "time": time.Now()})
	})
	router.Handle("/model/*", gateway)

	// 除健康检查和自带任务 Token 校验的模型网关外，平台接口统一使用 daemon Token。
	router.Group(func(api chi.Router) {
		api.Use(server.authenticate)
		api.Get("/api/system", server.system)
		api.Post("/api/system/model-probe", server.probeModel)
		api.Get("/api/models/config", server.modelConfigs)
		api.Put("/api/models/config", server.saveModelConfig)
		api.Delete("/api/models/config/{profile}", server.deleteModelConfig)
		api.Get("/api/settings", server.executionSettings)
		api.Put("/api/settings", server.updateExecutionSettings)
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
		api.Get("/api/tasks/{taskID}/subtasks", server.listSubtasks)
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

// ListenAndServe 启动本地 HTTP 服务，并把正常 Shutdown 视为成功退出。
func (s *Server) ListenAndServe() error {
	slog.Info("CTF-BTFly daemon listening", "address", s.address)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown 请求 HTTP 服务停止接收连接并等待在途请求结束。
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// SetShutdownRequest 设置桌面端请求关闭 daemon 时调用的生命周期回调。
// 必须在开始监听前设置；nil 会恢复为无操作，方便独立的 API 测试。
func (s *Server) SetShutdownRequest(callback func()) {
	if callback == nil {
		s.requestShutdown = func() {}
		return
	}
	s.requestShutdown = callback
}

// authenticate 校验 Authorization Bearer Token；WebSocket 因浏览器限制也可使用查询参数。
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		presented := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if presented == "" {
			presented = request.URL.Query().Get("token")
		}
		// 先比较长度再恒定时间比较，避免直接字符串比较泄漏令牌前缀。
		if len(presented) != len(s.token) || subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) != 1 {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// cors 允许嵌入式 Wails WebView 跨源访问本机 daemon，并响应预检请求。
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

// system 返回控制平面、Docker 隔离运行时、模型配置与 UI 技术栈概况。
func (s *Server) system(writer http.ResponseWriter, request *http.Request) {
	scheduler, err := s.agents.QueueStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"daemon":       map[string]string{"address": s.address, "version": buildinfo.Version},
		"docker":       s.sandboxes.Health(request.Context()),
		"modelGateway": map[string]any{"configured": s.gateway.Configured(), "model": s.gateway.ModelID(""), "probe": s.gateway.ProbeStatus(""), "defaultModel": s.gateway.DefaultProfile(), "models": s.gateway.Profiles()},
		"resources":    s.resources.Snapshot(),
		"scheduler":    scheduler,
		"stack":        []string{"Wails v3", "React 19", "Tailwind CSS 4", "Go daemon", "SQLite", "Docker SDK", "Pi RPC"},
	})
}

// probeModel 由系统概况的“重新读取并检测”触发。daemon 会原子加载最新
// .env，同时保留仍服务运行中任务的旧模型池；上游失败依旧以 200 和结构化
// 状态返回，避免将鉴权/522 等诊断伪装成平台接口故障。
func (s *Server) probeModel(writer http.ResponseWriter, request *http.Request) {
	profile := request.URL.Query().Get("profile")
	if s.modelConfigProbe != nil {
		status, err := s.modelConfigProbe(request.Context(), profile)
		if err != nil {
			writeError(writer, http.StatusBadRequest, fmt.Errorf("read latest model configuration: %w", err))
			return
		}
		writeJSON(writer, http.StatusOK, ModelProbeResult{ProbeStatus: status, ConfigLoaded: true})
		return
	}
	_ = s.gateway.Probe(request.Context(), profile)
	writeJSON(writer, http.StatusOK, ModelProbeResult{ProbeStatus: s.gateway.ProbeStatus(profile)})
}

// executionSettings 返回可由用户调整的本机执行队列上限。
func (s *Server) executionSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := s.agents.QueueStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, settings.Settings)
}

// updateExecutionSettings 只接受并发题目数；提高上限后 Agent Service 会自动调度队列。
func (s *Server) updateExecutionSettings(writer http.ResponseWriter, request *http.Request) {
	var settings platform.ExecutionSettings
	if err := decodeJSON(request, &settings); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	updated, err := s.agents.UpdateExecutionSettings(request.Context(), settings)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}

// modelUsage 返回 SQLite 聚合的模型请求与 Token 统计。
func (s *Server) modelUsage(writer http.ResponseWriter, request *http.Request) {
	report, err := s.store.ModelUsageReport(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

// listTasks 只返回用户可见的根任务，隐藏内部专项交接子任务。
func (s *Server) listTasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.store.ListRootTasks(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, tasks)
}

// listSubtasks 仅公开父题目自身派生的内部专项任务，用于协作状态面板。
func (s *Server) listSubtasks(writer http.ResponseWriter, request *http.Request) {
	tasks, err := s.agents.ListSubtasks(request.Context(), chi.URLParam(request, "taskID"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, tasks)
}

// createTask 严格解码输入并委托 Agent 服务验证题型、标题与描述。
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

// getTask 返回单个任务，并把 sql.ErrNoRows 映射为 404。
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

// taskPromptResponse 将可编辑的题目补充提示与只读系统 Prompt 分离。
// 系统策略始终由 daemon 构造，渲染层不能削弱其安全边界。
type taskPromptResponse struct {
	Prompt       string `json:"prompt"`
	SystemPrompt string `json:"systemPrompt"`
	Editable     bool   `json:"editable"`
	Retryable    bool   `json:"retryable"`
	Resumable    bool   `json:"resumable"`
}

// getTaskPrompt 返回当前补充提示、最终系统 Prompt 预览和可执行操作标志。
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

// updateTaskPromptRequest 是提示词更新接口唯一允许的字段。
type updateTaskPromptRequest struct {
	Prompt string `json:"prompt"`
}

// updateTaskPrompt 更新非运行中任务的补充提示，并返回最新任务实体。
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

// deleteTask 删除已结束任务的容器、工作区和数据库记录。
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

// uploadAttachments 接收一批文件及其目录相对路径，并写入任务 attachments/。
func (s *Server) uploadAttachments(writer http.ResponseWriter, request *http.Request) {
	// 同一请求最多 2 GiB；Multipart 超过 32 MiB 的部分由 net/http 落临时文件。
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

	// paths 与 files 必须一一对应，目录结构由 paths 保存而不是信任文件名。
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

	// 每个 Open 闭包延迟打开 multipart 文件，由服务层按顺序复制并关闭。
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

// startTask 请求创建沙箱并启动 Agent；实际启动状态通过事件流更新。
func (s *Server) startTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Start(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	task, err := s.store.GetTask(request.Context(), chi.URLParam(request, "taskID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	status, err := s.agents.QueueStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"status": task.Status, "scheduler": status})
}

// abortTask 中止当前 Agent 回合并把任务标记为取消。
func (s *Server) abortTask(writer http.ResponseWriter, request *http.Request) {
	if err := s.agents.Abort(request.Context(), chi.URLParam(request, "taskID")); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

// pauseTask 中止当前回合但保留 Pi 会话、容器与工作区。
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

// resumeTask 在原 RPC 会话中继续执行暂停任务。
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

// retryTask 释放已结束实例，并使用保留的工作区和最新提示重新启动。
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

// closeSandbox 仅释放已结束任务的容器与模型会话，保留题目资料和历史。
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

// listEvents 按 after 序号返回最多 5000 条持久化事件。
func (s *Server) listEvents(writer http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	events, err := s.store.ListEvents(request.Context(), chi.URLParam(request, "taskID"), after, 5000)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, events)
}

// listWorkspaceFiles 返回工作区普通文件的安全元数据。
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

// readWorkspaceFile 返回一个有界 UTF-8 文本预览或二进制标志。
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

// downloadWorkspaceFile 下载单个任务内普通文件。路径与符号链接边界复用
// 预览接口的服务方法，浏览器不会看到宿主机真实路径。
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

	// Content-Disposition 使用安全格式化函数编码文件名，响应体直接流式复制。
	name := filepath.Base(path)
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = io.Copy(writer, file)
}

// readWriteup 返回报告内容和后端统一识别的 Flag 结果；读取接口本身也会
// 触发一次检测，使升级前已完成的历史任务无需重跑即可获得候选。
func (s *Server) readWriteup(writer http.ResponseWriter, request *http.Request) {
	taskID := chi.URLParam(request, "taskID")
	flags := s.agents.DetectFlags(request.Context(), taskID)
	file, err := s.agents.ReadWorkspaceFile(request.Context(), taskID, "WRITEUP.md")
	if errors.Is(err, os.ErrNotExist) {
		// 兼容模型生成的 writeup.md、Writeup.md 等大小写变体。
		if files, listErr := s.agents.ListWorkspaceFiles(request.Context(), taskID); listErr == nil {
			for _, candidate := range files {
				if strings.EqualFold(candidate.Path, "WRITEUP.md") {
					file, err = s.agents.ReadWorkspaceFile(request.Context(), taskID, candidate.Path)
					break
				}
			}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusOK, map[string]any{"exists": false, "content": "", "flags": flags})
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
	writeJSON(writer, http.StatusOK, map[string]any{
		"exists": true, "content": file.Content, "truncated": file.Truncated,
		"binary": file.Binary, "flags": flags,
	})
}

// shutdownDaemon 在任何任务处于运行、创建中或暂停时拒绝退出，
// 防止桌面托盘动作遗留无人管理的自主沙箱。
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

	// 先返回 202 再请求 daemon 主循环退出。若在此直接调用 HTTP
	// Shutdown，可能等待当前 handler 自己结束，造成 Windows 上的后台残留。
	go s.requestShutdown()
}

// taskEvents 先重放 SQLite 历史，再持续转发 Hub 实时事件。
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

	// 订阅发生在查历史之前，避免历史查询窗口内的新事件丢失；
	// lastSequence 会过滤历史与实时流之间可能出现的重复。
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

// decodeJSON 将普通 JSON 请求体限制在 2 MiB，并拒绝未知字段。
func decodeJSON(request *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

// writeJSON 统一设置响应类型、状态码并编码 JSON。
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// writeError 使用固定 {"error": "..."} 结构返回错误。
func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

// writeSocket 将事件编码成单条 WebSocket 文本消息。
func writeSocket(ctx context.Context, connection *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, data)
}
