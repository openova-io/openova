/**
 * RequiredActionsModal — post-login first-use prompt for Sovereign operators.
 *
 * After a successful OIDC login (or handover-token reception), the
 * id_token may carry a `requiredActions` claim from Keycloak listing
 * mandatory first-use tasks the user must complete before accessing the
 * console:
 *
 *   - UPDATE_PASSWORD     — password must be changed
 *   - configure-passkey   — passkey must be registered
 *   - CONFIGURE_TOTP      — TOTP 2FA must be configured
 *
 * This modal is shown as a blocking interstitial — the user cannot
 * dismiss it; they must complete the required action in Keycloak's
 * account-management UI (opened in a new tab) and then click
 * "I've completed the setup" to refresh their tokens. If the refresh
 * still shows required actions, the modal re-appears.
 *
 * Design rationale:
 *   - We do NOT embed a Keycloak account-management iframe because
 *     Keycloak's CSP and X-Frame-Options headers prevent framing in
 *     modern browsers without explicit configuration. A new-tab link to
 *     the Keycloak account console is the standard approach.
 *   - The "I've completed the setup" button triggers a silent token
 *     refresh; if required actions are gone, the modal closes.
 *   - If Keycloak's refresh returns new required actions, the modal
 *     re-renders with the updated list.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the Keycloak
 * account URL is derived from the Sovereign FQDN at runtime.
 *
 * Related: GitHub issue #607
 */

import { useState } from 'react'
import { ShieldCheck, Key, Lock, RefreshCw, ExternalLink } from 'lucide-react'
import { Button } from '@/shared/ui/button'
import { silentRefresh, getRequiredActions, saveTokens } from '@/shared/lib/oidc'
import type { TokenSet } from '@/shared/lib/oidc'
import {
  REQUIRED_ACTION_UPDATE_PASSWORD,
  REQUIRED_ACTION_CONFIGURE_PASSKEY,
  REQUIRED_ACTION_CONFIGURE_TOTP,
} from '@/shared/lib/oidc'

interface RequiredActionsModalProps {
  /** Sovereign FQDN — used to derive the Keycloak account URL. */
  sovereignFQDN: string
  /** Required actions from the current id_token. */
  requiredActions: string[]
  /**
   * Called when tokens are refreshed and no required actions remain.
   * The parent re-renders without the modal.
   */
  onComplete: (tokens: TokenSet) => void
}

interface ActionDescriptor {
  code: string
  label: string
  description: string
  icon: React.ReactNode
}

const ACTION_DESCRIPTORS: ActionDescriptor[] = [
  {
    code: REQUIRED_ACTION_UPDATE_PASSWORD,
    label: 'Update your password',
    description: 'Your password must be changed before you can access the Sovereign Console.',
    icon: <Lock className="h-5 w-5 text-[var(--color-accent)]" />,
  },
  {
    code: REQUIRED_ACTION_CONFIGURE_PASSKEY,
    label: 'Register a passkey',
    description: 'Set up a passkey (WebAuthn / FIDO2) for passwordless sign-in.',
    icon: <Key className="h-5 w-5 text-[var(--color-accent)]" />,
  },
  {
    code: REQUIRED_ACTION_CONFIGURE_TOTP,
    label: 'Configure two-factor authentication',
    description: 'Configure TOTP (e.g. Google Authenticator) to secure your account.',
    icon: <ShieldCheck className="h-5 w-5 text-[var(--color-accent)]" />,
  },
]

function describeAction(code: string): ActionDescriptor {
  return (
    ACTION_DESCRIPTORS.find((a) => a.code === code) ?? {
      code,
      label: code,
      description: 'Complete this required action in the Keycloak account console.',
      icon: <ShieldCheck className="h-5 w-5 text-[var(--color-accent)]" />,
    }
  )
}

export function RequiredActionsModal({
  sovereignFQDN,
  requiredActions,
  onComplete,
}: RequiredActionsModalProps) {
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [currentActions, setCurrentActions] = useState<string[]>(requiredActions)

  const accountUrl = `https://auth.${sovereignFQDN}/realms/sovereign/account`

  async function handleDone() {
    setRefreshing(true)
    setError(null)
    try {
      const tokens = await silentRefresh(sovereignFQDN)
      if (!tokens) {
        setError('Session expired. Please reload the page and sign in again.')
        return
      }
      const remaining = getRequiredActions(tokens.idToken)
      if (remaining.length === 0) {
        saveTokens(tokens)
        onComplete(tokens)
      } else {
        setCurrentActions(remaining)
        setError('Some required actions are still pending. Please complete them and try again.')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Token refresh failed. Please reload and sign in again.')
    } finally {
      setRefreshing(false)
    }
  }

  return (
    /* Blocking overlay — no close button, no backdrop click dismiss */
    <div
      className="fixed inset-0 z-[9999] flex items-center justify-center bg-black/70 backdrop-blur-sm"
      data-testid="required-actions-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="ra-modal-title"
    >
      <div className="w-full max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 shadow-2xl">
        {/* Header */}
        <div className="mb-6 flex items-start gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-full bg-[var(--color-accent)]/15">
            <ShieldCheck className="h-6 w-6 text-[var(--color-accent)]" />
          </div>
          <div>
            <h2
              id="ra-modal-title"
              className="text-lg font-semibold text-[var(--color-text-strong)]"
            >
              Account setup required
            </h2>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Complete the following steps in the Keycloak account console before accessing the Sovereign Console.
            </p>
          </div>
        </div>

        {/* Action list */}
        <ul className="mb-6 space-y-3" role="list">
          {currentActions.map((code) => {
            const desc = describeAction(code)
            return (
              <li
                key={code}
                className="flex items-start gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-4"
                data-testid={`required-action-${code}`}
              >
                <span className="mt-0.5 flex-shrink-0">{desc.icon}</span>
                <div>
                  <p className="text-sm font-medium text-[var(--color-text-strong)]">
                    {desc.label}
                  </p>
                  <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                    {desc.description}
                  </p>
                </div>
              </li>
            )
          })}
        </ul>

        {/* Open Keycloak account console */}
        <a
          href={accountUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="mb-4 flex w-full items-center justify-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-2.5 text-sm font-medium text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text-strong)]"
          data-testid="ra-open-account-link"
        >
          Open Keycloak account console
          <ExternalLink className="h-3.5 w-3.5" />
        </a>

        {/* Error */}
        {error ? (
          <p
            className="mb-4 rounded-lg border border-[var(--color-error)]/30 bg-[var(--color-error)]/10 px-4 py-2.5 text-sm text-[var(--color-error)]"
            data-testid="ra-error"
            role="alert"
          >
            {error}
          </p>
        ) : null}

        {/* Done button — triggers silent refresh */}
        <Button
          type="button"
          variant="primary"
          size="md"
          className="w-full"
          loading={refreshing}
          onClick={handleDone}
          data-testid="ra-done-button"
        >
          {refreshing ? null : <RefreshCw className="h-4 w-4" />}
          I've completed the setup
        </Button>

        <p className="mt-3 text-center text-xs text-[var(--color-text-dimmer)]">
          After completing all steps in the account console, click the button above to continue.
        </p>
      </div>
    </div>
  )
}
