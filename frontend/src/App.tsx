import { useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type MouseEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity, Bot, Boxes, BrainCircuit, ChevronRight, CircleStop, Clock3, Container,
  Copy, Cpu, Database, Download, FileCode2, Flag, Gauge, Globe2, Hexagon, KeyRound, LayoutDashboard,
  LockKeyhole, Network, Play, Plus, Radio, RefreshCw, Search, Server, ShieldCheck, SquareTerminal,
  Moon, Palette, Settings, Sun, TriangleAlert, X,
} from 'lucide-react'
import { DesktopService } from '../bindings/github.com/ctfagentpi/ctfagentpi'
import { PlatformClient } from './lib/api'
import type { AttachmentInput, Category, CreateTask, DaemonConnection, PlatformEvent, Task, TaskStatus, WorkspaceFile, WorkspaceFileContent, Writeup } from './lib/types'
import { useTaskEvents } from './lib/useTaskEvents'
import { cn, eventText } from './lib/utils'
import { Button } from './components/ui/button'
import { Badge } from './components/ui/badge'

const categories: Array<{ id: Category; label: string; icon: typeof Globe2; colour: string }> = [
  { id: 'web', label: 'Web', icon: Globe2, colour: 'text-sky-300' },
  { id: 'pwn', label: 'Pwn', icon: SquareTerminal, colour: 'text-red-300' },
  { id: 'reverse', label: '逆向', icon: FileCode2, colour: 'text-violet-300' },
  { id: 'crypto', label: '密码', icon: KeyRound, colour: 'text-amber-300' },
  { id: 'forensics', label: '取证', icon: Search, colour: 'text-emerald-300' },
  { id: 'misc', label: '杂项', icon: Hexagon, colour: 'text-fuchsia-300' },
]

const statusStyles: Record<TaskStatus, string> = {
  ready: 'border-slate-600 bg-slate-800 text-slate-300', provisioning: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  running: 'border-sky-500/30 bg-sky-500/10 text-sky-300', paused: 'border-amber-500/30 bg-amber-500/10 text-amber-300', settled: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  failed: 'border-red-500/30 bg-red-500/10 text-red-300', cancelled: 'border-slate-600 bg-slate-800 text-slate-400',
}

const statusLabels: Record<TaskStatus, string> = {
  ready: '待启动',
  provisioning: '创建沙箱中',
  running: '运行中',
  paused: '已暂停',
  settled: '已稳定',
  failed: '失败',
  cancelled: '已取消',
}

type SidebarView = 'overview' | 'all' | 'running' | 'security' | 'usage' | 'task' | `category:${Category}`
type TaskContextMenuState = { task: Task; x: number; y: number }
type CreateTaskSubmission = { task: CreateTask; attachments: AttachmentInput[] }
type ConfirmationRequest = { kind: 'delete' | 'closeSandbox'; task: Task }
type ThemeID = 'dark' | 'light' | 'vscode' | 'one-dark' | 'dracula' | 'nord'

const themes: Array<{ id: ThemeID; name: string; description: string; colours: [string, string, string] }> = [
  { id: 'dark', name: '基础暗黑', description: 'CTF-BTFly 默认高密度深色界面', colours: ['#080c11', '#0d131b', '#38bdf8'] },
  { id: 'light', name: '基础亮色', description: '浅色背景与清晰文本对比', colours: ['#f8fafc', '#ffffff', '#0284c7'] },
  { id: 'vscode', name: 'VS Code Dark', description: '经典 Visual Studio Code 深色', colours: ['#1e1e1e', '#252526', '#007acc'] },
  { id: 'one-dark', name: 'One Dark', description: 'Atom / VS Code One Dark 风格', colours: ['#282c34', '#2c313a', '#61afef'] },
  { id: 'dracula', name: 'Dracula', description: '紫粉高对比开发者配色', colours: ['#282a36', '#44475a', '#bd93f9'] },
  { id: 'nord', name: 'Nord', description: '低饱和北境蓝灰配色', colours: ['#2e3440', '#3b4252', '#88c0d0'] },
]

const themeStorageKey = 'cpi.ui-theme'

function isThemeID(value: string | null): value is ThemeID { return !!value && themes.some(theme => theme.id === value) }
function loadTheme(): ThemeID {
  try {
    const saved = window.localStorage.getItem(themeStorageKey)
    return isThemeID(saved) ? saved : 'dark'
  } catch {
    return 'dark'
  }
}

// Scroll offsets are intentionally retained outside component state: changing
// them must not re-render the event list, and offsets should survive switching
// between a task's tabs or between retry attempts.
const processScrollPositions = new Map<string, number>()

const eventLabels: Record<string, string> = {
  'task.created': '任务已创建',
  'task.settled': '本轮解题结束',
  'task.paused': '任务已暂停',
  'task.resumed': '任务已恢复',
  'task.failed': '任务失败',
  'task.cancelled': '任务已取消',
	'task.prompt_updated': '提示词已更新',
	'task.retry_requested': '正在重新尝试',
  'sandbox.provisioning': '正在创建沙箱',
  'sandbox.started': '沙箱已启动',
  'sandbox.stopped': '沙箱实例已关闭',
  'handoff.crypto.started': '已转交 Crypto 专项分析',
  'handoff.crypto.completed': 'Crypto 专项结果已回传',
  'handoff.crypto.resuming_parent': '正在恢复杂项解题',
  'handoff.crypto.failed': 'Crypto 专项交接失败',
  'agent.started': 'Agent 已启动',
  'agent.ended': 'Agent 本轮结束',
  'agent.settled': 'Agent 已空闲',
  'agent.turn_started': '开始新一轮分析',
  'agent.turn_completed': '本轮分析完成',
  'agent.message.delta': 'Agent 输出',
  'agent.message.completed': 'Agent 消息完成',
  'agent.message.updated': 'Agent 消息更新',
  'agent.thinking.delta': '推理片段',
  'agent.retrying': '模型请求重试',
  'agent.compacting': '正在压缩上下文',
  'agent.extension_error': '扩展异常',
  'agent.protocol_error': 'Pi 协议异常',
  'agent.stream_error': 'Pi 事件流中断',
  'agent.stderr': 'Agent 标准错误',
  'tool.started': '工具已启动',
  'tool.output': '工具输出',
  'tool.completed': '工具执行完成',
  'flag.candidate': '发现 Flag 候选',
}

function eventLabel(type: string) { return eventLabels[type] ?? type }
function runtimeWarning(warning: string) {
  if (warning.startsWith('gVisor/runsc')) return '未检测到 gVisor/runsc；普通题当前使用受限 runc 开发模式。'
  if (warning.startsWith('Kata runtime')) return '未检测到 Kata；Pwn 当前使用 runc + SYS_PTRACE 开发模式。'
  return warning
}
function displayError(error: string) {
  if (error.includes('model gateway is not configured')) return '尚未配置模型网关。请设置 CTF_UPSTREAM_MODEL_BASE_URL、CTF_UPSTREAM_MODEL_API_KEY 和 CTF_MODEL_ID。'
  if (error.includes('task is already running')) return '该任务正在运行。'
  if (error.includes('only a running task can be paused')) return '仅运行中的任务可以暂停。'
  if (error.includes('only a paused task can be resumed')) return '仅已暂停的任务可以恢复。'
  if (error.includes('has no active sandbox session to resume')) return '暂停期间后端已重启，原 Pi 会话不可恢复；请在补充提示后重新尝试。'
  if (error.includes('only settled, failed, or cancelled tasks can be deleted')) return '仅可删除已稳定、失败或已取消的任务。'
  if (error.includes('attachments cannot be changed while a task is provisioning or running')) return '任务正在创建沙箱或运行中，暂时不能修改附件。'
	if (error.includes('prompt cannot be changed while the agent is running')) return '任务运行中，提示词将在中止或本轮结束后才能修改。'
	if (error.includes('only a settled, failed, or cancelled task can be retried')) return '请先中止任务，或等待本轮解题结束后再重新尝试。'
  return error
}

function viewTitle(view: SidebarView) {
  if (view === 'overview') return '系统概况'
  if (view === 'all') return '全部题目'
  if (view === 'running') return '运行中任务'
  if (view === 'security') return '安全策略'
  if (view === 'usage') return '模型用量'
  if (view.startsWith('category:')) return categories.find(category => category.id === view.slice('category:'.length))?.label ?? '题目类型'
  return '题目工作区'
}

function categoryForView(view: SidebarView): Category | undefined {
  return view.startsWith('category:') ? view.slice('category:'.length) as Category : undefined
}

function taskCanBeDeleted(task: Task) {
  return task.status === 'settled' || task.status === 'failed' || task.status === 'cancelled'
}

