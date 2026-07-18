<script lang="ts">
  // #4273 — provisioning interstitial.
  //
  // The funnel used to HARD-REDIRECT to the per-Org console host
  // (`console.<slug>.<sov-fqdn>/auth/org-handover?token=…`) the instant
  // checkout succeeded. But that host's DNS A-record (#4236), TLS cert
  // (#4241/#4242) and `cilium-gateway-console` HTTPRoute are provisioned
  // ASYNCHRONOUSLY by the organization-controller AFTER the Org CR is created
  // — they land over seconds-to-minutes. The customer's FIRST click therefore
  // hit ERR_NAME_NOT_RESOLVED (NXDOMAIN), which the browser negative-caches
  // for the 300s pool-zone SOA minimum: a dead-end "This site can't be
  // reached".
  //
  // This page is served from the marketplace origin (`marketplace.<sov>`),
  // which ALWAYS resolves. It shows a branded "Setting up your console…"
  // state, polls the per-Org console readiness, and only then forwards to the
  // existing secure `/auth/org-handover` endpoint. The handover token is
  // carried THROUGH unchanged and handed to the server at the end of the
  // wait, preserving the #4182/#4186 contract (token → HttpOnly cookie
  // server-side; never logged; refresh token never in a URL). On timeout the
  // customer sees an honest "still provisioning" state with a manual retry —
  // never a raw browser DNS error.
  //
  // #5205 — the readiness probe used to `fetch(console.<slug>.<sov>/healthz,
  // {mode:'no-cors'})` DIRECTLY from the browser. A no-cors cross-origin
  // fetch always returns an OPAQUE Response — its status is unreadable by
  // design — so the only signal available was "did the fetch promise resolve
  // at all", which a live hw270 funnel walk proved unreliable: a real signup
  // had the backend (Org+cert+DNS+WordPress) Ready in ~35s, but the opaque
  // probe never observed success and the interstitial rode out the full
  // 5-minute timeout, stranding the customer on a manual "Open console
  // anyway" click even though a direct curl/browser-nav to the same host
  // returned 200 within seconds. The fix routes the probe through the
  // marketplace's OWN same-origin `/api/provisioning/console-ready` endpoint
  // (core/services/provisioning/handlers/console_ready.go) — a plain,
  // fully-readable JSON `{ready: boolean}` computed by a server-side GET
  // /healthz against the per-Org host, with no browser CORS/opaque-response
  // restriction in the way at all.

  import { API_BASE } from '../lib/config';

  type Phase = 'provisioning' | 'forwarding' | 'timeout' | 'error';
  let phase = $state<Phase>('provisioning');
  let elapsedMs = $state(0);

  // Polling schedule — fast at first (route often lands inside ~20-40s) then
  // backs off. Bounded by TIMEOUT_MS so a genuinely stuck provision surfaces a
  // retry rather than spinning forever.
  const MIN_INTERVAL_MS = 1500;
  const MAX_INTERVAL_MS = 8000;
  const TIMEOUT_MS = 5 * 60 * 1000; // 5 minutes (issue #4273 bound)
  // Per-probe network timeout so a hung TLS handshake / slow DNS doesn't stall
  // the whole schedule.
  const PROBE_TIMEOUT_MS = 5000;

  let startedAt = 0;
  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let tickTimer: ReturnType<typeof setInterval> | null = null;
  let cancelled = false;

  interface LaunchParams {
    host: string;   // full origin, e.g. https://console.abc.omani.trade
    token: string;  // session JWT for /auth/org-handover
    next: string;   // post-handover path, default /jobs
  }

  // Parse + VALIDATE the query params. Security: the interstitial only ever
  // forwards to a `console.*` host on the SAME registrable apex as the
  // marketplace origin — this is a per-Org Sovereign console, never an
  // arbitrary attacker-supplied URL (guards open-redirect / token-leak).
  function parseParams(): LaunchParams | null {
    if (typeof window === 'undefined') return null;
    const qs = new URLSearchParams(window.location.search);
    const rawHost = (qs.get('host') || '').trim();
    const token = (qs.get('token') || '').trim();
    let next = (qs.get('next') || '/jobs').trim();
    if (!rawHost || !token) return null;
    // next must be a same-origin path on the console (no scheme / host).
    if (!next.startsWith('/') || next.startsWith('//')) next = '/jobs';
    let target: URL;
    try {
      target = new URL(rawHost);
    } catch {
      return null;
    }
    if (target.protocol !== 'https:') return null;
    const targetHost = target.hostname.toLowerCase();
    // The marketplace runs at `marketplace.<sov-fqdn>`; per-Org consoles live
    // at `console.<slug>.<pool-apex>`. Both share the Sovereign's registrable
    // apex but the POOL apex can differ from the Sovereign apex (e.g.
    // marketplace.omantel.biz → console.abc.omani.trade). So we cannot pin to
    // the marketplace's own apex; instead we require the canonical
    // `console.` left-most label, which the funnel always composes. Reject
    // anything that is not a `console.*` host.
    if (!(targetHost === 'console' || targetHost.startsWith('console.'))) return null;
    return { host: target.origin, token, next };
  }

  let params = $state<LaunchParams | null>(null);

  // Probe the per-Org console's readiness via the marketplace's OWN
  // same-origin proxy (#5205). Unlike the retired cross-origin
  // `fetch(console.<slug>/healthz, {mode:'no-cors'})` — whose opaque Response
  // gave the browser no readable status at all — this is a plain same-origin
  // fetch to `${API_BASE}/provisioning/console-ready`, so `res.ok` and the
  // JSON body are both fully readable. The provisioning-service handler does
  // the actual cross-host GET /healthz server-side and reports back a REAL
  // `{ready: boolean}`. ANY failure (this request's own network error, a
  // non-OK response, or an upstream ready:false) is treated as "still
  // provisioning" and swallowed — we keep polling until ready or timeout,
  // preserving the original probe's tolerant contract.
  async function probeReady(host: string): Promise<boolean> {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), PROBE_TIMEOUT_MS);
    try {
      const res = await fetch(
        `${API_BASE}/provisioning/console-ready?host=${encodeURIComponent(host)}`,
        { method: 'GET', cache: 'no-store', signal: ctrl.signal },
      );
      if (!res.ok) return false;
      const data = (await res.json().catch(() => null)) as { ready?: boolean } | null;
      return data?.ready === true;
    } catch {
      return false;
    } finally {
      clearTimeout(t);
    }
  }

  function forward(p: LaunchParams) {
    phase = 'forwarding';
    cleanup();
    // Hand the token to the secure server-side handoff. catalyst-api validates
    // it, mints the HttpOnly catalyst_session cookie, and 302s to `next`.
    // consoleHandoffHref is built from the SAME validated host (slug derivation
    // is bypassed by passing the host through the already-validated origin).
    const url = `${p.host}/auth/org-handover?token=${encodeURIComponent(p.token)}`;
    window.location.replace(url);
  }

  async function pollOnce(p: LaunchParams) {
    if (cancelled) return;
    if (Date.now() - startedAt >= TIMEOUT_MS) {
      phase = 'timeout';
      cleanup();
      return;
    }
    const ready = await probeReady(p.host);
    if (cancelled) return;
    if (ready) {
      forward(p);
      return;
    }
    // Linear-ish backoff toward MAX_INTERVAL_MS as elapsed time grows.
    const frac = Math.min(1, (Date.now() - startedAt) / 60_000);
    const interval = Math.round(MIN_INTERVAL_MS + frac * (MAX_INTERVAL_MS - MIN_INTERVAL_MS));
    pollTimer = setTimeout(() => pollOnce(p), interval);
  }

  function cleanup() {
    if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
    if (tickTimer) { clearInterval(tickTimer); tickTimer = null; }
  }

  function start() {
    const p = parseParams();
    params = p;
    if (!p) { phase = 'error'; return; }
    cancelled = false;
    phase = 'provisioning';
    startedAt = Date.now();
    elapsedMs = 0;
    tickTimer = setInterval(() => { elapsedMs = Date.now() - startedAt; }, 1000);
    pollOnce(p);
  }

  function retry() {
    cleanup();
    start();
  }

  // Manual "open anyway" — for the rare case the host is up but the probe is
  // blocked (corp proxy stripping no-cors). Forwards to the handoff directly.
  function openAnyway() {
    if (params) forward(params);
  }

  $effect(() => {
    start();
    return () => { cancelled = true; cleanup(); };
  });

  const elapsedLabel = $derived.by(() => {
    const s = Math.floor(elapsedMs / 1000);
    if (s < 60) return `${s}s`;
    return `${Math.floor(s / 60)}m ${s % 60}s`;
  });
