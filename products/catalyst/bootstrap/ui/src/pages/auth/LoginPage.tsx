import { useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight, Mail } from 'lucide-react'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import { API_BASE } from '@/shared/config/urls'

type State = 'idle' | 'sending' | 'sent' | 'error'

export function LoginPage() {
  const [email, setEmail] = useState('')
  const [state, setState] = useState<State>('idle')
  const [errorMsg, setErrorMsg] = useState('')

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!email.trim()) return
    setState('sending')
    setErrorMsg('')
    try {
      const res = await fetch(`${API_BASE}/v1/auth/magic-link`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email: email.trim() }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error((body as { error?: string }).error ?? `HTTP ${res.status}`)
      }
      setState('sent')
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : 'Something went wrong')
      setState('error')
    }
  }

  function resetEmail() {
    setState('idle')
    setEmail('')
    setErrorMsg('')
  }

  if (state === 'sent') {
    return (
      <AuthShell>
        <motion.div
          initial={{ opacity: 0, y: 16 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.4, ease: [0.4, 0, 0.2, 1] }}
          className="flex flex-col gap-6"
        >
          <div className="flex flex-col items-center gap-4 py-4 text-center">
            <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[oklch(22%_0.02_250)] ring-1 ring-[oklch(30%_0.02_250)]">
              <Mail className="h-6 w-6 text-[--color-brand-400]" />
            </div>
            <div>
              <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Check your email</h1>
              <p className="mt-2 text-sm text-[oklch(55%_0.01_250)]">
                We sent a sign-in link to
              </p>
              <p className="mt-1 text-sm font-medium text-[oklch(75%_0.01_250)]">{email}</p>
            </div>
            <p className="text-xs text-[oklch(45%_0.01_250)]">
              The link expires in 1 hour. Sent from{' '}
              <span className="font-mono">noreply@openova.io</span>.
            </p>
          </div>

          <button
            type="button"
            onClick={resetEmail}
            className="text-center text-sm text-[--color-brand-400] hover:text-[--color-brand-300] transition-colors"
          >
            Use a different email
          </button>
        </motion.div>
      </AuthShell>
    )
  }

  return (
    <AuthShell>
      <motion.div
        initial={{ opacity: 0, y: 16 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.4, ease: [0.4, 0, 0.2, 1] }}
        className="flex flex-col gap-8"
      >
        <div>
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Welcome back</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
            Enter your email — we'll send you a sign-in link.
          </p>
        </div>

        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          <Input
            label="Email"
            type="email"
            placeholder="you@company.com"
            autoComplete="email"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            error={state === 'error' ? errorMsg : undefined}
          />

          <Button
            type="submit"
            loading={state === 'sending'}
            disabled={state === 'sending' || !email.trim()}
            size="lg"
            className="mt-1 w-full"
          >
            Send sign-in link
            <ArrowRight className="h-4 w-4" />
          </Button>
        </form>
      </motion.div>
    </AuthShell>
  )
}
