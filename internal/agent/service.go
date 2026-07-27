package agent

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

// 这些哨兵错误描述任务状态机的操作边界，API 层据此映射为 409 Conflict。
var (
	ErrTaskNotDeletable   = errors.New("only settled, failed, or cancelled tasks can be deleted")
	ErrSandboxNotClosable = errors.New("only a settled, failed, or cancelled task instance can be closed")
	ErrAttachmentsLocked  = errors.New("attachments cannot be changed while a task is provisioning or running")
	ErrPromptLocked       = errors.New("the task prompt cannot be changed while the agent is running")
	ErrTaskNotRetryable   = errors.New("only a settled, failed, or cancelled task can be retried")
	ErrTaskNotPausable    = errors.New("only a running task can be paused")
	ErrTaskNotResumable   = errors.New("only a paused task can be resumed")
)

// Service 是任务编排核心：连接 SQLite、事件 Hub、Docker 沙箱和模型网关。
type Service struct {
	store      *storage.Store
	hub        *eventhub.Hub
	sandboxes  *sandbox.Manager
	gateway    modelgateway.Manager
	workspaces string
	publicURL  string
	mu         sync.Mutex
	// schedulerMu 串行化“检查名额 → 写入状态 → 创建容器”流程，避免并发 HTTP
	// 请求绕过全局上限或为同一个排队任务重复创建 Pi 沙箱。
	schedulerMu sync.Mutex
	// delegationMu 串行化父子 Agent 的请求消费、结果回传和父任务恢复，
	// 避免多个子 Agent 同时结束时重复重启父容器。
	delegationMu sync.Mutex
	tokens       map[string]string
	settled      map[string]bool
	paused       map[string]bool
	flagBuffers  map[string]string
	flagFindings map[string]map[string]bool
	// flagFindingLoaded 标记已从持久事件恢复过去的识别结果，避免重试或
	// 打开历史任务时重复追加完全相同的候选事件。
	flagFindingLoaded map[string]bool
}

// 预览与 Flag 提取都采用有界读取，避免前端或正则处理超大 Agent 产物。
const maxWorkspacePreviewBytes = 1 << 20
const maxWriteupFlagBytes = 4 << 20

// WorkspaceFile 是可安全展示的工作区文件元数据，原文件仍留在本机。
type WorkspaceFile struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// WorkspaceFileContent 是最多 1 MiB 的文件预览结果。
type WorkspaceFileContent struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// AttachmentUpload 由 API 层提供请求期读取器，目标路径必须解析到 attachments/ 下。
type AttachmentUpload struct {
	Path string
	Open func() (io.ReadCloser, error)
}

// NewService 注入全部基础设施，并初始化题目 Token、结束状态和暂停状态索引。
func NewService(store *storage.Store, hub *eventhub.Hub, sandboxes *sandbox.Manager, gateway modelgateway.Manager, workspaces, publicURL string) *Service {
	return &Service{
		store: store, hub: hub, sandboxes: sandboxes, gateway: gateway,
		workspaces: workspaces, publicURL: publicURL,
		tokens: make(map[string]string), settled: make(map[string]bool), paused: make(map[string]bool),
		flagBuffers: make(map[string]string), flagFindings: make(map[string]map[string]bool),
		flagFindingLoaded: make(map[string]bool),
	}
}

// CreateTask 校验用户输入、选择专项镜像、持久化任务并写入首条创建事件。

// RecordModelError 实现模型网关的错误回调，将上游失败持久化后推送给前端。
func (s *Service) RecordModelError(ctx context.Context, taskID string, statusCode int, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "model upstream request failed"
	}
	payload := map[string]any{"error": message}
	if statusCode != 0 {
		payload["statusCode"] = statusCode
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "model", Type: "model.request_failed", Payload: platform.JSONPayload(payload)})
}
func (s *Service) CreateTask(ctx context.Context, input platform.CreateTask) (platform.Task, error) {
	category, err := platform.ParseCategory(input.Category)
	if err != nil {
		return platform.Task{}, err
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Description) == "" {
		return platform.Task{}, fmt.Errorf("title and description are required")
	}

	profile, ok := s.gateway.Profile(input.ModelProfile)
	if !ok {
		return platform.Task{}, fmt.Errorf("unknown model profile %q", strings.TrimSpace(input.ModelProfile))
	}
	// 任务 ID、状态、镜像和时间均由 daemon 生成，不能由前端伪造。
	now := time.Now()
	task := platform.Task{
		ID: platform.NewID("task"), Title: strings.TrimSpace(input.Title), Category: category,
		Description: strings.TrimSpace(input.Description), Target: strings.TrimSpace(input.Target),
		FlagFormat: strings.TrimSpace(input.FlagFormat), ModelProfile: profile.Name, ModelID: profile.ModelID, Status: platform.TaskReady,
		Image: sandbox.ImageFor(category), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateTask(ctx, task); err != nil {
		return platform.Task{}, err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "system", Type: "task.created", Payload: platform.JSONPayload(task)})
	return task, nil
}

// Start 根据全局执行上限立即启动任务，或将它持久化为 queued 等待队列。
// 进入队列的任务会在任何运行名额释放后由 DispatchQueued 自动拉起。
func (s *Service) Start(ctx context.Context, taskID string) error {
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	return s.startOrQueueLocked(ctx, taskID)
}

