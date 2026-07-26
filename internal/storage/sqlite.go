package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	_ "modernc.org/sqlite"
)

// Store 封装平台的 SQLite 连接和全部持久化操作。
// 上层服务不直接拼接 SQL，从而让事务、时间格式与错误语义保持一致。
type Store struct {
	db *sql.DB
}

// Open 创建数据库目录、打开 SQLite，并在返回前完成表结构迁移。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// 单连接配合 WAL 可避免同一进程内事件序号分配发生写竞争；
	// busy_timeout 负责短暂等待其他 SQLite 访问者释放锁。
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 释放底层数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// migrate 创建当前版本所需的任务、事件与模型用量表。
func (s *Store) migrate(ctx context.Context) error {
	// task_events 通过外键随任务删除；(task_id, sequence) 唯一约束
	// 保证每个任务的事件时间线不会出现重复序号。
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
	model_profile TEXT NOT NULL DEFAULT '',
	model_id TEXT NOT NULL DEFAULT '',
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
CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	// 兼容早期本地数据库。SQLite 不支持 ADD COLUMN IF NOT EXISTS，
	// 所以仅忽略明确的“列已存在”错误，其他迁移错误照常终止启动。
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN prompt TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task prompt: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN parent_task_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate parent task: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN handoff_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task handoff: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN model_profile TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task model profile: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN model_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return fmt.Errorf("migrate task model id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_tasks_parent_task ON tasks(parent_task_id)`); err != nil {
		return fmt.Errorf("create task parent index: %w", err)
	}
	return nil
}

// CreateTask 插入一条完整任务记录；状态、镜像和父子关系均由服务层决定。
func (s *Store) CreateTask(ctx context.Context, task platform.Task) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks (
 id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.ParentTaskID, task.HandoffID, task.Title, string(task.Category), task.Description, task.Prompt, task.Target,
		task.FlagFormat, task.ModelProfile, task.ModelID, string(task.Status), task.Image, task.Runtime,
		task.ContainerID, task.LastError, formatTime(task.CreatedAt), formatTime(task.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// ListTasks 返回所有任务（包括前端隐藏的内部专项子任务），
// 主要供生命周期检查和 daemon 退出保护使用。
func (s *Store) ListTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	// 复用 scanTask，确保列表与单条查询使用同一字段顺序和时间解析规则。
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

// ListRootTasks 只返回用户创建的根任务。专项子任务仍供 daemon 管理，
// 但其进度通过父任务时间线呈现，不在桌面端显示为无关题目。
func (s *Store) ListRootTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
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

// ListChildTasks 返回一个父题目创建的全部内部专项子任务，按创建顺序稳定排序。
// 该方法只供 daemon 编排和父任务工作区的协作状态展示使用，普通题目列表仍隐藏它们。
func (s *Store) ListChildTasks(ctx context.Context, parentTaskID string) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE parent_task_id = ? ORDER BY created_at ASC`, parentTaskID)
	if err != nil {
		return nil, fmt.Errorf("list child tasks: %w", err)
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

// CountActiveTasks 统计真正占用执行名额的任务。暂停任务仍持有 Pi 容器与会话，
// 因此必须计入；排队和已结束任务则不占用名额。
func (s *Store) CountActiveTasks(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tasks
WHERE status IN (?, ?, ?)`, string(platform.TaskProvisioning), string(platform.TaskRunning), string(platform.TaskPaused)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active tasks: %w", err)
	}
	return count, nil
}

