package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
	parent_task_id TEXT NOT NULL DEFAULT '',
	handoff_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    category TEXT NOT NULL,
    description TEXT NOT NULL,
	prompt TEXT NOT NULL DEFAULT '',
    target TEXT NOT NULL DEFAULT '',
    flag_format TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    runtime TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    source TEXT NOT NULL,
    event_type TEXT NOT NULL,
    turn_id TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    payload BLOB NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(task_id, sequence)
);
CREATE INDEX IF NOT EXISTS idx_task_events_task_sequence
ON task_events(task_id, sequence);
CREATE TABLE IF NOT EXISTS model_usage (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    usage_reported INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_usage_task_created
ON model_usage(task_id, created_at);
CREATE INDEX IF NOT EXISTS idx_model_usage_created
ON model_usage(created_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	// Existing local installations predate the per-task prompt column. SQLite
	// does not support ADD COLUMN IF NOT EXISTS, so ignore the expected error
	// when the migration has already been applied.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN prompt TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task prompt: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN parent_task_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate parent task: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN handoff_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task handoff: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_tasks_parent_task ON tasks(parent_task_id)`); err != nil {
		return fmt.Errorf("create task parent index: %w", err)
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, task platform.Task) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks (
 id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, status, image,
 runtime, container_id, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.ParentTaskID, task.HandoffID, task.Title, string(task.Category), task.Description, task.Prompt, task.Target,
		task.FlagFormat, string(task.Status), task.Image, task.Runtime,
		task.ContainerID, task.LastError, formatTime(task.CreatedAt), formatTime(task.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

func (s *Store) ListTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []platform.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// ListRootTasks returns only user-created tasks. Specialist handoffs remain
// available to the daemon for lifecycle checks but are presented through their
// parent task's event timeline rather than as unrelated challenges.
func (s *Store) ListRootTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE parent_task_id = '' ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list root tasks: %w", err)
	}
	defer rows.Close()
	var tasks []platform.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) GetTask(ctx context.Context, id string) (platform.Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return platform.Task{}, err
	}
	return task, err
}

