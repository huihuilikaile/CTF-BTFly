import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'
export function cn(...values: ClassValue[]) { return twMerge(clsx(values)) }
export function eventText(payload: Record<string, unknown>): string {
  const assistant = payload.assistantMessageEvent as Record<string, unknown> | undefined
  if (assistant?.delta) return String(assistant.delta)
  const partial = payload.partialResult as Record<string, unknown> | undefined
  const result = (payload.result ?? partial) as Record<string, unknown> | undefined
  const content = result?.content
  if (Array.isArray(content)) return content.map(item => String((item as Record<string, unknown>).text ?? '')).join('\n')
  const message = payload.message as Record<string, unknown> | undefined
  const messageContent = message?.content
  if (Array.isArray(messageContent)) return messageContent.map(item => String((item as Record<string, unknown>).text ?? '')).join('\n')
  if (payload.text) return String(payload.text)
  if (payload.error) return String(payload.error)
  return ''
}
