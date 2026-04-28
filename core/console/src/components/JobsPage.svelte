<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import {
    getApps, getProvisionStatus, getJobs, getMyOrgs,
    type User, type Org, type CatalogApp, type Provision, type ProvisionStep, type Job, type JobStep,
  } from '../lib/api';
  import { getAppStateStore } from '../lib/stores/appState.svelte';

  const ACTIVE_ORG_KEY = 'sme-active-org';

  // Unified timeline row — renders both the tenant provision (initial setup)
  // and day-2 install / uninstall jobs with the same visual shape.
  type TimelineEntry = {
    id: string;
    kind: 'provision' | 'install' | 'uninstall';
    title: string;
    status: string;  // pending, running/provisioning, completed/succeeded, failed
    progress: number;
    createdAt?: string;
    updatedAt?: string;
    steps: { name: string; status: string; message?: string; started_at?: string; done_at?: string }[];
    purged?: string[];
    retained?: string[];
  };

  let catalog = $state<CatalogApp[]>([]);
  let provision = $state<Provision | null>(null);
  let loading = $state(true);
  let activeOrg = $state<Org | null>(null);
  let provPollTimer: ReturnType<typeof setInterval> | null = null;
  let expandedIds = $state<Record<string, boolean>>({});
  // Shared store — jobs now flow through the same source as AppsPage /
  // AppDetail so a newly queued uninstall appears here within the fast
  // poll cadence, no page-local refresh required. #64
  let store = $state<ReturnType<typeof getAppStateStore> | null>(null);
  const jobs = $derived<Job[]>(store?.state.jobs ?? []);

  $effect(() => { getApps().then(a => { catalog = a; }).catch(() => {}); });

  function pickActiveOrg(list: Org[]): Org | null {
    if (!list.length) return null;
    const saved = localStorage.getItem(ACTIVE_ORG_KEY);
    if (saved) {
      const match = list.find(o => o.id === saved);
      if (match) return match;
    }
    return list[0];
  }

  $effect(() => {
    getMyOrgs().then(list => {
      const picked = pickActiveOrg(list || []);
      activeOrg = picked;
      if (picked) {
        store = getAppStateStore(picked.id);
        const dispose = store.subscribe();
        // Initial tenant provision is a separate backend model from day-2
        // jobs; poll it on its own cadence only while it's in-flight.
        loadProvisionInitial(picked.id);
        loading = false;
        return () => dispose();
      }
      loading = false;
    }).catch(() => { loading = false; });
    return () => { if (provPollTimer) clearInterval(provPollTimer); };
  });

  function loadProvisionInitial(orgId: string) {
    getProvisionStatus(orgId)
      .then((p) => {
        provision = p;
        if (p && (p.status === 'pending' || p.status === 'provisioning')) {
          startProvPolling(orgId);
        }
      })
      .catch(() => { provision = null; });
  }

  function startProvPolling(orgId: string) {
    if (provPollTimer) clearInterval(provPollTimer);
    provPollTimer = setInterval(async () => {
      try {
        const p = await getProvisionStatus(orgId).catch(() => null);
        provision = p;
        if (!p || (p.status !== 'pending' && p.status !== 'provisioning')) {
          if (provPollTimer) { clearInterval(provPollTimer); provPollTimer = null; }
        }
      } catch {}
    }, 3000);
  }

  function isRealTs(ts?: string): boolean {
    if (!ts) return false;
    if (ts.startsWith('0001-')) return false;
    const t = new Date(ts).getTime();
    return Number.isFinite(t) && t > 0;
  }

  function fmtTime(ts?: string): string {
    if (!isRealTs(ts)) return '';
    return new Date(ts!).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function duration(step: { started_at?: string; done_at?: string }): string {
    if (!isRealTs(step.started_at)) return '';
    const start = new Date(step.started_at!).getTime();
    const end = isRealTs(step.done_at) ? new Date(step.done_at!).getTime() : Date.now();
    if (end <= start) return '';
    const secs = Math.round((end - start) / 1000);
    if (secs < 60) return `${secs}s`;
    return `${Math.floor(secs / 60)}m ${secs % 60}s`;
  }

  // Map a raw status to the UI rendering bucket — the provision and job
  // backends use slightly different vocabularies (provisioning→running,
  // completed→succeeded) so collapse them here.
  function ui(status: string): 'pending' | 'running' | 'succeeded' | 'failed' {
    if (status === 'succeeded' || status === 'completed') return 'succeeded';
    if (status === 'failed') return 'failed';
    if (status === 'running' || status === 'provisioning') return 'running';
    return 'pending';
  }

  function statusBadge(status: string): { text: string; classes: string } {
    switch (ui(status)) {
      case 'succeeded': return { text: 'Succeeded', classes: 'bg-[var(--color-success)]/15 text-[var(--color-success)]' };
      case 'running':   return { text: 'Running',   classes: 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]' };
      case 'failed':    return { text: 'Failed',    classes: 'bg-[var(--color-danger)]/15 text-[var(--color-danger)]' };
      default:          return { text: 'Pending',   classes: 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]' };
    }
  }

  // Merge provision + jobs into a single newest-first timeline.
  const timeline = $derived.by<TimelineEntry[]>(() => {
    const rows: TimelineEntry[] = [];
    if (provision) {
      const completed = provision.steps.filter(s => s.status === 'completed').length;
      const total = provision.steps.length;
      rows.push({
        id: provision.id,
        kind: 'provision',
        title: `Tenant provisioning · ${provision.subdomain || activeOrg?.slug || 'tenant'}`,
        status: provision.status,
        progress: provision.progress ?? (total ? Math.round((completed / total) * 100) : 0),
        createdAt: provision.created_at,
        updatedAt: provision.updated_at,
        steps: provision.steps,
      });
    }
    for (const j of jobs) {
      const completed = j.steps.filter(s => s.status === 'completed').length;
      const total = j.steps.length;
      const label = j.app_name || j.app_slug;
      rows.push({
        id: j.id,
        kind: j.kind,
        title: `${j.kind === 'install' ? 'Install' : 'Uninstall'} ${label}`,
        status: j.status,
        progress: j.progress ?? (total ? Math.round((completed / total) * 100) : 0),
        createdAt: j.created_at,
        updatedAt: j.updated_at,
        steps: j.steps,
        purged: j.purged_services ?? [],
        retained: j.retained_services ?? [],
      });
    }
    rows.sort((a, b) => {
      const at = a.createdAt ? new Date(a.createdAt).getTime() : 0;
      const bt = b.createdAt ? new Date(b.createdAt).getTime() : 0;
      return bt - at;
    });
    return rows;
  });

  // Expand the most-recently-created in-progress row by default.
  $effect(() => {
    for (const row of timeline) {
      if (expandedIds[row.id] === undefined) {
        expandedIds = { ...expandedIds, [row.id]: ui(row.status) === 'running' };
      }
    }
  });
</script>

<PortalShell activePage="jobs">
  {#snippet children(user: User, org: Org | null)}
    <div class="flex items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Jobs</h1>
        <p class="mt-1 text-sm text-[var(--color-text-dim)]">Provisioning, installs, and uninstalls for your tenant</p>
      </div>
    </div>

    {#if loading}
      <div class="mt-12 flex justify-center">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
      </div>
    {:else if !timeline.length}
      <div class="mt-12 text-center">
        <p class="text-[var(--color-text-dim)]">No jobs yet for this tenant.</p>
      </div>
    {:else}
      <div class="mt-6 flex flex-col gap-3">
        {#each timeline as row (row.id)}
          {@const badge = statusBadge(row.status)}
          {@const state = ui(row.status)}
          {@const completedN = row.steps.filter(s => s.status === 'completed').length}
          <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
            <button
              onclick={() => (expandedIds = { ...expandedIds, [row.id]: !expandedIds[row.id] })}
              class="flex w-full items-center gap-4 p-4 text-left"
              data-job-kind={row.kind}
              data-job-status={state}
            >
              <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-accent)]/10">
                {#if state === 'running'}
                  <div class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
                {:else if state === 'succeeded'}
                  <svg class="h-5 w-5 text-[var(--color-success)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
                  </svg>
                {:else if state === 'failed'}
                  <svg class="h-5 w-5 text-[var(--color-danger)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/>
                  </svg>
                {:else}
                  <svg class="h-5 w-5 text-[var(--color-text-dim)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
                  </svg>
                {/if}
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <p class="truncate text-sm font-semibold text-[var(--color-text-strong)]">{row.title}</p>
                  <span class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide {badge.classes}">{badge.text}</span>
                </div>
                <p class="mt-0.5 text-xs text-[var(--color-text-dim)]">
                  {completedN}/{row.steps.length} steps · started {fmtTime(row.createdAt)}
                  {#if state === 'succeeded' && row.updatedAt}· finished {fmtTime(row.updatedAt)}{/if}
                </p>
                {#if state === 'running'}
                  <div class="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-border)]">
                    <div class="h-full rounded-full bg-[var(--color-accent)] transition-all" style:width="{row.progress ?? 0}%"></div>
                  </div>
                {/if}
              </div>
              <svg class="h-4 w-4 shrink-0 text-[var(--color-text-dim)] transition-transform {expandedIds[row.id] ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7"/>
              </svg>
            </button>

            {#if expandedIds[row.id]}
              <div class="border-t border-[var(--color-border)] p-4">
                {#if row.kind === 'uninstall'}
                  {#if (row.purged?.length ?? 0) > 0}
                    <p class="mb-2 text-[11px] text-[var(--color-text-dim)]">
                      <span class="font-semibold text-[var(--color-danger)]">Purging data</span>: {row.purged!.join(', ')}
                    </p>
                  {/if}
                  {#if (row.retained?.length ?? 0) > 0}
                    <p class="mb-2 text-[11px] text-[var(--color-text-dim)]">
                      <span class="font-semibold text-[var(--color-success)]">Retaining (shared)</span>: {row.retained!.join(', ')}
                    </p>
                  {/if}
                {/if}
                <ol class="flex flex-col gap-3">
                  {#each row.steps as step, i}
                    <li class="flex items-start gap-3">
                      <div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center">
                        {#if step.status === 'completed'}
                          <div class="flex h-6 w-6 items-center justify-center rounded-full bg-[var(--color-success)]">
                            <svg class="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3">
                              <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
                            </svg>
                          </div>
                        {:else if step.status === 'running'}
                          <div class="h-5 w-5 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
                        {:else if step.status === 'failed'}
                          <div class="flex h-6 w-6 items-center justify-center rounded-full bg-[var(--color-danger)]">
                            <svg class="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3">
                              <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/>
                            </svg>
                          </div>
                        {:else}
                          <div class="flex h-6 w-6 items-center justify-center rounded-full border border-[var(--color-border-strong)] text-[10px] text-[var(--color-text-dimmer)]">{i + 1}</div>
                        {/if}
                      </div>
                      <div class="min-w-0 flex-1">
                        <p class="text-sm {step.status === 'completed' ? 'text-[var(--color-text)]' : step.status === 'running' ? 'text-[var(--color-accent)] font-medium' : step.status === 'failed' ? 'text-[var(--color-danger)] font-medium' : 'text-[var(--color-text-dimmer)]'}">{step.name}</p>
                        <p class="mt-0.5 flex items-center gap-2 text-[11px] text-[var(--color-text-dimmer)]">
                          {#if isRealTs(step.started_at)}<span>started {fmtTime(step.started_at)}</span>{/if}
                          {#if duration(step)}<span>· {duration(step)}</span>{/if}
                          {#if step.message && step.message !== 'ok'}<span>· {step.message}</span>{/if}
                        </p>
                      </div>
                    </li>
                  {/each}
                </ol>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {/snippet}
</PortalShell>
