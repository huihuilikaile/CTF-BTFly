package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Category string

const (
	CategoryWeb       Category = "web"
	CategoryCrypto    Category = "crypto"
	CategoryPwn       Category = "pwn"
	CategoryReverse   Category = "reverse"
	CategoryForensics Category = "forensics"
	CategoryMisc      Category = "misc"
)

var validCategories = map[Category]struct{}{
	CategoryWeb: {}, CategoryCrypto: {}, CategoryPwn: {},
	CategoryReverse: {}, CategoryForensics: {}, CategoryMisc: {},
}

func ParseCategory(value string) (Category, error) {
	category := Category(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := validCategories[category]; !ok {
		return "", fmt.Errorf("unsupported challenge category %q", value)
	}
	return category, nil
}

type TaskStatus string

const (
	TaskReady        TaskStatus = "ready"
	TaskProvisioning TaskStatus = "provisioning"
	TaskRunning      TaskStatus = "running"
	TaskPaused       TaskStatus = "paused"
	TaskSettled      TaskStatus = "settled"
	TaskFailed       TaskStatus = "failed"
	TaskCancelled    TaskStatus = "cancelled"
)

type Task struct {
	ID string `json:"id"`
	// ParentTaskID is set only for internal specialist handoffs. The desktop
	// task list deliberately hides these children; their progress is relayed to
	// the parent task through events and copied artifacts.
	ParentTaskID string     `json:"parentTaskId,omitempty"`
	HandoffID    string     `json:"handoffId,omitempty"`
	Title        string     `json:"title"`
	Category     Category   `json:"category"`
	Description  string     `json:"description"`
	Prompt       string     `json:"prompt"`
	Target       string     `json:"target,omitempty"`
	FlagFormat   string     `json:"flagFormat,omitempty"`
	Status       TaskStatus `json:"status"`
	Image        string     `json:"image"`
	Runtime      string     `json:"runtime,omitempty"`
	ContainerID  string     `json:"containerId,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type CreateTask struct {
	Title       string `json:"title"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Target      string `json:"target,omitempty"`
	FlagFormat  string `json:"flagFormat,omitempty"`
}

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

// ModelUsage is one completed request made from an Agent sandbox through the
// local model gateway. Prompts and model responses are deliberately excluded:
// the usage journal is an accounting record, not a second copy of task data.
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

// ModelUsageSummary is the aggregate for every request currently retained in
// the local SQLite journal. Costs are intentionally not inferred here because
// every provider has different pricing, cache accounting, and billing rules.
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

// ModelUsageTask groups requests by the user-visible root task. Specialist
// handoff containers are deliberately folded into their parent challenge.
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

// ModelUsageDay is a local-calendar daily aggregate for the bar chart.
type ModelUsageDay struct {
	Date             string `json:"date"`
	RequestCount     int64  `json:"requestCount"`
	ReportedRequests int64  `json:"reportedRequests"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	TotalTokens      int64  `json:"totalTokens"`
}

type ModelUsageReport struct {
	Summary ModelUsageSummary `json:"summary"`
	Tasks   []ModelUsageTask  `json:"tasks"`
	Days    []ModelUsageDay   `json:"days"`
}

func NewID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

func JSONPayload(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"error":"payload encoding failed"}`)
	}
	return data
}
