import type { HTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

// Badge 是状态、运行时和分类使用的轻量标签，允许调用方追加语义颜色。
export function Badge({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span className={cn('inline-flex items-center rounded-full border border-slate-700 bg-slate-900 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-slate-400', className)} {...props} />
}
