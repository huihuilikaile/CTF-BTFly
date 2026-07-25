import { useEffect, useRef, useState } from 'react'
import { PlatformClient } from './api'
import type { PlatformEvent } from './types'

// 每个任务最多在前端保留 5000 条事件；SQLite 中仍保存完整历史。
const maxCachedEvents = 5_000
const eventCache = new Map<string, PlatformEvent[]>()

// snapshot 带 taskId，防止任务快速切换时把上一任务异步结果绘制到当前页面。
type EventSnapshot = {
  taskId?: string
  events: PlatformEvent[]
}

// lastSequenceOf 返回增量拉取与 WebSocket 重连所需的游标。
function lastSequenceOf(events: PlatformEvent[]) {
  return events.length === 0 ? 0 : events[events.length - 1].sequence
}

// mergeEvents 按 sequence 去重、排序并裁剪缓存，兼容 REST 历史与 WS 实时数据重叠。
function mergeEvents(taskId: string, current: PlatformEvent[], incoming: PlatformEvent[]) {
  if (incoming.length === 0) return current

  const bySequence = new Map(current.map(event => [event.sequence, event]))
  for (const event of incoming) bySequence.set(event.sequence, event)

  const next = [...bySequence.values()]
    .sort((left, right) => left.sequence - right.sequence)
    .slice(-maxCachedEvents)

  eventCache.set(taskId, next)
  return next
}

/**
 * 事件会在内存中按题目缓存：切换到其他页面再返回时，先同步恢复已显示的过程，
 * 然后通过 REST/WS 补齐离开期间新增的事件，避免“清空后重新灌入”的闪屏。
 */
export function useTaskEvents(client: PlatformClient | null, taskId?: string) {
  const [snapshot, setSnapshot] = useState<EventSnapshot>({ events: [] })
  const [connected, setConnected] = useState(false)
  const lastSequence = useRef(0)

  const cachedEvents = taskId ? eventCache.get(taskId) ?? [] : []
  const events = snapshot.taskId === taskId ? snapshot.events : cachedEvents

  // client 或 taskId 变化时重建 REST/WS 同步链，并在清理函数中停止重连。
  useEffect(() => {
    setConnected(false)

    if (!client || !taskId) {
      lastSequence.current = 0
      setSnapshot({ taskId, events: [] })
      return
    }

    const cached = eventCache.get(taskId) ?? []
    lastSequence.current = lastSequenceOf(cached)
    setSnapshot({ taskId, events: cached })

    let disposed = false
    let socket: WebSocket | undefined
    let retry: number | undefined

    // publish 统一合并任何来源的事件，并只更新仍显示同一任务的快照。
    const publish = (incoming: PlatformEvent[]) => {
      if (disposed || incoming.length === 0) return

      const current = eventCache.get(taskId) ?? []
      const next = mergeEvents(taskId, current, incoming)
      lastSequence.current = Math.max(lastSequence.current, lastSequenceOf(next))
      setSnapshot(active => active.taskId === taskId ? { taskId, events: next } : active)
    }

    // 首次打开从 REST 拉取已保存历史；重新进入则只补齐缓存之后的事件。
    void client.events(taskId, lastSequence.current).then(publish).catch(() => {
      // WebSocket 仍会尝试补齐历史，REST 失败不应影响实时事件。
    })

    // WebSocket 使用最后 sequence 建连；断开后固定延迟重连，服务端会先补历史。
    const connect = () => {
      if (disposed) return

      socket = client.eventSocket(taskId, lastSequence.current)
      socket.onopen = () => {
        if (!disposed) setConnected(true)
      }
      socket.onmessage = message => {
        try {
          publish([JSON.parse(message.data) as PlatformEvent])
        } catch {
          // 服务端会记录协议错误，前端保持当前可见日志。
        }
      }
      socket.onclose = () => {
        if (disposed) return
        setConnected(false)
        retry = window.setTimeout(connect, 1_200)
      }
    }

    connect()

    // 组件卸载或任务切换时阻止旧回调写入状态，并释放 Socket/计时器。
    return () => {
      disposed = true
      socket?.close()
      if (retry !== undefined) window.clearTimeout(retry)
    }
  }, [client, taskId])

  return { events, connected: snapshot.taskId === taskId && connected }
}
