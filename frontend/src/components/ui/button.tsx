import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '../../lib/utils'

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

type Props = ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>
export const Button = forwardRef<HTMLButtonElement, Props>(({ className, variant, ...props }, ref) => (
  <button ref={ref} className={cn(buttonVariants({ variant }), className)} {...props} />
))
Button.displayName = 'Button'
