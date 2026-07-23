package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

func TestTaskAndEventJournal(t *testing.T) {
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
	events, err := store.ListEvents(ctx, task.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 2 || events[0].Type != "agent.started" {
		t.Fatalf("unexpected event replay: %#v", events)
	}
}

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