// DeleteTask removes the task row. SQLite foreign keys cascade to the
// associated event stream, so no orphaned task events remain in the database.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted task count: %w", err)
	}
	if deleted == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateTaskState(ctx context.Context, id string, status platform.TaskStatus, runtime, containerID, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tasks SET status = ?, runtime = ?, container_id = ?, last_error = ?, updated_at = ?
WHERE id = ?`, string(status), runtime, containerID, lastError, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update task state: %w", err)
	}
	return nil
}

// UpdateTaskPrompt persists the user-provided extra direction that is appended
// to the next Pi system prompt for this task.
func (s *Store) UpdateTaskPrompt(ctx context.Context, id, prompt string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE tasks SET prompt = ?, updated_at = ? WHERE id = ?`, prompt, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update task prompt: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated task count: %w", err)
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordModelUsage appends a small accounting record after a proxied model
// request completes. It intentionally stores no prompts, responses, headers,
// model credentials, or other task contents.
func (s *Store) RecordModelUsage(ctx context.Context, usage platform.ModelUsage) error {
	if usage.ID == "" {
		usage.ID = platform.NewID("usage")
	}
	if usage.CreatedAt.IsZero() {
		usage.CreatedAt = time.Now()
	}
	if usage.TotalTokens == 0 && usage.UsageReported {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO model_usage (
 id, task_id, model, input_tokens, cached_input_tokens, output_tokens,
 reasoning_tokens, total_tokens, usage_reported, latency_ms, status_code,
 status, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		usage.ID, usage.TaskID, usage.Model, usage.InputTokens,
		usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningTokens,
		usage.TotalTokens, boolToInt(usage.UsageReported), usage.LatencyMS,
		usage.StatusCode, usage.Status, formatTime(usage.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert model usage: %w", err)
	}
	return nil
}

// ModelUsageReport returns aggregates for the whole local CTF-BTFly journal. Child
// specialist tasks are accounted to their visible parent challenge.
func (s *Store) ModelUsageReport(ctx context.Context) (platform.ModelUsageReport, error) {
	var report platform.ModelUsageReport
	row := s.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status_code < 200 OR status_code >= 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(usage_reported), 0),
       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
       COALESCE(SUM(output_tokens), 0), COALESCE(SUM(reasoning_tokens), 0),
       COALESCE(SUM(total_tokens), 0)
FROM model_usage`)
	if err := row.Scan(&report.Summary.RequestCount, &report.Summary.SuccessfulRequests,
		&report.Summary.FailedRequests, &report.Summary.ReportedRequests,
		&report.Summary.InputTokens, &report.Summary.CachedInputTokens,
		&report.Summary.OutputTokens, &report.Summary.ReasoningTokens,
		&report.Summary.TotalTokens); err != nil {
		return report, fmt.Errorf("summarize model usage: %w", err)
	}

	taskRows, err := s.db.QueryContext(ctx, `
SELECT root.id, root.title, root.category,
       COALESCE(GROUP_CONCAT(DISTINCT usage.model), ''),
       COUNT(*), COALESCE(SUM(usage.usage_reported), 0),
       COALESCE(SUM(usage.input_tokens), 0), COALESCE(SUM(usage.cached_input_tokens), 0),
       COALESCE(SUM(usage.output_tokens), 0), COALESCE(SUM(usage.reasoning_tokens), 0),
       COALESCE(SUM(usage.total_tokens), 0)
FROM model_usage AS usage
JOIN tasks AS source ON source.id = usage.task_id
JOIN tasks AS root ON root.id = CASE WHEN source.parent_task_id = '' THEN source.id ELSE source.parent_task_id END
GROUP BY root.id, root.title, root.category
ORDER BY SUM(usage.total_tokens) DESC, MAX(usage.created_at) DESC`)
	if err != nil {
		return report, fmt.Errorf("list task model usage: %w", err)
	}
	defer taskRows.Close()
	for taskRows.Next() {
		var item platform.ModelUsageTask
		var category, models string
		if err := taskRows.Scan(&item.TaskID, &item.Title, &category, &models,
			&item.RequestCount, &item.ReportedRequests, &item.InputTokens,
			&item.CachedInputTokens, &item.OutputTokens, &item.ReasoningTokens,
			&item.TotalTokens); err != nil {
			return report, fmt.Errorf("scan task model usage: %w", err)
		}
		item.Category = platform.Category(category)
		item.Models = splitModels(models)
		report.Tasks = append(report.Tasks, item)
	}
	if err := taskRows.Err(); err != nil {
		return report, err
	}

	dayRows, err := s.db.QueryContext(ctx, `
SELECT date(created_at, 'localtime'), COUNT(*), COALESCE(SUM(usage_reported), 0),
       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
       COALESCE(SUM(total_tokens), 0)
FROM model_usage
GROUP BY date(created_at, 'localtime')
ORDER BY date(created_at, 'localtime') DESC
LIMIT 30`)
	if err != nil {
		return report, fmt.Errorf("list daily model usage: %w", err)
	}
	defer dayRows.Close()
	for dayRows.Next() {
		var item platform.ModelUsageDay
		if err := dayRows.Scan(&item.Date, &item.RequestCount, &item.ReportedRequests,
			&item.InputTokens, &item.OutputTokens, &item.TotalTokens); err != nil {
			return report, fmt.Errorf("scan daily model usage: %w", err)
		}
		report.Days = append(report.Days, item)
	}
	if err := dayRows.Err(); err != nil {
		return report, err
	}
	// The query is descending for an efficient LIMIT. The chart is easier to
	// read from oldest to newest.
	for left, right := 0, len(report.Days)-1; left < right; left, right = left+1, right-1 {
		report.Days[left], report.Days[right] = report.Days[right], report.Days[left]
	}
	return report, nil
}

func (s *Store) AppendEvent(ctx context.Context, event platform.Event) (platform.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	if event.ID == "" {
		event.ID = platform.NewID("evt")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM task_events WHERE task_id = ?`,
		event.TaskID,
	).Scan(&event.Sequence); err != nil {
		return event, fmt.Errorf("allocate event sequence: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO task_events (
 id, task_id, sequence, source, event_type, turn_id, tool_call_id, payload, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.TaskID, event.Sequence,
		event.Source, event.Type, event.TurnID, event.ToolCallID, []byte(event.Payload),
		formatTime(event.CreatedAt))
	if err != nil {
		return event, fmt.Errorf("insert task event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}

func (s *Store) ListEvents(ctx context.Context, taskID string, after int64, limit int) ([]platform.Event, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, sequence, source, event_type, turn_id, tool_call_id, payload, created_at
FROM task_events WHERE task_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, taskID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	defer rows.Close()
	var events []platform.Event
	for rows.Next() {
		var event platform.Event
		var created string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.Sequence, &event.Source,
			&event.Type, &event.TurnID, &event.ToolCallID, &event.Payload, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		events = append(events, event)
	}
	return events, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanTask(row scanner) (platform.Task, error) {
	var task platform.Task
	var category, status, created, updated string
	if err := row.Scan(&task.ID, &task.ParentTaskID, &task.HandoffID, &task.Title, &category, &task.Description, &task.Prompt, &task.Target,
		&task.FlagFormat, &status, &task.Image, &task.Runtime, &task.ContainerID,
		&task.LastError, &created, &updated); err != nil {
		return task, err
	}
	task.Category = platform.Category(category)
	task.Status = platform.TaskStatus(status)
	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return task, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func splitModels(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			models = append(models, trimmed)
		}
	}
	return models
}