function App() {
  const queryClient = useQueryClient()
  const [connection, setConnection] = useState<DaemonConnection | null>(null)
  const [startupError, setStartupError] = useState('')
  const [activeView, setActiveView] = useState<SidebarView>('overview')
  const [selectedTaskID, setSelectedTaskID] = useState<string>()
  const [showCreate, setShowCreate] = useState(false)
  const [taskMenu, setTaskMenu] = useState<TaskContextMenuState | null>(null)
  const [deleteError, setDeleteError] = useState('')
  const [confirmation, setConfirmation] = useState<ConfirmationRequest | null>(null)
  const [theme, setTheme] = useState<ThemeID>(loadTheme)
  const [showSettings, setShowSettings] = useState(false)
  const client = useMemo(() => connection ? new PlatformClient(connection) : null, [connection])

  useEffect(() => {
    DesktopService.GetDaemonConnection().then(value => setConnection(value)).catch(error => setStartupError(String(error)))
  }, [])
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try { window.localStorage.setItem(themeStorageKey, theme) } catch { /* Keep the in-memory selection in restrictive WebViews. */ }
  }, [theme])
  const system = useQuery({ queryKey: ['system'], queryFn: () => client!.system(), enabled: !!client, refetchInterval: 5_000 })
  const tasks = useQuery({
    queryKey: ['tasks'],
    queryFn: () => client!.tasks(),
    enabled: !!client,
    // 解题页的实时状态由事件流驱动；降低轮询频率，避免无意义地重绘整个工作区。
    refetchInterval: activeView === 'task' ? 8_000 : 2_000,
    refetchIntervalInBackground: false,
  })
  const selected = activeView === 'task' ? tasks.data?.find(task => task.id === selectedTaskID) : undefined
  const stream = useTaskEvents(client, selected?.id)
  const createTask = useMutation({
    mutationFn: async ({ task, attachments }: CreateTaskSubmission) => {
      const created = await client!.createTask(task)
      await client!.uploadAttachments(created.id, attachments)
      return created
    },
    onSuccess: task => { queryClient.invalidateQueries({ queryKey: ['tasks'] }); setSelectedTaskID(task.id); setActiveView('task'); setShowCreate(false) },
  })
  const startTask = useMutation({ mutationFn: (id: string) => client!.startTask(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }) })
  const abortTask = useMutation({ mutationFn: (id: string) => client!.abortTask(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }) })
  const pauseTask = useMutation({ mutationFn: (id: string) => client!.pauseTask(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }) })
  const resumeTask = useMutation({ mutationFn: (id: string) => client!.resumeTask(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }) })
  const closeSandbox = useMutation({ mutationFn: (id: string) => client!.closeSandbox(id), onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }) })
  const deleteTask = useMutation({
    mutationFn: (id: string) => client!.deleteTask(id),
    onSuccess: (_, id) => {
      queryClient.invalidateQueries({ queryKey: ['tasks'] })
      if (selectedTaskID === id) { setSelectedTaskID(undefined); setActiveView('overview') }
      setTaskMenu(null)
    },
    onError: error => { setDeleteError(displayError(String(error))); setTaskMenu(null) },
  })
  const confirmAction = () => {
    if (!confirmation) return
    const request = confirmation
    setConfirmation(null)
    if (request.kind === 'delete') deleteTask.mutate(request.task.id)
    else closeSandbox.mutate(request.task.id)
  }

  if (!connection) return <StartupScreen error={startupError} retry={() => window.location.reload()} />

  return (
    <div className="flex h-full flex-col bg-[#080c11] text-slate-200">
      <header className="flex h-12 shrink-0 items-center border-b border-slate-800 bg-[#0b1016] px-3">
        <div className="flex w-[248px] items-center border-r border-slate-800 px-2.5">
          <div><div className="brand-wordmark" aria-label="CTF-BTFly"><span className="brand-prefix">CTF-</span><span className="brand-name">BTFly</span></div><div className="text-[9px] tracking-[.12em] text-slate-600">自主解题工作台</div></div>
        </div>
        <div className="flex flex-1 items-center gap-2 px-4 text-xs text-slate-500">
          <span>工作区</span><ChevronRight size={13} />
          <span className="text-slate-300">{selected?.title ?? viewTitle(activeView)}</span>
        </div>
        <div className="flex items-center gap-2">
          <ConnectionBadge label="后端" ok={!!system.data} />
          <ConnectionBadge label="Docker" ok={!!system.data?.docker.available} />
          <Button variant="primary" onClick={() => setShowCreate(true)}><Plus size={14} /> 新建题目</Button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        <aside className="flex w-[248px] shrink-0 flex-col border-r border-slate-800 bg-[#0a0f15]">
          <nav className="border-b border-slate-800 p-2">
            <NavItem active={activeView === 'overview'} icon={LayoutDashboard} label="系统概况" onClick={() => { setActiveView('overview'); setSelectedTaskID(undefined) }} />
            <NavItem active={activeView === 'all'} icon={Boxes} label="全部题目" count={tasks.data?.length ?? 0} onClick={() => { setActiveView('all'); setSelectedTaskID(undefined) }} />
            <NavItem active={activeView === 'running'} icon={Activity} label="运行中任务" count={tasks.data?.filter(t => t.status === 'running' || t.status === 'provisioning' || t.status === 'paused').length ?? 0} onClick={() => { setActiveView('running'); setSelectedTaskID(undefined) }} />
          </nav>
          <div className="px-3 pb-1 pt-4 text-[10px] font-semibold tracking-[.12em] text-slate-600">题目类型</div>
          <div className="px-2 pb-3">
            {categories.map(category => <NavItem key={category.id} active={activeView === `category:${category.id}`} icon={category.icon} label={category.label} count={tasks.data?.filter(t => t.category === category.id).length ?? 0} iconClass={category.colour} onClick={() => { setActiveView(`category:${category.id}`); setSelectedTaskID(undefined) }} />)}
          </div>
          <div className="flex items-center justify-between border-t border-slate-800 px-3 pb-1 pt-3 text-[10px] font-semibold uppercase tracking-[.16em] text-slate-600">
            最近任务 <RefreshCw size={11} className={cn(tasks.isFetching && 'animate-spin')} />
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-2">
            {tasks.data?.map(task => <TaskItem key={task.id} task={task} active={activeView === 'task' && task.id === selectedTaskID} onClick={() => { setSelectedTaskID(task.id); setActiveView('task') }} onContextMenu={event => { event.preventDefault(); if (taskCanBeDeleted(task)) { setDeleteError(''); setTaskMenu({ task, x: event.clientX, y: event.clientY }) } }} />)}
            {!tasks.data?.length && <div className="m-2 rounded-md border border-dashed border-slate-800 p-5 text-center text-xs text-slate-600">暂无题目</div>}
          </div>
          <div className="border-t border-slate-800 p-2"><NavItem active={activeView === 'security'} icon={LockKeyhole} label="安全策略" onClick={() => { setActiveView('security'); setSelectedTaskID(undefined) }} /><NavItem active={activeView === 'usage'} icon={Gauge} label="模型用量" onClick={() => { setActiveView('usage'); setSelectedTaskID(undefined) }} /></div>
        </aside>

        <main className="min-w-0 flex-1">
          {activeView === 'task' && selected ? (
            <TaskWorkspace client={client!} task={selected} events={stream.events} socketConnected={stream.connected}
              onStart={() => startTask.mutate(selected.id)} onAbort={() => abortTask.mutate(selected.id)} onPause={() => pauseTask.mutate(selected.id)} onResume={() => resumeTask.mutate(selected.id)} onCloseSandbox={() => setConfirmation({ kind: 'closeSandbox', task: selected })}
              actionError={displayError(String(startTask.error ?? abortTask.error ?? pauseTask.error ?? resumeTask.error ?? closeSandbox.error ?? ''))} />
          ) : activeView === 'overview' || activeView === 'task' ? <Overview system={system.data} tasks={tasks.data ?? []} onCreate={() => setShowCreate(true)} />
            : activeView === 'security' ? <SecurityPolicyView system={system.data} />
              : activeView === 'usage' ? <ModelUsageView client={client!} system={system.data} tasks={tasks.data ?? []} />
                : <TaskListView client={client!} title={activeView === 'all' ? '全部题目' : activeView === 'running' ? '运行中任务' : `${viewTitle(activeView)}题目`} subtitle={activeView === 'all' ? '显示本机全部题目；可直接查看 Flag、复制或下载解题报告。' : activeView === 'running' ? '正在创建沙箱、执行 Pi Agent 或已暂停保留容器的题目' : `仅显示“${viewTitle(activeView)}”方向的题目`} tasks={(tasks.data ?? []).filter(task => activeView === 'all' ? true : activeView === 'running' ? task.status === 'running' || task.status === 'provisioning' || task.status === 'paused' : task.category === categoryForView(activeView))} onSelect={task => { setSelectedTaskID(task.id); setActiveView('task') }} onCreate={() => setShowCreate(true)} />}
        </main>
      </div>
      <footer className="flex h-6 shrink-0 items-center justify-between border-t border-slate-800 bg-[#0b1016] px-3 text-[10px] text-slate-600">
        <div className="flex items-center gap-4"><span>Wails v3</span><span>Go 后端 {system.data?.daemon.version ?? '—'}</span><span>Pi RPC</span></div>
        <div className="flex items-center gap-2"><ShieldCheck size={11} className="text-emerald-400" />本机控制平面<button type="button" onClick={() => setShowSettings(open => !open)} className="ml-1 grid size-5 place-items-center rounded text-slate-500 transition-colors hover:bg-slate-800 hover:text-sky-300" title="界面设置与主题" aria-label="界面设置与主题"><Settings size={13} /></button></div>
      </footer>
      {showCreate && <CreateDialog pending={createTask.isPending} error={displayError(String(createTask.error ?? ''))} onClose={() => setShowCreate(false)} onSubmit={(task, attachments) => createTask.mutate({ task, attachments })} />}
      {taskMenu && <TaskContextMenu task={taskMenu.task} x={taskMenu.x} y={taskMenu.y} pending={deleteTask.isPending} onClose={() => setTaskMenu(null)} onRequestDelete={() => { setConfirmation({ kind: 'delete', task: taskMenu.task }); setTaskMenu(null) }} />}
      {confirmation && <ConfirmationDialog request={confirmation} pending={confirmation.kind === 'delete' ? deleteTask.isPending : closeSandbox.isPending} onCancel={() => setConfirmation(null)} onConfirm={confirmAction} />}
      {showSettings && <ThemeSettings theme={theme} onChange={setTheme} onClose={() => setShowSettings(false)} />}
      {deleteError && <div className="fixed bottom-8 right-3 z-[60] flex max-w-sm items-start gap-2 rounded-md border border-red-500/30 bg-[#211417] p-3 text-xs text-red-200 shadow-xl shadow-black/40"><TriangleAlert size={14} className="mt-0.5 shrink-0" /><span className="flex-1">{deleteError}</span><button type="button" onClick={() => setDeleteError('')} className="text-red-300/70 hover:text-red-100"><X size={14} /></button></div>}
    </div>
  )
}

function ThemeSettings({ theme: selectedTheme, onChange, onClose }: { theme: ThemeID; onChange: (theme: ThemeID) => void; onClose: () => void }) {
  const current = themes.find(theme => theme.id === selectedTheme) ?? themes[0]
  return <aside role="dialog" aria-label="界面设置" className="fixed bottom-8 right-3 z-[70] w-[340px] overflow-hidden rounded-lg border border-slate-700 bg-[#111923] shadow-2xl shadow-black/70"><div className="flex items-start gap-3 border-b border-slate-800 px-4 py-3"><div className="grid size-8 place-items-center rounded-md bg-sky-400/10 text-sky-300"><Palette size={16} /></div><div className="min-w-0 flex-1"><div className="text-sm font-semibold text-slate-100">界面设置</div><div className="mt-0.5 text-[10px] text-slate-500">主题会自动保存到本机</div></div><button type="button" onClick={onClose} className="rounded p-1 text-slate-500 hover:bg-slate-800 hover:text-slate-200" title="关闭"><X size={15} /></button></div><div className="p-3"><div className="mb-2 flex items-center gap-1.5 text-[10px] font-medium tracking-wide text-slate-500">{selectedTheme === 'light' ? <Sun size={12} /> : <Moon size={12} />}当前主题 · {current.name}</div><div className="grid grid-cols-2 gap-2">{themes.map(theme => <button key={theme.id} type="button" onClick={() => onChange(theme.id)} className={cn('rounded-md border p-2.5 text-left transition-colors', selectedTheme === theme.id ? 'border-sky-400/60 bg-sky-400/10' : 'border-slate-800 bg-slate-900/60 hover:border-slate-700 hover:bg-slate-800/70')}><span className="mb-2 flex h-6 overflow-hidden rounded border border-black/20">{theme.colours.map((colour, index) => <span key={`${theme.id}-${colour}`} className="flex-1" style={{ backgroundColor: colour, opacity: index === 2 ? 0.92 : 1 }} />)}</span><span className="block text-[11px] font-medium text-slate-200">{theme.name}</span><span className="mt-0.5 block text-[9px] leading-4 text-slate-500">{theme.description}</span></button>)}</div></div></aside>
}

function StartupScreen({ error, retry }: { error: string; retry: () => void }) {
  return <div className="panel-grid grid h-full place-items-center bg-[#080c11] text-slate-200"><div className="w-[440px] rounded-xl border border-slate-800 bg-[#0d131b] p-8 shadow-2xl shadow-black/40">
    <div className="mb-5 flex items-center gap-3"><div className="size-10 overflow-hidden rounded-lg border border-sky-400/20"><img src="/cpi-icon.png" alt="CTF-BTFly" className="size-full object-cover" /></div><div><h1 className="font-semibold">正在启动 CTF-BTFly</h1><p className="text-xs text-slate-500">正在连接独立的 Go 后端</p></div></div>
    {error ? <><div className="mb-4 rounded-md border border-red-500/20 bg-red-500/10 p-3 text-xs leading-5 text-red-300"><TriangleAlert size={14} className="mb-2" />{displayError(error)}</div><Button onClick={retry}><RefreshCw size={13} /> 重试</Button></> : <div className="flex items-center gap-2 text-xs text-slate-400"><RefreshCw size={14} className="animate-spin text-sky-400" />正在检查后端、SQLite 与 Docker…</div>}
  </div></div>
}

