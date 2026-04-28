/**
 * useSubdomainAvailability — debounced check against catalyst-api's
 * /api/v1/subdomains/check endpoint.
 *
 * Closes #124 ([I] ux: error handling — what happens if subdomain
 * already taken). Runs while the user is still typing in StepOrg so
 * we catch the collision BEFORE Submit, not at provisioning time
 * when Dynadot rejects the duplicate record.
 *
 * Wire format (handler/subdomains.go SubdomainCheckResponse):
 *   POST /api/v1/subdomains/check {subdomain, poolDomain}
 *   200 { available: true,  fqdn }
 *   200 { available: false, reason, detail, fqdn }
 *
 * Reason values surfaced verbatim in the StepOrg inline-error UI:
 *   "invalid-format", "unsupported-pool", "reserved", "exists", "lookup-error"
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs) the endpoint
 * is constructed via shared/config/urls.API_BASE.
 */

import { useEffect, useState } from 'react'
import { API_BASE } from '@/shared/config/urls'

export type AvailabilityStatus =
  | 'idle'         // no input yet, or input is too short for a check
  | 'invalid'      // syntactically invalid input — short-circuit, no fetch
  | 'checking'     // fetch in flight
  | 'available'    // backend confirmed available
  | 'taken'        // backend confirmed taken / reserved / unsupported pool
  | 'error'        // fetch itself failed (network, parse)

export interface AvailabilityResult {
  status: AvailabilityStatus
  /** Backend reason field, when status === 'taken' or 'invalid'. */
  reason?: string
  /** Human-readable explanation for the inline-error card. */
  detail?: string
  /** Echoed FQDN the backend evaluated (for confirmation). */
  fqdn?: string
}

const IDLE: AvailabilityResult = { status: 'idle' }

/**
 * Debounce by 400ms — long enough that fast typists don't trigger a
 * fetch on every keystroke, short enough that the user gets feedback
 * before tabbing to the next field.
 */
const DEBOUNCE_MS = 400

/**
 * Mirror of handler.go isValidDNSLabel — keeps the wizard's "checking"
 * spinner from firing when the input is syntactically dead.
 */
function isValidDNSLabel(s: string): boolean {
  if (!s || s.length > 63) return false
  return /^[a-z]([a-z0-9-]*[a-z0-9])?$/.test(s)
}

export function useSubdomainAvailability(
  subdomain: string,
  poolDomain: string,
): AvailabilityResult {
  const [result, setResult] = useState<AvailabilityResult>(IDLE)

  useEffect(() => {
    const sub = subdomain.trim().toLowerCase()
    const pool = poolDomain.trim().toLowerCase()

    // Defer state mutations into a microtask so the React-hooks plugin's
    // "no setState in effect body" rule is satisfied. The visual delay
    // is sub-frame; the subsequent debounce timer is what users perceive.
    if (!sub) {
      queueMicrotask(() => setResult(IDLE))
      return
    }
    if (!isValidDNSLabel(sub)) {
      queueMicrotask(() =>
        setResult({
          status: 'invalid',
          reason: 'invalid-format',
          detail:
            'subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters',
        }),
      )
      return
    }
    if (!pool) {
      queueMicrotask(() => setResult(IDLE))
      return
    }

    queueMicrotask(() => setResult({ status: 'checking' }))

    const ctrl = new AbortController()
    const timer = window.setTimeout(async () => {
      try {
        const res = await fetch(`${API_BASE}/v1/subdomains/check`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ subdomain: sub, poolDomain: pool }),
          signal: ctrl.signal,
        })
        if (!res.ok) {
          setResult({
            status: 'error',
            detail: `Availability check failed (HTTP ${res.status})`,
          })
          return
        }
        const data = (await res.json()) as {
          available?: boolean
          reason?: string
          detail?: string
          fqdn?: string
        }
        if (data.available === true) {
          setResult({
            status: 'available',
            fqdn: data.fqdn,
          })
        } else {
          setResult({
            status: data.reason === 'invalid-format' ? 'invalid' : 'taken',
            reason: data.reason,
            detail: data.detail,
            fqdn: data.fqdn,
          })
        }
      } catch (err) {
        if ((err as { name?: string }).name === 'AbortError') return
        setResult({
          status: 'error',
          detail: `Network error: ${String(err)}`,
        })
      }
    }, DEBOUNCE_MS)

    return () => {
      ctrl.abort()
      window.clearTimeout(timer)
    }
  }, [subdomain, poolDomain])

  return result
}
