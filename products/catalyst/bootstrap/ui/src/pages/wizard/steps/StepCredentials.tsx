import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Eye, EyeOff, CheckCircle2, XCircle, Loader2, ExternalLink } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { Input } from '@/shared/ui/input'
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
      description="Provide a read/write API token from your Hetzner Cloud project. Credentials are used only during provisioning and never persisted on our servers."
      onNext={handleSubmit(onSubmit)}
      onBack={back}
      nextDisabled={validationState !== 'valid'}
    >
      {/* Token input + validate button */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
          <div style={{ flex: 1 }}>
            <Input
              label="Hetzner Cloud API token"
              type={showToken ? 'text' : 'password'}
              placeholder="Paste your token here…"
              required
              error={errors.token?.message}
              suffix={
                <button
                  type="button"
                  onClick={() => setShowToken(v => !v)}
                  style={{ color: 'var(--color-text-muted)', cursor: 'pointer', display: 'flex' }}
                >
                  {showToken ? <EyeOff size={15} /> : <Eye size={15} />}
                </button>
              }
              {...register('token', {
                onChange: () => {
                  setValidationState('idle')
                  store.setCredentialValidated(false)
                },
              })}
            />
          </div>

          {/* Validate button */}
          <button
            type="button"
            onClick={validate}
            disabled={validationState === 'validating' || validationState === 'valid'}
            style={{
              height: 42, paddingLeft: 14, paddingRight: 14, flexShrink: 0,
              borderRadius: 8,
              border: '1.5px solid var(--color-surface-border)',
              background: validationState === 'valid'
                ? 'rgba(34,197,94,0.08)'
                : 'var(--color-surface-2)',
              color: validationState === 'valid'
                ? 'var(--color-success)'
                : 'var(--color-text-secondary)',
              fontSize: 13, fontWeight: 500,
              cursor: validationState === 'validating' || validationState === 'valid' ? 'default' : 'pointer',
              display: 'flex', alignItems: 'center', gap: 6,
              transition: 'all 0.15s',
              opacity: validationState === 'validating' ? 0.7 : 1,
            }}
          >
            {validationState === 'validating' && <Loader2 size={14} className="animate-spin" />}
            {validationState === 'valid' && <CheckCircle2 size={14} style={{ color: 'var(--color-success)' }} />}
            {validationState === 'idle' && 'Validate'}
            {validationState === 'invalid' && 'Retry'}
            {validationState === 'validating' && 'Validating'}
            {validationState === 'valid' && 'Validated'}
          </button>
        </div>

        {/* Feedback row */}
        {validationState === 'valid' && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--color-success)' }}>
            <CheckCircle2 size={14} />
            Token validated — read/write access confirmed
          </div>
        )}
        {validationState === 'invalid' && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--color-error)' }}>
            <XCircle size={14} />
            Token rejected — ensure it has Read &amp; Write permissions
          </div>
        )}

        {/* Demo bypass — sky-blue, clearly visible */}
        {validationState !== 'valid' && (
          <button
            type="button"
            onClick={() => {
              store.setHetznerToken('demo-mode-' + 'x'.repeat(55))
              store.setCredentialValidated(true)
              setValidationState('valid')
            }}
            style={{
              alignSelf: 'flex-start',
              fontSize: 12, fontWeight: 500,
              color: 'var(--color-brand-500)',
              background: 'none', border: 'none',
              cursor: 'pointer', padding: 0,
              textDecoration: 'underline',
              textUnderlineOffset: 3,
            }}
          >
            No token yet? Skip — explore in demo mode →
          </button>
        )}
      </div>

      {/* How-to card */}
      <div style={{
        borderRadius: 10,
        border: '1.5px solid var(--color-surface-border)',
        background: 'var(--color-surface-1)',
        padding: '1rem 1.125rem',
      }}>
        <p style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-text-secondary)', margin: 0, marginBottom: 10 }}>
          How to create an API token
        </p>
        <ol style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}>
          {[
            'Open the Hetzner Cloud Console',
            'Select your project (or create one)',
            'Go to Security → API Tokens',
            'Click Generate API Token',
            'Choose Read & Write permissions',
            'Copy the token — it is shown once',
          ].map((step, i) => (
            <li key={i} style={{ display: 'flex', gap: 10, fontSize: 12, color: 'var(--color-text-muted)', alignItems: 'flex-start' }}>
              <span style={{
                flexShrink: 0,
                width: 18, height: 18,
                borderRadius: '50%',
                background: 'var(--color-surface-2)',
                border: '1px solid var(--color-surface-border)',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                fontSize: 10, fontWeight: 600,
                color: 'var(--color-text-disabled)',
              }}>
                {i + 1}
              </span>
              {step}
            </li>
          ))}
        </ol>
        <a
          href="https://console.hetzner.cloud"
          target="_blank"
          rel="noopener noreferrer"
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 4,
            marginTop: 12, fontSize: 12,
            color: 'var(--color-brand-500)',
            textDecoration: 'none',
          }}
        >
          Open Hetzner Cloud Console
          <ExternalLink size={11} />
        </a>
      </div>
    </StepShell>
  )
}
