export type Category = 'web' | 'crypto' | 'pwn' | 'reverse' | 'forensics' | 'misc'
export type TaskStatus = 'ready' | 'provisioning' | 'running' | 'paused' | 'settled' | 'failed' | 'cancelled'
export interface DaemonConnection { baseUrl: string; token: string }
export interface Task {
  id: string; title: string; category: Category; description: string; prompt: string; target?: string; flagFormat?: string
  status: TaskStatus; image: string; runtime?: string; containerId?: string; lastError?: string
  createdAt: string; updatedAt: string
}
export interface CreateTask { title: string; category: Category; description: string; target?: string; flagFormat?: string }
export interface TaskPrompt {
  prompt: string
  systemPrompt: string
  editable: boolean
  retryable: boolean
  resumable: boolean
}
export interface AttachmentInput { file: File; path: string }
export interface PlatformEvent {
  id: string; taskId: string; sequence: number; source: string; type: string; turnId?: string
  toolCallId?: string; payload: Record<string, unknown>; createdAt: string
}
export interface WorkspaceFile {
  path: string; size: number; modifiedAt: string
}
export interface WorkspaceFileContent {
  path: string; content: string; truncated: boolean; binary: boolean
}
export interface Writeup {
  exists: boolean; content: string; truncated?: boolean; binary?: boolean
}
export interface DockerHealth {
  available: boolean; serverVersion?: string; runtimes: string[]; normalRuntime: string; pwnRuntime: string
  isolationWarnings?: string[]
}
export interface SystemStatus {
  daemon: { address: string; version: string }
  docker: DockerHealth
  modelGateway: { configured: boolean; model: string }
  stack: string[]
}
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