function Overview({ system, tasks, onCreate }: { system?: Awaited<ReturnType<PlatformClient['system']>>; tasks: Task[]; onCreate: () => void }) {
  const running = tasks.filter(task => task.status === 'running').length
  const settled = tasks.filter(task => task.status === 'settled').length
  const activeContainers = tasks.filter(task => !!task.containerId)
  return <div className="panel-grid h-full overflow-y-auto p-6">
    <div className="mx-auto max-w-6xl"><div className="mb-6 flex items-end justify-between"><div><div className="mb-1 text-xs font-medium text-sky-400">本地控制平面</div><h1 className="text-2xl font-semibold tracking-tight">系统概况</h1><p className="mt-1 text-sm text-slate-500">每道题都使用独立、可销毁的 Pi 沙箱；容器外由控制平面统一管理。</p></div><Button variant="primary" onClick={onCreate}><Plus size={14} /> 创建题目</Button></div>
      <div className="grid grid-cols-4 gap-3"><Stat icon={Activity} label="运行中" value={running} accent="text-sky-300" /><Stat icon={ShieldCheck} label="已稳定" value={settled} accent="text-emerald-300" /><Stat icon={Boxes} label="镜像" value="6" accent="text-violet-300" /><Stat icon={Cpu} label="Docker" value={system?.docker.serverVersion ?? '—'} accent="text-amber-300" small /></div>
      <div className="mt-5 grid grid-cols-[1.4fr_1fr] gap-4">
        <section className="rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={Network} title="执行链路" subtitle="每一层都有可追踪事件" /><div className="grid grid-cols-4 gap-2 p-4"><PipelineStep icon={LayoutDashboard} label="Wails 桌面端" detail="React 19" /><PipelineStep icon={Server} label="Go 后端" detail="REST + WS" /><PipelineStep icon={Container} label="沙箱" detail="Docker SDK" /><PipelineStep icon={Bot} label="Pi Agent" detail="JSONL RPC" /></div></section>
        <section className="rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={ShieldCheck} title="隔离状态" subtitle="运行时探测" /><div className="space-y-3 p-4 text-xs"><RuntimeRow label="普通题" value={system?.docker.normalRuntime ?? '检测中'} preferred="gVisor" /><RuntimeRow label="Pwn 题" value={system?.docker.pwnRuntime ?? '检测中'} preferred="Kata/VM" />{system?.docker.isolationWarnings?.map(warning => <div key={warning} className="flex gap-2 rounded-md border border-amber-500/20 bg-amber-500/5 p-2.5 text-amber-200/80"><TriangleAlert size={14} className="shrink-0" />{runtimeWarning(warning)}</div>)}</div></section>
      </div>
      <section className="mt-4 overflow-hidden rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={Container} title="已启用容器" subtitle={`${activeContainers.length} 个实例仍保留；完成后可在解题页关闭实例以释放内存`} />{activeContainers.length === 0 ? <div className="border-t border-slate-800 px-4 py-7 text-center text-xs text-slate-600">当前没有已启用的题目容器。</div> : <div className="grid grid-cols-2 gap-px border-t border-slate-800 bg-slate-800">{activeContainers.map(task => <div key={task.id} className="min-w-0 bg-[#0d131b] p-4"><div className="flex items-center gap-2"><span className={cn('size-1.5 shrink-0 rounded-full', task.status === 'running' ? 'status-pulse bg-emerald-400' : 'bg-sky-400')} /><CategoryIcon category={task.category} /><span className="truncate text-xs font-medium text-slate-200">{task.title}</span><span className="ml-auto shrink-0 text-[10px] text-slate-500">{statusLabels[task.status]}</span></div><div className="mono mt-3 truncate text-[10px] text-sky-300/80">{task.image}</div><div className="mt-2 flex items-center gap-2 text-[10px] text-slate-600"><span className="mono truncate">{task.containerId?.slice(0, 12)}</span><span>·</span><span>{task.runtime || 'Docker'}</span></div></div>)}</div>}</section>
      <section className="mt-4 rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={Boxes} title="Pi 镜像配置" subtitle="版本化镜像模板" /><div className="grid grid-cols-3 gap-px overflow-hidden border-t border-slate-800 bg-slate-800">{categories.map(category => <div key={category.id} className="flex items-center gap-3 bg-[#0d131b] p-4"><category.icon size={17} className={category.colour} /><div><div className="text-xs font-medium">ctf-agent-pi-{category.id}:0.1.0</div><div className="mt-1 text-[10px] text-slate-600">Pi RPC · 专项 Skill · 受限工具集</div></div></div>)}</div></section>
    </div>
  </div>
}

function TaskListView({ client, title, subtitle, tasks, onSelect, onCreate }: { client: PlatformClient; title: string; subtitle: string; tasks: Task[]; onSelect: (task: Task) => void; onCreate: () => void }) {
  return <div className="panel-grid h-full overflow-y-auto p-6"><div className="mx-auto max-w-5xl">
    <div className="mb-6 flex items-end justify-between"><div><div className="mb-1 text-xs font-medium text-sky-400">题目筛选</div><h1 className="text-2xl font-semibold tracking-tight">{title}</h1><p className="mt-1 text-sm text-slate-500">{subtitle}</p></div><Button variant="primary" onClick={onCreate}><Plus size={14} /> 创建题目</Button></div>
    {!tasks.length ? <EmptyPanel icon={Activity} title="没有匹配的题目" description="创建新题目后，或在左侧选择其他分类查看。" /> : <div className="grid grid-cols-2 gap-3">{tasks.map(task => <TaskFilterCard key={task.id} client={client} task={task} onSelect={() => onSelect(task)} />)}</div>}
  </div></div>
}

function TaskFilterCard({ client, task, onSelect }: { client: PlatformClient; task: Task; onSelect: () => void }) {
  const events = useQuery({
    queryKey: ['task-card-events', task.id],
    queryFn: () => client.events(task.id),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  })
  const writeupPreview = useQuery({
    queryKey: ['task-card-writeup', task.id],
    queryFn: () => client.writeup(task.id),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  })
  const [message, setMessage] = useState('')
  const flag = useMemo(() => {
    const eventFlag = (events.data ?? [])
      .filter(event => event.type === 'flag.candidate' && event.source === 'writeup')
      .map(event => String(event.payload.value ?? ''))
      .filter(Boolean)
      .pop()
    return eventFlag || finalFlagFromWriteup(writeupPreview.data)
  }, [events.data, writeupPreview.data])
  const download = useMutation({
    mutationFn: () => client.writeup(task.id),
    onSuccess: writeup => {
      if (!writeup.exists || writeup.binary) { setMessage('尚无可下载的文本解题报告。'); return }
      downloadWriteup(task.title, writeup.content)
    },
    onError: error => setMessage(displayError(String(error))),
  })
  const copyFlag = async () => {
    if (!flag) return
    try { await copyText(flag); setMessage('Flag 已复制。') } catch { setMessage('复制失败，请手动复制 Flag。') }
  }
  return <article className="rounded-lg border border-slate-800 bg-[#0d131b]/95 transition-colors hover:border-slate-700 hover:bg-slate-800/60"><button type="button" onClick={onSelect} className="block w-full p-4 text-left"><div className="flex items-center gap-2"><CategoryIcon category={task.category} /><span className="min-w-0 flex-1 truncate text-sm font-medium text-slate-200">{task.title}</span><Badge className={statusStyles[task.status]}>{statusLabels[task.status]}</Badge></div><p className="mt-3 line-clamp-2 min-h-10 text-xs leading-5 text-slate-500">{task.description || '未填写题目描述'}</p><div className="mono mt-3 truncate border-t border-slate-800 pt-3 text-[10px] text-slate-600">{task.image}</div></button>{flag && <div className="border-t border-emerald-500/15 bg-emerald-500/[0.03] px-4 py-3"><div className="mb-2 flex items-center gap-1.5 text-[10px] font-medium text-emerald-300"><Flag size={12} />已验证 Flag</div><div className="mono break-all rounded border border-emerald-500/20 bg-[#09130f] px-2.5 py-2 text-[11px] text-emerald-200">{flag}</div><div className="mt-2 flex items-center gap-2"><Button variant="secondary" onClick={copyFlag}><Copy size={12} /> 复制 Flag</Button><Button variant="ghost" disabled={download.isPending} onClick={() => download.mutate()}>{download.isPending ? <RefreshCw size={12} className="animate-spin" /> : <Download size={12} />} 下载 WP</Button></div>{message && <div className="mt-2 text-[10px] text-slate-500">{message}</div>}</div>}</article>
}

// 与 daemon 的规则一致：只信任 WRITEUP.md 中明确的“最终 Flag”二级小节。
// 这是旧任务或遗漏 flag.candidate 事件时的展示兜底，不会读取终端和过程日志。
function finalFlagFromWriteup(writeup?: Writeup) {
  if (!writeup?.exists || writeup.binary || !writeup.content) return ''

  const heading = /^#{1,6}\s*最终\s*Flag\s*$/im.exec(writeup.content)
  if (!heading) return ''

  const afterHeading = writeup.content.slice((heading.index ?? 0) + heading[0].length)
  const nextHeading = afterHeading.search(/^#{1,6}\s+/m)
  const section = (nextHeading >= 0 ? afterHeading.slice(0, nextHeading) : afterHeading).trim()
  const codeBlock = /```[^\r\n]*\r?\n([\s\S]*?)```/.exec(section)
  const value = (codeBlock?.[1] ?? '').trim()

  return value && !/[\r\n]/.test(value) && !/^未找到$/u.test(value) ? value : ''
}

function SecurityPolicyView({ system }: { system?: Awaited<ReturnType<PlatformClient['system']>> }) {
  const normalRuntime = system?.docker.normalRuntime ?? '检测中'
  const pwnRuntime = system?.docker.pwnRuntime ?? '检测中'
  return <div className="panel-grid h-full overflow-y-auto p-6"><div className="mx-auto max-w-5xl">
    <div className="mb-6"><div className="mb-1 text-xs font-medium text-sky-400">本机控制平面</div><h1 className="text-2xl font-semibold tracking-tight">安全策略</h1><p className="mt-1 text-sm text-slate-500">Agent 在题目沙箱中自主执行；控制平面只控制容器外部边界与资源上限。</p></div>
    <div className="grid grid-cols-2 gap-4"><PolicyCard icon={Container} title="独立任务沙箱" description="每道题启动独立容器与工作区，任务结束后可导出产物并销毁实例。" value="按题隔离" /><PolicyCard icon={ShieldCheck} title="容器运行时" description="普通题优先使用 gVisor；Pwn 题优先使用 Kata 或虚拟机。当前环境的实时探测结果如下。" value={`普通题：${normalRuntime} · Pwn：${pwnRuntime}`} /><PolicyCard icon={Network} title="网络边界" description="主动探测与利用仅限题目授权目标；允许下载官方工具依赖，并被动查阅公开题源、历史赛题和 Writeup。" value="目标白名单" /><PolicyCard icon={LockKeyhole} title="密钥边界" description="模型长期 API Key 由本地 daemon 托管，沙箱仅使用任务级临时访问凭证。" value="密钥不入容器" /></div>
    <section className="mt-4 rounded-lg border border-amber-500/20 bg-amber-500/5 p-4 text-xs leading-6 text-amber-100/85"><div className="mb-1 flex items-center gap-2 font-medium text-amber-200"><TriangleAlert size={14} />当前开发机提醒</div>{system?.docker.isolationWarnings?.length ? <ul className="list-inside list-disc">{system.docker.isolationWarnings.map(warning => <li key={warning}>{runtimeWarning(warning)}</li>)}</ul> : <span>未发现额外运行时告警。</span>}</section>
  </div></div>
}

function ModelUsageView({ client, system, tasks }: { client: PlatformClient; system?: Awaited<ReturnType<PlatformClient['system']>>; tasks: Task[] }) {
  const configured = !!system?.modelGateway.configured
  const active = tasks.filter(task => task.status === 'running' || task.status === 'provisioning').length
  const usage = useQuery({ queryKey: ['model-usage'], queryFn: () => client.modelUsage(), refetchInterval: configured ? 5_000 : false, refetchOnWindowFocus: false })
  const summary = usage.data?.summary
  return <div className="panel-grid h-full overflow-y-auto p-6"><div className="mx-auto max-w-5xl">
    <div className="mb-6"><div className="mb-1 text-xs font-medium text-sky-400">模型网关</div><h1 className="text-2xl font-semibold tracking-tight">模型用量</h1><p className="mt-1 text-sm text-slate-500">真实 API Key 始终只保存在本机 daemon；此页面不会显示或导出密钥。</p></div>
    <div className="grid grid-cols-3 gap-3"><Stat icon={BrainCircuit} label="网关状态" value={configured ? '已配置' : '未配置'} accent={configured ? 'text-emerald-300' : 'text-amber-300'} small /><Stat icon={Bot} label="当前模型" value={configured ? system?.modelGateway.model || '已配置' : '—'} accent="text-sky-300" small /><Stat icon={Activity} label="活跃任务" value={active} accent="text-violet-300" /></div>
    {!configured ? <section className="mt-4 rounded-lg border border-amber-500/20 bg-amber-500/5 p-5 text-sm leading-6 text-amber-100/85">尚未配置模型网关，暂时无法产生用量记录。请在 <span className="mono">CTF-BTFly.exe</span> 同目录的 <span className="mono">.env</span> 中填写模型配置后重启应用。</section> : usage.isLoading ? <EmptyPanel icon={RefreshCw} spinning title="正在读取模型用量" description="从本机 SQLite 用量账本汇总数据…" /> : usage.error ? <section className="mt-4 rounded-lg border border-red-500/20 bg-red-500/10 p-4 text-xs text-red-300">读取模型用量失败：{displayError(String(usage.error))}</section> : <>
      <div className="mt-4 grid grid-cols-4 gap-3"><Stat icon={Gauge} label="总 Token" value={formatTokens(summary?.totalTokens ?? 0)} accent="text-sky-300" /><Stat icon={Bot} label="输入 Token" value={formatTokens(summary?.inputTokens ?? 0)} accent="text-violet-300" /><Stat icon={Activity} label="输出 Token" value={formatTokens(summary?.outputTokens ?? 0)} accent="text-emerald-300" /><Stat icon={Radio} label="模型请求" value={summary?.requestCount ?? 0} accent="text-amber-300" /></div>
      <section className="mt-4 overflow-hidden rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={Activity} title="按日期 Token 用量" subtitle="最近 30 天 · 柱高代表当日输入与输出 Token 总量" /><TokenUsageChart days={usage.data?.days ?? []} /><div className="border-t border-slate-800 px-4 py-2 text-[10px] text-slate-600">已收到上游 usage：{summary?.reportedRequests ?? 0} / {summary?.requestCount ?? 0} 次请求。未返回 usage 的上游响应仅计为请求数，不会凭空估算 Token。</div></section>
      <section className="mt-4 overflow-hidden rounded-lg border border-slate-800 bg-[#0d131b]/95"><SectionTitle icon={Database} title="按题目统计" subtitle="专项子 Agent 的用量会汇总到所属题目" /><TaskUsageTable items={usage.data?.tasks ?? []} /></section>
      <section className="mt-4 rounded-lg border border-slate-800 bg-[#0d131b]/95 p-4 text-xs leading-5 text-slate-500"><div className="mb-1 text-slate-300">统计说明</div><p>数据仅来自模型服务商响应中的 <span className="mono">usage</span> 字段，并在网关侧持久化；不会保存 Prompt、模型回复、真实 API Key 或完整请求内容。缓存与推理 Token 仅在上游明确返回时展示。</p>{(summary?.cachedInputTokens ?? 0) > 0 || (summary?.reasoningTokens ?? 0) > 0 ? <p className="mt-2">缓存输入：<span className="mono text-slate-300">{formatTokens(summary?.cachedInputTokens ?? 0)}</span> · 推理 Token：<span className="mono text-slate-300">{formatTokens(summary?.reasoningTokens ?? 0)}</span></p> : null}</section>
    </>}
  </div></div>
}

function TokenUsageChart({ days }: { days: Array<{ date: string; inputTokens: number; outputTokens: number; totalTokens: number; requestCount: number }> }) {
  if (!days.length) return <div className="p-8 text-center text-xs text-slate-600">尚无模型请求记录。启动一个已配置模型网关的题目后，Token 用量会自动出现在这里。</div>
  const max = Math.max(...days.map(day => day.totalTokens), 1)
  return <div className="flex h-52 items-end gap-1 overflow-x-auto px-4 pb-6 pt-5">{days.map(day => {
    const totalHeight = Math.max(4, Math.round(day.totalTokens / max * 156))
    const inputHeight = day.totalTokens ? Math.max(2, Math.round(totalHeight * day.inputTokens / day.totalTokens)) : 2
    const outputHeight = Math.max(2, totalHeight - inputHeight)
    return <div key={day.date} className="group flex h-full min-w-9 flex-1 flex-col justify-end"><div title={`${day.date}\n总计：${formatTokens(day.totalTokens)}\n输入：${formatTokens(day.inputTokens)} · 输出：${formatTokens(day.outputTokens)}\n请求：${day.requestCount}`} className="relative flex cursor-default flex-col justify-end overflow-hidden rounded-t border border-slate-700/70 bg-slate-900/80 transition-colors group-hover:border-sky-400/60"><span className="bg-violet-400/80" style={{ height: inputHeight }} /><span className="bg-sky-400/90" style={{ height: outputHeight }} /></div><span className="mt-2 text-center text-[9px] text-slate-600">{day.date.slice(5)}</span></div>
  })}</div>
}

function TaskUsageTable({ items }: { items: Array<{ taskId: string; title: string; category: Category; models: string[]; requestCount: number; reportedRequests: number; inputTokens: number; outputTokens: number; totalTokens: number }> }) {
  if (!items.length) return <div className="p-8 text-center text-xs text-slate-600">还没有可汇总的题目用量。</div>
  return <div className="max-h-[360px] overflow-auto"><table className="w-full min-w-[760px] text-left text-xs"><thead className="sticky top-0 bg-[#101720] text-[10px] uppercase tracking-wide text-slate-600"><tr><th className="px-4 py-3 font-medium">题目</th><th className="px-4 py-3 font-medium">模型</th><th className="px-4 py-3 text-right font-medium">输入</th><th className="px-4 py-3 text-right font-medium">输出</th><th className="px-4 py-3 text-right font-medium">总 Token</th><th className="px-4 py-3 text-right font-medium">请求</th></tr></thead><tbody className="divide-y divide-slate-800">{items.map(item => <tr key={item.taskId} className="hover:bg-slate-800/30"><td className="px-4 py-3"><div className="flex items-center gap-2"><CategoryIcon category={item.category} /><span className="max-w-52 truncate text-slate-300">{item.title}</span></div><div className="mono mt-1 max-w-60 truncate text-[9px] text-slate-700">{item.taskId}</div></td><td className="px-4 py-3"><div className="flex flex-wrap gap-1">{item.models.length ? item.models.map(model => <span key={model} className="mono rounded border border-sky-400/15 bg-sky-400/5 px-1.5 py-0.5 text-[10px] text-sky-200">{model}</span>) : <span className="text-slate-600">—</span>}</div></td><td className="mono px-4 py-3 text-right text-violet-200">{formatTokens(item.inputTokens)}</td><td className="mono px-4 py-3 text-right text-emerald-200">{formatTokens(item.outputTokens)}</td><td className="mono px-4 py-3 text-right font-medium text-slate-100">{formatTokens(item.totalTokens)}</td><td className="px-4 py-3 text-right text-slate-400">{item.requestCount}<span className="ml-1 text-[9px] text-slate-600">({item.reportedRequests} 已统计)</span></td></tr>)}</tbody></table></div>
}

function PolicyCard({ icon: Icon, title, description, value }: { icon: typeof Activity; title: string; description: string; value: string }) {
  return <section className="rounded-lg border border-slate-800 bg-[#0d131b]/95 p-4"><div className="flex items-center justify-between"><Icon size={16} className="text-sky-300" /><span className="rounded-full border border-slate-700 bg-slate-900 px-2 py-0.5 text-[10px] text-slate-400">{value}</span></div><h2 className="mt-4 text-sm font-medium text-slate-200">{title}</h2><p className="mt-2 text-xs leading-5 text-slate-500">{description}</p></section>
}

type WorkspaceTab = 'prompt' | 'process' | 'terminal' | 'files' | 'writeup'

function TaskWorkspace({ client, task, events, socketConnected, onStart, onAbort, onPause, onResume, onCloseSandbox, actionError }: { client: PlatformClient; task: Task; events: PlatformEvent[]; socketConnected: boolean; onStart: () => void; onAbort: () => void; onPause: () => void; onResume: () => void; onCloseSandbox: () => void; actionError: string }) {
  const [activeTab, setActiveTab] = useState<WorkspaceTab>('process')
  const [selectedPath, setSelectedPath] = useState('')
  const [flagNotice, setFlagNotice] = useState<string>()
  const notifiedFlag = useRef('')
  const solveTiming = useMemo(() => latestSolveTiming(task.status, task.createdAt, task.updatedAt, events), [events, task.status, task.createdAt, task.updatedAt])
  const elapsed = useElapsedTime(solveTiming?.active ?? false, solveTiming?.startedAt ?? '', solveTiming?.endedAt)
  const files = useQuery({ queryKey: ['workspace-files', task.id], queryFn: () => client.files(task.id), enabled: activeTab === 'files' })
  const file = useQuery({ queryKey: ['workspace-file', task.id, selectedPath], queryFn: () => client.file(task.id, selectedPath), enabled: activeTab === 'files' && !!selectedPath })
  const writeup = useQuery({ queryKey: ['writeup', task.id], queryFn: () => client.writeup(task.id), enabled: activeTab === 'writeup' || taskCanBeDeleted(task) })
  const flags = useMemo(() => {
    const eventFlags = events.filter(event => event.type === 'flag.candidate' && event.source === 'writeup').map(event => String(event.payload.value ?? '')).filter(Boolean)
    const reportFlag = finalFlagFromWriteup(writeup.data)
    return reportFlag && !eventFlags.includes(reportFlag) ? [...eventFlags, reportFlag] : eventFlags
  }, [events, writeup.data])

  useEffect(() => { setActiveTab('process'); setSelectedPath(''); setFlagNotice(undefined); notifiedFlag.current = '' }, [task.id])
  useEffect(() => {
    const flag = flags[flags.length - 1]
    const key = flag ? `${task.id}:${flag}` : ''
    if (!flag || notifiedFlag.current === key) return
    notifiedFlag.current = key
	if (!rememberFlagToast(key)) return
    setFlagNotice(flag)
    const timer = window.setTimeout(() => setFlagNotice(undefined), 12_000)
    return () => window.clearTimeout(timer)
  }, [flags, task.id])

  return <div className="flex h-full min-w-0 flex-col">
    <div className="flex h-[66px] shrink-0 items-center justify-between border-b border-slate-800 bg-[#0c1219] px-4"><div className="min-w-0"><div className="flex items-center gap-2"><CategoryIcon category={task.category} /><h1 className="truncate text-base font-semibold">{task.title}</h1><Badge className={statusStyles[task.status]}>{statusLabels[task.status]}</Badge></div><div className="mt-1 flex items-center gap-3 text-[10px] text-slate-600"><span className="mono">{task.id}</span>{task.target && <span>{task.target}</span>}<span>{task.image}</span></div></div><div className="flex items-center gap-2">{task.status === 'running' ? <><Button variant="secondary" onClick={onPause}>暂停</Button><Button variant="danger" onClick={onAbort}><CircleStop size={13} /> 中止</Button></> : task.status === 'provisioning' ? <Button variant="danger" onClick={onAbort}><CircleStop size={13} /> 中止</Button> : task.status === 'paused' ? <><Button variant="danger" onClick={onAbort}><CircleStop size={13} /> 中止</Button><Button variant="primary" onClick={onResume}><Play size={13} /> 恢复解题</Button></> : taskCanBeDeleted(task) && task.containerId ? <Button variant="secondary" onClick={onCloseSandbox}><Container size={13} /> 关闭实例</Button> : <Button variant="primary" onClick={onStart}><Play size={13} /> 启动 Pi Agent</Button>}</div></div>
    {actionError && <div className="border-b border-red-500/20 bg-red-500/10 px-4 py-2 text-xs text-red-300">{actionError}</div>}
    <div className="flex min-h-0 flex-1"><section className="flex min-w-0 flex-1 flex-col"><div className="flex h-9 items-center gap-2 border-b border-slate-800 bg-[#0a0f15] px-4 text-[11px]"><WorkspaceTabButton active={activeTab === 'prompt'} onClick={() => setActiveTab('prompt')}>提示词</WorkspaceTabButton><WorkspaceTabButton active={activeTab === 'process'} onClick={() => setActiveTab('process')}>解题过程</WorkspaceTabButton><WorkspaceTabButton active={activeTab === 'terminal'} onClick={() => setActiveTab('terminal')}>终端</WorkspaceTabButton><WorkspaceTabButton active={activeTab === 'files'} onClick={() => setActiveTab('files')}>文件</WorkspaceTabButton><WorkspaceTabButton active={activeTab === 'writeup'} onClick={() => setActiveTab('writeup')}>解题报告</WorkspaceTabButton>{activeTab === 'process' && <ContainerStatus task={task} />}<span className="ml-auto flex items-center gap-1.5 text-slate-600"><span className={cn('size-1.5 rounded-full', socketConnected ? 'status-pulse bg-emerald-400' : 'bg-slate-600')} />{socketConnected ? '实时' : '重连中'}</span></div><div className={cn('panel-grid min-h-0 flex-1', activeTab === 'process' ? 'overflow-hidden p-4' : 'overflow-y-auto p-4')}>{activeTab === 'prompt' && <PromptPanel client={client} task={task} />}{activeTab === 'process' && <EventTimeline taskID={task.id} events={events} timing={solveTiming} elapsed={elapsed} />}{activeTab === 'terminal' && <TerminalTranscript events={events} />}{activeTab === 'files' && <WorkspaceFilesView client={client} taskID={task.id} files={files.data ?? []} loading={files.isLoading} error={files.error} selectedPath={selectedPath} onSelect={setSelectedPath} preview={file.data} previewLoading={file.isLoading} previewError={file.error} />}{activeTab === 'writeup' && <WriteupView loading={writeup.isLoading} error={writeup.error} writeup={writeup.data} taskTitle={task.title} />}</div></section>
      <aside className="w-[300px] shrink-0 overflow-y-auto border-l border-slate-800 bg-[#0a0f15]"><SectionTitle icon={Flag} title="Flag 候选" subtitle={`已发现 ${flags.length} 个`} /><div className="space-y-2 p-3">{flags.map((flag, index) => <div key={`${flag}-${index}`} className="mono rounded-md border border-emerald-500/25 bg-emerald-500/5 p-2.5 text-xs text-emerald-300">{flag}</div>)}{flags.length === 0 && <div className="rounded-md border border-dashed border-slate-800 p-4 text-center text-xs text-slate-600">正在监听 Agent 消息、工具输出与产物</div>}</div><SectionTitle icon={Container} title="沙箱" subtitle="容器外安全边界" /><dl className="space-y-3 p-4 text-xs"><Detail label="镜像" value={task.image} mono /><Detail label="运行时" value={task.runtime || '尚未启动'} /><Detail label="容器" value={task.containerId?.slice(0, 12) || '—'} mono /><Detail label="工作区" value={task.id} mono /></dl><SectionTitle icon={Database} title="事件日志" subtitle="SQLite 持久化日志" /><div className="p-4 text-xs text-slate-500"><div className="flex justify-between"><span>序号</span><span className="mono text-slate-300">{events[events.length - 1]?.sequence ?? 0}</span></div><div className="mt-2 flex justify-between"><span>当前事件数</span><span className="mono text-slate-300">{events.length}</span></div></div></aside>
    </div>
    {flagNotice && <FlagSuccessToast taskTitle={task.title} flag={flagNotice} onClose={() => setFlagNotice(undefined)} />}
  </div>
}

function ContainerStatus({ task }: { task: Task }) {
  if (task.containerId) {
    return <span title={`容器 ID：${task.containerId}`} className="ml-2 flex min-w-0 items-center gap-1.5 rounded border border-emerald-500/20 bg-emerald-500/5 px-2 py-1 text-[10px] text-emerald-300"><span className="status-pulse size-1.5 shrink-0 rounded-full bg-emerald-400" /><Container size={12} className="shrink-0" /><span className="shrink-0">已启用容器</span><span className="mono max-w-48 truncate text-emerald-200/75">{task.image}</span><span className="mono shrink-0 text-emerald-200/45">{task.containerId.slice(0, 12)}</span></span>
  }

  if (task.status === 'provisioning') {
    return <span className="ml-2 flex items-center gap-1.5 text-[10px] text-sky-300"><RefreshCw size={12} className="animate-spin" />正在启用容器</span>
  }

  return <span className="ml-2 flex items-center gap-1.5 text-[10px] text-slate-600"><Container size={12} />容器尚未启用</span>
}

type SolveAttempt = { id: string; label: string; events: PlatformEvent[] }

function solveAttemptsFromEvents(events: PlatformEvent[]): SolveAttempt[] {
  const attempts: SolveAttempt[] = [{ id: 'initial', label: '首次尝试', events: [] }]
  for (const event of events) {
    // The daemon persists this event immediately before it provisions a fresh
    // sandbox, making it a stable boundary between two complete Pi attempts.
    if (event.type === 'task.retry_requested') {
      attempts.push({ id: event.id, label: `第 ${attempts.length + 1} 次尝试`, events: [event] })
      continue
    }
    attempts[attempts.length - 1].events.push(event)
  }
  return attempts
}

function EventTimeline({ taskID, events, timing, elapsed }: { taskID: string; events: PlatformEvent[]; timing?: SolveTiming; elapsed: string }) {
  const attempts = useMemo(() => solveAttemptsFromEvents(events), [events])
  const latestAttempt = attempts[attempts.length - 1]
  const [selectedAttemptID, setSelectedAttemptID] = useState('')
  const previousAttemptCount = useRef(0)
  const scrollRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const attemptCountIncreased = attempts.length > previousAttemptCount.current
    setSelectedAttemptID(current => attemptCountIncreased || !attempts.some(attempt => attempt.id === current) ? latestAttempt.id : current)
    previousAttemptCount.current = attempts.length
  }, [attempts, latestAttempt.id])

  const selectedAttempt = attempts.find(attempt => attempt.id === selectedAttemptID) ?? latestAttempt
  const scrollKey = `${taskID}:${selectedAttempt.id}`
  useLayoutEffect(() => {
    const saved = processScrollPositions.get(scrollKey)
    if (saved === undefined || !scrollRef.current) return
    scrollRef.current.scrollTop = saved
  }, [scrollKey])
  useEffect(() => () => {
    if (scrollRef.current) processScrollPositions.set(scrollKey, scrollRef.current.scrollTop)
  }, [scrollKey])

  const important = selectedAttempt.events.filter(isImportantEvent)
  const omitted = selectedAttempt.events.length - important.length
  return <div className="relative h-full min-h-0"><div ref={scrollRef} onScroll={event => processScrollPositions.set(scrollKey, event.currentTarget.scrollTop)} className="h-full overflow-y-auto pr-1"><div className="mx-auto max-w-4xl space-y-2 pb-24">{!events.length && <div className="grid min-h-[360px] place-items-center"><div className="text-center"><Radio size={26} className="mx-auto mb-3 text-slate-700" /><div className="text-sm text-slate-400">正在等待任务事件</div><div className="mt-1 text-xs text-slate-600">启动 Pi Agent 后，这里会实时显示 RPC 事件日志。</div></div></div>}{events.length > 0 && <>{attempts.length > 1 && <div className="flex items-center gap-1 overflow-x-auto rounded-md border border-slate-800 bg-[#0b1118] p-1.5">{attempts.map((attempt, index) => <button key={attempt.id} type="button" onClick={() => setSelectedAttemptID(attempt.id)} className={cn('flex shrink-0 items-center gap-1.5 rounded px-3 py-1.5 text-[11px] transition-colors', selectedAttempt.id === attempt.id ? 'bg-sky-400/10 text-sky-200' : 'text-slate-500 hover:bg-slate-800 hover:text-slate-300')}><span>{attempt.label}</span><span className="mono text-[9px] text-slate-600">{attempt.events.length}</span>{index === attempts.length - 1 && <span className="rounded bg-emerald-400/10 px-1 py-0.5 text-[8px] text-emerald-300">当前</span>}</button>)}</div>}{omitted > 0 && <div className="rounded-md border border-slate-800 bg-[#0d141c]/80 px-3 py-2 text-[10px] text-slate-500">{selectedAttempt.label}已自动隐藏 {omitted} 条流式文本、推理片段和中间工具输出；完整事件仍保存在 SQLite。</div>}{important.map(event => <EventCard key={event.id} event={event} />)}</>}</div></div>{timing && <div className={cn('pointer-events-none absolute bottom-3 right-3 z-20 flex items-center justify-between gap-5 rounded-md border px-4 py-3 shadow-[0_10px_30px_rgba(2,6,23,0.75)] backdrop-blur-md', timing.active ? 'border-sky-400/30 bg-[#0d1a25]/95' : 'border-emerald-400/30 bg-[#0c1b14]/95')}><span className={cn('flex items-center gap-2 text-xs', timing.active ? 'text-sky-200' : 'text-emerald-200')}><Clock3 size={14} />{timing.active ? '本轮解题已用时' : '本轮解题总用时'}</span><strong className="mono text-sm font-semibold text-slate-100">{elapsed}</strong></div>}</div>
}

function WorkspaceTabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className={cn('h-9 border-b px-2.5 text-[11px] transition-colors', active ? 'border-sky-400 font-medium text-sky-300' : 'border-transparent text-slate-600 hover:text-slate-300')}>{children}</button>
}

