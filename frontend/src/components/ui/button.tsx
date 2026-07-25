import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '../../lib/utils'

// buttonVariants 集中定义按钮公共交互样式和四种语义外观。
const buttonVariants = cva('inline-flex h-8 items-center justify-center gap-2 rounded-md px-3 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/50 disabled:pointer-events-none disabled:opacity-45', {
  variants: {
    variant: {
      primary: 'bg-sky-400 text-slate-950 hover:bg-sky-300',
      secondary: 'border border-slate-700 bg-slate-900 text-slate-200 hover:bg-slate-800',
      ghost: 'text-slate-400 hover:bg-slate-800 hover:text-slate-100',
      danger: 'border border-red-500/30 bg-red-500/10 text-red-300 hover:bg-red-500/20',
    },
  },
  defaultVariants: { variant: 'secondary' },
})

// Props 保留全部原生 button 属性，并增加 variant 变体类型。
type Props = ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>

// Button 通过 forwardRef 支持弹窗焦点管理和外部 DOM 引用。
export const Button = forwardRef<HTMLButtonElement, Props>(({ className, variant, ...props }, ref) => (
  <button ref={ref} className={cn(buttonVariants({ variant }), className)} {...props} />
))

// 显式 displayName 让 React DevTools 中的组件名称保持可读。
Button.displayName = 'Button'