// ListQueuedTasks 按进入队列的时间 FIFO 排序。updated_at 在任务进入 queued
// 状态时更新，后续不会被排队操作改写，因此可作为稳定的本地队列时间。
func (s *Store) ListQueuedTasks(ctx context.Context) ([]platform.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE status = ? ORDER BY updated_at ASC, created_at ASC`, string(platform.TaskQueued))
	if err != nil {
		return nil, fmt.Errorf("list queued tasks: %w", err)
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

// ExecutionSettings 读取本机执行设置；首次运行或旧数据库没有该配置时，
// 返回保守的默认并发 5，避免升级后突然无限并行。
func (s *Store) ExecutionSettings(ctx context.Context) (platform.ExecutionSettings, error) {
	settings := platform.ExecutionSettings{MaxConcurrentTasks: platform.DefaultMaxConcurrentTasks}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'max_concurrent_tasks'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, fmt.Errorf("read execution settings: %w", err)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return settings, nil
	}
	settings.MaxConcurrentTasks = parsed
	if err := settings.Validate(); err != nil {
		return platform.ExecutionSettings{MaxConcurrentTasks: platform.DefaultMaxConcurrentTasks}, nil
	}
	return settings, nil
}

// UpdateExecutionSettings 保存经过服务层校验的本机执行上限。
func (s *Store) UpdateExecutionSettings(ctx context.Context, settings platform.ExecutionSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO app_settings(key, value, updated_at) VALUES ('max_concurrent_tasks', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		strconv.Itoa(settings.MaxConcurrentTasks), formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("update execution settings: %w", err)
	}
	return nil
}

// GetTask 按主键读取任务；找不到时原样返回 sql.ErrNoRows，
// 供 API 层准确映射为 HTTP 404。
func (s *Store) GetTask(ctx context.Context, id string) (platform.Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, parent_task_id, handoff_id, title, category, description, prompt, target, flag_format, model_profile, model_id, status, image,
 runtime, container_id, last_error, created_at, updated_at
FROM tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return platform.Task{}, err
	}
	return task, err
}

// DeleteTask 删除任务行，SQLite 外键会级联删除该任务的事件与模型用量。
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

// UpdateTaskState 原子更新状态、运行时、容器 ID 与最后错误，
// 同时刷新 updated_at 供前端计时和排序。
func (s *Store) UpdateTaskState(ctx context.Context, id string, status platform.TaskStatus, runtime, containerID, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tasks SET status = ?, runtime = ?, container_id = ?, last_error = ?, updated_at = ?
WHERE id = ?`, string(status), runtime, containerID, lastError, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update task state: %w", err)
	}
	return nil
}

// UpdateTaskPrompt 持久化用户补充提示；服务层负责保证运行期间不可修改。
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

// RecordModelUsage 在代理请求结束后追加一条精简账本。
// 数据库刻意不保存 Prompt、回复、请求头、模型凭据或其他题目内容。
func (s *Store) RecordModelUsage(ctx context.Context, usage platform.ModelUsage) error {
	// 为测试或内部调用补齐 ID、时间和兼容上游缺省 total_tokens 的情况。
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

// ModelUsageReport 聚合整个本地模型账本，专项子任务会计入可见父题目。
func (s *Store) ModelUsageReport(ctx context.Context) (platform.ModelUsageReport, error) {
	var report platform.ModelUsageReport

	// 第一段查询生成全局请求成功率和 Token 汇总。
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

	// 第二段查询把 source 子任务通过 parent_task_id 折叠到 root，
	// 并按总 Token 与最近调用时间排序。
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

	// 第三段查询按本机时区生成最近 30 个有调用记录的自然日。
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
	// SQL 为高效 LIMIT 使用倒序，图表展示前再原地翻转为从旧到新。
	for left, right := 0, len(report.Days)-1; left < right; left, right = left+1, right-1 {
		report.Days[left], report.Days[right] = report.Days[right], report.Days[left]
	}
	return report, nil
}

// AppendEvent 在事务内分配下一个任务序号并写入事件，
// 使断线重放可以依赖稳定、单调的 sequence。
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
	// MAX(sequence)+1 与 INSERT 位于同一事务；Store 的单连接设置进一步
	// 串行化本进程中的并发事件写入。
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

// ListEvents 返回指定 sequence 之后的事件，并把单次读取限制在 5000 条以内。
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

// scanner 抽象 sql.Row 与 sql.Rows 共有的 Scan 方法，供 scanTask 复用。
type scanner interface{ Scan(dest ...any) error }

// scanTask 按固定列顺序解码任务，并恢复枚举与 RFC3339Nano 时间。
func scanTask(row scanner) (platform.Task, error) {
	var task platform.Task
	var category, status, created, updated string
	if err := row.Scan(&task.ID, &task.ParentTaskID, &task.HandoffID, &task.Title, &category, &task.Description, &task.Prompt, &task.Target,
		&task.FlagFormat, &task.ModelProfile, &task.ModelID, &status, &task.Image, &task.Runtime, &task.ContainerID,
		&task.LastError, &created, &updated); err != nil {
		return task, err
	}
	task.Category = platform.Category(category)
	task.Status = platform.TaskStatus(status)
	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return task, nil
}

// formatTime 统一使用 UTC RFC3339Nano 文本，避免 SQLite 时区歧义。
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

// boolToInt 把 Go 布尔值转换为 SQLite 使用的 0/1。
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// splitModels 清理 GROUP_CONCAT 生成的模型名列表。
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
