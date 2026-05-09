/**
 * HandoverErrorPage — the SPA-rendered error surface for failed
 * cross-cluster auth handovers (TC-004 / 2026-05-07, refined
 * 2026-05-09 qa-loop iter-1).
 *
 * The catalyst-api `AuthHandover` Go handler 302-redirects browser
 * visits without a valid token to `/auth/handover-error?reason=<code>`.
 * This keeps the seamless-handover UX promise even when the operator
 * pastes a bare `/auth/handover` URL or follows a stale email link with
 * the token stripped — they see this friendly error surface instead of
 * raw `{"error":"missing token parameter"}` JSON.
 *
 * Programmatic callers (curl / monitors with `Accept: application/json`)
 * still get the legacy 401 JSON contract — `wantsHTML` in the Go
 * handler discriminates by Accept header.
 *
 * Each `reason` branch contains the literal token the routing matrix
 * asserts on (TC-004: 'missing' for missing_token). Keep these phrases
 * verbatim — they are the user-visible contract and are checked via
 * `document.body.innerText`, not URL or HTTP status.
 *
 * Extracted from `app/router.tsx` 2026-05-09 so it can be unit-tested
 * without booting the router or React-router context.
 */

export interface HandoverErrorPageProps {
  /** Optional override for tests; defaults to window.location.search. */
  search?: string
}

export function HandoverErrorPage({ search: searchOverride }: HandoverErrorPageProps = {}) {
  const search = new URLSearchParams(
    typeof searchOverride === 'string'
      ? searchOverride
      : typeof window !== 'undefined'
        ? window.location.search
        : '',
  )
  const reason = search.get('reason') ?? 'unknown'
  const message =
    reason === 'missing_token'
      ? 'The handover link is missing its token. Please open the link from your most recent email exactly as it was delivered, or request a fresh handover from the OpenOva mothership.'
      : reason === 'expired'
        ? 'This handover link has expired. Handover tokens are valid for a few minutes — please request a fresh one from the OpenOva mothership.'
        : reason === 'replayed'
          ? 'This handover link has already been used. Each token is single-use; request a fresh one from the OpenOva mothership.'
          : 'We could not complete the handover. Please request a fresh handover link from the OpenOva mothership.'
  return (
    <div
      className="mx-auto flex min-h-screen max-w-xl flex-col items-center justify-center gap-6 px-6 text-center"
      data-testid="handover-error-page"
    >
      <h1 className="text-2xl font-semibold text-[var(--color-text)]">
        Handover incomplete
      </h1>
      <p className="text-sm leading-relaxed text-[var(--color-text-dim)]">
        {message}
      </p>
      <a
        href="/dashboard"
        className="rounded-md border border-[var(--color-border)] bg-transparent px-4 py-2 text-sm text-[var(--color-text)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
      >
        Continue to console
      </a>
    </div>
  )
}
