package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
)

// 每个根题目最多派生三个专项子 Agent。该上限按父任务生命周期计算，
// 这样模型不能通过反复结束/恢复父任务无限扩张容器和 Token 消耗。
const (
	maxSubtasksPerParent       = 3
	subtaskRequestsRelativeDir = ".cpi/subtasks/requests"
	maxSubtaskRequestBytes     = 64 << 10
)

// subtaskRequest 是根 Agent 写入工作区的受控委派请求。它不允许指定镜像、
// Docker 参数、模型凭据或任意主机路径；daemon 只接受白名单题型和普通文件引用。
type subtaskRequest struct {
	Category       string   `json:"category"`
	Title          string   `json:"title"`
	Question       string   `json:"question"`
	Summary        string   `json:"summary"`
	ArtifactPaths  []string `json:"artifactPaths"`
	ExpectedOutput []string `json:"expectedOutput"`
}

// subtaskResult 是回传到父工作区的机器可读摘要，完整报告和产物仍保留在独立目录。
type subtaskResult struct {
	HandoffID     string            `json:"handoffId"`
	Category      platform.Category `json:"category"`
	Status        string            `json:"status"`
	Question      string            `json:"question"`
	ChildTaskID   string            `json:"childTaskId"`
	ReportPath    string            `json:"reportPath,omitempty"`
	ArtifactsPath string            `json:"artifactsPath,omitempty"`
	Error         string            `json:"error,omitempty"`
	CompletedAt   string            `json:"completedAt"`
}

func (request *subtaskRequest) normalise() (platform.Category, error) {
	category, err := platform.ParseCategory(request.Category)
	if err != nil {
		return "", err
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Question = strings.TrimSpace(request.Question)
	request.Summary = strings.TrimSpace(request.Summary)
	if request.Question == "" && request.Summary == "" {
		return "", fmt.Errorf("subtask requires question or summary")
	}
	if len(request.Title) > 256 || len(request.Question) > 12*1024 || len(request.Summary) > 24*1024 || len(request.ArtifactPaths) > 64 || len(request.ExpectedOutput) > 32 {
		return "", fmt.Errorf("subtask request is too large")
	}
	for index, path := range request.ArtifactPaths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			return "", fmt.Errorf("artifactPaths contains an empty path")
		}
		request.ArtifactPaths[index] = path
	}
	for index, value := range request.ExpectedOutput {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("expectedOutput contains an empty value")
		}
		request.ExpectedOutput[index] = value
	}
	return category, nil
}

func (request subtaskRequest) input() subtaskInput {
	return subtaskInput{ArtifactPaths: request.ArtifactPaths}
}

// isSubtask 将历史单专项子任务也视为通用子任务处理，确保升级后的 daemon
// 能安全回收旧实例，但不会再创建旧协议的任务。
func isSubtask(task platform.Task) bool {
	return task.ParentTaskID != ""
}

// hasOpenSubtasks 判断父题目是否仍有尚未结束的子 Agent（包括排队、创建中、
// 运行中和暂停中的子任务）。父 Agent 在这些辅助工作尚未全部回传前会保留运行
// 名额和原会话，避免子任务回传时额外突破全局并发上限。
func (s *Service) hasOpenSubtasks(parentID string) (bool, error) {
	children, err := s.store.ListChildTasks(context.Background(), parentID)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		if !isFinished(child.Status) {
			return true, nil
		}
	}
	return false, nil
}

