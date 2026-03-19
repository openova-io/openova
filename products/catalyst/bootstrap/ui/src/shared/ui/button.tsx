import { type ButtonHTMLAttributes, forwardRef } from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/shared/lib/utils'

const buttonVariants = cva(
  [
    'inline-flex items-center justify-center gap-2 whitespace-nowrap',
    'font-medium text-sm transition-all duration-150',
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[--color-brand-500] focus-visible:ring-offset-2 focus-visible:ring-offset-[--color-surface-0]',
    'disabled:pointer-events-none disabled:opacity-40',
    'select-none cursor-pointer',
  ],
  {
    variants: {
      variant: {
        primary: [
          'bg-[--color-brand-500] text-white rounded-[--radius-md]',
          'hover:bg-[--color-brand-400] active:bg-[--color-brand-600]',
          'shadow-sm',
        ],
        secondary: [
          'bg-[--color-surface-2] text-[oklch(85%_0.01_250)] rounded-[--radius-md]',
          'border border-[--color-surface-border]',
          'hover:bg-[--color-surface-3] hover:border-[oklch(30%_0.025_250)]',
          'active:bg-[--color-surface-1]',
        ],
        ghost: [
          'text-[oklch(70%_0.01_250)] rounded-[--radius-md]',
          'hover:bg-[--color-surface-2] hover:text-[oklch(92%_0.01_250)]',
          'active:bg-[--color-surface-1]',
        ],
        destructive: [
          'bg-[--color-error]/10 text-[--color-error] rounded-[--radius-md]',
          'border border-[--color-error]/20',
          'hover:bg-[--color-error]/20',
        ],
        outline: [
          'border border-[--color-brand-500]/40 text-[--color-brand-400] rounded-[--radius-md]',
          'hover:bg-[--color-brand-500]/10 hover:border-[--color-brand-500]/60',
        ],
      },
      size: {
        sm: 'h-8 px-3 text-xs',
        md: 'h-9 px-4 text-sm',
        lg: 'h-11 px-6 text-base',
        xl: 'h-13 px-8 text-base',
        icon: 'h-9 w-9',
        'icon-sm': 'h-8 w-8',
      },
    },
    defaultVariants: {
      variant: 'primary',
      size: 'md',
    },
  }
)

interface ButtonProps
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  loading?: boolean
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, loading, children, disabled, ...props }, ref) => {
    return (
      <button
        ref={ref}
        className={cn(buttonVariants({ variant, size }), className)}
        disabled={disabled ?? loading}
        aria-busy={loading}
        {...props}
      >
        {loading && (
          <svg
            className="animate-spin h-4 w-4 shrink-0"
            xmlns="http://www.w3.org/2000/svg"
            fill="none"
            viewBox="0 0 24 24"
            aria-hidden="true"
          >
            <circle
              className="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              strokeWidth="4"
            />
            <path
              className="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
        )}
        {children}
      </button>
    )
  }
)
Button.displayName = 'Button'

export { Button, buttonVariants }
export type { ButtonProps }
