// Category 与 Go platform.Category 一一对应，用于镜像选择和界面筛选。
export type Category = 'web' | 'crypto' | 'pwn' | 'reverse' | 'forensics' | 'misc'

// TaskStatus 镜像后端任务状态机；新增后端状态时必须同步更新此联合类型。
export type TaskStatus = 'ready' | 'queued' | 'delegating' | 'provisioning' | 'running' | 'paused' | 'settled' | 'failed' | 'cancelled'

// DaemonConnection 由 Wails DesktopService 返回，只在当前 WebView 内存中使用。
export interface DaemonConnection { baseUrl: string; token: string }

// Task 是 REST API 返回的用户可见根任务。
export interface Task {
  id: string; parentTaskId?: string; handoffId?: string; title: string; category: Category; description: string; prompt: string; target?: string; flagFormat?: string
  status: TaskStatus; image: string; runtime?: string; containerId?: string; lastError?: string
  createdAt: string; updatedAt: string
}

// CreateTask 只包含允许用户提交的题目字段。
export interface CreateTask { title: string; category: Category; description: string; target?: string; flagFormat?: string }

// TaskPrompt 分离可编辑补充提示、只读系统 Prompt 和当前可执行操作。
export interface TaskPrompt {
  prompt: string
  systemPrompt: string
  editable: boolean
  retryable: boolean
  resumable: boolean
}

// AttachmentInput 同时保留浏览器 File 与文件夹内相对路径。
export interface AttachmentInput { file: File; path: string }

// PlatformEvent 是按 sequence 排序、可经 REST/WS 重放的统一事件。
export interface PlatformEvent {
  id: string; taskId: string; sequence: number; source: string; type: string; turnId?: string
  toolCallId?: string; payload: Record<string, unknown>; createdAt: string
}

// WorkspaceFile 与 WorkspaceFileContent 分别表示文件元数据和有界预览。
export interface WorkspaceFile {
  path: string; size: number; modifiedAt: string
}
export interface WorkspaceFileContent {
  path: string; content: string; truncated: boolean; binary: boolean
}

// Writeup 是固定 WRITEUP.md 的读取结果。
export interface Writeup {
  exists: boolean; content: string; truncated?: boolean; binary?: boolean
}

// DockerHealth 描述 Engine 可用性、运行时选择和隔离降级警告。
export interface DockerHealth {
  available: boolean; serverVersion?: string; runtimes: string[]; normalRuntime: string; pwnRuntime: string
  isolationWarnings?: string[]
}

// ExecutionSettings 与 SchedulerStatus 描述 daemon 持久化的任务并发上限和 FIFO 队列。
export interface ExecutionSettings { maxConcurrentTasks: number }
export interface SchedulerQueueItem {
  taskId: string; title: string; category: Category; position: number; internal: boolean; queuedAt: string
}
export interface SchedulerStatus {
  settings: ExecutionSettings; activeTaskCount: number; queuedTaskCount: number; queue: SchedulerQueueItem[]
}

// SystemStatus 汇总 daemon、Docker、模型网关与项目技术栈。
export interface SystemStatus {
  daemon: { address: string; version: string }
  docker: DockerHealth
  modelGateway: { configured: boolean; model: string; probe: ModelProbeStatus }
  scheduler: SchedulerStatus
  stack: string[]
}

// ModelProbeStatus 是 daemon 对真实上游模型的最近一次轻量连接检测结果；
// 不包含 API 地址、密钥、Prompt 或模型响应内容。
export interface ModelProbeStatus {
  configured: boolean; available: boolean; checkedAt?: string; error?: string
}

// 以下模型用量类型与后端三层统计结构保持一致。
export interface ModelUsageSummary {
  requestCount: number; successfulRequests: number; failedRequests: number; reportedRequests: number
  inputTokens: number; cachedInputTokens: number; outputTokens: number; reasoningTokens: number; totalTokens: number
}
export interface ModelUsageTask {
  taskId: string; title: string; category: Category; models: string[]
  requestCount: number; reportedRequests: number
  inputTokens: number; cachedInputTokens: number; outputTokens: number; reasoningTokens: number; totalTokens: number
}
export interface ModelUsageDay {
  date: string; requestCount: number; reportedRequests: number; inputTokens: number; outputTokens: number; totalTokens: number
}
export interface ModelUsageReport {
  summary: ModelUsageSummary
  tasks: ModelUsageTask[]
  days: ModelUsageDay[]
}
