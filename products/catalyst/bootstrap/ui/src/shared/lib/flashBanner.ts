/**
 * Flash-banner sessionStorage seam (issue #689).
 *
 * The provision-route auth guard in `app/router.tsx` runs BEFORE any
 * component mounts, so it cannot pass props to the wizard.  Instead it
 * stashes a banner message in `sessionStorage` and the wizard reads +
 * clears it on mount.
 *
 * sessionStorage (NOT localStorage) is intentional: the banner is a
 * one-shot per-tab signal, not persistent state — it should NOT survive
 * a tab close, and a banner from one tab should NOT leak into another
 * concurrent tab.
 */

const KEY = 'openova-wizard-flash-banner'

export function setProvisionFlashBanner(message: string): void {
  if (typeof window === 'undefined') return
  try {
    sessionStorage.setItem(KEY, message)
  } catch {
    // sessionStorage may throw in private mode or when the quota
    // is exceeded — ignore; a missing banner is not a fatal failure.
  }
}

/**
 * Read the banner message and clear it atomically.  Returns null when
 * no banner is queued.  The wizard calls this on mount and again
 * whenever `currentStep` changes (in case a deep-link guard fired
 * mid-session).
 */
export function consumeProvisionFlashBanner(): string | null {
  if (typeof window === 'undefined') return null
  try {
    const v = sessionStorage.getItem(KEY)
    if (v) sessionStorage.removeItem(KEY)
    return v
  } catch {
    return null
  }
}
