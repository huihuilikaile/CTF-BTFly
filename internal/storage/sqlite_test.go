package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

// TestTaskAndEventJournal 验证任务落库、事件序号单调递增以及 after 重放语义。
func TestTaskAndEventJournal(t *testing.T) {
	// 每个测试使用独立临时数据库，避免并行或重复运行相互污染。
	store, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now()
	task := platform.Task{
		ID: "task_test", Title: "journal", Category: platform.CategoryCrypto,
		Description: "test", Status: platform.TaskReady, Image: "test-image", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// 连续追加不同来源事件，序号应由存储层分配为 1、2。
	first, err := store.AppendEvent(ctx, platform.Event{TaskID: task.ID, Source: "system", Type: "task.created", Payload: platform.JSONPayload(map[string]string{"ok": "true"})})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendEvent(ctx, platform.Event{TaskID: task.ID, Source: "pi", Type: "agent.started", Payload: platform.JSONPayload(map[string]string{"ok": "true"})})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("unexpected sequences: %d, %d", first.Sequence, second.Sequence)
	}
	// after=1 只应重放第二条事件。
	events, err := store.ListEvents(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 2 || events[0].Type != "agent.started" {
		t.Fatalf("unexpected event replay: %#v", events)
	}
}

// TestModelUsageReportAggregatesChildIntoParentTask 验证专项子任务用量
// 不会在报表中单独出现，而是归并到用户可见的父任务。
func TestModelUsageReportAggregatesChildIntoParentTask(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now()
	parent := platform.Task{ID: "task_parent", Title: "Mixed challenge", Category: platform.CategoryMisc, Description: "test", Status: platform.TaskReady, Image: "misc", CreatedAt: now, UpdatedAt: now}
	child := platform.Task{ID: "task_child", ParentTaskID: parent.ID, Title: "internal crypto", Category: platform.CategoryCrypto, Description: "test", Status: platform.TaskReady, Image: "crypto", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTask(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, child); err != nil {
		t.Fatal(err)
	}
	// 父子任务各记录一次调用，并使用不同模型以同时覆盖模型名去重聚合。
	for _, usage := range []platform.ModelUsage{
		{TaskID: parent.ID, Model: "model-a", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, UsageReported: true, StatusCode: 200, Status: "completed", CreatedAt: now},
		{TaskID: child.ID, Model: "model-b", InputTokens: 20, OutputTokens: 10, TotalTokens: 30, UsageReported: true, StatusCode: 200, Status: "completed", CreatedAt: now},
	} {
		if err := store.RecordModelUsage(ctx, usage); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.ModelUsageReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.RequestCount != 2 || report.Summary.TotalTokens != 45 || len(report.Tasks) != 1 {
		t.Fatalf("unexpected report %#v", report)
	}
	if report.Tasks[0].TaskID != parent.ID || report.Tasks[0].TotalTokens != 45 || len(report.Tasks[0].Models) != 2 {
		t.Fatalf("unexpected task report %#v", report.Tasks[0])
	}
}

// TestExecutionSettingsAndQueuedTasks 验证默认并发上限、手动设置和 FIFO 排队顺序。
func TestExecutionSettingsAndQueuedTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	settings, err := store.ExecutionSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxConcurrentTasks != platform.DefaultMaxConcurrentTasks {
		t.Fatalf("unexpected default settings %#v", settings)
	}
	if err := store.UpdateExecutionSettings(ctx, platform.ExecutionSettings{MaxConcurrentTasks: 3}); err != nil {
		t.Fatal(err)
	}
	settings, err = store.ExecutionSettings(ctx)
	if err != nil || settings.MaxConcurrentTasks != 3 {
		t.Fatalf("unexpected stored settings %#v, err=%v", settings, err)
	}

	now := time.Now().UTC()
	for _, task := range []platform.Task{
		{ID: "task_running", Title: "running", Category: platform.CategoryWeb, Description: "test", Status: platform.TaskRunning, Image: "web", CreatedAt: now, UpdatedAt: now},
		{ID: "task_first", Title: "first", Category: platform.CategoryCrypto, Description: "test", Status: platform.TaskQueued, Image: "crypto", CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "task_second", Title: "second", Category: platform.CategoryMisc, Description: "test", Status: platform.TaskQueued, Image: "misc", CreatedAt: now, UpdatedAt: now.Add(2 * time.Second)},
	} {
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	active, err := store.CountActiveTasks(ctx)
	if err != nil || active != 1 {
		t.Fatalf("unexpected active count %d, err=%v", active, err)
	}
	queued, err := store.ListQueuedTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 || queued[0].ID != "task_first" || queued[1].ID != "task_second" {
		t.Fatalf("unexpected queued order %#v", queued)
	}
}

// TestListChildTasks 验证内部子 Agent 不会混入其他父题目，并保持创建顺序。
func TestListChildTasks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now()
	for _, task := range []platform.Task{
		{ID: "root_a", Title: "root a", Category: platform.CategoryMisc, Description: "test", Status: platform.TaskSettled, Image: "misc", CreatedAt: now, UpdatedAt: now},
		{ID: "root_b", Title: "root b", Category: platform.CategoryWeb, Description: "test", Status: platform.TaskReady, Image: "web", CreatedAt: now, UpdatedAt: now},
		{ID: "child_one", ParentTaskID: "root_a", HandoffID: "subtask_one", Title: "one", Category: platform.CategoryCrypto, Description: "test", Status: platform.TaskQueued, Image: "crypto", CreatedAt: now.Add(time.Second), UpdatedAt: now},
		{ID: "child_two", ParentTaskID: "root_a", HandoffID: "subtask_two", Title: "two", Category: platform.CategoryReverse, Description: "test", Status: platform.TaskReady, Image: "reverse", CreatedAt: now.Add(2 * time.Second), UpdatedAt: now},
		{ID: "child_other", ParentTaskID: "root_b", HandoffID: "subtask_other", Title: "other", Category: platform.CategoryPwn, Description: "test", Status: platform.TaskReady, Image: "pwn", CreatedAt: now.Add(3 * time.Second), UpdatedAt: now},
	} {
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	children, err := store.ListChildTasks(ctx, "root_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 || children[0].ID != "child_one" || children[1].ID != "child_two" {
		t.Fatalf("unexpected child task list %#v", children)
	}
}
