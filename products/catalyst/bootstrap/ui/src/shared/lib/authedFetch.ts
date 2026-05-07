/**
 * authedFetch — fetch wrapper that injects the OIDC access token as
 * Authorization: Bearer for catalyst-api calls on Sovereign mode.
 *
 * The chroot Sovereign Console SPA performs its own OIDC flow with
 * Keycloak and stores the access_token in sessionStorage (oidc:access_token).
 * The catalyst-api session middleware ALSO accepts Authorization: Bearer
 * (in addition to the catalyst_session cookie used by mother), so the
 * SPA can authenticate API calls by attaching the token from
 * sessionStorage.
 *
 * Mother (Catalyst-Zero) flow uses the catalyst_session cookie minted
 * by the PIN-verify endpoint; cookie carries through credentials:'include'
 * and no Authorization header is needed. authedFetch adds the header
 * when (and only when) sessionStorage has an OIDC access_token — so the
 * same code path works on both sides without an explicit mode check.
 */

import { loadTokens } from './oidc'

export type AuthedRequestInit = RequestInit & { skipAuth?: boolean }

/**
 * Fetch with credentials:'include' AND an Authorization: Bearer header
 * sourced from sessionStorage when available. Pass `skipAuth: true` to
 * suppress the Authorization header (e.g. for the OIDC token-exchange
 * call itself).
 */
export async function authedFetch(
  input: RequestInfo | URL,
  init: AuthedRequestInit = {},
): Promise<Response> {
  const { skipAuth, ...rest } = init
  const headers = new Headers(rest.headers || {})
  if (!skipAuth) {
    const tokens = loadTokens()
    if (tokens && tokens.accessToken && !headers.has('Authorization')) {
      headers.set('Authorization', `Bearer ${tokens.accessToken}`)
    }
  }
  const credentials: RequestCredentials = rest.credentials ?? 'include'
  return fetch(input, { ...rest, credentials, headers })
}

/**
 * installFetchAuthInterceptor — monkey-patches window.fetch so every
 * call to a same-origin /api/v1/ path automatically picks up the
 * Authorization: Bearer header from the OIDC sessionStorage token,
 * when one is present.
 *
 * This unblocks the chroot Sovereign Console where the SPA performs
 * its own PKCE OIDC flow (no server-minted catalyst_session cookie)
 * and dozens of fetch() callsites would otherwise need individual
 * authedFetch swaps. With the interceptor installed, existing code
 * paths Just Work — and on mother (where no OIDC token is ever in
 * sessionStorage) the interceptor is a transparent passthrough.
 *
 * Idempotent — running twice is a no-op (the marker symbol guards
 * against double-installation under hot-reload).
 */
const INSTALLED = Symbol.for('catalyst.fetchAuthInterceptor.installed')
type WithMarker = typeof globalThis & { [INSTALLED]?: true }

export function installFetchAuthInterceptor(): void {
  if (typeof window === 'undefined') return
  const g = globalThis as WithMarker
  if (g[INSTALLED]) return
  const origFetch = window.fetch.bind(window)
  window.fetch = async (input, init = {}): Promise<Response> => {
    let url = ''
    if (typeof input === 'string') url = input
    else if (input instanceof URL) url = input.toString()
    else if (input instanceof Request) url = input.url
    // Only attach to API calls; static assets, cross-origin URLs and
    // anything outside /api/v1 retain their original semantics.
    const isApiCall =
      url.includes('/api/v1/') &&
      (url.startsWith('/') ||
        url.startsWith(window.location.origin) ||
        url.startsWith('http') === false)
    if (!isApiCall) return origFetch(input, init)
    const headers = new Headers(init.headers || {})
    if (!headers.has('Authorization')) {
      const tokens = loadTokens()
      if (tokens && tokens.accessToken) {
        headers.set('Authorization', `Bearer ${tokens.accessToken}`)
      }
    }
    const credentials: RequestCredentials = init.credentials ?? 'include'
    return origFetch(input, { ...init, credentials, headers })
  }
  g[INSTALLED] = true
}
