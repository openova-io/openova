/**
 * lib/sandbox.api.ts — typed REST client for the Sovereign-side Sandbox
 * surfaces (Wave 3 — UI scaffold).
 *
 * Sandbox = per-Org agent-coding workspace. Each session runs a chosen
 * agent CLI (`claude`, `cursor-agent`, `aider`, `qwen-code`, `opencode`,
 * `little-coder`) in a pod inside the user's vcluster; the pod's
 * `pty-server` shim pipes ANSI stdout to the browser's xterm.js over a
 * WebSocket. See `products/sandbox/docs/architecture.md` §1.
 *
 * Wire paths (Wave 1b backend stubs):
 *
 *   browser ──/api/v1/sandbox/sessions──▶ catalyst-api ──▶ Sandbox CRD list
 *   browser ──/api/v1/sandbox/sessions  POST──▶ catalyst-api ──▶ Sandbox CR
 *   browser ──/api/v1/sandbox/byos/claude-code/status──▶ catalyst-api
 *
 * All endpoints tolerate 404 / 5xx so the page renders its target-state
 * shape on first paint (per docs/INVIOLABLE-PRINCIPLES.md #1) — the
 * landing flips its "API pending" pill when the backend isn't wired.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/**
 * Canonical agent catalogue. The id is the binary name the pty-server
 * spawns; the label / blurb drive the Landing's card grid. Adding a new
 * supported agent is a one-line append here — no other site touches the
 * list (per INVIOLABLE-PRINCIPLES.md #4 — never hardcode at call sites).
 */
export interface SandboxAgent {
  id:
    | 'aider'
    | 'claude-code'
    | 'cursor-agent'
    | 'little-coder'
    | 'opencode'
    | 'qwen-code'
  label: string
  blurb: string
}

export const SANDBOX_AGENTS: readonly SandboxAgent[] = [
  {
    id: 'aider',
    label: 'Aider',
    blurb: 'AI pair-programming in your terminal. Git-native edits, multi-file refactors.',
  },
  {
    id: 'claude-code',
    label: 'Claude Code',
    blurb: 'Anthropic’s official agent CLI. Connect Claude Max to use your subscription.',
  },
  {
    id: 'cursor-agent',
    label: 'Cursor Agent',
    blurb: 'Cursor’s background agent CLI for autonomous code changes.',
  },
  {
    id: 'little-coder',
    label: 'Little Coder',
    blurb: 'Lightweight scoped agent for small, focused edits.',
  },
  {
    id: 'opencode',
    label: 'OpenCode',
    blurb: 'Open-source agent runtime with a swappable model backend.',
  },
  {
    id: 'qwen-code',
    label: 'Qwen Code',
    blurb: 'Alibaba Qwen coding agent. OSS-friendly model defaults.',
  },
] as const

/* ── Sessions ────────────────────────────────────────────────────── */

export type SandboxStatus = 'pending' | 'running' | 'stopped' | 'failed' | 'unknown'

export interface Sandbox {
  /** Stable Sandbox CR name (sandbox-<slug>). */
  id: string
  /** Operator-facing label (defaults to id when empty). */
  name: string
  /** Agent binary id chosen at create time. */
  agent: SandboxAgent['id']
  status: SandboxStatus
  /** ISO-8601 creation timestamp. */
  createdAt: string
  /** Repo path mounted at /repo (e.g. 'org/site'); empty when none. */
  repo: string
}

export interface SandboxesResponse {
  /** True when the BE returned a non-2xx — the page still renders
   *  its target-state shape with the "API pending" pill. */
  pendingApi: boolean
  sandboxes: Sandbox[]
}

const EMPTY_SANDBOXES: SandboxesResponse = { pendingApi: true, sandboxes: [] }

const SANDBOX_BASE = `${API_BASE}/v1/sandbox`

/**
 * getSandboxes — fetch the per-Org sandbox roster for the Landing's
 * "Recent sessions" rail.
 *
 * Returns `{ pendingApi: true, sandboxes: [] }` on 404 / 5xx / network
 * error so the Landing renders the agent grid + empty-state rail on
 * first paint without crashing the surface (per INVIOLABLE-PRINCIPLES.md
 * #1 — waterfall, target-state shape first time).
 */
export async function getSandboxes(): Promise<SandboxesResponse> {
  let res: Response
  try {
    res = await authedFetch(`${SANDBOX_BASE}/sessions`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return EMPTY_SANDBOXES
  }
  if (!res.ok) {
    return EMPTY_SANDBOXES
  }
  try {
    const body = (await res.json()) as { sandboxes?: unknown } | null
    if (!body || typeof body !== 'object' || !Array.isArray(body.sandboxes)) {
      return { pendingApi: false, sandboxes: [] }
    }
    const sandboxes: Sandbox[] = body.sandboxes
      .map((raw): Sandbox | null => {
        if (!raw || typeof raw !== 'object') return null
        const r = raw as Record<string, unknown>
        const id = typeof r.id === 'string' ? r.id : ''
        if (id === '') return null
        return {
          id,
          name: typeof r.name === 'string' && r.name !== '' ? r.name : id,
          agent: normalizeAgent(r.agent),
          status: normalizeStatus(r.status),
          createdAt: typeof r.createdAt === 'string' ? r.createdAt : '',
          repo: typeof r.repo === 'string' ? r.repo : '',
        }
      })
      .filter((s): s is Sandbox => s !== null)
    return { pendingApi: false, sandboxes }
  } catch {
    return EMPTY_SANDBOXES
  }
}

export interface CreateSandboxRequest {
  /** Agent binary id (must match a SANDBOX_AGENTS row). */
  agent: SandboxAgent['id']
  /** Optional human label; defaults server-side to `<agent>-<short-id>`. */
  name?: string
  /** Optional repo to clone into /repo (e.g. 'org/site'). */
  repo?: string
}

/**
 * createSandbox — POST /v1/sandbox/sessions. Returns the freshly
 * provisioned Sandbox row on success. Surfaces the BE's `detail` /
 * `error` field on non-2xx so the Landing's create-modal can show the
 * actual server-side message.
 */
export async function createSandbox(req: CreateSandboxRequest): Promise<Sandbox> {
  const res = await authedFetch(`${SANDBOX_BASE}/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // non-JSON body — keep the status-line message
    }
    throw new Error(`create sandbox: ${detail}`)
  }
  const raw = (await res.json()) as Record<string, unknown>
  return {
    id: typeof raw.id === 'string' ? raw.id : '',
    name: typeof raw.name === 'string' ? raw.name : '',
    agent: normalizeAgent(raw.agent),
    status: normalizeStatus(raw.status),
    createdAt: typeof raw.createdAt === 'string' ? raw.createdAt : '',
    repo: typeof raw.repo === 'string' ? raw.repo : '',
  }
}

/* ── BYOS (Bring-Your-Own-Subscription) — Claude Max OAuth ─────────
 *
 * Wave 1b backend stubs:
 *   GET    /v1/sandbox/byos/claude-code/status
 *   POST   /v1/sandbox/byos/claude-code/connect       → returns OAuth URL
 *   DELETE /v1/sandbox/byos/claude-code/disconnect
 *
 * The Connect button on SandboxSettings opens the OAuth URL in a new tab;
 * Anthropic redirects to /v1/sandbox/byos/claude-code/callback which the
 * BE persists. Status flips to `connected` on the next status poll.
 */

export type ByosStatus = 'connected' | 'disconnected' | 'pending' | 'error'

export interface ByosClaudeCodeStatus {
  /** True when the BE returned a non-2xx — the page still renders
   *  but flags the "API pending" pill. */
  pendingApi: boolean
  status: ByosStatus
  /** Account label shown next to the Disconnect button (e.g. user's
   *  Anthropic email). Empty when disconnected. */
  accountLabel: string
  /** ISO-8601 timestamp of the last successful token refresh. */
  connectedAt: string
}

const DISCONNECTED_BYOS: ByosClaudeCodeStatus = {
  pendingApi: true,
  status: 'disconnected',
  accountLabel: '',
  connectedAt: '',
}

/**
 * getByosStatus — fetch the BYOS Claude Code connection status for
 * the SandboxSettings page. Tolerates 404 / 5xx / network error by
 * returning `{ pendingApi: true, status: 'disconnected', ... }` so the
 * page renders its full Connect / Disconnect chrome on first paint.
 */
export async function getByosStatus(): Promise<ByosClaudeCodeStatus> {
  let res: Response
  try {
    res = await authedFetch(`${SANDBOX_BASE}/byos/claude-code/status`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return DISCONNECTED_BYOS
  }
  if (!res.ok) {
    return DISCONNECTED_BYOS
  }
  try {
    const body = (await res.json()) as Partial<ByosClaudeCodeStatus> | null
    if (!body || typeof body !== 'object') return DISCONNECTED_BYOS
    return {
      pendingApi: false,
      status: normalizeByosStatus(body.status),
      accountLabel: typeof body.accountLabel === 'string' ? body.accountLabel : '',
      connectedAt: typeof body.connectedAt === 'string' ? body.connectedAt : '',
    }
  } catch {
    return DISCONNECTED_BYOS
  }
}

/**
 * connectByosClaudeCode — POST /v1/sandbox/byos/claude-code/connect.
 * Returns the OAuth authorization URL the SandboxSettings page opens in
 * a new tab. The BE persists the returned tokens via the OAuth callback.
 */
export async function connectByosClaudeCode(): Promise<{ authorizeUrl: string }> {
  const res = await authedFetch(`${SANDBOX_BASE}/byos/claude-code/connect`, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // ignore
    }
    throw new Error(`connect claude-code: ${detail}`)
  }
  const body = (await res.json()) as { authorizeUrl?: string }
  return { authorizeUrl: typeof body.authorizeUrl === 'string' ? body.authorizeUrl : '' }
}

/**
 * BYOS config shape — the pre-flight the SandboxSettings card runs
 * before showing the Connect button. When `clientIdConfigured` is false
 * the chart's `SANDBOX_BYOS_CLAUDE_CODE_CLIENT_ID` is still the
 * `PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION` sentinel (PR #1619) and
 * the FE MUST render the button as DISABLED with the amber pendingApi
 * pill + a tooltip pointing the operator at the founder action.
 */
export interface ByosClaudeCodeConfig {
  /** True when the FE was unable to reach `/config` — treated as
   *  pending operator setup so the UI never offers a button that would
   *  fail at Anthropic. */
  pendingApi: boolean
  clientIdConfigured: boolean
  /** Optional informational hint; only populated when configured. */
  oauthAuthorizeURL: string
}

const PENDING_BYOS_CONFIG: ByosClaudeCodeConfig = {
  pendingApi: true,
  clientIdConfigured: false,
  oauthAuthorizeURL: '',
}

/**
 * getByosClaudeCodeConfig — GET /v1/sandbox/byos/claude-code/config.
 *
 * The SandboxSettings card calls this before rendering the Connect
 * button. On 404 / 5xx / network error returns
 * `{pendingApi: true, clientIdConfigured: false, ...}` so the UI defaults
 * to the safer "disabled with pending tooltip" state — never the
 * "enabled but the OAuth URL will 400" state.
 */
export async function getByosClaudeCodeConfig(): Promise<ByosClaudeCodeConfig> {
  let res: Response
  try {
    res = await authedFetch(`${SANDBOX_BASE}/byos/claude-code/config`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return PENDING_BYOS_CONFIG
  }
  if (!res.ok) {
    return PENDING_BYOS_CONFIG
  }
  try {
    const body = (await res.json()) as Partial<ByosClaudeCodeConfig> | null
    if (!body || typeof body !== 'object') return PENDING_BYOS_CONFIG
    return {
      pendingApi: false,
      clientIdConfigured: body.clientIdConfigured === true,
      oauthAuthorizeURL:
        typeof body.oauthAuthorizeURL === 'string' ? body.oauthAuthorizeURL : '',
    }
  } catch {
    return PENDING_BYOS_CONFIG
  }
}

/**
 * disconnectByosClaudeCode — DELETE /v1/sandbox/byos/claude-code/
 * disconnect. Drops the stored OAuth tokens. Surfaces the BE's `detail` /
 * `error` field on non-2xx.
 */
export async function disconnectByosClaudeCode(): Promise<void> {
  const res = await authedFetch(`${SANDBOX_BASE}/byos/claude-code/disconnect`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok && res.status !== 204) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // ignore
    }
    throw new Error(`disconnect claude-code: ${detail}`)
  }
}

/* ── Normalisers ─────────────────────────────────────────────────── */

function normalizeAgent(raw: unknown): SandboxAgent['id'] {
  if (typeof raw !== 'string') return 'claude-code'
  const hit = SANDBOX_AGENTS.find((a) => a.id === raw)
  return hit ? hit.id : 'claude-code'
}

function normalizeStatus(raw: unknown): SandboxStatus {
  if (typeof raw !== 'string') return 'unknown'
  const s = raw.toLowerCase()
  if (s === 'pending' || s === 'running' || s === 'stopped' || s === 'failed') return s
  return 'unknown'
}

function normalizeByosStatus(raw: unknown): ByosStatus {
  if (typeof raw !== 'string') return 'disconnected'
  const s = raw.toLowerCase()
  if (s === 'connected' || s === 'disconnected' || s === 'pending' || s === 'error') return s
  return 'disconnected'
}