type SolveTiming = { startedAt: string; endedAt?: string; active: boolean }

function latestSolveTiming(status: TaskStatus, fallbackStart: string, fallbackEnd: string, events: PlatformEvent[]): SolveTiming | undefined {
  let startIndex = -1
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index].type === 'sandbox.provisioning') {
      startIndex = index
      break
    }
  }
  if (status === 'ready') return undefined
  const startedAt = startIndex < 0 ? fallbackStart : events[startIndex].createdAt
  const active = status === 'running' || status === 'provisioning'
  if (active) return { startedAt, active: true }
  const terminalEvent = events.slice(startIndex + 1).find(event => ['task.settled', 'task.failed', 'task.cancelled'].includes(event.type))
  return { startedAt, endedAt: terminalEvent?.createdAt ?? fallbackEnd, active: false }
}

function useElapsedTime(active: boolean, startedAt: string, endedAt?: string) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    setNow(Date.now())
    if (!active) return undefined
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [active, startedAt, endedAt])
  const started = Date.parse(startedAt)
  const ended = active ? now : Date.parse(endedAt ?? '')
  const seconds = Number.isFinite(started) && Number.isFinite(ended) ? Math.max(0, Math.floor((ended - started) / 1_000)) : 0
  const hours = Math.floor(seconds / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  const remainder = seconds % 60
  return [hours, minutes, remainder].map(value => String(value).padStart(2, '0')).join(':')
}

