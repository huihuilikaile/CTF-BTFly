package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
)

const (
	cryptoHandoffRequestRelative = ".cpi/handoff/crypto-request.json"
	maxHandoffRequestBytes       = 64 << 10
)

// cryptoHandoffRequest is deliberately file based. A Pi sandbox receives no
// daemon credential, so it cannot create arbitrary platform tasks. The daemon
// accepts this one narrowly scoped request only after its Misc parent settles.
type cryptoHandoffRequest struct {
	Question       string   `json:"question"`
	Summary        string   `json:"summary"`
	ArtifactPaths   []string `json:"artifactPaths"`
	ExpectedOutput  []string `json:"expectedOutput"`
}

type cryptoHandoffResult struct {
	HandoffID      string   `json:"handoffId"`
	Status         string   `json:"status"`
	Question       string   `json:"question"`
	ChildTaskID    string   `json:"childTaskId"`
	ReportPath     string   `json:"reportPath,omitempty"`
	ArtifactsPath  string   `json:"artifactsPath,omitempty"`
	Error          string   `json:"error,omitempty"`
	CompletedAt    string   `json:"completedAt"`
}

func (request *cryptoHandoffRequest) normalise() error {
	request.Question = strings.TrimSpace(request.Question)
	request.Summary = strings.TrimSpace(request.Summary)
	if request.Question == "" && request.Summary == "" {
		return fmt.Errorf("crypto handoff requires question or summary")
	}
	if len(request.Question) > 12*1024 || len(request.Summary) > 24*1024 || len(request.ArtifactPaths) > 64 || len(request.ExpectedOutput) > 32 {
		return fmt.Errorf("crypto handoff request is too large")
	}
	for index, path := range request.ArtifactPaths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			return fmt.Errorf("artifactPaths contains an empty path")
		}
		request.ArtifactPaths[index] = path
	}
	for index, value := range request.ExpectedOutput {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("expectedOutput contains an empty value")
		}
		request.ExpectedOutput[index] = value
	}
	return nil
}

// startRequestedCryptoHandoff runs after a Misc Pi session intentionally
// settles. It consumes the request before provisioning the child, so retrying
// the parent cannot accidentally create duplicate specialist containers.
func (s *Service) startRequestedCryptoHandoff(parent platform.Task) {
	if parent.Category != platform.CategoryMisc || parent.ParentTaskID != "" {
		return
	}
	parentRoot := filepath.Join(s.workspaces, parent.ID)
	requestPath := filepath.Join(parentRoot, filepath.FromSlash(cryptoHandoffRequestRelative))
	data, err := os.ReadFile(requestPath)
	if errorsIsNotExist(err) {
		return
	}
	if err != nil {
		s.emitHandoffError(parent.ID, "无法读取密码专项交接请求："+err.Error())
		return
	}
	if len(data) > maxHandoffRequestBytes {
		s.emitHandoffError(parent.ID, "密码专项交接请求超过 64 KiB 限制")
		return
	}
	var request cryptoHandoffRequest
	if err := json.Unmarshal(data, &request); err != nil {
		s.emitHandoffError(parent.ID, "密码专项交接请求不是有效 JSON："+err.Error())
		return
	}
	if err := request.normalise(); err != nil {
		s.emitHandoffError(parent.ID, "密码专项交接请求无效："+err.Error())
		return
	}

	handoffID := platform.NewID("handoff")
	archivePath := filepath.Join(parentRoot, ".cpi", "handoff", "requests", handoffID+".json")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		s.emitHandoffError(parent.ID, "无法归档密码专项交接请求："+err.Error())
		return
	}
	if err := os.Rename(requestPath, archivePath); err != nil {
		s.emitHandoffError(parent.ID, "无法消费密码专项交接请求："+err.Error())
		return
	}

	now := time.Now()
	child := platform.Task{
		ID:           platform.NewID("task"),
		ParentTaskID: parent.ID,
		HandoffID:    handoffID,
		Title:        parent.Title + " · 密码专项分析",
		Category:     platform.CategoryCrypto,
		Description:  handoffDescription(parent, request),
		Prompt:       cryptoHandoffPrompt(request),
		Target:       parent.Target,
		FlagFormat:   parent.FlagFormat,
		Status:       platform.TaskReady,
		Image:        sandbox.ImageFor(platform.CategoryCrypto),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.store.CreateTask(context.Background(), child); err != nil {
		s.emitHandoffError(parent.ID, "无法创建密码专项任务："+err.Error())
		return
	}
	if err := s.copyHandoffInput(parent, child, request, archivePath); err != nil {
		_ = s.store.UpdateTaskState(context.Background(), child.ID, platform.TaskFailed, "", "", err.Error())
		s.emitHandoffError(parent.ID, "无法准备密码专项附件："+err.Error())
		_ = s.finishCryptoHandoff(child, "failed", err.Error())
		return
	}

	// The parent is already settled. Releasing it keeps only the specialist
	// container alive while analysis is delegated and preserves its workspace.
	if err := s.CloseSandbox(context.Background(), parent.ID); err != nil {
		_ = s.store.UpdateTaskState(context.Background(), child.ID, platform.TaskFailed, "", "", err.Error())
		s.emitHandoffError(parent.ID, "无法释放等待交接的 Misc 实例："+err.Error())
		_ = s.finishCryptoHandoff(child, "failed", err.Error())
		return
	}
	_, _ = s.emit(context.Background(), platform.Event{TaskID: parent.ID, Source: "handoff", Type: "handoff.crypto.started", Payload: platform.JSONPayload(map[string]string{
		"handoffId": handoffID, "childTaskId": child.ID, "question": request.Question,
	})})
	if err := s.Start(context.Background(), child.ID); err != nil {
		_ = s.store.UpdateTaskState(context.Background(), child.ID, platform.TaskFailed, "", "", err.Error())
		_ = s.finishCryptoHandoff(child, "failed", err.Error())
	}
}