// startOrQueueLocked 必须在 schedulerMu 保护下调用。
func (s *Service) startOrQueueLocked(ctx context.Context, taskID string) error {
	// 防止为正在创建、运行或暂停的任务重复创建容器。
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("task %s was not found", taskID)
		}
		return err
	}
	if task.Status == platform.TaskRunning || task.Status == platform.TaskProvisioning || task.Status == platform.TaskPaused || task.Status == platform.TaskDelegating {
		return fmt.Errorf("task is already running")
	}
	if task.Status == platform.TaskQueued {
		// 用户再次点击排队任务时不重复入队，而是立即尝试调度队首。
		return s.dispatchQueuedLocked(ctx)
	}
	settings, err := s.store.ExecutionSettings(ctx)
	if err != nil {
		return err
	}
	active, err := s.store.CountActiveTasks(ctx)
	if err != nil {
		return err
	}
	if active >= settings.MaxConcurrentTasks {
		if err := s.store.UpdateTaskState(ctx, taskID, platform.TaskQueued, "", "", ""); err != nil {
			return err
		}
		status, _ := s.queueStatus(ctx)
		position := status.QueuedTaskCount
		for _, queued := range status.Queue {
			if queued.TaskID == taskID {
				position = queued.Position
				break
			}
		}
		_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "scheduler", Type: "task.queued", Payload: platform.JSONPayload(map[string]any{
			"position": position, "active": active, "limit": settings.MaxConcurrentTasks,
		})})
		return nil
	}
	return s.startNow(ctx, task)
}

// startNow 为已获取运行名额的任务签发模型 Token、创建 Docker 沙箱并启动读取协程。
// 调用方负责持有 schedulerMu，确保在 Docker 创建期间其他请求不会越过容量上限。
func (s *Service) startNow(ctx context.Context, task platform.Task) error {
	// 在创建任何 Docker 容器、签发任务 Token 前，先验证真实模型接口是否可用。
	// 这样 522、鉴权错误或错误模型名会直接反馈给用户，而不会被 Pi 表现为
	// “Agent 空闲 / 本轮结束”。
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "model", Type: "model.probe_started", Payload: platform.JSONPayload(map[string]string{"model": s.gateway.ModelID(task.ModelProfile)})})
	if err := s.gateway.Probe(ctx, task.ModelProfile); err != nil {
		message := "model connection check failed: " + err.Error()
		_ = s.store.UpdateTaskState(ctx, task.ID, task.Status, task.Runtime, task.ContainerID, message)
		_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "model", Type: "model.probe_failed", Payload: platform.JSONPayload(map[string]string{"error": err.Error()})})
		return fmt.Errorf("%s", message)
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "model", Type: "model.probe_succeeded", Payload: platform.JSONPayload(map[string]string{"model": s.gateway.ModelID(task.ModelProfile)})})

	// 每次启动都签发新 Token，并重置本进程内的结束/暂停标志。
	token, err := s.gateway.Issue(task.ID, task.ModelProfile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.tokens[task.ID] = token
	s.settled[task.ID] = false
	delete(s.paused, task.ID)
	s.mu.Unlock()
	if err := s.store.UpdateTaskState(ctx, task.ID, platform.TaskProvisioning, "", "", ""); err != nil {
		s.gateway.Revoke(token)
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "sandbox", Type: "sandbox.provisioning", Payload: platform.JSONPayload(map[string]string{"image": task.Image})})

	// Prompt 由 daemon 固定策略与题目数据组合；容器只能通过宿主模型网关联网调用模型。
	workspace := filepath.Join(s.workspaces, task.ID)
	prompt := buildPrompt(task)
	session, err := s.sandboxes.Start(context.Background(), sandbox.StartConfig{
		Task: task, Workspace: workspace, Prompt: prompt,
		Model:   sandbox.ModelAccess{BaseURL: s.publicURL + "/model", Token: token, ModelID: s.gateway.ModelID(task.ModelProfile), SupportsImages: s.gateway.SupportsImages(task.ModelProfile)},
		Network: true,
	})
	if err != nil {
		// 创建失败时撤销短期 Token、持久化失败状态并广播原因。
		s.gateway.Revoke(token)
		_ = s.store.UpdateTaskState(ctx, task.ID, platform.TaskFailed, "", "", err.Error())
		_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "sandbox", Type: "task.failed", Payload: platform.JSONPayload(map[string]string{"error": err.Error()})})
		return err
	}
	if err := s.store.UpdateTaskState(ctx, task.ID, platform.TaskRunning, session.Runtime, session.ContainerID, ""); err != nil {
		_ = s.sandboxes.Stop(context.Background(), task.ID, true)
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "sandbox", Type: "sandbox.started", Payload: platform.JSONPayload(map[string]string{
		"containerId": session.ContainerID, "runtime": session.Runtime,
	})})

	// stdout 是 Pi JSONL 协议，stderr 是普通诊断文本，必须分开读取。
	go s.readRPC(task, session.Stdout)
	go s.readStderr(task.ID, session.Stderr)
	return nil
}

// QueueStatus 返回 UI 所需的执行上限、运行占用和 FIFO 排队详情。
func (s *Service) QueueStatus(ctx context.Context) (platform.SchedulerStatus, error) {
	return s.queueStatus(ctx)
}

func (s *Service) queueStatus(ctx context.Context) (platform.SchedulerStatus, error) {
	settings, err := s.store.ExecutionSettings(ctx)
	if err != nil {
		return platform.SchedulerStatus{}, err
	}
	active, err := s.store.CountActiveTasks(ctx)
	if err != nil {
		return platform.SchedulerStatus{}, err
	}
	queued, err := s.store.ListQueuedTasks(ctx)
	if err != nil {
		return platform.SchedulerStatus{}, err
	}
	status := platform.SchedulerStatus{Settings: settings, ActiveTaskCount: active, QueuedTaskCount: len(queued), Queue: make([]platform.QueuedTask, 0, len(queued))}
	for index, task := range queued {
		status.Queue = append(status.Queue, platform.QueuedTask{TaskID: task.ID, Title: task.Title, Category: task.Category, Position: index + 1, Internal: task.ParentTaskID != "", QueuedAt: task.UpdatedAt})
	}
	return status, nil
}