// startRequestedSubtasks 只在根 Agent 一轮结束后读取请求目录。它以原子 Rename
// 消费每个 JSON，避免后续继续分析时再次读取同一份请求。子 Agent 是主 Agent
// 的并行辅助：父容器和 Pi 会话不会被关闭，主 Agent 会立即继续自己的独立路线。
func (s *Service) startRequestedSubtasks(parent platform.Task) bool {
	s.delegationMu.Lock()
	defer s.delegationMu.Unlock()
	if parent.ParentTaskID != "" || parent.Status != platform.TaskSettled {
		return false
	}

	parentRoot := filepath.Join(s.workspaces, parent.ID)
	requestRoot := filepath.Join(parentRoot, filepath.FromSlash(subtaskRequestsRelativeDir))
	entries, err := os.ReadDir(requestRoot)
	if errorsIsNotExist(err) {
		return false
	}
	if err != nil {
		s.emitDelegationEvent(parent.ID, "delegation.failed", map[string]string{"error": "无法读取子任务请求目录：" + err.Error()})
		return false
	}
	files := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		files = append(files, entry)
	}
	if len(files) == 0 {
		return false
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Name() < files[right].Name() })
	children, err := s.store.ListChildTasks(context.Background(), parent.ID)
	if err != nil {
		s.emitDelegationEvent(parent.ID, "delegation.failed", map[string]string{"error": "无法读取既有子任务：" + err.Error()})
		return false
	}
	remaining := maxSubtasksPerParent - len(children)
	if remaining <= 0 || len(files) > remaining {
		for _, file := range files {
			_ = s.archiveSubtaskRequest(parentRoot, filepath.Join(requestRoot, file.Name()), "rejected")
		}
		s.emitDelegationEvent(parent.ID, "delegation.rejected", map[string]any{
			"error": "每个父题目最多只能创建 3 个子 Agent", "existing": len(children), "requested": len(files),
		})
		return false
	}

	created := make([]platform.Task, 0, len(files))
	failed := make([]struct {
		task  platform.Task
		cause string
	}, 0)
	for _, file := range files {
		requestPath := filepath.Join(requestRoot, file.Name())
		data, readErr := os.ReadFile(requestPath)
		archivePath := s.archiveSubtaskRequest(parentRoot, requestPath, "processed")
		if readErr != nil {
			s.emitDelegationEvent(parent.ID, "delegation.rejected", map[string]string{"file": file.Name(), "error": "无法读取子任务请求：" + readErr.Error()})
			continue
		}
		if len(data) > maxSubtaskRequestBytes {
			s.emitDelegationEvent(parent.ID, "delegation.rejected", map[string]string{"file": file.Name(), "error": "子任务请求超过 64 KiB 限制"})
			continue
		}
		var request subtaskRequest
		if err := json.Unmarshal(data, &request); err != nil {
			s.emitDelegationEvent(parent.ID, "delegation.rejected", map[string]string{"file": file.Name(), "error": "子任务请求不是有效 JSON：" + err.Error()})
			continue
		}
		category, err := request.normalise()
		if err != nil {
			s.emitDelegationEvent(parent.ID, "delegation.rejected", map[string]string{"file": file.Name(), "error": "子任务请求无效：" + err.Error()})
			continue
		}
		now := time.Now()
		handoffID := platform.NewID("subtask")
		title := request.Title
		if title == "" {
			title = string(category) + " 专项分析"
		}
		child := platform.Task{
			ID: platform.NewID("task"), ParentTaskID: parent.ID, HandoffID: handoffID,
			Title: parent.Title + " · " + title, Category: category,
			Description: subtaskDescription(parent, category, request), Prompt: subtaskPrompt(category, request),
			Target: parent.Target, FlagFormat: parent.FlagFormat, ModelProfile: parent.ModelProfile, ModelID: parent.ModelID, Status: platform.TaskReady,
			Image: sandbox.ImageFor(category), CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.CreateTask(context.Background(), child); err != nil {
			s.emitDelegationEvent(parent.ID, "delegation.failed", map[string]string{"file": file.Name(), "error": "无法创建子任务：" + err.Error()})
			continue
		}
		created = append(created, child)
		if archivePath == "" {
			failed = append(failed, struct {
				task  platform.Task
				cause string
			}{child, "无法归档子任务请求"})
			continue
		}
		if err := s.copySubtaskInput(parent, child, request.input(), archivePath); err != nil {
			failed = append(failed, struct {
				task  platform.Task
				cause string
			}{child, "无法准备子任务输入：" + err.Error()})
		}
	}
	if len(created) == 0 {
		return false
	}

	categories := make([]string, 0, len(created))
	for _, child := range created {
		categories = append(categories, string(child.Category))
	}
	s.emitDelegationEvent(parent.ID, "delegation.started", map[string]any{"count": len(created), "categories": categories, "limit": maxSubtasksPerParent, "mode": "parallel_assist"})
	// 先让父任务重新计入运行名额，再为子任务走正常 FIFO 调度；这样一个
	// "主 + 三子" 组合也绝不会绕过全局并发上限。
	if err := s.continueParentAfterDelegation(parent, created); err != nil {
		s.emitDelegationEvent(parent.ID, "delegation.failed", map[string]string{"error": "无法让主 Agent 继续分析：" + err.Error()})
		for _, child := range created {
			_ = s.store.UpdateTaskState(context.Background(), child.ID, platform.TaskFailed, "", "", "主 Agent 会话不可用，未启动子任务："+err.Error())
			go func(task platform.Task) { _ = s.finishGenericSubtask(task, "failed", "主 Agent 会话不可用") }(child)
		}
		return true
	}

	failedIDs := make(map[string]string, len(failed))
	for _, item := range failed {
		failedIDs[item.task.ID] = item.cause
		_ = s.store.UpdateTaskState(context.Background(), item.task.ID, platform.TaskFailed, "", "", item.cause)
	}
	for _, child := range created {
		if cause, failed := failedIDs[child.ID]; failed {
			go func(task platform.Task, reason string) { _ = s.finishGenericSubtask(task, "failed", reason) }(child, cause)
			continue
		}
		if err := s.Start(context.Background(), child.ID); err != nil {
			_ = s.store.UpdateTaskState(context.Background(), child.ID, platform.TaskFailed, "", "", err.Error())
			go func(task platform.Task, reason string) { _ = s.finishGenericSubtask(task, "failed", reason) }(child, err.Error())
		}
	}
	return true
}