func handoffDescription(parent platform.Task, request cryptoHandoffRequest) string {
	parts := []string{
		"这是来自杂项题目的密码学专项交接，请仅处理密码分析部分，并向父任务返回可复现结论。",
		"原题名称：" + parent.Title,
		"原题描述：" + parent.Description,
		"交接问题：" + request.Question,
	}
	if request.Summary != "" {
		parts = append(parts, "已有发现："+request.Summary)
	}
	return strings.Join(parts, "\n\n")
}

func cryptoHandoffPrompt(request cryptoHandoffRequest) string {
	parts := []string{
		"你是由 Misc Agent 交接的 Crypto 专项 Agent。先阅读 /workspace/handoff/request.json、/workspace/handoff/input 和 /workspace/attachments。",
		"不要创建更多子任务；在当前 Crypto 实例内完成数学/密码分析。把解题脚本保存到 /workspace/artifacts，并在 WRITEUP.md 中说明结论、验证方式、脚本路径及可供原 Misc 任务继续使用的下一步。",
	}
	if len(request.ExpectedOutput) > 0 {
		parts = append(parts, "交接方期望输出：\n- "+strings.Join(request.ExpectedOutput, "\n- "))
	}
	return strings.Join(parts, "\n\n")
}

func (s *Service) copyHandoffInput(parent, child platform.Task, request cryptoHandoffRequest, archivedRequest string) error {
	parentRoot := filepath.Join(s.workspaces, parent.ID)
	childRoot := filepath.Join(s.workspaces, child.ID)
	if err := os.MkdirAll(childRoot, 0o700); err != nil {
		return fmt.Errorf("create child workspace: %w", err)
	}
	if err := copyDirectory(filepath.Join(parentRoot, "attachments"), filepath.Join(childRoot, "attachments")); err != nil {
		return fmt.Errorf("copy parent attachments: %w", err)
	}
	if err := copyFile(archivedRequest, filepath.Join(childRoot, "handoff", "request.json")); err != nil {
		return fmt.Errorf("copy handoff request: %w", err)
	}
	for _, requested := range request.ArtifactPaths {
		source, err := resolveWorkspaceFile(parentRoot, requested)
		if err != nil {
			return fmt.Errorf("resolve handoff artifact %q: %w", requested, err)
		}
		target := filepath.Join(childRoot, "handoff", "input", filepath.FromSlash(requested))
		if err := copyFile(source, target); err != nil {
			return fmt.Errorf("copy handoff artifact %q: %w", requested, err)
		}
	}
	return nil
}

