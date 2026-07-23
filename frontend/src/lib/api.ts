import type { AttachmentInput, CreateTask, DaemonConnection, ModelUsageReport, PlatformEvent, SystemStatus, Task, TaskPrompt, WorkspaceFile, WorkspaceFileContent, Writeup } from './types'

export class PlatformClient {
  constructor(readonly connection: DaemonConnection) {}
  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(this.connection.baseUrl + path, {
      ...init,
      headers: { Authorization: `Bearer ${this.connection.token}`, 'Content-Type': 'application/json', ...init?.headers },
    })
    const body = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(String(body.error ?? `Request failed (${response.status})`))
    return body as T
  }
  system = () => this.request<SystemStatus>('/api/system')
  modelUsage = () => this.request<ModelUsageReport>('/api/model-usage')
  tasks = () => this.request<Task[]>('/api/tasks')
  createTask = (value: CreateTask) => this.request<Task>('/api/tasks', { method: 'POST', body: JSON.stringify(value) })
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
  deleteTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}`, { method: 'DELETE' })
  taskPrompt = (id: string) => this.request<TaskPrompt>(`/api/tasks/${encodeURIComponent(id)}/prompt`)
  updateTaskPrompt = (id: string, prompt: string) => this.request<Task>(`/api/tasks/${encodeURIComponent(id)}/prompt`, { method: 'PUT', body: JSON.stringify({ prompt }) })
  startTask = (id: string) => this.request(`/api/tasks/${encodeURIComponent(id)}/start`, { method: 'POST' })
  abortTask = (id: string) => this.request(`/api/tasks/${encodeURIComponent(id)}/abort`, { method: 'POST' })
  pauseTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/pause`, { method: 'POST' })
  resumeTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/resume`, { method: 'POST' })
  retryTask = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/retry`, { method: 'POST' })
  closeSandbox = (id: string) => this.request<{ status: string }>(`/api/tasks/${encodeURIComponent(id)}/close-sandbox`, { method: 'POST' })
  events = (id: string, after = 0) => this.request<PlatformEvent[]>(`/api/tasks/${encodeURIComponent(id)}/events?after=${after}`)
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
  eventSocket(id: string, after: number): WebSocket {
    const url = new URL(this.connection.baseUrl)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.pathname = `/ws/tasks/${encodeURIComponent(id)}`
    url.searchParams.set('after', String(after))
    url.searchParams.set('token', this.connection.token)
    return new WebSocket(url)
  }
}
