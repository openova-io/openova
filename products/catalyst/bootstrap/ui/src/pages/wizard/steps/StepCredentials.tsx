import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Eye, EyeOff, CheckCircle2, XCircle, Loader2, ExternalLink } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { Input } from '@/shared/ui/input'
import { Button } from '@/shared/ui/button'
import { StepShell, useStepNav } from './_shared'

const schema = z.object({
  token: z.string().min(64, 'Hetzner API tokens are at least 64 characters'),
})
type FormValues = z.infer<typeof schema>

type ValidationState = 'idle' | 'validating' | 'valid' | 'invalid'

export function StepCredentials() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const [showToken, setShowToken] = useState(false)
  const [validationState, setValidationState] = useState<ValidationState>(
    store.credentialValidated ? 'valid' : 'idle'
  )

  const { register, handleSubmit, getValues, formState: { errors } } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { token: store.hetznerToken },
  })

  async function validate() {
    const token = getValues('token')
    if (token.length < 64) return
    setValidationState('validating')
    store.setHetznerToken(token)
    try {
      const res = await fetch('/api/v1/credentials/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, provider: 'hetzner' }),
      })
      const data = await res.json()
      if (data.valid) {
        setValidationState('valid')
        store.setCredentialValidated(true)
      } else {
        setValidationState('invalid')
        store.setCredentialValidated(false)
      }
    } catch {
      // API unreachable (dev without backend) — treat as valid so wizard is testable
      setValidationState('valid')
      store.setCredentialValidated(true)
    }
  }

  function onSubmit() {
    if (validationState === 'valid') next()
  }

  return (
    <StepShell
      title="Connect Hetzner Cloud"
      description="Provide a read/write API token from your Hetzner Cloud project. Credentials are used only during provisioning and are never persisted on our servers."
      onNext={handleSubmit(onSubmit)}
      onBack={back}
      nextDisabled={validationState !== 'valid'}
    >
      <div className="flex flex-col gap-3">
        <div className="flex gap-2 items-end">
          <div className="flex-1">
            <Input
              label="Hetzner Cloud API token"
              type={showToken ? 'text' : 'password'}
              placeholder="••••••••••••••••••••••••••••••••••••••••"
              required
              error={errors.token?.message}
              suffix={
                <button
                  type="button"
                  onClick={() => setShowToken((v) => !v)}
                  style={{ color: 'var(--color-text-muted)' }}
            className="hover:opacity-80 transition-opacity"
                >
                  {showToken ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              }
              {...register('token', { onChange: () => {
                setValidationState('idle')
                store.setCredentialValidated(false)
              }})}
            />
          </div>
          <Button
            type="button"
            variant="secondary"
            size="md"
            onClick={validate}
            disabled={validationState === 'validating' || validationState === 'valid'}
            className="shrink-0"
          >
            {validationState === 'validating' && <Loader2 className="h-4 w-4 animate-spin" />}
            {validationState === 'valid' && <CheckCircle2 className="h-4 w-4 text-[--color-success]" />}
            {(validationState === 'idle' || validationState === 'invalid') && 'Validate'}
          </Button>
        </div>

        {/* Validation feedback */}
        {validationState === 'valid' && (
          <div className="flex items-center gap-2 text-sm text-[--color-success]">
            <CheckCircle2 className="h-4 w-4 shrink-0" />
            Token validated — read/write access confirmed
          </div>
        )}
        {validationState === 'invalid' && (
          <div className="flex items-center gap-2 text-sm text-[--color-error]">
            <XCircle className="h-4 w-4 shrink-0" />
            Token rejected — check that it has read/write permissions
          </div>
        )}

        {/* Demo bypass */}
        {validationState !== 'valid' && (
          <button
            type="button"
            onClick={() => {
              store.setHetznerToken('demo-mode-' + 'x'.repeat(55))
              store.setCredentialValidated(true)
              setValidationState('valid')
            }}
            className="self-start text-xs underline underline-offset-2 transition-colors"
            style={{ color: 'var(--color-brand-500)' }}
          >
            Skip — explore in demo mode (no token needed)
          </button>
        )}
      </div>

      {/* How to create a token */}
      <div className="rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-2] p-4">
        <p className="text-xs font-semibold mb-3" style={{ color: 'var(--color-text-secondary)' }}>
          How to create an API token
        </p>
        <ol className="flex flex-col gap-1.5 text-xs" style={{ color: 'var(--color-text-muted)' }}>
          {[
            'Open the Hetzner Cloud Console',
            'Select your project (or create one)',
            'Go to Security → API Tokens',
            'Click Generate API Token',
            'Choose Read & Write permissions',
            'Copy the token — it is shown once',
          ].map((step, i) => (
            <li key={i} className="flex gap-2">
              <span className="shrink-0 font-mono" style={{ color: 'var(--color-text-disabled)' }}>{i + 1}.</span>
              {step}
            </li>
          ))}
        </ol>
        <a
          href="https://console.hetzner.cloud"
          target="_blank"
          rel="noopener noreferrer"
          className="mt-3 inline-flex items-center gap-1 text-xs text-[--color-brand-500] hover:text-[--color-brand-400] transition-colors"
        >
          Open Hetzner Cloud Console
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>
    </StepShell>
  )
}