function PromptPanel({ client, task }: { client: PlatformClient; task: Task }) {
  const queryClient = useQueryClient()
  const prompt = useQuery({ queryKey: ['task-prompt', task.id], queryFn: () => client.taskPrompt(task.id) })
  const [draft, setDraft] = useState('')
  const [savedHint, setSavedHint] = useState('')
  useEffect(() => { if (prompt.data) setDraft(prompt.data.prompt) }, [prompt.data])
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['tasks'] })
    queryClient.invalidateQueries({ queryKey: ['task-prompt', task.id] })
  }
  const save = useMutation({
    mutationFn: () => client.updateTaskPrompt(task.id, draft),
    onSuccess: () => { setSavedHint('提示词已保存；下次启动将使用这份内容。'); refresh() },
  })
  const retry = useMutation({
    mutationFn: async () => { await client.updateTaskPrompt(task.id, draft); return client.retryTask(task.id) },
    onSuccess: () => { setSavedHint('正在用最新提示词创建新的 Pi 解题实例。'); refresh() },
  })
  const resume = useMutation({
    mutationFn: async () => { await client.updateTaskPrompt(task.id, draft); return client.resumeTask(task.id) },
    onSuccess: () => { setSavedHint('已保存补充信息，正在使用原容器与原 Pi 会话恢复解题。'); refresh() },
  })
  if (prompt.isLoading) return <EmptyPanel icon={RefreshCw} title="正在读取提示词" description="正在加载系统策略和当前题目的补充提示。" spinning />
  if (prompt.error) return <EmptyPanel icon={TriangleAlert} title="无法读取提示词" description={displayError(prompt.error.message)} />
  const data = prompt.data
  if (!data) return null
  const busy = save.isPending || retry.isPending || resume.isPending
  const error = save.error ?? retry.error ?? resume.error
  const subtitle = data.resumable ? '任务已暂停。可补充线索、修正方向或说明新附件，再恢复原 Pi 会话。' : data.editable ? '可在启动前编辑；会附加到下一次 Pi 解题任务。' : '当前 Agent 正在运行。可先暂停以补充信息，或等待本轮结束后再编辑。'
  return <div className="mx-auto max-w-4xl space-y-4"><section className="rounded-md border border-slate-800 bg-[#0d141c]/95"><SectionTitle icon={ShieldCheck} title="系统提示词" subtitle="由控制平面生成，只读；包含授权边界、工作区规则和报告要求" /><pre className="mono max-h-[360px] overflow-auto whitespace-pre-wrap break-words p-4 text-[11px] leading-5 text-slate-400">{data.systemPrompt}</pre></section><section className="rounded-md border border-slate-800 bg-[#0d141c]/95"><SectionTitle icon={Bot} title="当前题目的补充提示" subtitle={subtitle} /><div className="space-y-3 p-4"><textarea value={draft} disabled={!data.editable || busy} rows={8} maxLength={32 * 1024} onChange={event => { setDraft(event.target.value); setSavedHint('') }} className="input mono min-h-44 w-full resize-y disabled:cursor-not-allowed disabled:opacity-55" placeholder="例如：此前没有找到 Flag，请重点检查附件中的 RSA 参数关系，并保留已有产物后换一种思路。" /><div className="flex flex-wrap items-center justify-between gap-3"><div className="text-[10px] text-slate-600">{draft.length.toLocaleString()} / 32,768 字节 · 不会覆盖系统安全边界</div><div className="flex gap-2">{data.editable && <Button variant="secondary" disabled={busy} onClick={() => save.mutate()}>{save.isPending ? <RefreshCw size={13} className="animate-spin" /> : <Bot size={13} />} 保存提示词</Button>}{data.resumable && <Button variant="primary" disabled={busy} onClick={() => resume.mutate()}>{resume.isPending ? <RefreshCw size={13} className="animate-spin" /> : <Play size={13} />} 保存并恢复解题</Button>}{data.retryable && <Button variant="primary" disabled={busy} onClick={() => retry.mutate()}>{retry.isPending ? <RefreshCw size={13} className="animate-spin" /> : <Play size={13} />} 保存并重新尝试</Button>}</div></div>{savedHint && <div className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-xs text-emerald-200">{savedHint}</div>}{error && <div className="rounded-md border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">{displayError(error.message)}</div>}{!data.editable && <div className="rounded-md border border-slate-700 bg-slate-900/70 px-3 py-2 text-xs text-slate-500">当前实例运行中。使用顶部“暂停”后即可补充提示和信息；中止会结束当前任务。</div>}</div></section></div>
}