// UpdateExecutionSettings 保存用户手动选择的并发上限；提高上限后立即尝试
// 拉起队首任务，降低上限不会强制终止已经运行的题目。
func (s *Service) UpdateExecutionSettings(ctx context.Context, settings platform.ExecutionSettings) (platform.ExecutionSettings, error) {
	if err := settings.Validate(); err != nil {
		return platform.ExecutionSettings{}, err
	}
	s.schedulerMu.Lock()
	err := s.store.UpdateExecutionSettings(ctx, settings)
	s.schedulerMu.Unlock()
	if err != nil {
		return platform.ExecutionSettings{}, err
	}
	go func() { _ = s.DispatchQueued(context.Background()) }()
	return settings, nil
}

// DispatchQueued 在启动时、任务结束后和提高上限后调用，尽可能填满空闲名额。
func (s *Service) DispatchQueued(ctx context.Context) error {
	s.schedulerMu.Lock()
	defer s.schedulerMu.Unlock()
	return s.dispatchQueuedLocked(ctx)
}

func (s *Service) dispatchQueuedLocked(ctx context.Context) error {
	for {
		status, err := s.queueStatus(ctx)
		if err != nil || status.ActiveTaskCount >= status.Settings.MaxConcurrentTasks || len(status.Queue) == 0 {
			return err
		}
		next, err := s.store.GetTask(ctx, status.Queue[0].TaskID)
		if err != nil {
			return err
		}
		if err := s.startNow(ctx, next); err != nil {
			// Docker/image 错误会把当前任务标为 failed，可继续尝试后续队列；
			// 模型网关未配置等前置错误会保留 queued，等待用户修复配置。
			current, getErr := s.store.GetTask(ctx, next.ID)
			if getErr == nil && current.Status == platform.TaskFailed {
				continue
			}
			return err
		}
	}
}

// requestQueueDispatch 避免 RPC 读取协程因启动下一题而阻塞。
func (s *Service) requestQueueDispatch() { go func() { _ = s.DispatchQueued(context.Background()) }() }

// Abort 中止当前 Pi 回合并把任务置为 cancelled，但保留容器和工作区供检查。
func (s *Service) Abort(ctx context.Context, taskID string) error {
	// 排队任务尚未创建容器，直接取消队列意图即可。
	queuedTask, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if queuedTask.Status == platform.TaskQueued {
		if err := s.store.UpdateTaskState(ctx, taskID, platform.TaskCancelled, "", "", ""); err != nil {
			return err
		}
		_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "scheduler", Type: "task.cancelled", Payload: platform.JSONPayload(map[string]string{"reason": "user cancelled queued task"})})
		if queuedTask.ParentTaskID != "" {
			go func() { _ = s.finishGenericSubtask(queuedTask, "cancelled", "子任务在排队期间被取消") }()
		}
		return nil
	}
	if queuedTask.Status == platform.TaskDelegating {
		// 父实例已释放；先标记父任务取消，再逐个取消/中止其子 Agent。
		// 子任务的回传逻辑会看到父任务已取消，因此只归档结果而不会恢复父任务。
		if err := s.store.UpdateTaskState(ctx, taskID, platform.TaskCancelled, "", "", ""); err != nil {
			return err
		}
		children, err := s.store.ListChildTasks(ctx, taskID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if isFinished(child.Status) {
				continue
			}
			if child.Status == platform.TaskReady {
				_ = s.store.UpdateTaskState(ctx, child.ID, platform.TaskCancelled, "", "", "parent task cancelled")
				go func(task platform.Task) { _ = s.finishGenericSubtask(task, "cancelled", "父任务已取消") }(child)
				continue
			}
			_ = s.Abort(ctx, child.ID)
		}
		s.emitDelegationEvent(taskID, "delegation.cancelled", map[string]string{"message": "已取消父任务及其未完成子 Agent"})
		s.requestQueueDispatch()
		return nil
	}
	if err := s.sandboxes.Abort(ctx, taskID); err != nil {
		return err
	}
	task, _ := s.store.GetTask(ctx, taskID)
	s.mu.Lock()
	delete(s.paused, taskID)
	s.settled[taskID] = true
	s.mu.Unlock()
	_ = s.store.UpdateTaskState(ctx, taskID, platform.TaskCancelled, task.Runtime, task.ContainerID, "")
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "system", Type: "task.cancelled", Payload: platform.JSONPayload(map[string]string{"reason": "user requested abort"})})

	// 内部子任务取消后仍需回传已经产生的报告与产物。
	if task.ParentTaskID != "" {
		go func() { _ = s.finishGenericSubtask(task, "cancelled", "专项子任务已取消") }()
	}
	s.requestQueueDispatch()
	return nil
}

// Pause 只中止 Pi 当前回合，保留沙箱、会话、工作区和产物。
// 后续 Resume 会继续同一 Pi 会话，因此模型对话上下文仍然可用。
func (s *Service) Pause(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != platform.TaskRunning {
		return fmt.Errorf("%w (current status: %s)", ErrTaskNotPausable, task.Status)
	}

	// 发送 abort 前先标记暂停。Pi 可能立即发出 agent_settled，
	// markSettled 必须据此保留 paused，而不能错误转换为 settled。
	s.mu.Lock()
	s.paused[taskID] = true
	s.settled[taskID] = true
	s.mu.Unlock()
	if err := s.sandboxes.Abort(ctx, taskID); err != nil {
		s.mu.Lock()
		delete(s.paused, taskID)
		s.settled[taskID] = false
		s.mu.Unlock()
		return err
	}
	if err := s.store.UpdateTaskState(ctx, taskID, platform.TaskPaused, task.Runtime, task.ContainerID, ""); err != nil {
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "system", Type: "task.paused", Payload: platform.JSONPayload(map[string]string{"message": "Pi 当前回合已暂停；容器、会话和工作区会继续保留"})})
	return nil
}

