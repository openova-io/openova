// Shared source-of-truth for the console's apps / app-detail / jobs pages.
//
// Before #64, each page polled /tenant/orgs/:id and /provisioning/jobs on its
// own cadence and held its own $state. That allowed a single tab to disagree
// with itself across routes — app-detail could render "installed" while the
// apps list still rendered "installing", the jobs page could run ahead of
// both, and uninstall could leave a perpetual spinner on whichever page
// missed the terminal tick. This module centralises the fetch, so every
// subscriber reads the same snapshot at the same time.
//
// Design (poll, not SSE):
//   No SSE endpoint exists in services/tenant yet. Adding one would require
//   a broker fanout + auth middleware rework to accept query-param tokens
//   for EventSource. Centralising the poll fixes the drift with bounded
//   scope; transport can be swapped later without touching consumers.
//
//   One AppStateStore per tenant. Mount triggers an initial hydrate
//   (/tenant/orgs/:id + /provisioning/jobs?tenant_id=...). After that:
//     - fast cadence (1.5s) while any app is mid-transition or any job is
//       pending/running — gives the "<2s" reactivity the acceptance asks for
//     - slow cadence (10s) otherwise — keeps the tab warm without thrashing
//   Writes (installApp / uninstallApp) call refreshNow() so the store
//   flips in the same tick the user clicks, without waiting for the next
//   poll tick.
//
//   BroadcastChannel fans updates across tabs of the same origin so a
//   second console tab reflects a transition without doing its own round-
//   trip. Each tab still polls on its own so a stale / hung tab can recover
//   without relying on a neighbour.

import {
  getMyOrgs,
  getJobs,
  type Org,
  type Job,
} from '../api';

export type StoreStatus = 'idle' | 'loading' | 'error';

type State = {
  tenantId: string;
  org: Org | null;
  jobs: Job[];
  status: StoreStatus;
  error: string | null;
  lastUpdated: number;
};

const FAST_INTERVAL = 1500; // ms — while any app is transitioning
const SLOW_INTERVAL = 10000; // ms — steady state
const BROADCAST_CHANNEL = 'sme-console-app-state';

// Shallow-compare the fields the UI actually renders so we only mutate
// the store when something observable changed. Prevents `activeOrg = o`
// reassignment in AppsPage from cascading into child $effects on every
// poll (issue #106 flicker).
function orgEqual(a: Org | null, b: Org | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  if (a.id !== b.id || a.status !== b.status) return false;
  const aa = a.apps ?? [];
  const bb = b.apps ?? [];
  if (aa.length !== bb.length) return false;
  for (let i = 0; i < aa.length; i++) if (aa[i] !== bb[i]) return false;
  const as = a.app_states ?? {};
  const bs = b.app_states ?? {};
  const ak = Object.keys(as);
  if (ak.length !== Object.keys(bs).length) return false;
  for (const k of ak) if (as[k] !== bs[k]) return false;
  return true;
}

function jobsEqual(a: Job[], b: Job[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    const x = a[i], y = b[i];
    if (x.id !== y.id || x.status !== y.status || x.progress !== y.progress) return false;
  }
  return true;
}

function hasActiveWork(org: Org | null, jobs: Job[]): boolean {
  if (org?.app_states) {
    for (const s of Object.values(org.app_states)) {
      if (s === 'installing' || s === 'uninstalling') return true;
    }
  }
  for (const j of jobs) {
    if (j.status === 'pending' || j.status === 'running') return true;
  }
  return false;
}

function bcSupported(): boolean {
  return typeof window !== 'undefined' && typeof BroadcastChannel !== 'undefined';
}

export class AppStateStore {
  // $state is mutable from outside via accessors so consumers subscribe by
  // reading fields directly. All rendering goes through $derived in the
  // calling component, so Svelte picks up every write here.
  state = $state<State>({
    tenantId: '',
    org: null,
    jobs: [],
    status: 'idle',
    error: null,
    lastUpdated: 0,
  });

  private pollTimer: ReturnType<typeof setTimeout> | null = null;
  private bc: BroadcastChannel | null = null;
  private subs = 0;
  private inFlight: Promise<void> | null = null;

  constructor(tenantId: string) {
    this.state.tenantId = tenantId;
    if (bcSupported()) {
      try {
        this.bc = new BroadcastChannel(BROADCAST_CHANNEL);
        this.bc.onmessage = (ev) => this.onBroadcast(ev.data);
      } catch {
        this.bc = null;
      }
    }
  }

  // Called by each component on mount. Starts polling on the first subscriber
  // and hydrates once. Returns a disposer that decrements the ref count and
  // stops polling when the last subscriber leaves.
  subscribe(): () => void {
    this.subs += 1;
    if (this.subs === 1) {
      this.state.status = 'loading';
      void this.refreshNow();
      this.schedule();
    }
    return () => {
      this.subs -= 1;
      if (this.subs <= 0) {
        this.subs = 0;
        if (this.pollTimer) {
          clearTimeout(this.pollTimer);
          this.pollTimer = null;
        }
      }
    };
  }