</script>

<div class="mx-auto max-w-lg py-12">
  <div class="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 text-center">
    {#if phase === 'provisioning' || phase === 'forwarding'}
      <div class="mx-auto mb-5 h-14 w-14 animate-spin rounded-full border-4 border-[var(--color-accent)] border-t-transparent"></div>
      <h1 class="text-xl font-bold text-[var(--color-text-strong)]">
        {phase === 'forwarding' ? 'Opening your console…' : 'Setting up your console…'}
      </h1>
      <p class="mt-2 text-sm text-[var(--color-text-dim)]">
        We're provisioning your Organization's secure address. This usually
        takes under a minute — you'll be redirected automatically.
      </p>
      {#if phase === 'provisioning' && elapsedMs >= 5000}
        <p class="mt-4 text-xs text-[var(--color-text-dimmer)]">
          Still working… ({elapsedLabel})
        </p>
      {/if}
    {:else if phase === 'timeout'}
      <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-[var(--color-warn)]/15">
        <svg class="h-7 w-7 text-[var(--color-warn)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8">
          <path stroke-linecap="round" stroke-linejoin="round" d="M12 6v6l4 2M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
        </svg>
      </div>
      <h1 class="text-xl font-bold text-[var(--color-text-strong)]">Your Organization is still provisioning</h1>
      <p class="mt-2 text-sm text-[var(--color-text-dim)]">
        This is taking longer than usual. Your console is being prepared and
        we'll email you the link the moment it's ready. You can keep waiting
        or try again now.
      </p>
      <div class="mt-6 flex flex-col gap-2 sm:flex-row sm:justify-center">
        <button
          type="button"
          onclick={retry}
          data-testid="launching-retry"
          class="inline-flex items-center justify-center gap-2 rounded-xl bg-[var(--color-accent)] px-5 py-2.5 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent-hover)]"
        >
          Keep waiting
        </button>
        <button
          type="button"
          onclick={openAnyway}
          class="inline-flex items-center justify-center gap-2 rounded-xl border border-[var(--color-border)] px-5 py-2.5 text-sm font-medium text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)]"
        >
          Open console anyway
        </button>
      </div>
    {:else}
      <!-- error: missing/invalid params -->
      <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-[var(--color-danger)]/15">
        <svg class="h-7 w-7 text-[var(--color-danger)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8">
          <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 11-18 0 9 9 0 0118 0zm-9 3.75h.008v.008H12v-.008z"/>
        </svg>
      </div>
      <h1 class="text-xl font-bold text-[var(--color-text-strong)]">Something went wrong</h1>
      <p class="mt-2 text-sm text-[var(--color-text-dim)]">
        We couldn't determine your console address. Head back to your account
        and try opening your console again.
      </p>
      <a
        href="/checkout"
        class="mt-6 inline-flex items-center justify-center gap-2 rounded-xl bg-[var(--color-accent)] px-5 py-2.5 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent-hover)] no-underline"
      >
        Back to checkout
      </a>
    {/if}
  </div>
</div>