// Resume 唤醒已有 Pi RPC 进程而不是创建新沙箱。
// 暂停期间保存的最新补充提示会作为用户消息立即提供给 Agent。
func (s *Service) Resume(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status != platform.TaskPaused {
		return fmt.Errorf("%w (current status: %s)", ErrTaskNotResumable, task.Status)
	}
	message := "操作员已恢复当前解题任务。请从已有会话、/workspace 中的附件和 artifacts 继续分析；不要丢弃已完成的工作。"
	if extra := strings.TrimSpace(task.Prompt); extra != "" {
		message += "\n\n操作员在暂停期间补充的信息：\n" + extra
	}

	// 先确认消息已送达原会话，再更新持久化状态，避免 UI 显示“运行中”但 RPC 不存在。
	if err := s.sandboxes.Prompt(ctx, taskID, message); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.paused, taskID)
	s.settled[taskID] = false
	s.mu.Unlock()
	if err := s.store.UpdateTaskState(ctx, taskID, platform.TaskRunning, task.Runtime, task.ContainerID, ""); err != nil {
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "system", Type: "task.resumed", Payload: platform.JSONPayload(map[string]string{"message": "已使用原容器与原 Pi 会话继续解题"})})
	return nil
}

// UpdatePrompt 保存操作员给下一次运行的补充方向。
// Pi 运行时锁定修改，因为此时系统 Prompt 已生成，中途编辑不会生效且会误导用户。
func (s *Service) UpdatePrompt(ctx context.Context, taskID, prompt string) (platform.Task, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return platform.Task{}, err
	}
	if task.Status == platform.TaskProvisioning || task.Status == platform.TaskRunning || task.Status == platform.TaskDelegating {
		return platform.Task{}, ErrPromptLocked
	}
	prompt = strings.TrimSpace(prompt)
	if len(prompt) > 32*1024 {
		return platform.Task{}, fmt.Errorf("prompt is too large (maximum 32 KiB)")
	}

	// 32 KiB 上限既限制数据库大小，也避免把过量补充内容塞进模型上下文。
	if err := s.store.UpdateTaskPrompt(ctx, taskID, prompt); err != nil {
		return platform.Task{}, err
	}
	task.Prompt = prompt
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "system", Type: "task.prompt_updated", Payload: platform.JSONPayload(map[string]string{"message": "task prompt updated"})})
	return task, nil
}

// Retry 释放旧的终态沙箱，并使用最新 Prompt 重新启动 Pi。
// 工作区继续保留，让 Agent 能复用附件与此前产物。
func (s *Service) Retry(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !isFinished(task.Status) {
		return fmt.Errorf("%w (current status: %s)", ErrTaskNotRetryable, task.Status)
	}
	if err := s.CloseSandbox(ctx, taskID); err != nil {
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "system", Type: "task.retry_requested", Payload: platform.JSONPayload(map[string]string{"message": "starting another attempt with the latest prompt"})})
	return s.Start(ctx, taskID)
}

