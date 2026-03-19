import { type InputHTMLAttributes, forwardRef } from 'react'
import { cn } from '@/shared/lib/utils'

interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'prefix'> {
  error?: string
  label?: string
  hint?: string
  prefix?: React.ReactNode
  suffix?: React.ReactNode
}

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, error, label, hint, prefix, suffix, id, ...props }, ref) => {
    const inputId = id ?? label?.toLowerCase().replace(/\s+/g, '-')

    return (
      <div className="flex flex-col gap-1.5 w-full">
        {label && (
          <label
            htmlFor={inputId}
            className="text-sm font-medium"
            style={{ color: 'var(--color-text-secondary)' }}
          >
            {label}
            {props.required && (
              <span className="text-[--color-error] ml-1" aria-hidden="true">*</span>
            )}
          </label>
        )}
        <div className="relative flex items-center">
          {prefix && (
            <div
              className="absolute left-3 flex items-center pointer-events-none"
              style={{ color: 'var(--color-text-muted)' }}
            >
              {prefix}
            </div>
          )}
          <input
            ref={ref}
            id={inputId}
            className={cn(
              'w-full h-9 rounded-[--radius-md]',
              'bg-[--color-surface-1] border border-[--color-surface-border]',
              'text-sm transition-all duration-150',
              'hover:border-[--color-surface-border-hover]',
              'focus:outline-none focus:border-[--color-brand-500]/60 focus:ring-1 focus:ring-[--color-brand-500]/30',
              error && 'border-[--color-error]/50 focus:border-[--color-error]/70 focus:ring-[--color-error]/20',
              prefix ? 'pl-9' : 'px-3',
              suffix ? 'pr-9' : 'px-3',
              'disabled:opacity-40 disabled:cursor-not-allowed',
              className,
            )}
            style={{
              color: 'var(--color-text-primary)',
            }}
            aria-invalid={!!error}
            aria-describedby={
              error ? `${inputId}-error` : hint ? `${inputId}-hint` : undefined
            }
            {...props}
          />
          {suffix && (
            <div
              className="absolute right-3 flex items-center"
              style={{ color: 'var(--color-text-muted)' }}
            >
              {suffix}
            </div>
          )}
        </div>
        {error && (
          <p
            id={`${inputId}-error`}
            className="text-xs text-[--color-error] flex items-center gap-1"
            role="alert"
          >
            {error}
          </p>
        )}
        {hint && !error && (
          <p
            id={`${inputId}-hint`}
            className="text-xs"
            style={{ color: 'var(--color-text-muted)' }}
          >
            {hint}
          </p>
        )}
      </div>
    )
  }
)
Input.displayName = 'Input'

export { Input }
export type { InputProps }
