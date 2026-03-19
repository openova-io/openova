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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, width: '100%' }}>
        {label && (
          <label
            htmlFor={inputId}
            style={{
              fontSize: 13, fontWeight: 500,
              color: 'var(--color-text-secondary)',
              display: 'flex', alignItems: 'center', gap: 4,
            }}
          >
            {label}
            {props.required && (
              <span style={{ color: 'var(--color-error)', fontSize: 13 }} aria-hidden="true">*</span>
            )}
          </label>
        )}

        <div style={{ position: 'relative', display: 'flex', alignItems: 'center' }}>
          {prefix && (
            <div style={{
              position: 'absolute', left: 12,
              color: 'var(--color-text-muted)',
              display: 'flex', alignItems: 'center', pointerEvents: 'none',
            }}>
              {prefix}
            </div>
          )}

          <input
            ref={ref}
            id={inputId}
            className={cn(className)}
            style={{
              width: '100%',
              height: 42,
              borderRadius: 8,
              border: error
                ? '1.5px solid rgba(239,68,68,0.6)'
                : '1.5px solid var(--color-surface-border)',
              background: 'var(--color-surface-1)',
              color: 'var(--color-text-primary)',
              fontSize: 14,
              paddingLeft: prefix ? 38 : 12,
              paddingRight: suffix ? 38 : 12,
              outline: 'none',
              transition: 'border-color 0.15s, box-shadow 0.15s',
              fontFamily: 'var(--font-sans)',
            }}
            onFocus={e => {
              e.currentTarget.style.borderColor = error
                ? 'rgba(239,68,68,0.7)'
                : 'rgba(56,189,248,0.5)'
              e.currentTarget.style.boxShadow = error
                ? '0 0 0 3px rgba(239,68,68,0.08)'
                : '0 0 0 3px rgba(56,189,248,0.08)'
            }}
            onBlur={e => {
              e.currentTarget.style.borderColor = error
                ? 'rgba(239,68,68,0.6)'
                : 'var(--color-surface-border)'
              e.currentTarget.style.boxShadow = 'none'
            }}
            aria-invalid={!!error}
            aria-describedby={
              error ? `${inputId}-error` : hint ? `${inputId}-hint` : undefined
            }
            {...props}
          />

          {suffix && (
            <div style={{
              position: 'absolute', right: 12,
              color: 'var(--color-text-muted)',
              display: 'flex', alignItems: 'center',
            }}>
              {suffix}
            </div>
          )}
        </div>

        {error && (
          <p
            id={`${inputId}-error`}
            role="alert"
            style={{ fontSize: 12, color: 'var(--color-error)', margin: 0, display: 'flex', alignItems: 'center', gap: 4 }}
          >
            {error}
          </p>
        )}
        {hint && !error && (
          <p
            id={`${inputId}-hint`}
            style={{ fontSize: 12, color: 'var(--color-text-muted)', margin: 0, lineHeight: 1.5 }}
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