// finishCryptoHandoff copies only durable work back to the parent workspace,
// records a compact result manifest, then automatically restarts the parent
// so the Misc Agent can consume the specialist's findings.
func (s *Service) finishCryptoHandoff(child platform.Task, status, cause string) error {
	if child.ParentTaskID == "" || child.HandoffID == "" {
		return nil
	}
	// The child is terminal by the time this method is called. Release its
	// container before resuming the parent so a completed handoff never leaves a
	// hidden Crypto instance consuming memory. Its bind-mounted workspace stays
	// available for the artifact copy below.
	if err := s.CloseSandbox(context.Background(), child.ID); err != nil {
		return fmt.Errorf("close crypto handoff sandbox: %w", err)
	}
	parent, err := s.store.GetTask(context.Background(), child.ParentTaskID)
	if err != nil {
		return err
	}
	parentRoot := filepath.Join(s.workspaces, parent.ID)
	childRoot := filepath.Join(s.workspaces, child.ID)
	resultRoot := filepath.Join(parentRoot, "artifacts", "handoffs", child.HandoffID)
	if err := os.MkdirAll(resultRoot, 0o700); err != nil {
		return fmt.Errorf("create handoff result directory: %w", err)
	}
	result := cryptoHandoffResult{
		HandoffID: child.HandoffID, Status: status, ChildTaskID: child.ID,
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Error: cause,
	}
	if err := copyOptionalFile(filepath.Join(childRoot, "WRITEUP.md"), filepath.Join(resultRoot, "crypto-WRITEUP.md")); err != nil {
		return fmt.Errorf("copy crypto writeup: %w", err)
	} else if err == nil {
		if _, statErr := os.Stat(filepath.Join(resultRoot, "crypto-WRITEUP.md")); statErr == nil {
			result.ReportPath = filepath.ToSlash(filepath.Join("artifacts", "handoffs", child.HandoffID, "crypto-WRITEUP.md"))
		}
	}
	if err := copyDirectory(filepath.Join(childRoot, "artifacts"), filepath.Join(resultRoot, "crypto-artifacts")); err != nil {
		return fmt.Errorf("copy crypto artifacts: %w", err)
	}
	if _, err := os.Stat(filepath.Join(resultRoot, "crypto-artifacts")); err == nil {
		result.ArtifactsPath = filepath.ToSlash(filepath.Join("artifacts", "handoffs", child.HandoffID, "crypto-artifacts"))
	}
	manifest, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode handoff result: %w", err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "crypto-result.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("write handoff result: %w", err)
	}
	_, _ = s.emit(context.Background(), platform.Event{TaskID: parent.ID, Source: "handoff", Type: "handoff.crypto.completed", Payload: platform.JSONPayload(result)})

	// The parent remains a normal settled task while the child is running. It
	// may have been cancelled by the user, in which case do not restart it.
	parent, err = s.store.GetTask(context.Background(), parent.ID)
	if err != nil || parent.Status != platform.TaskSettled {
		return err
	}
	_, _ = s.emit(context.Background(), platform.Event{TaskID: parent.ID, Source: "handoff", Type: "handoff.crypto.resuming_parent", Payload: platform.JSONPayload(map[string]string{
		"handoffId": child.HandoffID, "message": "密码专项结果已回传，正在恢复杂项解题实例",
	})})
	return s.Start(context.Background(), parent.ID)
}

func (s *Service) emitHandoffError(parentTaskID, message string) {
	_, _ = s.emit(context.Background(), platform.Event{TaskID: parentTaskID, Source: "handoff", Type: "handoff.crypto.failed", Payload: platform.JSONPayload(map[string]string{"error": message})})
}

func copyOptionalFile(source, target string) error {
	if _, err := os.Stat(source); errorsIsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return copyFile(source, target)
}

func copyFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyDirectory(source, target string) error {
	if _, err := os.Stat(source); errorsIsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return copyFile(path, destination)
	})
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }
