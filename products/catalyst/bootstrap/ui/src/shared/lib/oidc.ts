/**
 * oidc.ts — Minimal PKCE/silent-auth OIDC helper for the Sovereign Console.
 *
 * Design constraints:
 *   • No external OIDC library — keeps the bundle lean and avoids a new
 *     npm dep that could drift from Keycloak's supported flows.
 *   • PKCE code_challenge_method=S256 — the spec-mandatory choice for
 *     SPAs where a client_secret cannot be kept secret.
 *   • The OIDC issuer / realm URL is NEVER hardcoded — it is derived
 *     from the Sovereign's FQDN at runtime per INVIOLABLE-PRINCIPLES #4.
 *   • Token storage: sessionStorage only.  localStorage would survive
 *     tab-close / private-browse, which conflicts with Keycloak's session
 *     lifecycle and creates credential-hygiene risks (CLAUDE.md §10).
 *
 * Keycloak OIDC endpoints for a Sovereign:
 *   Issuer:   https://auth.<sov-fqdn>/realms/sovereign
 *   Auth:     <issuer>/protocol/openid-connect/auth
 *   Token:    <issuer>/protocol/openid-connect/token
 *   JWKS:     <issuer>/protocol/openid-connect/certs
 *   Logout:   <issuer>/protocol/openid-connect/logout
 *
 * Client:     catalyst-ui (public client, no secret)
 * Scope:      openid profile email
 * Redirect:   https://console.<sov-fqdn>/auth/callback
 *
 * Related: GitHub issue #607
 */

// ── Constants ────────────────────────────────────────────────────────────────

const CLIENT_ID = 'catalyst-ui'
const SCOPE = 'openid profile email'
const SESSION_KEY_VERIFIER = 'oidc:pkce_verifier'
const SESSION_KEY_STATE = 'oidc:state'
const SESSION_KEY_ID_TOKEN = 'oidc:id_token'
const SESSION_KEY_ACCESS_TOKEN = 'oidc:access_token'
const SESSION_KEY_REFRESH_TOKEN = 'oidc:refresh_token'
const SESSION_KEY_EXPIRES_AT = 'oidc:expires_at'

// ── OIDC endpoint derivation ─────────────────────────────────────────────────

export interface OIDCEndpoints {
  issuer: string
  authEndpoint: string
  tokenEndpoint: string
  logoutEndpoint: string
}

/**
 * Build the Keycloak OIDC endpoint set for a given Sovereign FQDN.
 * Example:
 *   sovereignFQDN = 'otech23.omani.works'
 *   → issuer = 'https://auth.otech23.omani.works/realms/sovereign'
 */
export function buildOIDCEndpoints(sovereignFQDN: string): OIDCEndpoints {
  const issuer = `https://auth.${sovereignFQDN}/realms/sovereign`
  return {
    issuer,
    authEndpoint: `${issuer}/protocol/openid-connect/auth`,
    tokenEndpoint: `${issuer}/protocol/openid-connect/token`,
    logoutEndpoint: `${issuer}/protocol/openid-connect/logout`,
  }
}

/**
 * Build the OIDC redirect_uri for the current Sovereign.
 * Always points to /auth/callback on this origin.
 */
export function buildRedirectURI(): string {
  if (typeof window === 'undefined') return ''
  return `${window.location.origin}/auth/callback`
}

// ── PKCE helpers ─────────────────────────────────────────────────────────────

/** Generate a cryptographically random PKCE code verifier (43–128 chars). */
async function generateVerifier(): Promise<string> {
  const bytes = crypto.getRandomValues(new Uint8Array(48))
  return base64UrlEncode(bytes)
}

/** Compute the PKCE S256 code challenge from the verifier. */
async function generateChallenge(verifier: string): Promise<string> {
  const encoder = new TextEncoder()
  const data = encoder.encode(verifier)
  const digest = await crypto.subtle.digest('SHA-256', data)
  return base64UrlEncode(new Uint8Array(digest))
}

function base64UrlEncode(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '')
}

/** Generate a random state nonce. */
function generateState(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return base64UrlEncode(bytes)
}

// ── Token storage ─────────────────────────────────────────────────────────────

export interface TokenSet {
  idToken: string
  accessToken: string
  refreshToken: string | null
  expiresAt: number // Unix timestamp ms
}

export function saveTokens(tokens: TokenSet): void {
  sessionStorage.setItem(SESSION_KEY_ID_TOKEN, tokens.idToken)
  sessionStorage.setItem(SESSION_KEY_ACCESS_TOKEN, tokens.accessToken)
  if (tokens.refreshToken) {
    sessionStorage.setItem(SESSION_KEY_REFRESH_TOKEN, tokens.refreshToken)
  }
  sessionStorage.setItem(SESSION_KEY_EXPIRES_AT, String(tokens.expiresAt))
}

export function loadTokens(): TokenSet | null {
  const idToken = sessionStorage.getItem(SESSION_KEY_ID_TOKEN)
  const accessToken = sessionStorage.getItem(SESSION_KEY_ACCESS_TOKEN)
  const expiresAtStr = sessionStorage.getItem(SESSION_KEY_EXPIRES_AT)
  if (!idToken || !accessToken || !expiresAtStr) return null
  return {
    idToken,
    accessToken,
    refreshToken: sessionStorage.getItem(SESSION_KEY_REFRESH_TOKEN),
    expiresAt: Number(expiresAtStr),
  }
}

export function clearTokens(): void {
  sessionStorage.removeItem(SESSION_KEY_ID_TOKEN)
  sessionStorage.removeItem(SESSION_KEY_ACCESS_TOKEN)
  sessionStorage.removeItem(SESSION_KEY_REFRESH_TOKEN)
  sessionStorage.removeItem(SESSION_KEY_EXPIRES_AT)
}

export function isTokenExpired(tokens: TokenSet): boolean {
  // Consider expired if less than 60s of validity remains.
  return Date.now() >= tokens.expiresAt - 60_000
}

// ── JWT parsing (no signature verification — trust the issuer TLS) ───────────

export interface JWTClaims {
  sub?: string
  email?: string
  name?: string
  preferred_username?: string
  given_name?: string
  family_name?: string
  /** Keycloak required actions list. */
  requiredActions?: string[]
  exp?: number
  iat?: number
  [key: string]: unknown
}

/**
 * Decode the payload of a JWT without verifying the signature.
 *
 * Security note: this is intentionally non-verifying.  The SPA runs in
 * a browser; the token arrives over TLS from the Keycloak issuer we
 * initiated the PKCE flow with.  Full signature verification (JWK fetch,
 * RS256 verify) is performed by the catalyst-api backend on every
 * protected API call — the browser does not need to re-verify for UI
 * display purposes.
 */
export function parseJWTClaims(jwt: string): JWTClaims {
  try {
    const parts = jwt.split('.')
    if (parts.length !== 3) return {}
    const payload = parts[1]
    // Base64URL → Base64 → decode
    const padded = payload.replace(/-/g, '+').replace(/_/g, '/').padEnd(
      payload.length + ((4 - (payload.length % 4)) % 4),
      '=',
    )
    return JSON.parse(atob(padded)) as JWTClaims
  } catch {
    return {}
  }
}

// ── Authorization code flow (PKCE) ───────────────────────────────────────────

export interface InitiateLoginOptions {
  /**
   * OpenID Connect `prompt` parameter (OIDC Core §3.1.2.1).
   *
   * Pass `'none'` for a SILENT re-authentication attempt (#3374 row 29):
   * Keycloak authenticates the visitor against an EXISTING SSO session
   * without rendering any UI, and — when no reusable session exists —
   * returns `error=login_required` to the redirect_uri INSTEAD of showing
   * the interactive catalyst-pin PIN form. Omit for an ordinary
   * interactive authorization request.
   */
  prompt?: 'none'
}

/**
 * Build the Keycloak authorization-endpoint URL for the PKCE code flow.
 *
 * Pure + exported so the parameter set — including the silent
 * `prompt=none` variant (#3374 row 29) — is unit-testable without
 * navigating the browser away.
 */
export function buildAuthorizeURL(
  sovereignFQDN: string,
  args: {
    challenge: string
    state: string
    redirectURI: string
    prompt?: 'none'
  },
): string {
  const { authEndpoint } = buildOIDCEndpoints(sovereignFQDN)
  const params = new URLSearchParams({
    response_type: 'code',
    client_id: CLIENT_ID,
    redirect_uri: args.redirectURI,
    scope: SCOPE,
    state: args.state,
    code_challenge: args.challenge,
    code_challenge_method: 'S256',
  })
  // A silent re-auth request must carry `prompt=none` so Keycloak returns
  // login_required rather than rendering the interactive PIN form.
  if (args.prompt === 'none') params.set('prompt', 'none')
  return `${authEndpoint}?${params.toString()}`
}

/**
 * Initiate the PKCE authorization code flow.
 *
 * Stores the PKCE verifier + state nonce in sessionStorage, then
 * hard-navigates the browser to the Keycloak authorization endpoint.
 * Returns void — the page will unload.
 *
 * Pass `{ prompt: 'none' }` for a silent re-authentication (#3374 row 29):
 * the request carries `prompt=none`, so Keycloak either issues a code
 * against a live SSO session (→ /auth/callback?code, no UI) or bounces back
 * with `?error=login_required` (→ AuthCallbackPage falls back to the PIN
 * form), never showing an interactive login during the silent attempt.
 */
export async function initiateLogin(
  sovereignFQDN: string,
  opts: InitiateLoginOptions = {},
): Promise<void> {
  const verifier = await generateVerifier()
  const challenge = await generateChallenge(verifier)
  const state = generateState()
  const redirectURI = buildRedirectURI()

  sessionStorage.setItem(SESSION_KEY_VERIFIER, verifier)
  sessionStorage.setItem(SESSION_KEY_STATE, state)

  window.location.replace(
    buildAuthorizeURL(sovereignFQDN, {
      challenge,
      state,
      redirectURI,
      prompt: opts.prompt,
    }),
  )
}

/**
 * OIDC error codes a `prompt=none` silent-auth attempt returns to the
 * redirect_uri when it cannot complete without user interaction (OpenID
 * Connect Core §3.1.2.6). These are EXPECTED "no reusable session"
 * outcomes — NOT failures: the caller falls back to the interactive PIN
 * login instead of surfacing an error screen.
 */
export const SILENT_AUTH_RECOVERABLE_ERRORS = [
  'login_required',
  'interaction_required',
  'consent_required',
  'account_selection_required',
] as const

/**
 * Inspect an OIDC callback query-string. Returns the recoverable
 * silent-auth error code (login_required / interaction_required / …) when
 * the callback carries such an `error` and NO authorization `code`;
 * otherwise null.
 *
 * AuthCallbackPage uses this to distinguish a silent-SSO miss — where the
 * correct response is a clean fall-back to the PIN form (#3374 row 29) —
 * from a genuine token-exchange failure (which still surfaces an error).
 */
export function parseSilentAuthError(params: URLSearchParams): string | null {
  if (params.get('code')) return null
  const err = params.get('error')
  if (!err) return null
  return (SILENT_AUTH_RECOVERABLE_ERRORS as readonly string[]).includes(err)
    ? err
    : null
}

/**
 * Exchange the authorization code for tokens.
 *
 * Called by the `/auth/callback` route after Keycloak redirects back.
 * Validates the state nonce, exchanges the code, and persists the
 * resulting token set.
 *
 * @throws Error if state mismatch or token exchange fails.
 */
export async function handleCallback(
  sovereignFQDN: string,
  params: URLSearchParams,
): Promise<TokenSet> {
  const code = params.get('code')
  const returnedState = params.get('state')
  const storedState = sessionStorage.getItem(SESSION_KEY_STATE)
  const verifier = sessionStorage.getItem(SESSION_KEY_VERIFIER)

  if (!code) throw new Error('oidc: no authorization code in callback')
  if (returnedState !== storedState) {
    throw new Error('oidc: state mismatch — possible CSRF')
  }
  if (!verifier) throw new Error('oidc: no PKCE verifier in sessionStorage')

  // Clean up nonces
  sessionStorage.removeItem(SESSION_KEY_STATE)
  sessionStorage.removeItem(SESSION_KEY_VERIFIER)

  const { tokenEndpoint } = buildOIDCEndpoints(sovereignFQDN)
  const redirectURI = buildRedirectURI()

  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: CLIENT_ID,
    redirect_uri: redirectURI,
    code,
    code_verifier: verifier,
  })

  const resp = await fetch(tokenEndpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: body.toString(),
  })

  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`oidc: token exchange failed (${resp.status}): ${text}`)
  }

  const data = (await resp.json()) as {
    id_token: string
    access_token: string
    refresh_token?: string
    expires_in: number
  }

  const tokens: TokenSet = {
    idToken: data.id_token,
    accessToken: data.access_token,
    refreshToken: data.refresh_token ?? null,
    expiresAt: Date.now() + data.expires_in * 1000,
  }

  saveTokens(tokens)
  return tokens
}

/**
 * Attempt a silent token refresh using the stored refresh_token.
 *
 * Returns the new TokenSet on success, null if the refresh token is
 * absent or expired (caller should initiate a new login).
 */
export async function silentRefresh(
  sovereignFQDN: string,
): Promise<TokenSet | null> {
  const existing = loadTokens()
  if (!existing?.refreshToken) return null

  const { tokenEndpoint } = buildOIDCEndpoints(sovereignFQDN)

  try {
    const body = new URLSearchParams({
      grant_type: 'refresh_token',
      client_id: CLIENT_ID,
      refresh_token: existing.refreshToken,
    })

    const resp = await fetch(tokenEndpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString(),
    })

    if (!resp.ok) {
      clearTokens()
      return null
    }

    const data = (await resp.json()) as {
      id_token?: string
      access_token: string
      refresh_token?: string
      expires_in: number
    }

    const tokens: TokenSet = {
      idToken: data.id_token ?? existing.idToken,
      accessToken: data.access_token,
      refreshToken: data.refresh_token ?? existing.refreshToken,
      expiresAt: Date.now() + data.expires_in * 1000,
    }

    saveTokens(tokens)
    return tokens
  } catch {
    clearTokens()
    return null
  }
}

/**
 * Initiate Keycloak logout — clears local tokens and redirects to the
 * Keycloak end-session endpoint.
 */
export function initiateLogout(sovereignFQDN: string): void {
  const { logoutEndpoint } = buildOIDCEndpoints(sovereignFQDN)
  const tokens = loadTokens()
  clearTokens()

  const params = new URLSearchParams({
    client_id: CLIENT_ID,
    post_logout_redirect_uri: `${window.location.origin}/`,
  })

  if (tokens?.idToken) {
    params.set('id_token_hint', tokens.idToken)
  }

  window.location.replace(`${logoutEndpoint}?${params.toString()}`)
}

// ── Required actions ─────────────────────────────────────────────────────────

/** Keycloak required-action codes we handle in the RequiredActionsModal. */
export const REQUIRED_ACTION_UPDATE_PASSWORD = 'UPDATE_PASSWORD'
export const REQUIRED_ACTION_CONFIGURE_PASSKEY = 'configure-passkey'
export const REQUIRED_ACTION_CONFIGURE_TOTP = 'CONFIGURE_TOTP'

/** Return the list of required actions from an id_token, or []. */
export function getRequiredActions(idToken: string): string[] {
  const claims = parseJWTClaims(idToken)
  const ra = claims['requiredActions'] ?? claims['required_actions']
  if (Array.isArray(ra)) return ra as string[]
  return []
}