// StoreAttachments 把上传文件复制到任务隔离工作区。
// 保留文件夹相对路径，但在写宿主机前拒绝绝对路径和目录穿越。
func (s *Service) StoreAttachments(ctx context.Context, taskID string, uploads []AttachmentUpload) ([]WorkspaceFile, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == platform.TaskProvisioning || task.Status == platform.TaskRunning {
		return nil, ErrAttachmentsLocked
	}
	if len(uploads) == 0 {
		return []WorkspaceFile{}, nil
	}
	// 所有文件统一落在 <task>/attachments，容器中对应 /workspace/attachments。
	workspace, err := s.taskWorkspace(ctx, taskID)
	if err != nil {
		return nil, err
	}
	attachmentsRoot := filepath.Join(workspace, "attachments")
	if err := os.MkdirAll(attachmentsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create attachments directory: %w", err)
	}
	saved := make([]WorkspaceFile, 0, len(uploads))
	// 每个文件先验证目标、创建父目录，再以截断方式复制并显式检查 Close 错误。
	for _, upload := range uploads {
		target, relative, err := resolveAttachmentPath(attachmentsRoot, upload.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, fmt.Errorf("create attachment directory: %w", err)
		}
		source, err := upload.Open()
		if err != nil {
			return nil, fmt.Errorf("open uploaded attachment: %w", err)
		}
		destination, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if createErr != nil {
			_ = source.Close()
			return nil, fmt.Errorf("create attachment: %w", createErr)
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := destination.Close()
		_ = source.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("store attachment: %w", copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close attachment: %w", closeErr)
		}
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		saved = append(saved, WorkspaceFile{Path: filepath.ToSlash(filepath.Join("attachments", relative)), Size: info.Size(), ModifiedAt: info.ModTime()})
	}
	return saved, nil
}

// Delete 删除已结束任务的沙箱、工作区、SQLite 任务行及级联事件。
// 活跃任务会被明确拒绝，避免清理与 Pi 写文件并发。
func (s *Service) Delete(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !isFinished(task.Status) {
		return fmt.Errorf("%w (current status: %s)", ErrTaskNotDeletable, task.Status)
	}
	if err := s.sandboxes.Remove(ctx, task.ID, task.ContainerID); err != nil {
		return err
	}
	if err := s.removeWorkspace(task.ID); err != nil {
		return err
	}
	// 文件和容器清理成功后再撤销内存状态与模型 Token，最后删除数据库事实记录。
	s.mu.Lock()
	token := s.tokens[task.ID]
	delete(s.tokens, task.ID)
	delete(s.settled, task.ID)
	delete(s.paused, task.ID)
	delete(s.flagFindings, task.ID)
	delete(s.flagFindingLoaded, task.ID)
	for key := range s.flagBuffers {
		if strings.HasPrefix(key, task.ID+"|") {
			delete(s.flagBuffers, key)
		}
	}
	s.mu.Unlock()
	if token != "" {
		s.gateway.Revoke(token)
	}
	return s.store.DeleteTask(ctx, task.ID)
}

// CloseSandbox 释放已结束任务的 Docker 实例，但保留工作区、Writeup 和事件历史。
func (s *Service) CloseSandbox(ctx context.Context, taskID string) error {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if !isFinished(task.Status) {
		return fmt.Errorf("%w (current status: %s)", ErrSandboxNotClosable, task.Status)
	}
	s.mu.Lock()
	token := s.tokens[task.ID]
	delete(s.tokens, task.ID)
	// 关闭终态会话也会终止 RPC 流。预先标记 settled，防止读取协程
	// 把正常关闭错误覆盖成“流意外中断”。
	s.settled[task.ID] = true
	s.mu.Unlock()
	if token != "" {
		s.gateway.Revoke(token)
	}
	if err := s.sandboxes.Remove(ctx, task.ID, task.ContainerID); err != nil {
		return err
	}
	if err := s.store.UpdateTaskState(ctx, task.ID, task.Status, task.Runtime, "", task.LastError); err != nil {
		return err
	}
	_, _ = s.emit(ctx, platform.Event{TaskID: task.ID, Source: "sandbox", Type: "sandbox.stopped", Payload: platform.JSONPayload(map[string]string{"reason": "user released finished sandbox"})})
	return nil
}

// ListSubtasks 返回根题目当前生命周期中创建的内部专项任务，供父题目的协作面板
// 展示；子任务本身不会出现在全局题目列表中。
func (s *Service) ListSubtasks(ctx context.Context, taskID string) ([]platform.Task, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.ParentTaskID != "" {
		return nil, fmt.Errorf("subtasks can only be listed for a root task")
	}
	return s.store.ListChildTasks(ctx, taskID)
}

// ListWorkspaceFiles 最多返回 500 个普通文件，使桌面端能浏览脚本和产物，
// 同时不暴露任意宿主机路径。
func (s *Service) ListWorkspaceFiles(ctx context.Context, taskID string) ([]WorkspaceFile, error) {
	root, err := s.taskWorkspace(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []WorkspaceFile{}, nil
	} else if err != nil {
		return nil, err
	}
	files := make([]WorkspaceFile, 0)
	// 遍历时跳过目录、符号链接和设备等特殊文件，只返回相对路径元数据。
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(files) >= 500 {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, WorkspaceFile{Path: filepath.ToSlash(relative), Size: info.Size(), ModifiedAt: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list task workspace: %w", err)
	}
	return files, nil
}

// ReadWorkspaceFile 返回普通文件的有界 UTF-8 预览，并拒绝目录穿越及
// 指向任务工作区之外的符号链接。
func (s *Service) ReadWorkspaceFile(ctx context.Context, taskID, relativePath string) (WorkspaceFileContent, error) {
	root, err := s.taskWorkspace(ctx, taskID)
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	path, err := resolveWorkspaceFile(root, relativePath)
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	if !info.Mode().IsRegular() {
		return WorkspaceFileContent{}, fmt.Errorf("workspace path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	defer file.Close()
	// 多读一个字节用于准确判断截断，不把二进制内容强行转成字符串。
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspacePreviewBytes+1))
	if err != nil {
		return WorkspaceFileContent{}, err
	}
	preview := WorkspaceFileContent{Path: filepath.ToSlash(relativePath), Truncated: int64(len(data)) > maxWorkspacePreviewBytes}
	if preview.Truncated {
		data = data[:maxWorkspacePreviewBytes]
	}
	if !utf8.Valid(data) {
		preview.Binary = true
		return preview, nil
	}
	preview.Content = string(data)
	return preview, nil
}

// OpenWorkspaceFile 返回任务内普通文件的只读句柄。
// 下载与预览复用同一路径及符号链接边界检查。
func (s *Service) OpenWorkspaceFile(ctx context.Context, taskID, relativePath string) (*os.File, error) {
	root, err := s.taskWorkspace(ctx, taskID)
	if err != nil {
		return nil, err
	}
	path, err := resolveWorkspaceFile(root, relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace path is not a regular file")
	}
	return os.Open(path)
}

// taskWorkspace 先确认任务存在，再返回由可信任务 ID 派生的工作区路径。
func (s *Service) taskWorkspace(ctx context.Context, taskID string) (string, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.workspaces, task.ID), nil
}

// removeWorkspace 在递归删除前验证最终绝对路径确实是工作区根目录的后代。
func (s *Service) removeWorkspace(taskID string) error {
	root, err := filepath.Abs(s.workspaces)
	if err != nil {
		return err
	}
	workspace, err := filepath.Abs(filepath.Join(root, taskID))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, workspace)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("invalid task workspace path")
	}
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("remove task workspace: %w", err)
	}
	return nil
}

// resolveWorkspaceFile 对预览/下载路径执行词法边界与真实符号链接边界双重检查。
func resolveWorkspaceFile(root, requested string) (string, error) {
	// 第一层拒绝空路径、绝对路径和显式 ../ 穿越。
	requested = filepath.Clean(filepath.FromSlash(strings.TrimSpace(requested)))
	if requested == "." || requested == ".." || filepath.IsAbs(requested) || strings.HasPrefix(requested, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid workspace file path")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, requested)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid workspace file path")
	}
	// 第二层解析符号链接，并再次验证解析后文件仍在解析后的工作区根内。
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRelative) {
		return "", fmt.Errorf("workspace file resolves outside task workspace")
	}
	return resolvedCandidate, nil
}

