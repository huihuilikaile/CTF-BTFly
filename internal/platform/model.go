package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Category 是平台支持的 CTF 题型枚举，也是镜像选择的键。
type Category string

// 题型值在 API、SQLite、前端和 Docker 镜像命名之间保持一致。
const (
	CategoryWeb       Category = "web"
	CategoryCrypto    Category = "crypto"
	CategoryPwn       Category = "pwn"
	CategoryReverse   Category = "reverse"
	CategoryForensics Category = "forensics"
	CategoryMisc      Category = "misc"
)

// validCategories 为外部字符串输入提供白名单校验。
var validCategories = map[Category]struct{}{
	CategoryWeb: {}, CategoryCrypto: {}, CategoryPwn: {},
	CategoryReverse: {}, CategoryForensics: {}, CategoryMisc: {},
}

// ParseCategory 规范化大小写与空白，并拒绝未支持的题型。
func ParseCategory(value string) (Category, error) {
	category := Category(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := validCategories[category]; !ok {
		return "", fmt.Errorf("unsupported challenge category %q", value)
	}
	return category, nil
}

// TaskStatus 表示题目从创建到结束的持久化状态。
type TaskStatus string

// 状态流大致为 ready → provisioning → running → paused/terminal。
// settled 表示 Agent 本轮已结束，但 Docker 实例仍可能保留供检查或重试。
const (
	TaskReady TaskStatus = "ready"
	// queued 表示用户已经请求启动，但全局运行名额已满；它不占用 Docker 容器。
	TaskQueued TaskStatus = "queued"
	// delegating 表示父 Agent 已释放自己的实例，正在等待最多三个专项子 Agent 回传。
	// 它不占用运行名额，也不是可删除/重试的终态。
	TaskDelegating   TaskStatus = "delegating"
	TaskProvisioning TaskStatus = "provisioning"
	TaskRunning      TaskStatus = "running"
	TaskPaused       TaskStatus = "paused"
	TaskSettled      TaskStatus = "settled"
	TaskFailed       TaskStatus = "failed"
	TaskCancelled    TaskStatus = "cancelled"
)

// Task 是跨 SQLite、REST 与前端传输的核心题目实体。
type Task struct {
	ID string `json:"id"`
	// ParentTaskID 只用于内部专项交接。桌面任务列表隐藏子任务，
	// 子任务进度通过父任务事件和复制后的产物呈现。
	ParentTaskID string     `json:"parentTaskId,omitempty"`
	HandoffID    string     `json:"handoffId,omitempty"`
	Title        string     `json:"title"`
	Category     Category   `json:"category"`
	Description  string     `json:"description"`
	Prompt       string     `json:"prompt"`
	Target       string     `json:"target,omitempty"`
	FlagFormat   string     `json:"flagFormat,omitempty"`
	ModelProfile string     `json:"modelProfile,omitempty"`
	ModelID      string     `json:"modelId,omitempty"`
	Status       TaskStatus `json:"status"`
	Image        string     `json:"image"`
	Runtime      string     `json:"runtime,omitempty"`
	ContainerID  string     `json:"containerId,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// ExecutionSettings 是本机 daemon 持久化的资源调度设置。默认值保守，
// 防止在同时管理多道题时一次性创建过多 Pi/Docker 实例。
type ExecutionSettings struct {
	MaxConcurrentTasks int `json:"maxConcurrentTasks"`
}

const (
	DefaultMaxConcurrentTasks = 5
	MinConcurrentTasks        = 1
	MaxConcurrentTasks        = 8
)

// Validate 约束手动配置的有效范围，避免零值导致队列永久停滞，
// 也避免在普通桌面设备上把并发无限提高。
func (settings ExecutionSettings) Validate() error {
	if settings.MaxConcurrentTasks < MinConcurrentTasks || settings.MaxConcurrentTasks > MaxConcurrentTasks {
		return fmt.Errorf("max concurrent tasks must be between %d and %d", MinConcurrentTasks, MaxConcurrentTasks)
	}
	return nil
}

// QueuedTask 是前端展示队列位置所需的最小公开信息。
type QueuedTask struct {
	TaskID   string    `json:"taskId"`
	Title    string    `json:"title"`
	Category Category  `json:"category"`
	Position int       `json:"position"`
	Internal bool      `json:"internal"`
	QueuedAt time.Time `json:"queuedAt"`
}

// SchedulerStatus 汇总当前真正占用运行名额的任务和等待队列。
type SchedulerStatus struct {
	Settings        ExecutionSettings `json:"settings"`
	ActiveTaskCount int               `json:"activeTaskCount"`
	QueuedTaskCount int               `json:"queuedTaskCount"`
	Queue           []QueuedTask      `json:"queue"`
}

// CreateTask 是创建 API 接受的用户输入，不允许调用方直接指定状态、
// 镜像、容器 ID 等受 daemon 控制的字段。
type CreateTask struct {
	Title        string `json:"title"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	Target       string `json:"target,omitempty"`
	FlagFormat   string `json:"flagFormat,omitempty"`
	ModelProfile string `json:"modelProfile,omitempty"`
}

// Event 是统一、可重放的事件记录；Sequence 在单个任务内严格递增。
type Event struct {
	ID         string          `json:"id"`
	TaskID     string          `json:"taskId"`
	Sequence   int64           `json:"sequence"`
	Source     string          `json:"source"`
	Type       string          `json:"type"`
	TurnID     string          `json:"turnId,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// ModelUsage 表示一次经本地网关完成的模型请求。该账本刻意不保存 Prompt
// 和模型回复，只用于调用量核算，避免复制敏感题目内容。
type ModelUsage struct {
	ID                string    `json:"id"`
	TaskID            string    `json:"taskId"`
	Model             string    `json:"model"`
	InputTokens       int64     `json:"inputTokens"`
	CachedInputTokens int64     `json:"cachedInputTokens"`
	OutputTokens      int64     `json:"outputTokens"`
	ReasoningTokens   int64     `json:"reasoningTokens"`
	TotalTokens       int64     `json:"totalTokens"`
	UsageReported     bool      `json:"usageReported"`
	LatencyMS         int64     `json:"latencyMs"`
	StatusCode        int       `json:"statusCode"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
}

// ModelUsageSummary 汇总 SQLite 中保留的全部模型请求。这里不推算费用，
// 因为不同供应商的价格、缓存计费和账单规则并不统一。
type ModelUsageSummary struct {
	RequestCount       int64 `json:"requestCount"`
	SuccessfulRequests int64 `json:"successfulRequests"`
	FailedRequests     int64 `json:"failedRequests"`
	ReportedRequests   int64 `json:"reportedRequests"`
	InputTokens        int64 `json:"inputTokens"`
	CachedInputTokens  int64 `json:"cachedInputTokens"`
	OutputTokens       int64 `json:"outputTokens"`
	ReasoningTokens    int64 `json:"reasoningTokens"`
	TotalTokens        int64 `json:"totalTokens"`
}

// ModelUsageTask 按用户可见的根任务聚合请求，专项子任务用量归并到父题目。
type ModelUsageTask struct {
	TaskID            string   `json:"taskId"`
	Title             string   `json:"title"`
	Category          Category `json:"category"`
	Models            []string `json:"models"`
	RequestCount      int64    `json:"requestCount"`
	ReportedRequests  int64    `json:"reportedRequests"`
	InputTokens       int64    `json:"inputTokens"`
	CachedInputTokens int64    `json:"cachedInputTokens"`
	OutputTokens      int64    `json:"outputTokens"`
	ReasoningTokens   int64    `json:"reasoningTokens"`
	TotalTokens       int64    `json:"totalTokens"`
}

// ModelUsageDay 是按本地日历日统计的图表数据。
type ModelUsageDay struct {
	Date             string `json:"date"`
	RequestCount     int64  `json:"requestCount"`
	ReportedRequests int64  `json:"reportedRequests"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	TotalTokens      int64  `json:"totalTokens"`
}

// ModelUsageReport 组合总览、按题目和按日期三种统计维度。
type ModelUsageReport struct {
	Summary ModelUsageSummary `json:"summary"`
	Tasks   []ModelUsageTask  `json:"tasks"`
	Days    []ModelUsageDay   `json:"days"`
}

// NewID 生成带业务前缀的 96 位随机 ID；仅当系统随机源失败时，
// 才回退到纳秒时间戳以保证调用流程仍可记录错误。
func NewID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

// JSONPayload 将任意结构编码为事件载荷；编码失败时返回可持久化的错误对象，
// 避免事件写入链因单个载荷而中断。
func JSONPayload(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":"payload encoding failed"}`)
	}
	return data
}
