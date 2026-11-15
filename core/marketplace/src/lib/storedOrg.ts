// storedOrg — the Organization a browser REMEMBERS, and when the storefront
// may still trust it (#7922).
//
// ── The defect ───────────────────────────────────────────────────────
//
// The storefront keeps three localStorage keys about "your Organization":
//
//   org-active-org           the Organization id (tenant_id)
//   org-active-org-slug      its slug
//   org-active-console-host  its console host, e.g. console.acme.omani.rest
//
// They are written when an Organization is created or listed and read on
// every later page load. Nothing on the storefront ever checked that the
// Organization they name still exists. On 16 Nov 2026 the test Sovereign
// hw307 was wiped and installed again under the same marketplace host, so a
// browser that had used the previous installation still held the old
// Organization's console host. After signing in, the readable session hint
// (`./sessionHint`) sent that browser to `console.<old-org>.omani.rest` — the
// pool domain could only have come from the stored key.
//
// ── The rule ─────────────────────────────────────────────────────────
//
// A stored Organization is used only after the server has been asked:
//
//   server knows it                -> today's behaviour (redirect, keep)
//   server says it does not exist  -> drop the keys, stay on the storefront
//   could not ask (error, offline) -> keep the keys, stay on the storefront
//
// Two existing endpoints answer, depending on what the visitor holds:
//
//   GET /api/tenant/orgs                 Bearer `org-token`; 200 = the
//                                        signed-in user's Organizations.
//   GET /api/tenant/check-slug/<slug>    public; 200 {"available":bool}.
//                                        `available:true` means no
//                                        Organization on this Sovereign has
//                                        the slug (core/services/tenant
//                                        CheckSlugAvailable counts records).
//
// The second is the only one a visitor without a storefront token can use:
// the Organization gateway reads the Bearer header only, never the console's
// session cookie (see `./redeemDestination`).
//
// This module is the SPEC. `Layout.astro` carries an inline copy (it must run
// before the bundle) and `layoutReturningVisitor.test.ts` runs that copy and
// requires it to agree with this one.

/** The localStorage keys that together name the remembered Organization. */
export const STORED_ORG_KEYS = [
  'org-active-org',
  'org-active-org-slug',
  'org-active-console-host',
] as const;

/** A legal lowercase DNS label — the only shape an Organization slug takes. */
const DNS_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export interface StoredOrg {
  /** `org-active-org`. */
  id?: string;
  /** `org-active-org-slug`. */
  slug?: string;
  /** `org-active-console-host`. */
  consoleHost?: string;
}

/**
 * The slug a stored console host names: the label after `console.`, when the
 * host is `console.<label>.<parent>` and the label is a DNS label; else ''.
 */
export function consoleHostSlug(host: string | null | undefined): string {
  const h = (host || '').trim().toLowerCase();
  if (!h.startsWith('console.')) return '';
  const rest = h.slice('console.'.length);
  const dot = rest.indexOf('.');
  if (dot <= 0 || dot === rest.length - 1) return '';
  const label = rest.slice(0, dot);
  return DNS_LABEL.test(label) ? label : '';
}

/**
 * What the server answered. `status` 0 means no answer at all (network error,
 * the fetch rejected). `body` is the parsed JSON, or null.
 */
export interface OrgCheckAnswer {
  endpoint: 'my-orgs' | 'check-slug';
  status: number;
  body: unknown;
}

export type StoredOrgVerdict =
  /** Nothing is stored: nothing to judge. */
  | 'none'
  /** The server knows the stored Organization. */
  | 'known'
  /** The server answered that the stored Organization does not exist. */
  | 'gone'
  /** No usable answer. The Organization is neither trusted nor dropped. */
  | 'unverified';

export interface StoredOrgDecision {
  verdict: StoredOrgVerdict;
  /** Remove every `STORED_ORG_KEYS` entry. Only on 'gone'. */
  clearStored: boolean;
  /** Where to send the visitor: `target` on 'known', otherwise '' (stay). */
  redirectTo: string;
}

interface ListedOrg {
  id?: unknown;
  slug?: unknown;
  status?: unknown;
  console_host?: unknown;
}

function lower(v: unknown): string {
  return typeof v === 'string' ? v.trim().toLowerCase() : '';
}

function isOk(status: number): boolean {
  return status >= 200 && status < 300;
}

/** The stored Organization, normalised; empty strings for absent keys. */
function normalise(stored: StoredOrg): { id: string; slug: string; host: string } {
  return {
    id: (stored.id || '').trim(),
    slug: lower(stored.slug),
    host: lower(stored.consoleHost),
  };
}

/**
 * Judge a stored Organization against the server's answer.
 *
 *   my-orgs, 2xx, an array   -> 'known' if a non-deleted entry matches the
 *                               stored id, slug, or console host; else 'gone'
 *   check-slug, 2xx, boolean -> available:false 'known', available:true 'gone'
 *   anything else            -> 'unverified' (401, 5xx, 404, no answer, a
 *                               body of the wrong shape)
 */
export function judgeStoredOrg(stored: StoredOrg, answer: OrgCheckAnswer): StoredOrgVerdict {
  const s = normalise(stored);
  if (!s.id && !s.slug && !s.host) return 'none';
  if (!isOk(answer.status)) return 'unverified';

  if (answer.endpoint === 'check-slug') {
    const available = (answer.body as { available?: unknown } | null)?.available;
    if (typeof available !== 'boolean') return 'unverified';
    return available ? 'gone' : 'known';
  }

  if (!Array.isArray(answer.body)) return 'unverified';
  const listed = (answer.body as ListedOrg[]).filter((o) => o && o.status !== 'deleted');
  const hostSlug = consoleHostSlug(s.host);
  const known = listed.some(
    (o) =>
      (s.id !== '' && typeof o.id === 'string' && o.id === s.id) ||
      (s.slug !== '' && lower(o.slug) === s.slug) ||
      (hostSlug !== '' && lower(o.slug) === hostSlug) ||
      (s.host !== '' && lower(o.console_host) === s.host),
  );
  return known ? 'known' : 'gone';
}

/**
 * The whole decision: the verdict, whether to drop the stored keys, and
 * whether to go to `target` (the console the stored Organization names).
 */
export function decideStoredOrg(input: {
  stored: StoredOrg;
  answer: OrgCheckAnswer;
  target?: string;
}): StoredOrgDecision {
  const verdict = judgeStoredOrg(input.stored, input.answer);
  return {
    verdict,
    clearStored: verdict === 'gone',
    redirectTo: verdict === 'known' ? input.target || '' : '',
  };
}

// ── Browser helpers ──────────────────────────────────────────────────

type ReadableStorage = Pick<Storage, 'getItem'>;
type WritableStorage = Pick<Storage, 'removeItem'>;

/** Read the stored Organization. Never throws. */
export function readStoredOrg(storage: ReadableStorage): StoredOrg {
  const get = (k: string): string => {
    try {
      return storage.getItem(k) || '';
    } catch {
      return '';
    }
  };
  return {
    id: get('org-active-org'),
    slug: get('org-active-org-slug'),
    consoleHost: get('org-active-console-host'),
  };
}

/** Drop the stored Organization. Never throws. */
export function forgetStoredOrg(storage: WritableStorage): void {
  for (const k of STORED_ORG_KEYS) {
    try {
      storage.removeItem(k);
    } catch {
      // storage unavailable — nothing to drop
    }
  }
}

/** `GET /api/tenant/check-slug/<slug>`, as an `OrgCheckAnswer`. Never rejects. */
export async function checkOrgSlug(
  slug: string,
  fetchFn: (url: string) => Promise<Pick<Response, 'status' | 'json'>>,
  apiBase = '/api',
): Promise<OrgCheckAnswer> {
  try {
    const res = await fetchFn(`${apiBase}/tenant/check-slug/${encodeURIComponent(slug)}`);
    let body: unknown = null;
    try {
      body = await res.json();
    } catch {
      body = null;
    }
    return { endpoint: 'check-slug', status: res.status, body };
  } catch {
    return { endpoint: 'check-slug', status: 0, body: null };
  }
}