// resolveAttachmentPath 规范化上传携带的目录相对路径，并确保目标位于 attachments/。
func resolveAttachmentPath(root, requested string) (string, string, error) {
	requested = filepath.Clean(filepath.FromSlash(strings.TrimSpace(requested)))
	if requested == "." || requested == ".." || filepath.IsAbs(requested) || strings.HasPrefix(requested, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("invalid attachment path")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.Abs(filepath.Join(root, requested))
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", fmt.Errorf("invalid attachment path")
	}
	return target, relative, nil
}

// readRPC 按行解析 Pi stdout 的 JSONL 协议，持久化并广播标准化事件。
func (s *Service) readRPC(task platform.Task, reader io.Reader) {
	// Pi 工具输出可能较长，允许单条事件最多 16 MiB。
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		if !json.Valid(line) {
			// 非法 JSON 不终止会话，记录协议错误后继续读取后续事件。
			_, _ = s.emit(context.Background(), platform.Event{TaskID: task.ID, Source: "pi", Type: "agent.protocol_error", Payload: platform.JSONPayload(map[string]string{"line": string(line)})})
			continue
		}
		event := normalize(task.ID, line)
		_, _ = s.emit(context.Background(), event)
		s.detectEventFlags(task, event)
		if event.Type == "agent.settled" {
			s.markSettled(task.ID)
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = s.emit(context.Background(), platform.Event{TaskID: task.ID, Source: "pi", Type: "agent.stream_error", Payload: platform.JSONPayload(map[string]string{"error": err.Error()})})
	}
	// 若未收到 agent.settled 且并非用户取消，RPC 关闭被视为任务失败。
	s.mu.Lock()
	wasSettled := s.settled[task.ID]
	s.mu.Unlock()
	current, getErr := s.store.GetTask(context.Background(), task.ID)
	if !wasSettled && getErr == nil && current.Status != platform.TaskCancelled {
		_ = s.store.UpdateTaskState(context.Background(), task.ID, platform.TaskFailed, "", "", "Pi RPC stream closed unexpectedly")
		_, _ = s.emit(context.Background(), platform.Event{TaskID: task.ID, Source: "system", Type: "task.failed", Payload: platform.JSONPayload(map[string]string{"error": "Pi RPC stream closed unexpectedly"})})
		if current.ParentTaskID != "" {
			go func() { _ = s.finishGenericSubtask(current, "failed", "Pi RPC 流意外关闭") }()
		}
		s.requestQueueDispatch()
	}
}

// readStderr 将 Pi 标准错误逐行转为事件，供前端终端转录和问题排查。
func (s *Service) readStderr(taskID string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 16*1024), 2*1024*1024)
	for scanner.Scan() {
		_, _ = s.emit(context.Background(), platform.Event{TaskID: taskID, Source: "pi", Type: "agent.stderr", Payload: platform.JSONPayload(map[string]string{"text": scanner.Text()})})
	}
}

// markSettled 幂等处理 Pi 的 agent.settled 事件，并触发 Flag 检测或专项交接。
func (s *Service) markSettled(taskID string) {
	s.mu.Lock()
	if s.paused[taskID] {
		// 暂停流程主动 abort，Pi 随后会发出 agent_settled；
		// 在 Resume 调用前必须保持持久化 paused 状态。
		s.settled[taskID] = true
		s.mu.Unlock()
		return
	}
	if s.settled[taskID] {
		s.mu.Unlock()
		return
	}
	s.settled[taskID] = true
	s.mu.Unlock()
	task, _ := s.store.GetTask(context.Background(), taskID)
	_ = s.store.UpdateTaskState(context.Background(), taskID, platform.TaskSettled, task.Runtime, task.ContainerID, "")
	// 后续委派检查需要看到已持久化的终态；内存副本也同步更新，避免把
	// 刚刚运行中的旧状态误判为不可委派。
	task.Status = platform.TaskSettled
	_, _ = s.emit(context.Background(), platform.Event{TaskID: taskID, Source: "system", Type: "task.settled", Payload: platform.JSONPayload(map[string]string{"message": "Pi is idle and the sandbox remains available"})})
	// Pi 可能在 settled 前一刻才落盘或 rename 报告，立即检测并进行短暂
	// 退避重试，覆盖 Windows/Docker 文件同步的时间差。
	s.scheduleFlagDetection(taskID)
	// 专项子任务完成后回传父任务；根任务则检查通用的最多三子 Agent 委派。
	if task.ParentTaskID != "" {
		go func() { _ = s.finishGenericSubtask(task, "completed", "") }()
		s.requestQueueDispatch()
		return
	}
	if s.startRequestedSubtasks(task) {
		s.requestQueueDispatch()
		return
	}
	// 主 Agent 本轮暂时空闲但仍有子 Agent 在排队或运行时，保留父容器和
	// 运行名额。这样子任务回传可以直接唤醒同一 Pi 会话，不会在全局容量已满
	// 时额外创建第 N+1 个活跃实例。
	if open, err := s.hasOpenSubtasks(task.ID); err == nil && open {
		_ = s.store.UpdateTaskState(context.Background(), taskID, platform.TaskRunning, task.Runtime, task.ContainerID, "")
		s.emitDelegationEvent(taskID, "delegation.parent_waiting", map[string]string{"message": "主 Agent 保留会话，继续等待并验证子 Agent 的并行结果"})
		s.requestQueueDispatch()
		return
	}
	s.requestQueueDispatch()
}

// isFinished 判断任务是否进入允许关闭实例、重试或删除的终态。
func isFinished(status platform.TaskStatus) bool {
	return status == platform.TaskSettled || status == platform.TaskFailed || status == platform.TaskCancelled
}