function TerminalTranscript({ events }: { events: PlatformEvent[] }) {
  const entries = events.filter(event => event.type.startsWith('tool.') || event.type === 'agent.stderr').map(terminalEntry).filter(Boolean)
  if (!entries.length) return <EmptyPanel icon={SquareTerminal} title="暂无终端记录" description="Pi 执行命令后，工具调用和输出会显示在这里。" />
  return <div className="mx-auto h-full max-w-4xl rounded-md border border-slate-800 bg-[#090e14] p-4"><div className="mb-3 flex items-center gap-2 text-[10px] text-slate-600"><SquareTerminal size={13} />Pi 自动执行记录</div><pre className="mono max-h-[calc(100vh-240px)] overflow-auto whitespace-pre-wrap break-words text-[11px] leading-5 text-slate-300">{entries.join('\n')}</pre></div>
}

function terminalEntry(event: PlatformEvent) {
  if (event.type === 'tool.started') {
    const args = event.payload.args as Record<string, unknown> | undefined
    const command = args?.command ?? args?.cmd ?? args?.script
    const tool = String(event.payload.toolName ?? event.payload.name ?? 'tool')
    return `\n$ ${command ? String(command) : `[${tool}]`}`
  }
  const text = eventText(event.payload)
  return text ? text : ''
}

function WorkspaceFilesView({ client, taskID, files, loading, error, selectedPath, onSelect, preview, previewLoading, previewError }: { client: PlatformClient; taskID: string; files: WorkspaceFile[]; loading: boolean; error: Error | null; selectedPath: string; onSelect: (path: string) => void; preview?: WorkspaceFileContent; previewLoading: boolean; previewError: Error | null }) {
  if (loading) return <EmptyPanel icon={RefreshCw} title="正在读取工作区文件" description="正在从该题专属工作区加载文件列表。" spinning />
  if (error) return <EmptyPanel icon={TriangleAlert} title="无法读取工作区" description={displayError(error.message)} />
  if (!files.length) return <EmptyPanel icon={FileCode2} title="工作区暂无文件" description="Agent 生成的脚本、产物和 WRITEUP.md 会出现在这里。" />
  return <div className="mx-auto flex h-full max-w-5xl overflow-hidden rounded-md border border-slate-800 bg-[#090e14]"><div className="w-[280px] shrink-0 overflow-y-auto border-r border-slate-800 p-2">{files.map(file => <button key={file.path} type="button" onClick={() => onSelect(file.path)} className={cn('mb-1 w-full rounded-md px-2.5 py-2 text-left text-xs transition-colors', selectedPath === file.path ? 'bg-sky-400/10 text-sky-200' : 'text-slate-400 hover:bg-slate-800')}><div className="truncate">{file.path}</div><div className="mt-1 mono text-[9px] text-slate-600">{formatBytes(file.size)} · {new Date(file.modifiedAt).toLocaleString()}</div></button>)}</div><div className="min-w-0 flex-1 overflow-auto p-4">{!selectedPath && <EmptyPanel icon={FileCode2} title="选择一个文件" description="左侧列出当前题目的工作区文件。" />}{selectedPath && previewLoading && <EmptyPanel icon={RefreshCw} title="正在读取文件" description={selectedPath} spinning />}{selectedPath && previewError && <EmptyPanel icon={TriangleAlert} title="无法预览文件" description={displayError(previewError.message)} />}{preview && preview.binary && <div className="space-y-3"><EmptyPanel icon={FileCode2} title="该文件为二进制文件" description="无法直接预览，但可以下载到本地查看。" /><Button variant="secondary" onClick={() => void downloadWorkspaceFile(client, taskID, preview.path)}><Download size={13} /> 下载文件</Button></div>}{preview && !preview.binary && <><div className="mb-3 flex items-center justify-between gap-3 text-[10px] text-slate-600"><span className="mono min-w-0 truncate">{preview.path}</span><div className="flex shrink-0 items-center gap-2">{preview.truncated && <span>仅显示前 1 MB</span>}<Button variant="ghost" onClick={() => void downloadWorkspaceFile(client, taskID, preview.path)}><Download size={13} /> 下载文件</Button></div></div><pre className="mono whitespace-pre-wrap break-words text-[11px] leading-5 text-slate-300">{preview.content || '（空文件）'}</pre></>}</div></div>
}

