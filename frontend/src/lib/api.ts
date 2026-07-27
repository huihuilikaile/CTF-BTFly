import type { AttachmentInput, CreateTask, DaemonConnection, ExecutionSettings, ModelConfigInput, ModelConfigList, ModelProbeResult, ModelUsageReport, PlatformEvent, SystemStatus, Task, TaskPrompt, WorkspaceFile, WorkspaceFileContent, Writeup } from './types'

// PlatformClient 封装所有 daemon REST/WebSocket 调用，并自动附加本机鉴权 Token。
export class PlatformClient {
  constructor(readonly connection: DaemonConnection) {}

  // request 是 JSON API 的统一入口：合并调用方配置、解析结构化错误并返回强类型结果。
  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(this.connection.baseUrl + path, {
      ...init,
      headers: { Authorization: `Bearer ${this.connection.token}`, 'Content-Type': 'application/json', ...init?.headers },
    })
    const body = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(String(body.error ?? `Request failed (${response.status})`))
    return body as T
  }

  // 系统、用量和任务列表属于无副作用查询。
  system = () => this.request<SystemStatus>('/api/system')
  modelProbe = (profile?: string) => this.request<ModelProbeResult>(`/api/system/model-probe${profile ? `?profile=${encodeURIComponent(profile)}` : ''}`, { method: 'POST' })
  modelConfigs = () => this.request<ModelConfigList>('/api/models/config')
  saveModelConfig = (config: ModelConfigInput) => this.request<ModelConfigList>('/api/models/config', { method: 'PUT', body: JSON.stringify(config) })
  deleteModelConfig = (profile: string) => this.request<ModelConfigList>(`/api/models/config/${encodeURIComponent(profile)}`, { method: 'DELETE' })
  executionSettings = () => this.request<ExecutionSettings>('/api/settings')
  updateExecutionSettings = (settings: ExecutionSettings) => this.request<ExecutionSettings>('/api/settings', { method: 'PUT', body: JSON.stringify(settings) })
  modelUsage = () => this.request<ModelUsageReport>('/api/model-usage')
  tasks = () => this.request<Task[]>('/api/tasks')

  // 任务创建只提交允许字段；附件在任务 ID 创建成功后单独上传。
  createTask = (value: CreateTask) => this.request<Task>('/api/tasks', { method: 'POST', body: JSON.stringify(value) })

  // uploadAttachments 使用 FormData 同时传递二进制文件和一一对应的相对路径数组。
  async uploadAttachments(id: string, attachments: AttachmentInput[]): Promise<WorkspaceFile[]> {
    if (!attachments.length) return []
    const body = new FormData()
    body.append('paths', JSON.stringify(attachments.map(attachment => attachment.path)))
    for (const attachment of attachments) body.append('files', attachment.file, attachment.file.name)
    const response = await fetch(this.connection.baseUrl + `/api/tasks/${encodeURIComponent(id)}/attachments`, {
      method: 'POST', headers: { Authorization: `Bearer ${this.connection.token}` }, body,
    })
    const result = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(String(result.error ?? `Request failed (${response.status})`))
    return Array.isArray(result.files) ? result.files as WorkspaceFile[] : []
  }

  // 任务生命周期与 Prompt 修改接口均通过统一 request 处理鉴权和错误。
  deleteTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}`, { method: 'DELETE' })
  taskPrompt = (id: string) => this.request<TaskPrompt>(`/api/tasks/${encodeURIComponent(id)}/prompt`)
  updateTaskPrompt = (id: string, prompt: string) => this.request<Task>(`/api/tasks/${encodeURIComponent(id)}/prompt`, { method: 'PUT', body: JSON.stringify({ prompt }) })
  startTask = (id: string) => this.request(`/api/tasks/${encodeURIComponent(id)}/start`, { method: 'POST' })
  abortTask = (id: string) => this.request(`/api/tasks/${encodeURIComponent(id)}/abort`, { method: 'POST' })
  pauseTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/pause`, { method: 'POST' })
  resumeTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/resume`, { method: 'POST' })
  retryTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/retry`, { method: 'POST' })
  closeSandbox = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/close-sandbox`, { method: 'POST' })
  subtasks = (id: string) => this.request<Task[]>(`/api/tasks/${encodeURIComponent(id)}/subtasks`)
  events = (id: string, after = 0) => this.request<PlatformEvent[]>(`/api/tasks/${encodeURIComponent(id)}/events?after=${after}`)

  // 文件预览返回 JSON；下载接口返回 Blob，供浏览器生成临时下载链接。
  files = (id: string) => this.request<WorkspaceFile[]>(`/api/tasks/${encodeURIComponent(id)}/files`)
  file = (id: string, path: string) => this.request<WorkspaceFileContent>(`/api/tasks/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}`)
  downloadFile = async (id: string, path: string): Promise<Blob> => {
    const response = await fetch(this.connection.baseUrl + `/api/tasks/${encodeURIComponent(id)}/download?path=${encodeURIComponent(path)}`, {
      headers: { Authorization: `Bearer ${this.connection.token}` },
    })
    if (!response.ok) {
      const body = await response.json().catch(() => ({}))
      throw new Error(String(body.error ?? `Request failed (${response.status})`))
    }
    return response.blob()
  }

  writeup = (id: string) => this.request<Writeup>(`/api/tasks/${encodeURIComponent(id)}/writeup`)

  // 浏览器 WebSocket API 不能设置 Authorization 请求头，因此只在 WS URL
  // 查询参数中携带 daemon Token；daemon 默认仅监听本机回环地址。
  eventSocket(id: string, after: number): WebSocket {
    const url = new URL(this.connection.baseUrl)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.pathname = `/ws/tasks/${encodeURIComponent(id)}`
    url.searchParams.set('after', String(after))
    url.searchParams.set('token', this.connection.token)
    return new WebSocket(url)
  }
}