// emit 先把事件写入 SQLite，再向实时 Hub 广播，确保实时消息都有持久化来源。
func (s *Service) emit(ctx context.Context, event platform.Event) (platform.Event, error) {
	stored, err := s.store.AppendEvent(ctx, event)
	if err == nil {
		s.hub.Publish(stored)
	}
	return stored, err
}

// normalize 将 Pi RPC 原始事件类型映射到平台稳定事件命名，同时保留原始载荷。
func normalize(taskID string, raw []byte) platform.Event {
	var envelope struct {
		Type       string          `json:"type"`
		TurnID     string          `json:"turnId"`
		ToolCallID string          `json:"toolCallId"`
		Inner      json.RawMessage `json:"assistantMessageEvent"`
	}
	_ = json.Unmarshal(raw, &envelope)
	eventType := map[string]string{
		"agent_start": "agent.started", "agent_end": "agent.ended", "agent_settled": "agent.settled",
		"turn_start": "agent.turn_started", "turn_end": "agent.turn_completed",
		"message_end": "agent.message.completed", "tool_execution_start": "tool.started",
		"tool_execution_update": "tool.output", "tool_execution_end": "tool.completed",
		"auto_retry_start": "agent.retrying", "compaction_start": "agent.compacting",
		"extension_error": "agent.extension_error",
	}[envelope.Type]
	if eventType == "" {
		eventType = "pi." + envelope.Type
	}
	// message_update 的具体类别嵌套在 assistantMessageEvent 中，需要二次解析。
	if envelope.Type == "message_update" {
		var inner struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(envelope.Inner, &inner)
		switch inner.Type {
		case "text_delta":
			eventType = "agent.message.delta"
		case "thinking_delta":
			eventType = "agent.thinking.delta"
		default:
			eventType = "agent.message.updated"
		}
	}
	return platform.Event{TaskID: taskID, Source: "pi", Type: eventType, TurnID: envelope.TurnID, ToolCallID: envelope.ToolCallID, Payload: bytes.Clone(raw)}
}