async function downloadWorkspaceFile(client: PlatformClient, taskID: string, path: string) {
  const blob = await client.downloadFile(taskID, path)
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = path.split('/').filter(Boolean).pop() || 'artifact'
  document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

function WriteupView({ loading, error, writeup, taskTitle }: { loading: boolean; error: Error | null; writeup?: Writeup; taskTitle: string }) {
  if (loading) return <EmptyPanel icon={RefreshCw} title="正在读取解题报告" description="正在读取 Agent 写入的 WRITEUP.md。" spinning />
  if (error) return <EmptyPanel icon={TriangleAlert} title="无法读取解题报告" description={displayError(error.message)} />
  if (!writeup?.exists) return <EmptyPanel icon={FileCode2} title="尚未生成解题报告" description="Pi 找到有效思路后会在工作区写入 WRITEUP.md。" />
  if (writeup.binary) return <EmptyPanel icon={FileCode2} title="解题报告不是文本文件" description="请在“文件”页检查工作区内容。" />
  return <article className="mx-auto max-w-4xl rounded-md border border-slate-800 bg-[#0d141c]/95 p-5"><div className="mb-4 flex items-center justify-between border-b border-slate-800 pb-3"><div className="flex items-center gap-2 text-sm font-medium text-slate-200"><FileCode2 size={15} className="text-sky-300" />WRITEUP.md</div><div className="flex items-center gap-3">{writeup.truncated && <span className="text-[10px] text-amber-300">仅显示前 1 MB</span>}<Button variant="ghost" onClick={() => downloadWriteup(taskTitle, writeup.content)}><FileCode2 size={13} /> 下载 WP</Button></div></div><pre className="whitespace-pre-wrap break-words text-xs leading-6 text-slate-300">{writeup.content || '（解题报告为空）'}</pre></article>
}

function downloadWriteup(taskTitle: string, content: string) {
  const safeTitle = taskTitle.trim().replace(/[\\/:*?"<>|]/g, '_') || 'CTF题目'
  const file = new Blob([content], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(file)
  const link = document.createElement('a')
  link.href = url
  link.download = `${safeTitle}-WRITEUP.md`
  document.body.appendChild(link)
  link.click()
  link.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

function copyText(value: string) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value)
  const input = document.createElement('textarea')
  input.value = value
  input.style.position = 'fixed'
  input.style.opacity = '0'
  document.body.appendChild(input)
  input.select()
  const copied = document.execCommand('copy')
  input.remove()
  return copied ? Promise.resolve() : Promise.reject(new Error('copy failed'))
}

// Persist the notification marker locally so entering the same finished task
// again cannot replay an old success toast. A different validated Flag still
// receives its own one-time notification.
function rememberFlagToast(key: string) {
  try {
    const storageKey = `ctfagentpi.flag-toast.${key}`
    if (window.localStorage.getItem(storageKey)) return false
    window.localStorage.setItem(storageKey, 'shown')
  } catch {
    // A restricted WebView storage implementation should not prevent the
    // actual workspace from working; the in-memory ref still avoids repeats.
  }
  return true
}

function EmptyPanel({ icon: Icon, title, description, spinning }: { icon: typeof Activity; title: string; description: string; spinning?: boolean }) {
  return <div className="grid h-full min-h-[360px] place-items-center"><div className="max-w-sm text-center"><Icon size={26} className={cn('mx-auto mb-3 text-slate-700', spinning && 'animate-spin')} /><div className="text-sm text-slate-400">{title}</div><div className="mt-1 text-xs leading-5 text-slate-600">{description}</div></div></div>
}

function formatBytes(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

function formatTokens(value: number) {
  if (value < 1_000) return value.toLocaleString('zh-CN')
  if (value < 1_000_000) return `${(value / 1_000).toFixed(value >= 100_000 ? 0 : 1)}k`
  return `${(value / 1_000_000).toFixed(value >= 100_000_000 ? 0 : 2)}M`
}

function isImportantEvent(event: PlatformEvent) {
  return !['agent.message.delta', 'agent.message.updated', 'agent.thinking.delta', 'tool.output'].includes(event.type)
}

function EventCard({ event }: { event: PlatformEvent }) {
  const text = eventSummary(event)
  const isTool = event.type.startsWith('tool.')
  const isError = event.type.includes('error') || event.type === 'task.failed'
  const Icon = isTool ? SquareTerminal : isError ? TriangleAlert : event.source === 'pi' ? Bot : Radio
  return <article className={cn('rounded-md border bg-[#0d141c]/95', isError ? 'border-red-500/25' : isTool ? 'border-slate-700' : 'border-slate-800')}><div className="flex items-center gap-2 border-b border-slate-800/80 px-3 py-2"><Icon size={13} className={cn(isError ? 'text-red-300' : isTool ? 'text-amber-300' : 'text-sky-300')} /><span className="text-[11px] font-medium text-slate-300">{eventLabel(event.type)}</span>{event.toolCallId && <span className="mono truncate text-[9px] text-slate-600">{event.toolCallId}</span>}<span className="ml-auto text-[9px] text-slate-600">{new Date(event.createdAt).toLocaleTimeString()}</span></div><div className="whitespace-pre-wrap break-words p-3 text-[11px] leading-5 text-slate-400">{text || '事件已记录。'}</div><details className="border-t border-slate-800/80 px-3 py-2 text-[10px] text-slate-600"><summary className="cursor-pointer select-none hover:text-slate-400">原始事件数据</summary><pre className="mono mt-2 max-h-44 overflow-auto whitespace-pre-wrap break-words text-[9px] leading-4 text-slate-600">{JSON.stringify(event.payload, null, 2)}</pre></details></article>
}

function eventSummary(event: PlatformEvent) {
  if (event.type === 'task.created') return `题目已创建：${String(event.payload.title ?? '')}`
  if (event.type === 'task.paused') return '已暂停当前 Pi 回合，容器、会话和已有产物仍会保留。'
  if (event.type === 'task.resumed') return '已恢复原容器与原 Pi 会话，Agent 将继续解题。'
  if (event.type === 'sandbox.provisioning') return `准备创建沙箱：${String(event.payload.image ?? '')}`
  if (event.type === 'sandbox.started') return `沙箱已启动：运行时 ${String(event.payload.runtime ?? '—')}`
  if (event.type === 'sandbox.stopped') return '已释放 Docker 容器与模型会话；题目文件、日志和报告仍被保留。'
  if (event.type === 'handoff.crypto.started') return `已创建隔离的 Crypto 专项实例：${String(event.payload.question ?? '正在分析密码学问题')}`
  if (event.type === 'handoff.crypto.completed') return `Crypto 专项分析已回传。报告：${String(event.payload.reportPath ?? '未生成')}；产物：${String(event.payload.artifactsPath ?? '未生成')}`
  if (event.type === 'handoff.crypto.resuming_parent') return '密码专项结果已写入 artifacts/handoffs，正在启动新的 Misc 实例继续解题。'
  if (event.type === 'handoff.crypto.failed') return `Crypto 专项交接失败：${String(event.payload.error ?? '未知错误')}`
	if (event.type === 'task.prompt_updated') return '当前题目的补充提示词已保存。'
	if (event.type === 'task.retry_requested') return '已释放旧实例，正在用最新提示词开始新的解题尝试。'
  if (event.type === 'flag.candidate') return `最终 Flag 已从 WRITEUP.md 的“最终 Flag”章节提取：${String(event.payload.value ?? '')}`
  if (event.type === 'tool.started') {
    const args = event.payload.args as Record<string, unknown> | undefined
    const command = args?.command ?? args?.cmd ?? args?.script
    return command ? `执行：${String(command)}` : `正在执行工具：${String(event.payload.toolName ?? event.payload.name ?? 'tool')}`
  }
  const text = eventText(event.payload)
  if (text) return truncateEventText(text)
  if (event.type === 'agent.message.completed') return 'Agent 已完成一条分析消息。'
  if (event.type === 'tool.completed') return '工具执行已完成。'
  return ''
}

function truncateEventText(text: string) {
  const maxLength = 900
  const normalized = text.trim()
  return normalized.length > maxLength ? `${normalized.slice(0, maxLength)}\n…（内容已截断，完整数据可展开查看）` : normalized
}

function CreateDialog({ pending, error, onClose, onSubmit }: { pending: boolean; error: string; onClose: () => void; onSubmit: (value: CreateTask, attachments: AttachmentInput[]) => void }) {
  const [form, setForm] = useState<CreateTask>({ title: '', category: 'web', description: '', target: '', flagFormat: 'flag{...}' })
  const [attachments, setAttachments] = useState<AttachmentInput[]>([])
  const addAttachments = (files: FileList | File[]) => {
    const next = Array.from(files).map(file => ({ file, path: file.webkitRelativePath || file.name }))
    setAttachments(current => {
      const indexed = new Map(current.map(item => [item.path, item]))
      next.forEach(item => indexed.set(item.path, item))
      return Array.from(indexed.values())
    })
  }
  const submit = (event: FormEvent) => { event.preventDefault(); onSubmit(form, attachments) }
  return <div className="fixed inset-0 z-50 grid place-items-center overflow-y-auto bg-black/70 p-4 backdrop-blur-sm" onMouseDown={event => event.target === event.currentTarget && onClose()}><form onSubmit={submit} className="my-4 w-full max-w-xl rounded-xl border border-slate-700 bg-[#0e151e] shadow-2xl shadow-black/60"><div className="flex items-center justify-between border-b border-slate-800 px-5 py-4"><div><h2 className="text-sm font-semibold">创建题目</h2><p className="mt-1 text-xs text-slate-500">系统会依据所选类型创建全新的 Pi 沙箱。</p></div><button type="button" onClick={onClose} className="rounded p-1 text-slate-500 hover:bg-slate-800"><X size={16} /></button></div><div className="space-y-4 p-5"><Field label="题目名称"><input required value={form.title} onChange={e => setForm({ ...form, title: e.target.value })} className="input" placeholder="例如：JWT 密钥混淆" /></Field><Field label="题目类型"><div className="grid grid-cols-6 gap-1.5">{categories.map(category => <button key={category.id} type="button" onClick={() => setForm({ ...form, category: category.id })} className={cn('flex flex-col items-center gap-1 rounded-md border px-2 py-2 text-[10px]', form.category === category.id ? 'border-sky-400/50 bg-sky-400/10 text-sky-200' : 'border-slate-800 bg-slate-900 text-slate-500 hover:border-slate-700')}><category.icon size={14} />{category.label}</button>)}</div></Field><Field label="题目描述"><textarea required rows={5} value={form.description} onChange={e => setForm({ ...form, description: e.target.value })} className="input resize-none" placeholder="粘贴题目描述和已授权的目标范围…" /></Field><div className="grid grid-cols-2 gap-3"><Field label="目标地址"><input value={form.target} onChange={e => setForm({ ...form, target: e.target.value })} className="input mono" placeholder="http://target:8080" /></Field><Field label="Flag 格式"><input value={form.flagFormat} onChange={e => setForm({ ...form, flagFormat: e.target.value })} className="input mono" /></Field></div><AttachmentDropZone attachments={attachments} onAdd={addAttachments} onRemove={path => setAttachments(current => current.filter(item => item.path !== path))} />{error && <div className="rounded-md border border-red-500/20 bg-red-500/10 p-2 text-xs text-red-300">{error}</div>}</div><div className="flex items-center justify-end gap-2 border-t border-slate-800 px-5 py-3"><Button type="button" variant="ghost" onClick={onClose}>取消</Button><Button type="submit" variant="primary" disabled={pending}>{pending ? <RefreshCw size={13} className="animate-spin" /> : <Plus size={13} />} 创建任务</Button></div></form></div>
}

function AttachmentDropZone({ attachments, onAdd, onRemove }: { attachments: AttachmentInput[]; onAdd: (files: FileList | File[]) => void; onRemove: (path: string) => void }) {
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const totalSize = attachments.reduce((total, attachment) => total + attachment.file.size, 0)
  return <section><div className="mb-1.5 text-[11px] font-medium text-slate-400">题目附件</div><input ref={fileInput} type="file" multiple className="hidden" onChange={event => { if (event.target.files) onAdd(event.target.files); event.currentTarget.value = '' }} /><input ref={folderInput} type="file" multiple className="hidden" {...({ webkitdirectory: '', directory: '' } as Record<string, string>)} onChange={event => { if (event.target.files) onAdd(event.target.files); event.currentTarget.value = '' }} /><div onDragEnter={event => { event.preventDefault(); setDragging(true) }} onDragOver={event => event.preventDefault()} onDragLeave={event => { if (event.currentTarget === event.target) setDragging(false) }} onDrop={event => { event.preventDefault(); setDragging(false); if (event.dataTransfer.files.length) onAdd(event.dataTransfer.files) }} className={cn('rounded-md border border-dashed p-4 transition-colors', dragging ? 'border-sky-400 bg-sky-400/10' : 'border-slate-700 bg-[#090e14]')}><div className="flex items-center justify-between gap-3"><div><div className="text-xs text-slate-300">拖拽文件或文件夹到这里</div><div className="mt-1 text-[10px] leading-4 text-slate-600">附件将复制到容器的 <span className="mono">/workspace/attachments</span>，启动 Agent 后可直接读取。</div></div><div className="flex shrink-0 gap-1.5"><Button type="button" variant="secondary" onClick={() => fileInput.current?.click()}>选择文件</Button><Button type="button" variant="secondary" onClick={() => folderInput.current?.click()}>选择文件夹</Button></div></div>{attachments.length > 0 && <div className="mt-3 border-t border-slate-800 pt-2"><div className="mb-1.5 text-[10px] text-slate-500">已选择 {attachments.length} 个文件 · {formatBytes(totalSize)}</div><div className="max-h-28 space-y-1 overflow-y-auto">{attachments.map(attachment => <div key={attachment.path} className="flex items-center gap-2 rounded bg-slate-900/80 px-2 py-1.5 text-[10px]"><FileCode2 size={12} className="shrink-0 text-sky-300" /><span className="mono min-w-0 flex-1 truncate text-slate-400">{attachment.path}</span><span className="text-slate-600">{formatBytes(attachment.file.size)}</span><button type="button" onClick={() => onRemove(attachment.path)} className="text-slate-600 hover:text-red-300" title="移除附件"><X size={12} /></button></div>)}</div></div>}</div></section>
}

function TaskContextMenu({ task, x, y, pending, onClose, onRequestDelete }: { task: Task; x: number; y: number; pending: boolean; onClose: () => void; onRequestDelete: () => void }) {
  const left = Math.min(x, window.innerWidth - 240)
  const top = Math.min(y, window.innerHeight - 132)
  return <div className="fixed inset-0 z-50" onMouseDown={onClose} onContextMenu={event => { event.preventDefault(); onClose() }}><div className="fixed w-[224px] rounded-md border border-slate-700 bg-[#121b25] p-1.5 shadow-2xl shadow-black/70" style={{ left, top }} onMouseDown={event => event.stopPropagation()}><div className="truncate px-2 py-1.5 text-[10px] text-slate-500">已结束任务 · {task.title}</div><button type="button" disabled={pending} onClick={onRequestDelete} className="flex w-full items-center gap-2 rounded px-2 py-2 text-left text-xs text-red-300 transition-colors hover:bg-red-500/10 disabled:opacity-50"><X size={14} />{pending ? '正在删除…' : '删除题解'}</button></div></div>
}

function ConfirmationDialog({ request, pending, onCancel, onConfirm }: { request: ConfirmationRequest; pending: boolean; onCancel: () => void; onConfirm: () => void }) {
  const deleting = request.kind === 'delete'
  const title = deleting ? '确认删除题解' : '确认关闭实例'
  const description = deleting ? `将永久删除“${request.task.title}”的 Docker 沙箱、工作区文件、附件、解题报告与 SQLite 事件记录，且无法恢复。` : `将关闭“${request.task.title}”的 Docker 容器并释放内存和模型会话。题目、附件、日志、解题报告会保留，之后仍可重新启动。`
  return <div className="fixed inset-0 z-[60] grid place-items-center bg-black/65 p-4 backdrop-blur-sm" onMouseDown={event => event.target === event.currentTarget && !pending && onCancel()}><section role="dialog" aria-modal="true" className="w-full max-w-md rounded-xl border border-slate-700 bg-[#111923] shadow-2xl shadow-black/70"><div className="flex items-start gap-3 border-b border-slate-800 px-5 py-4"><div className={cn('grid size-8 place-items-center rounded-md', deleting ? 'bg-red-500/10 text-red-300' : 'bg-amber-500/10 text-amber-300')}>{deleting ? <TriangleAlert size={16} /> : <Container size={16} />}</div><div><h2 className="text-sm font-semibold text-slate-100">{title}</h2><p className="mt-1 text-xs leading-5 text-slate-400">{description}</p></div></div><div className="flex justify-end gap-2 px-5 py-3"><Button type="button" variant="ghost" disabled={pending} onClick={onCancel}>取消</Button><Button type="button" variant={deleting ? 'danger' : 'primary'} disabled={pending} onClick={onConfirm}>{pending ? <RefreshCw size={13} className="animate-spin" /> : deleting ? <X size={13} /> : <Container size={13} />}{deleting ? '确认删除' : '关闭实例'}</Button></div></section></div>
}

function FlagSuccessToast({ taskTitle, flag, onClose }: { taskTitle: string; flag: string; onClose: () => void }) {
  return <aside className="fixed right-4 top-16 z-50 w-[360px] rounded-lg border border-emerald-400/30 bg-[#10231e] p-4 shadow-2xl shadow-black/60"><div className="flex items-start gap-3"><div className="grid size-8 shrink-0 place-items-center rounded-md bg-emerald-400/15 text-emerald-300"><Flag size={16} /></div><div className="min-w-0 flex-1"><div className="text-sm font-semibold text-emerald-200">解题成功</div><div className="mt-1 truncate text-xs text-slate-400">题目：{taskTitle}</div><div className="mono mt-2 break-all rounded border border-emerald-400/20 bg-black/15 px-2 py-1.5 text-xs text-emerald-200">{flag}</div></div><button type="button" onClick={onClose} className="text-slate-500 hover:text-slate-200" title="关闭提示"><X size={15} /></button></div></aside>
}

function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="block text-[11px] font-medium text-slate-400">{label}<div className="mt-1.5 [&_.input]:w-full [&_.input]:rounded-md [&_.input]:border [&_.input]:border-slate-700 [&_.input]:bg-[#090e14] [&_.input]:px-3 [&_.input]:py-2 [&_.input]:text-xs [&_.input]:text-slate-200 [&_.input]:outline-none [&_.input]:placeholder:text-slate-700 [&_.input]:focus:border-sky-500/60">{children}</div></label> }
function ConnectionBadge({ label, ok }: { label: string; ok: boolean }) { return <div className="flex items-center gap-1.5 rounded-full border border-slate-800 bg-slate-900 px-2.5 py-1 text-[10px] text-slate-500"><span className={cn('size-1.5 rounded-full', ok ? 'bg-emerald-400' : 'bg-amber-400')} />{label}</div> }
function NavItem({ active, icon: Icon, iconClass, label, count, onClick }: { active?: boolean; icon: typeof Activity; iconClass?: string; label: string; count?: number; onClick?: () => void }) { return <button type="button" onClick={onClick} className={cn('mb-0.5 flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-xs text-slate-500 hover:bg-slate-800/70 hover:text-slate-200', active && 'bg-slate-800 text-slate-100')}><Icon size={14} className={iconClass} /><span className="flex-1">{label}</span>{count !== undefined && <span className="mono text-[9px] text-slate-700">{count}</span>}</button> }
function TaskItem({ task, active, onClick, onContextMenu }: { task: Task; active: boolean; onClick: () => void; onContextMenu?: (event: MouseEvent<HTMLButtonElement>) => void }) { return <button type="button" onClick={onClick} onContextMenu={onContextMenu} title={taskCanBeDeleted(task) ? '右键可删除已结束题解' : '任务结束后可右键删除'} className={cn('mb-1 w-full rounded-md border border-transparent px-2 py-2 text-left hover:bg-slate-800/50', active && 'border-slate-700 bg-slate-800/70')}><div className="flex items-center gap-2"><CategoryIcon category={task.category} /><span className="min-w-0 flex-1 truncate text-xs text-slate-300">{task.title}</span><span className={cn('size-1.5 rounded-full', task.status === 'running' ? 'bg-sky-400' : task.status === 'paused' ? 'bg-amber-400' : task.status === 'settled' ? 'bg-emerald-400' : task.status === 'failed' ? 'bg-red-400' : 'bg-slate-600')} /></div><div className="mono mt-1 truncate pl-5 text-[9px] text-slate-700">{task.image}</div></button> }
function CategoryIcon({ category }: { category: Category }) { const value = categories.find(item => item.id === category)!; return <value.icon size={14} className={value.colour} /> }
function Stat({ icon: Icon, label, value, accent, small }: { icon: typeof Activity; label: string; value: string | number; accent: string; small?: boolean }) { return <div className="rounded-lg border border-slate-800 bg-[#0d131b]/95 p-4"><div className="flex items-center justify-between"><span className="text-xs text-slate-500">{label}</span><Icon size={15} className={accent} /></div><div className={cn('mono mt-3 text-2xl font-semibold text-slate-200', small && 'text-base')}>{value}</div></div> }
function SectionTitle({ icon: Icon, title, subtitle }: { icon: typeof Activity; title: string; subtitle: string }) { return <div className="flex items-center gap-2 border-b border-slate-800 px-4 py-3"><Icon size={14} className="text-slate-500" /><div><div className="text-xs font-medium text-slate-300">{title}</div><div className="mt-0.5 text-[9px] text-slate-600">{subtitle}</div></div></div> }
function PipelineStep({ icon: Icon, label, detail }: { icon: typeof Activity; label: string; detail: string }) { return <div className="relative rounded-md border border-slate-800 bg-[#090e14] p-3 after:absolute after:-right-2.5 after:top-1/2 after:h-px after:w-2.5 after:bg-slate-700 last:after:hidden"><Icon size={16} className="mb-4 text-sky-300" /><div className="text-xs font-medium">{label}</div><div className="mt-1 text-[10px] text-slate-600">{detail}</div></div> }
function RuntimeRow({ label, value, preferred }: { label: string; value: string; preferred: string }) { return <div className="flex items-center justify-between"><div><div className="text-slate-400">{label}</div><div className="text-[9px] text-slate-700">首选：{preferred}</div></div><Badge className={value === 'runc' ? 'border-amber-500/20 text-amber-300' : 'border-emerald-500/20 text-emerald-300'}>{value}</Badge></div> }
function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) { return <div><dt className="mb-1 text-[9px] uppercase tracking-wider text-slate-700">{label}</dt><dd className={cn('break-all text-slate-400', mono && 'mono text-[10px]')}>{value}</dd></div> }

export default App