// continueParentAfterDelegation 通过同一个 Pi RPC 会话唤醒刚刚 settled 的父
// Agent。子任务可能仍在排队，因此提示中明确要求主 Agent 不要等待其结果。
func (s *Service) continueParentAfterDelegation(parent platform.Task, children []platform.Task) error {
	items := make([]string, 0, len(children))
	for _, child := range children {
		items = append(items, fmt.Sprintf("- %s（%s）：%s", child.Title, child.Category, child.HandoffID))
	}
	message := "你已创建以下受控子 Agent，它们将并行完成边界明确的辅助分析：\n" + strings.Join(items, "\n") + `

请立即继续你自己的主线分析、验证和利用，不要等待子 Agent，也不要把最终判断交给子 Agent。它们完成后，平台会把报告和产物写入 /workspace/artifacts/subtasks/，并主动通知你。只有你负责交叉验证所有结果、确认最终 Flag 并完成原题 WRITEUP。`
	if err := s.sandboxes.Prompt(context.Background(), parent.ID, message); err != nil {
		return err
	}
	s.mu.Lock()
	s.settled[parent.ID] = false
	delete(s.paused, parent.ID)
	s.mu.Unlock()
	if err := s.store.UpdateTaskState(context.Background(), parent.ID, platform.TaskRunning, parent.Runtime, parent.ContainerID, ""); err != nil {
		return err
	}
	s.emitDelegationEvent(parent.ID, "delegation.parent_continues", map[string]any{"count": len(children), "message": "主 Agent 正在与子 Agent 并行解题"})
	return nil
}

// archiveSubtaskRequest 返回已归档文件路径。失败时返回空字符串，但不泄露原始
// 工作区以外的路径给 Agent。
func (s *Service) archiveSubtaskRequest(parentRoot, requestPath, state string) string {
	archivePath := filepath.Join(parentRoot, ".cpi", "subtasks", state, platform.NewID("request")+".json")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		return ""
	}
	if err := os.Rename(requestPath, archivePath); err != nil {
		return ""
	}
	return archivePath
}

func subtaskDescription(parent platform.Task, category platform.Category, request subtaskRequest) string {
	parts := []string{
		"这是来自主 Agent 的受控专项子任务，只处理所指定的独立问题，并回传可复现证据。",
		"原题名称：" + parent.Title,
		"专项方向：" + string(category),
		"子任务问题：" + request.Question,
	}
	if request.Summary != "" {
		parts = append(parts, "主 Agent 已完成的分析："+request.Summary)
	}
	return strings.Join(parts, "\n\n")
}

func subtaskPrompt(category platform.Category, request subtaskRequest) string {
	parts := []string{
		"你是由主 Agent 调度的 " + string(category) + " 专项子 Agent。先阅读 /workspace/handoff/request.json、/workspace/handoff/input 和 /workspace/attachments。",
		"只解决当前子问题，不得继续创建子任务。保存可复现脚本、关键证据和结论到 /workspace/artifacts；在 WRITEUP.md 中写清验证步骤、脚本路径、结论和可供主 Agent 使用的下一步。",
	}
	if len(request.ExpectedOutput) > 0 {
		parts = append(parts, "主 Agent 期望输出：\n- "+strings.Join(request.ExpectedOutput, "\n- "))
	}
	return strings.Join(parts, "\n\n")
}

