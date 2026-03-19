import { type HTMLAttributes } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/shared/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset',
  {
    variants: {
      variant: {
        default: 'bg-[--color-surface-2] text-[oklch(70%_0.01_250)] ring-[--color-surface-border]',
        brand: 'bg-[--color-brand-500]/15 text-[--color-brand-300] ring-[--color-brand-500]/30',
        success: 'bg-[--color-success]/10 text-[--color-success] ring-[--color-success]/25',
        warning: 'bg-[--color-warning]/10 text-[--color-warning] ring-[--color-warning]/25',
        error: 'bg-[--color-error]/10 text-[--color-error] ring-[--color-error]/25',
        info: 'bg-[--color-info]/10 text-[--color-info] ring-[--color-info]/25',
        outline: 'bg-transparent text-[oklch(70%_0.01_250)] ring-[--color-surface-border]',
      },
    },
    defaultVariants: { variant: 'default' },
  }
)

interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />
}

export { Badge, badgeVariants }
export type { BadgeProps }
