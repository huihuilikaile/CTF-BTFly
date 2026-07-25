package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

// TestNormalizePiTextAndToolEvents 验证 Pi 嵌套文本增量与工具事件映射。
func TestNormalizePiTextAndToolEvents(t *testing.T) {
	text := normalize("task_test", []byte(`{"type":"message_update","turnId":"turn_1","assistantMessageEvent":{"type":"text_delta","delta":"checking"}}`))
	if text.Type != "agent.message.delta" || text.TurnID != "turn_1" {
		t.Fatalf("unexpected text event %#v", text)
	}
	var payload map[string]any
	if err := json.Unmarshal(text.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	// 工具调用 ID 必须保留，前端才能把开始、输出和结束事件关联起来。
	tool := normalize("task_test", []byte(`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash"}`))
	if tool.Type != "tool.started" || tool.ToolCallID != "call_1" {
		t.Fatalf("unexpected tool event %#v", tool)
	}
}

// TestFlagsFromWriteupUsesOnlyFinalFlagSection 验证只接受最终章节的单行代码块。
func TestFlagsFromWriteupUsesOnlyFinalFlagSection(t *testing.T) {
	writeup := "# 解题报告\n\n工具输出中出现 flag{noise}\n\n## 最终 Flag\n\n```text\nflag{verified}\n```\n\n## 复盘\n\nflag{also-noise}\n"
	flags := flagsFromWriteup(writeup)
	if len(flags) != 1 || flags[0] != "flag{verified}" {
		t.Fatalf("unexpected flags %#v", flags)
	}
	if flags := flagsFromWriteup("# 报告\nflag{noise}"); len(flags) != 0 {
		t.Fatalf("expected no flags outside final section, got %#v", flags)
	}
	if flags := flagsFromWriteup("## 最终 Flag\n\n```text\nCTF2026-verified-value\n```"); len(flags) != 1 || flags[0] != "CTF2026-verified-value" {
		t.Fatalf("expected arbitrary verified flag format, got %#v", flags)
	}
	if flags := flagsFromWriteup("## 最终 Flag\n\n未找到"); len(flags) != 0 {
		t.Fatalf("expected no flag without an explicit code block, got %#v", flags)
	}
}

// TestResolveAttachmentPathRejectsTraversal 验证合法子目录可保留、../ 穿越被拒绝。
func TestResolveAttachmentPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	path, relative, err := resolveAttachmentPath(root, "source/input.bin")
	if err != nil || relative != filepath.Join("source", "input.bin") || filepath.Dir(path) == "" {
		t.Fatalf("unexpected attachment target path=%q relative=%q err=%v", path, relative, err)
	}
	if _, _, err := resolveAttachmentPath(root, "../outside.bin"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

// TestStartQueuesWhenGlobalCapacityIsFull 验证第 N+1 道题不会创建 Docker，
// 而是写入可恢复的 queued 状态和 FIFO 队列。这一测试不连接 Docker Engine。
func TestStartQueuesWhenGlobalCapacityIsFull(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpdateExecutionSettings(ctx, platform.ExecutionSettings{MaxConcurrentTasks: 1}); err != nil {
		t.Fatal(err)
	}
	manager, err := sandbox.New()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gateway, err := modelgateway.New(modelgateway.Config{})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, eventhub.New(), manager, gateway, t.TempDir(), "http://127.0.0.1:18731")

	now := time.Now()
	busy := platform.Task{ID: "task_busy", Title: "already running", Category: platform.CategoryWeb, Description: "test", Status: platform.TaskRunning, Image: sandbox.ImageFor(platform.CategoryWeb), CreatedAt: now, UpdatedAt: now}
	candidate := platform.Task{ID: "task_queued", Title: "wait for a slot", Category: platform.CategoryCrypto, Description: "test", Status: platform.TaskReady, Image: sandbox.ImageFor(platform.CategoryCrypto), CreatedAt: now, UpdatedAt: now}
	for _, task := range []platform.Task{busy, candidate} {
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.Start(ctx, candidate.ID); err != nil {
		t.Fatal(err)
	}
	queued, err := store.GetTask(ctx, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != platform.TaskQueued || queued.ContainerID != "" {
		t.Fatalf("task should wait without a sandbox, got %#v", queued)
	}
	status, err := service.QueueStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveTaskCount != 1 || status.Settings.MaxConcurrentTasks != 1 || len(status.Queue) != 1 || status.Queue[0].TaskID != candidate.ID || status.Queue[0].Position != 1 {
		t.Fatalf("unexpected queue status %#v", status)
	}
}

// TestSubtaskRequestNormalise 验证通用子任务只能选择白名单题型，并保留
// daemon 用于判定通用交接的稳定 ID 前缀。
func TestSubtaskRequestNormalise(t *testing.T) {
	request := subtaskRequest{
		Category: " CRYPTO ", Title: "  RSA 分析  ", Question: "  恢复明文  ",
		ArtifactPaths: []string{" artifacts\\params.txt "}, ExpectedOutput: []string{" 明文 "},
	}
	category, err := request.normalise()
	if err != nil {
		t.Fatal(err)
	}
	if category != platform.CategoryCrypto || request.Title != "RSA 分析" || request.Question != "恢复明文" || request.ArtifactPaths[0] != "artifacts/params.txt" || request.ExpectedOutput[0] != "明文" {
		t.Fatalf("unexpected normalized request %#v, category=%q", request, category)
	}
	if _, err := (&subtaskRequest{Category: "kernel", Question: "test"}).normalise(); err == nil {
		t.Fatal("expected unsupported category to be rejected")
	}
	if !isSubtask(platform.Task{ParentTaskID: "task_parent", HandoffID: "subtask_abc"}) {
		t.Fatal("expected a child task to be recognised as a subtask")
	}
	if !isSubtask(platform.Task{ParentTaskID: "task_parent", HandoffID: "handoff_abc"}) {
		t.Fatal("existing child tasks must be safely handled after the protocol migration")
	}
	if isSubtask(platform.Task{}) {
		t.Fatal("root task must not be treated as a subtask")
	}
}