// buildPrompt 把题目数据、补充提示、交接规则、安全边界与 Writeup 规范
// 组合成完全由 daemon 控制的最终系统 Prompt。
func buildPrompt(task platform.Task) string {
	extraPrompt := strings.TrimSpace(task.Prompt)
	if extraPrompt == "" {
		extraPrompt = "（无）"
	}
	// 根 Agent 可在有明确证据时委派最多三个互不冲突的专项子任务；子 Agent
	// 不得再次委派。所有请求由 daemon 审核，并由独立容器执行。
	handoffInstruction := ""
	if task.ParentTaskID == "" {
		handoffInstruction = `

【主 Agent：受控子 Agent 协作】
你负责本题全局判断、拆分、主线解题、证据整合和最终 Writeup。不要一开始就创建子 Agent；应先独立完成题目初判、附件盘点和最小验证。主 Agent 必须亲自持续推进并验证最可能的主路线；子 Agent 只用于扩大搜索宽度，绝不替代主 Agent，也不是主 Agent 停止解题的理由。

当出现以下有明确证据的情形时，可以委派：多个互不依赖的文件/密文需要并行分析、Web 存在两个以上可独立验证的攻击面或利用路线、附件中的不同模块需要不同专项工具、或当前方向有明确阻塞且另一专项方向可验证地处理其中一个子问题。对 Web 题，已发现多个独立接口、源码模块、参数入口或候选漏洞链时，应优先将其中边界清晰且不影响主线的一条路线交给子 Agent，同时你继续验证优先级最高的路线。

每个父题目在整个生命周期最多可创建 3 个子 Agent。子任务必须边界清晰、相互独立，不能仅仅让多个 Agent 重复猜同一条路线。先把必要参数、样本和中间结果写入 /workspace/artifacts，然后为每个子任务在 /workspace/.cpi/subtasks/requests/ 创建一个 JSON 文件，例如 /workspace/.cpi/subtasks/requests/01-crypto.json：
{
  "category": "crypto",
  "title": "RSA 参数求解",
  "question": "只解决需要恢复私钥/明文的数学问题",
  "summary": "已确认的参数含义、已尝试方法和当前阻塞点",
  "artifactPaths": ["artifacts/rsa-params.txt", "artifacts/cipher.bin"],
  "expectedOutput": ["明文或私钥", "可复现脚本", "验证步骤"]
}

category 只能是 web、pwn、reverse、crypto、forensics、misc；artifactPaths 只能引用当前工作区内实际存在的普通文件，不得使用绝对路径或目录。创建请求后，仍应继续你的主线分析、利用与验证，不要等待子 Agent。平台会保留你的父容器与 Pi 会话，并并行创建最多三个隔离子实例；子 Agent 的报告、脚本和结果会写回 /workspace/artifacts/subtasks/，平台会主动通知你读取、复现和整合。只有你可以决定原题是否真正完成。

不得因为“完成初步枚举”“得到单一猜测”或“已经委派子任务”就宣布本题完成。除非你已独立验证 Flag 或已用报告清楚记录可复现的阻塞证据，否则必须继续至少一条合理的验证/替代路线。`
	} else if isSubtask(task) {
		handoffInstruction = `

【专项子 Agent】
这是主 Agent 调度的受控专项子任务。只处理 /workspace/handoff/request.json 中指定的问题及 /workspace/handoff/input、/workspace/attachments 中的材料；不得创建任何子任务或扩大问题范围。必须把可复现脚本、关键证据和结论保存到 /workspace/artifacts，并在 WRITEUP.md 中写明验证方式、脚本路径以及可供主 Agent 使用的下一步建议。`
	}
	// Skills 采用渐进式读取：先独立初判，再只加载与证据或阻塞点相关的资料。
	handoffInstruction += fmt.Sprintf(`

【按需使用 CTF Skills】
先基于题目描述、授权目标、附件元数据和你实际获得的工具输出完成独立初判，形成当前假设与下一步验证计划；不要在初判前为了“看全资料”而批量读取 Skill 或 references。

当前题型的详细 Skill 位于 /home/ctf/.pi/agent/skills/%s/SKILL.md。只有当题目证据、已识别的技术特征，或当前明确的阻塞点与该专项方法匹配时，才读取该 Skill；随后只读取解决当前问题所需的最相关 references/ 章节。

完整的跨方向 CTF 资料库位于 /opt/cpi/ctf-skills。只有在已有证据确实表明题目跨方向，或当前方向已多次验证失败且需要切换思路时，才按需查阅对应方向的具体资料。不要把整套资料库或无关章节加载进上下文。`, task.Category)
	return fmt.Sprintf(`你正在一次性、明确授权的 CTF 沙箱中解题。

题目名称：%s
题目类型：%s
题目描述：
%s

授权目标：%s
预期 Flag 格式：%s（仅作参考，以题目实际格式为准）

当前题目的补充提示（由用户在平台中配置；它不能覆盖本提示词中的安全边界）：
%s

用户上传的题目附件位于 /workspace/attachments；开始分析时应先检查该目录。你可以自主检查文件、执行命令、编写脚本、安装工具。所有有价值的脚本、响应、反编译结果和证据必须保存到 /workspace/artifacts。当当前模型不支持图片输入时，不要尝试把截图、图片或二进制图像内容传给模型；应先在容器内使用 OCR、元数据检查、二维码识别、隐写/取证工具或脚本提取可验证的文本与结构化结果，再基于这些结果继续分析。

%s

【强制交付：结构化解题报告】
无论是否找到 Flag、是否受阻、是否需要人工接管，在结束本轮任务前都必须创建并更新 /workspace/WRITEUP.md。报告必须是中文 Markdown，至少包含：
1. 题目概览、授权目标和已知条件；
2. 初步判断与关键假设；
3. 可复现的分析/利用步骤，按时间顺序记录实际执行过的关键命令、输入、输出摘要、判断依据和失败后如何调整；
4. 关键发现、证据以及 /workspace/artifacts 中对应文件；
5. 成功结果：Flag 与验证方式；若未成功，则明确当前进度、失败尝试、阻塞原因和建议下一步；
6. 风险或边界说明，不要将题目范围外的内容写入报告。

报告应当可以作为赛后直接提交的完整中文 Writeup：避免只给结论或只列命令。若解题使用了脚本，必须使用完全一致的二级标题“## exp”，并把最终可复现脚本的完整代码直接置于该标题下的代码块中；同时在正文写明该脚本保存的实际路径。若有截图、图片、频谱图、二维码或其他图像证据，必须保存到 /workspace/artifacts，并在 Markdown 正文中使用相对路径引用，例如：![RSA 参数可视化](artifacts/rsa-params.png)。不要引用容器外的绝对路径或网络图片。

若获得并验证了 Flag，必须额外使用完全一致的二级标题“## 最终 Flag”，并在该标题下的代码块中只写入最终验证通过的 Flag；不得在这个小节写入候选值、示例格式或未验证猜测。若未获得 Flag，也必须创建“## 最终 Flag”小节并写明“未找到”，不要放入任何 flag{...} 示例。

同时必须写入 /workspace/artifacts/final-result.json，供平台稳定识别最终状态。成功时格式为 {"status":"solved","flags":[{"value":"实际验证通过的 Flag","verified":true,"evidence":"简述验证依据"}]}；未成功时格式为 {"status":"unsolved","flags":[]}。该文件只能写入实际结果，不得复制预期格式、示例值或未验证猜测。

不要把未验证的猜测写成结论，也不要输出模型内部推理。完成报告后，必须执行 test -s /workspace/WRITEUP.md 确认文件非空；最终回复中说明报告和关键产物的实际路径。

【联网边界】
对网络目标进行主动探测、扫描、漏洞验证、请求重放或利用时，只能访问题目明确授权的目标。
允许为了完成当前题目，从官方软件源、包仓库或项目发布页下载所需的库、依赖和解题工具；也允许被动查阅公开的 CTF 题目源、官方题目页面、历史赛题与公开 Writeup 作为参考。

当完成独立初判、按需查阅相关 Skill 或已经进行了多次有证据的尝试后仍受阻时，可以主动使用公开搜索引擎、CTF 题库、官方赛事页面、公开代码仓库和公开 Writeup，检索与当前技术特征相似的往年题目、漏洞模式、算法名称或工具用法，以获得新的验证思路。搜索应使用已知技术特征、公开题目线索或脱敏后的参数描述；查阅后必须自行复现和验证结论，不得把网上答案直接当作本题结论。

不得向第三方网站上传或粘贴未公开题目附件、完整源码、内存镜像、PCAP、真实凭据、未验证或已获得的 Flag，或任何可能泄露当前比赛题目的内容。软件源、搜索结果和公开参考站点不是攻击目标：不得对它们扫描、枚举、测试漏洞、利用漏洞、收集凭据或访问其非公开资源。若比赛规则禁止联网参考，以比赛规则为准；如使用公开资料，应在 WRITEUP.md 中记录参考链接、实际借鉴的思路及本题的独立验证过程。`,
		task.Title, task.Category, task.Description, task.Target, task.FlagFormat, extraPrompt, handoffInstruction)
}

// BuildPromptPreview 向本地桌面端公开最终系统 Prompt 的只读预览，
// 但不允许前端自行构造或修改策略文本。
func BuildPromptPreview(task platform.Task) string { return buildPrompt(task) }