// finishGenericSubtask 回收单个子实例、归档结果，并把回传结果通知仍在独立
// 解题的主 Agent。delegationMu 让多个子 Agent 同时结束时仍能串行写回产物。
func (s *Service) finishGenericSubtask(child platform.Task, status, cause string) error {
	if !isSubtask(child) {
		return nil
	}
	s.delegationMu.Lock()
	defer s.delegationMu.Unlock()
	current, err := s.store.GetTask(context.Background(), child.ID)
	if err == nil {
		child = current
	}
	if child.ContainerID != "" {
		if err := s.CloseSandbox(context.Background(), child.ID); err != nil {
			return fmt.Errorf("close subtask sandbox: %w", err)
		}
	}
	parent, err := s.store.GetTask(context.Background(), child.ParentTaskID)
	if err != nil {
		return err
	}
	parentRoot := filepath.Join(s.workspaces, parent.ID)
	childRoot := filepath.Join(s.workspaces, child.ID)
	resultRoot := filepath.Join(parentRoot, "artifacts", "subtasks", child.HandoffID)
	if err := os.MkdirAll(resultRoot, 0o700); err != nil {
		return fmt.Errorf("create subtask result directory: %w", err)
	}
	result := subtaskResult{HandoffID: child.HandoffID, Category: child.Category, Status: status, Question: child.Description, ChildTaskID: child.ID, Error: cause, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := copyOptionalFile(filepath.Join(childRoot, "WRITEUP.md"), filepath.Join(resultRoot, "WRITEUP.md")); err != nil {
		return fmt.Errorf("copy subtask writeup: %w", err)
	}
	if _, err := os.Stat(filepath.Join(resultRoot, "WRITEUP.md")); err == nil {
		result.ReportPath = filepath.ToSlash(filepath.Join("artifacts", "subtasks", child.HandoffID, "WRITEUP.md"))
	}
	if err := copyDirectory(filepath.Join(childRoot, "artifacts"), filepath.Join(resultRoot, "artifacts")); err != nil {
		return fmt.Errorf("copy subtask artifacts: %w", err)
	}
	if _, err := os.Stat(filepath.Join(resultRoot, "artifacts")); err == nil {
		result.ArtifactsPath = filepath.ToSlash(filepath.Join("artifacts", "subtasks", child.HandoffID, "artifacts"))
	}
	manifest, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode subtask result: %w", err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "result.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("write subtask result: %w", err)
	}
	s.emitDelegationEvent(parent.ID, "delegation.child_completed", result)

	// 用户已中止、失败或删除父实例时只归档结果，不自动重新唤醒主 Agent。
	if parent.Status == platform.TaskCancelled || parent.Status == platform.TaskFailed {
		return nil
	}
	return s.notifyParentOfSubtaskResult(parent, result)
}

// notifyParentOfSubtaskResult 将子任务结果作为同一 Pi 会话的一条后续消息。
// Pi 会在当前回合完成后消费消息；若主 Agent 此时已 settled，消息会直接让其
// 开始新一轮。无论哪种情况，主 Agent 都继续对最终结论负责。
func (s *Service) notifyParentOfSubtaskResult(parent platform.Task, result subtaskResult) error {
	message := fmt.Sprintf(`子 Agent 已完成辅助分析。

方向：%s
状态：%s
问题：%s
报告：/workspace/%s
产物目录：/workspace/%s

请读取该结果并自行复现、交叉验证；它只是辅助证据，不能直接替代你对最终 Flag 的验证。继续你的主线工作，并在 WRITEUP 中整合真正有效的结论。`, result.Category, result.Status, result.Question, result.ReportPath, result.ArtifactsPath)
	if result.Error != "" {
		message += "\n\n子 Agent 说明：" + result.Error
	}
	if err := s.sandboxes.Prompt(context.Background(), parent.ID, message); err != nil {
		return err
	}
	s.mu.Lock()
	s.settled[parent.ID] = false
	delete(s.paused, parent.ID)
	s.mu.Unlock()
	if parent.Status == platform.TaskSettled {
		if err := s.store.UpdateTaskState(context.Background(), parent.ID, platform.TaskRunning, parent.Runtime, parent.ContainerID, ""); err != nil {
			return err
		}
	}
	s.emitDelegationEvent(parent.ID, "delegation.result_available", map[string]any{"handoffId": result.HandoffID, "childTaskId": result.ChildTaskID, "message": "子 Agent 结果已发送给主 Agent 验证"})
	return nil
}

func (s *Service) emitDelegationEvent(parentID, eventType string, payload any) {
	_, _ = s.emit(context.Background(), platform.Event{TaskID: parentID, Source: "delegation", Type: eventType, Payload: platform.JSONPayload(payload)})
}