  // Force an immediate fetch. Used after install/uninstall so the UI flips
  // before the next poll tick.
  async refreshNow(): Promise<void> {
    if (this.inFlight) return this.inFlight;
    const p = this.runFetch();
    this.inFlight = p;
    try {
      await p;
    } finally {
      this.inFlight = null;
    }
  }

  private async runFetch(): Promise<void> {
    // Per-call failures must NOT overwrite cached state (issue #107):
    // previously a transient 429 / 500 on getJobs wiped the Jobs page to
    // empty and the UI flickered until refresh. We keep the previous
    // slice when a call throws and only update the fresh one.
    const orgsP = getMyOrgs().then(
      (v) => ({ ok: true as const, value: v }),
      (err) => ({ ok: false as const, err }),
    );
    const jobsP = getJobs(this.state.tenantId).then(
      (v) => ({ ok: true as const, value: v }),
      (err) => ({ ok: false as const, err }),
    );
    const [orgsR, jobsR] = await Promise.all([orgsP, jobsP]);

    let org: Org | null = this.state.org;
    if (orgsR.ok) {
      org = (orgsR.value || []).find((o) => o.id === this.state.tenantId) ?? null;
    }
    const jobs: Job[] = jobsR.ok ? (jobsR.value || []) : this.state.jobs;

    const anyFailed = !orgsR.ok || !jobsR.ok;
    this.applyUpdate(org, jobs, anyFailed ? (orgsR.ok ? (jobsR as any).err : (orgsR as any).err) : null);
  }

  private applyUpdate(org: Org | null, jobs: Job[], transientErr: unknown = null) {
    // Mutate only the fields that actually changed so downstream $effect
    // hooks (e.g. AppsPage reassigning activeOrg on every tick) don't
    // re-run on no-op polls. Identity changes cascade into children like
    // BackingServices that (pre-#106) re-fetched on every orgId prop-
    // change, producing a visible flicker during the 1.5s polling cadence.
    if (!orgEqual(this.state.org, org)) {
      this.state.org = org;
    }
    if (!jobsEqual(this.state.jobs, jobs)) {
      this.state.jobs = jobs;
    }
    this.state.status = transientErr ? 'error' : 'idle';
    this.state.error = transientErr ? ((transientErr as any)?.message ?? 'fetch failed') : null;
    this.state.lastUpdated = Date.now();
    this.broadcast();
    this.schedule();
  }

  private schedule() {
    if (this.subs === 0) return;
    if (this.pollTimer) clearTimeout(this.pollTimer);
    const interval = hasActiveWork(this.state.org, this.state.jobs) ? FAST_INTERVAL : SLOW_INTERVAL;
    this.pollTimer = setTimeout(() => {
      void this.runFetch();
    }, interval);
  }

  private broadcast() {
    if (!this.bc) return;
    try {
      this.bc.postMessage({
        tenantId: this.state.tenantId,
        org: this.state.org,
        jobs: this.state.jobs,
        lastUpdated: this.state.lastUpdated,
      });
    } catch {
      /* one-shot failures are fine; next poll will cover */
    }
  }

  private onBroadcast(msg: any) {
    if (!msg || typeof msg !== 'object') return;
    if (msg.tenantId !== this.state.tenantId) return;
    // Only accept broadcasts that are fresher than what we have.
    if (typeof msg.lastUpdated === 'number' && msg.lastUpdated <= this.state.lastUpdated) return;
    this.state.org = msg.org ?? null;
    this.state.jobs = Array.isArray(msg.jobs) ? msg.jobs : [];
    this.state.status = 'idle';
    this.state.error = null;
    this.state.lastUpdated = msg.lastUpdated ?? Date.now();
    // Re-arm the timer so we don't double-fire shortly after a sibling update.
    this.schedule();
  }

  dispose() {
    if (this.pollTimer) clearTimeout(this.pollTimer);
    this.pollTimer = null;
    if (this.bc) {
      try { this.bc.close(); } catch {}
      this.bc = null;
    }
  }
}

// One store per tenantId, shared across AppsPage / AppDetail / JobsPage
// within the same tab. Tabs in the same origin coordinate via the
// BroadcastChannel inside each store.
const stores = new Map<string, AppStateStore>();

export function getAppStateStore(tenantId: string): AppStateStore {
  if (!tenantId) tenantId = '__empty__';
  let s = stores.get(tenantId);
  if (!s) {
    s = new AppStateStore(tenantId);
    stores.set(tenantId, s);
  }
  return s;
}
