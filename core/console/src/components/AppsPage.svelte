<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import {
    getApps, getProvisionStatus, getMyOrgs, installApp, uninstallApp,
    type User, type Org, type CatalogApp, type Provision, type ProvisionStep,
  } from '../lib/api';
  import { path } from '../lib/config';
  import { getAppStateStore } from '../lib/stores/appState.svelte';

  const ACTIVE_ORG_KEY = 'sme-active-org';

  let catalog = $state<CatalogApp[]>([]);
  let provision = $state<Provision | null>(null);
  let provLoading = $state(true);
  let provPollTimer: ReturnType<typeof setInterval> | null = null;
  let activeOrg = $state<Org | null>(null);
  let query = $state('');
  let tab = $state<'installed' | 'catalog'>('installed');
  // Shared store — source of truth for tenant apps + app_states + jobs. #64
  let store = $state<ReturnType<typeof getAppStateStore> | null>(null);

  // Add/Remove modal state
  let modal = $state<null | {
    mode: 'add' | 'remove' | 'capacity';
    app: CatalogApp;
    message?: string;
    upgrade?: string;
    pending?: boolean;
    // Advanced: per-dependency "dedicated" (new instance) vs "reuse" (existing instance slug/id).
    depChoices?: Record<string, 'dedicated' | string>;
    advancedOpen?: boolean;
  }>(null);
  let toasts = $state<Array<{ id: number; text: string; kind: 'ok' | 'info' | 'error' }>>([]);
  let toastId = 0;

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
        // Subscribe the page to the shared store — one poller feeds apps,
        // app-detail, and jobs so they can't drift. #64
        store = getAppStateStore(picked.id);
        const dispose = store.subscribe();
        loadProvision(picked.id);
        return () => dispose();
      }
      provLoading = false;
    }).catch(() => { provLoading = false; });
    return () => { if (provPollTimer) clearInterval(provPollTimer); };
  });

  // Mirror the shared store's tenant record into activeOrg so the existing
  // derived values (installedIds, appStateFor) see fresh app_states without
  // further plumbing. Runs on every store update.
  $effect(() => {
    if (!store) return;
    const o = store.state.org;
    if (o && activeOrg && o.id === activeOrg.id) {
      activeOrg = o;
    }
  });

  function loadProvision(orgId: string) {
    getProvisionStatus(orgId)
      .then(p => {
        provision = p;
        provLoading = false;
        if (p.status === 'provisioning' && !provPollTimer) startProvPolling(orgId);
      })
      .catch(() => { provision = null; provLoading = false; });
  }

  // Initial-provision status is a separate backend model (one row per tenant,
  // the "first-run" flow) from the day-2 jobs the shared store tracks. Keep
  // a small focused poller for it so the setup banner remains accurate
  // without widening the store's surface.
  function startProvPolling(orgId: string) {
    if (provPollTimer) clearInterval(provPollTimer);
    provPollTimer = setInterval(() => {
      getProvisionStatus(orgId).then(p => {
        provision = p;
        if (p.status === 'completed' || p.status === 'failed') {
          if (provPollTimer) { clearInterval(provPollTimer); provPollTimer = null; }
        }
      }).catch(() => {});
    }, 3000);
  }

  type AppState = 'installing' | 'uninstalling' | 'pending' | 'installed' | 'failed' | 'not-installed';

  function stepForApp(appName: string, steps: ProvisionStep[] | undefined): ProvisionStep | null {
    if (!steps) return null;
    const target = `Deploying ${appName}`;
    return steps.find(s => s.name === target) || null;
  }

  function appStateFor(app: CatalogApp): { state: AppState; message?: string } {
    // app_states is the source of truth for day-2 transitions (#64). If the
    // tenant consumer flipped the app to "installing" / "uninstalling" /
    // "failed", render that regardless of whether the initial provisioning
    // is still in-flight or long since done.
    const dayTwo = activeOrg?.app_states?.[app.id];
    if (dayTwo === 'uninstalling') return { state: 'uninstalling' };
    if (dayTwo === 'installing') return { state: 'installing' };
    if (dayTwo === 'failed') return { state: 'failed' };

    const installed = installedIds.includes(app.id);
    if (!installed) return { state: 'not-installed' };
    const step = stepForApp(app.name, provision?.steps);
    // Step status is ALWAYS authoritative — if this specific app's deploy
    // step has a terminal status, use it. #114.
    if (step?.status === 'completed') return { state: 'installed', message: step.message };
    if (step?.status === 'failed') return { state: 'failed', message: step.message };
    if (step?.status === 'running') return { state: 'installing', message: step.message };
    // Day-2 installs have NO step in the provision record. If app_states
    // didn't flag installing/failed above AND tenant.Apps has the id, the
    // app is installed. Previously we fell through to the provision.status
    // branch here, which painted every day-2 app as INSTALLING for the
    // entire duration of the initial provision — the exact Uptime-Kuma
    // stale-state the user reported even after the pod was Ready. #115.
    if (!step) return { state: 'installed' };
    // Step exists but is still pending AND overall provision is running:
    // the app's step hasn't started yet. Show installing.
    if (provision?.status === 'provisioning') {
      return { state: 'installing', message: step?.message };
    }
    return { state: 'installed', message: step?.message };
  }

  function openApp(slug: string) {
    if (!activeOrg) return;
    window.open(`https://${activeOrg.slug}.omani.rest/${slug}`, '_blank');
  }

  // SOURCE OF TRUTH for "what's deployed" is the tenant record's Apps list.
  // Previously we overrode with provision.apps when present, which was stale
  // for day-2 installs that happened after the initial provisioning record
  // was created — those apps never appeared in the Deployments tab. #113.
  const installedIds = $derived<string[]>(activeOrg?.apps ?? []);

  // "service" apps are backing services (databases, caches, queues). Show
  // them in the SAME grid as business apps with a subtle SERVICE pill —
  // see #112. Users pick business apps; backing services ride along as
  // dependencies but are browsable for connection info.
  const isServiceApp = (a: CatalogApp) => a.kind === 'service' || a.system;
  const catalogApps = $derived(catalog); // all apps, business + service
  const installedApps = $derived(catalogApps.filter(a => installedIds.includes(a.id)));
  const installedServices = $derived(catalog.filter(a => isServiceApp(a) && installedIds.includes(a.id)));

  const visibleApps = $derived.by(() => {
    let list = tab === 'installed' ? installedApps : catalogApps;
    if (query) {
      const q = query.toLowerCase();
      list = list.filter(a =>
        a.name.toLowerCase().includes(q) ||
        a.tagline.toLowerCase().includes(q) ||
        a.category.toLowerCase().includes(q),
      );
    }
    return [...list].sort((a, b) => {
      if (tab === 'catalog') {
        const aIn = installedIds.includes(a.id) ? 0 : 1;
        const bIn = installedIds.includes(b.id) ? 0 : 1;
        if (aIn !== bIn) return aIn - bIn;
      }
      return a.name.localeCompare(b.name);
    });
  });

  function showToast(text: string, kind: 'ok' | 'info' | 'error' = 'info') {
    const id = ++toastId;
    toasts = [...toasts, { id, text, kind }];
    setTimeout(() => { toasts = toasts.filter(t => t.id !== id); }, 3500);
  }

  function requestAdd(app: CatalogApp) {
    // Default every dependency to a dedicated new instance; the user can flip
    // to "reuse" in the Advanced drawer if an existing shareable instance is
    // available.
    const depChoices: Record<string, 'dedicated' | string> = {};
    for (const depSlug of app.dependencies ?? []) {
      depChoices[depSlug] = 'dedicated';
    }
    modal = { mode: 'add', app, depChoices, advancedOpen: false };
  }

  // Only shareable deps are offered a reuse option. Non-shareable deps (none
  // today, but future-proof) stay locked to "dedicated".
  function shareableDeps(app: CatalogApp): CatalogApp[] {
    return (app.dependencies ?? [])
      .map((slug) => catalog.find((c) => c.slug === slug))
      .filter((c): c is CatalogApp => !!c && c.shareable === true);
  }

  // Existing instances of a service that the user can reuse. For v1 an org has
  // at most one instance per service slug — once multi-instance lands we'll
  // enumerate them here.
  function reusableInstances(depSlug: string): CatalogApp[] {
    return installedServices.filter((s) => s.slug === depSlug);
  }

  function requestRemove(app: CatalogApp) {
    modal = { mode: 'remove', app };
  }

  async function confirmAdd() {
    if (!modal || !activeOrg) return;
    modal.pending = true;
    try {
      const res = await installApp(activeOrg.id, modal.app.slug, modal.depChoices);
      showToast(`${modal.app.name} queued for install`, 'ok');
      modal = null;
      loadProvision(activeOrg.id);
      // Kick the shared store so this tab and any sibling tab flip to
      // "installing" within the same tick, not on the next poll. #64
      void store?.refreshNow();
    } catch (e: any) {
      const msg = e?.message ?? '';
      // Backend returns 409 with upgrade_suggestion when over capacity
      if (msg.startsWith('409')) {
        try {
          const body = JSON.parse(msg.slice(msg.indexOf(':') + 1).trim());
          modal = { mode: 'capacity', app: modal.app, upgrade: body.upgrade_suggestion, message: body.message };
          return;
        } catch {}
      }
      if (msg.startsWith('501')) {
        modal = { mode: 'add', app: modal.app, message: 'Day-2 installs are launching soon — we will email you when live.' };
        modal.pending = false;
        return;
      }
      showToast(`Install failed: ${msg}`, 'error');
      modal = null;
    }
  }

  async function confirmRemove() {
    if (!modal || !activeOrg) return;
    modal.pending = true;
    try {
      await uninstallApp(activeOrg.id, modal.app.slug);
      showToast(`${modal.app.name} removed`, 'ok');
      modal = null;
      loadProvision(activeOrg.id);
      // Kick the shared store — flips this tab AND sibling tabs (jobs,
      // app-detail) to "uninstalling" without waiting for the next tick. #64
      void store?.refreshNow();
    } catch (e: any) {
      const msg = e?.message ?? '';
      if (msg.startsWith('501')) {
        modal = { mode: 'remove', app: modal.app, message: 'Day-2 removal is launching soon — we will email you when live.' };
        modal.pending = false;
        return;
      }
      showToast(`Remove failed: ${msg}`, 'error');
      modal = null;
    }
  }

  function closeModal() { modal = null; }
</script>

<PortalShell activePage="apps">
  {#snippet children(user: User, org: Org | null)}
    <div class="flex items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Applications</h1>
        <p class="mt-1 text-sm text-[var(--color-text-dim)]">Manage your tenant — add, remove, and open apps.</p>
      </div>
      {#if provision && provision.status === 'provisioning'}
        <div class="flex items-center gap-2 rounded-lg border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs text-[var(--color-accent)]">
          <div class="h-3 w-3 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
          Provisioning · {provision.progress ?? 0}%
          <a href={path('jobs')} class="ml-2 text-[var(--color-accent)] underline">View job</a>
        </div>
      {:else if provision && provision.status === 'completed'}
        <a href={path('jobs')} class="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]">View install history</a>
      {/if}
    </div>

    {#if provLoading}
      <div class="mt-12 flex justify-center">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
      </div>
    {:else}
      <!-- Tabs: "Deployments" covers installing+installed+failed (anything
           recorded in tenant.Apps, regardless of state); "Catalog" is the
           full library including backing services. Issue #113. -->
      <div class="tabs">
        <button class="tab" class:active={tab === 'installed'} onclick={() => (tab = 'installed')}>
          Deployments <span class="tab-count">{installedApps.length}</span>
        </button>
        <button class="tab" class:active={tab === 'catalog'} onclick={() => (tab = 'catalog')}>
          Catalog <span class="tab-count">{catalogApps.length}</span>
        </button>
      </div>

      <!-- Search -->
      <div class="apps-toolbar">
        <div class="search-wrap">
          <svg class="search-icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
          <input type="text" placeholder={tab === 'installed' ? `Search your ${installedApps.length} apps…` : `Search ${catalogApps.length} apps…`} bind:value={query} class="search-input" />
        </div>
      </div>

      {#if tab === 'installed' && installedApps.length === 0}
        <div class="empty-state">
          <p class="empty-title">No applications installed yet.</p>
          <p class="empty-sub">Browse the catalog to add your first app.</p>
          <button class="btn btn-primary" onclick={() => (tab = 'catalog')}>Open catalog →</button>
        </div>
      {:else}
      <div class="apps-grid">
        {#each visibleApps as app}
          {@const st = appStateFor(app)}
          {@const isService = isServiceApp(app)}
          <a class="app-card state-{st.state} {isService ? 'is-service' : ''}" href={path(`app?slug=${app.slug}`)}>
            {#if app.logo}
              <img src={app.logo} alt={app.name} class="app-logo" loading="lazy" />
            {:else}
              <span class="app-icon" style="background: {app.color}">{app.icon}</span>
            {/if}
            <div class="app-body">
              <div class="app-top">
                <span class="app-name">{app.name}</span>
                <span class="app-cat">{app.category}</span>
              </div>
              <p class="app-desc">{app.description || app.tagline}</p>
              <div class="app-chips">
                <span class="chip chip-free">FREE</span>
                {#if isService}
                  <span class="chip chip-service" title="Backing service — bundled with apps that need it">SERVICE</span>
                {/if}
                {#if app.dependencies && app.dependencies.length > 0}
                  {#each app.dependencies as dep}
                    {@const depApp = catalog.find(c => c.slug === dep)}
                    <span class="chip chip-dep" title="Bundled dependency">+ {depApp?.name ?? dep}</span>
                  {/each}
                {/if}
              </div>
            </div>

            <!-- Status chip: bottom-right corner (uppercase — unified across views) -->
            <div class="status-corner">
              {#if st.state === 'installed'}
                <span class="status-chip s-installed">
                  <span class="dot"></span> INSTALLED
                </span>
              {:else if st.state === 'installing'}
                <span class="status-chip s-installing">
                  <span class="dot dot-spin"></span> INSTALLING
                </span>
              {:else if st.state === 'uninstalling'}
                <span class="status-chip s-installing">
                  <span class="dot dot-spin"></span> UNINSTALLING
                </span>
              {:else if st.state === 'pending'}
                <span class="status-chip s-pending">
                  <span class="dot"></span> QUEUED
                </span>
              {:else if st.state === 'failed'}
                <span class="status-chip s-failed">
                  <span class="dot"></span> FAILED
                </span>
              {/if}
            </div>

            <!-- Hover actions: Open + Remove for installed; Add for not-installed -->
            <div class="actions-corner">
              {#if st.state === 'installed'}
                <button class="icon-btn open" onclick={(e) => { e.preventDefault(); e.stopPropagation(); openApp(app.slug); }} title="Open">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>
                </button>
                <button class="icon-btn del" onclick={(e) => { e.preventDefault(); e.stopPropagation(); requestRemove(app); }} title="Remove">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6h14z"/></svg>
                </button>
              {:else if st.state === 'not-installed'}
                <button class="icon-btn add" onclick={(e) => { e.preventDefault(); e.stopPropagation(); requestAdd(app); }} title="Add to tenant">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
                </button>
              {/if}
            </div>
          </a>
        {/each}
      </div>

      {/if}
    {/if}

    <!-- Add / Remove / Capacity modal -->
    {#if modal}
      <div class="modal-backdrop" onclick={closeModal} role="presentation">
        <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog">
          {#if modal.mode === 'capacity'}
            <h3 class="modal-title">Plan upgrade required</h3>
            <p class="modal-body">Installing <strong>{modal.app.name}</strong> exceeds the capacity of your current plan.</p>
            {#if modal.message}<p class="modal-body muted">{modal.message}</p>{/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={closeModal}>Cancel</button>
              <a class="btn btn-primary" href={path('billing')}>{modal.upgrade ? `Upgrade to ${modal.upgrade}` : 'Upgrade plan'} →</a>
            </div>
          {:else if modal.mode === 'add'}
            <h3 class="modal-title">Add {modal.app.name}</h3>
            <p class="modal-body">{modal.app.description || modal.app.tagline}</p>
            {#if modal.app.dependencies && modal.app.dependencies.length > 0}
              <p class="modal-body muted">
                Includes:
                {#each modal.app.dependencies as dep, i}
                  {#if i > 0}, {/if}{catalog.find(c => c.slug === dep)?.name ?? dep}
                {/each}
              </p>
            {/if}
            <p class="modal-body muted">Current tenant: {installedIds.length} apps installed on {org?.name ?? 'your plan'}.</p>

            {#if shareableDeps(modal.app).length > 0}
              <button
                type="button"
                class="adv-toggle"
                onclick={() => modal && (modal.advancedOpen = !modal.advancedOpen)}
                aria-expanded={!!modal.advancedOpen}
              >
                <span class="caret" class:open={modal.advancedOpen}>▸</span>
                {modal.advancedOpen ? 'Hide advanced' : 'Advanced: database & backing services'}
              </button>
              {#if modal.advancedOpen}
                <div class="adv-panel">
                  <p class="adv-hint">
                    Each app gets its own isolated database by default — safest and easiest.
                    If you already run one, pick <strong>Reuse</strong> to save resources.
                  </p>
                  {#each shareableDeps(modal.app) as dep}
                    {@const instances = reusableInstances(dep.slug)}
                    <div class="dep-picker">
                      <div class="dep-picker-head">
                        {#if dep.logo}<img src={dep.logo} alt="" class="dep-logo" />{:else}<span class="dep-dot" style="background: {dep.color}"></span>{/if}
                        <div>
                          <div class="dep-name">{dep.name}</div>
                          <div class="dep-tagline">{dep.tagline}</div>
                        </div>
                      </div>
                      <div class="dep-options">
                        <label class="dep-option">
                          <input
                            type="radio"
                            name={`dep-${dep.slug}`}
                            value="dedicated"
                            checked={modal.depChoices?.[dep.slug] === 'dedicated'}
                            onchange={() => modal && modal.depChoices && (modal.depChoices[dep.slug] = 'dedicated')}
                          />
                          <span>
                            <strong>Dedicated</strong>
                            <span class="dep-sub">New {dep.name.toLowerCase()} just for {modal.app.name}</span>
                          </span>
                        </label>
                        {#if instances.length > 0}
                          <label class="dep-option">
                            <input
                              type="radio"
                              name={`dep-${dep.slug}`}
                              value={instances[0].slug}
                              checked={modal.depChoices?.[dep.slug] === instances[0].slug}
                              onchange={() => modal && modal.depChoices && (modal.depChoices[dep.slug] = instances[0].slug)}
                            />
                            <span>
                              <strong>Reuse existing</strong>
                              <span class="dep-sub">Share the {dep.name.toLowerCase()} already running in this tenant</span>
                            </span>
                          </label>
                        {:else}
                          <div class="dep-option disabled">
                            <input type="radio" disabled />
                            <span>
                              <strong>Reuse existing</strong>
                              <span class="dep-sub">No {dep.name.toLowerCase()} instance running yet</span>
                            </span>
                          </div>
                        {/if}
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            {/if}

            {#if modal.message}
              <div class="info-banner">{modal.message}</div>
            {/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={closeModal} disabled={modal.pending}>Cancel</button>
              <button class="btn btn-primary" onclick={confirmAdd} disabled={modal.pending}>
                {modal.pending ? 'Installing…' : 'Install'}
              </button>
            </div>
          {:else if modal.mode === 'remove'}
            <h3 class="modal-title">Remove {modal.app.name}?</h3>
            <p class="modal-body">This will stop <strong>{modal.app.name}</strong> and delete its data. Other apps in your tenant are unaffected.</p>
            {#if modal.message}
              <div class="info-banner">{modal.message}</div>
            {/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={closeModal} disabled={modal.pending}>Cancel</button>
              <button class="btn btn-danger" onclick={confirmRemove} disabled={modal.pending}>
                {modal.pending ? 'Removing…' : 'Remove'}
              </button>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Toasts -->
    <div class="toast-container">
      {#each toasts as t (t.id)}
        <div class="toast {t.kind}">{t.text}</div>
      {/each}
    </div>
  {/snippet}
</PortalShell>

<style>
  .apps-toolbar {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    margin: 1rem 0 0.75rem;
  }
  .search-wrap { position: relative; flex: 1; }
  .search-icon {
    position: absolute; left: 12px; top: 50%; transform: translateY(-50%);
    color: var(--color-text-dim); opacity: 0.6;
  }
  .search-input {
    width: 100%;
    padding: 0.6rem 0.85rem 0.6rem 2.2rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.88rem;
  }
  .search-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
  .installed-pill {
    padding: 0.45rem 0.85rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--color-success) 10%, transparent);
    color: var(--color-success);
    font-size: 0.8rem;
    font-weight: 600;
    white-space: nowrap;
  }

  /* Auto-fit: pack as many cards as fit, then stretch remaining width across them.
     min = 360px mirrors the marketplace 3-col width at ~1100px container, so console
     cards are never narrower than marketplace. */
  .apps-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
    gap: 0.65rem;
  }

  /* Tabs */
  .tabs {
    display: flex;
    gap: 0.25rem;
    margin: 1rem 0 0.5rem;
    border-bottom: 1px solid var(--color-border);
  }
  .tab {
    background: transparent;
    border: none;
    padding: 0.6rem 0.9rem;
    color: var(--color-text-dim);
    font: inherit;
    font-size: 0.88rem;
    font-weight: 500;
    cursor: pointer;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
  }
  .tab:hover { color: var(--color-text); }
  .tab.active {
    color: var(--color-text-strong);
    border-bottom-color: var(--color-accent);
    font-weight: 600;
  }
  .tab-count {
    font-size: 0.7rem;
    padding: 0.08rem 0.4rem;
    border-radius: 999px;
    background: color-mix(in srgb, var(--color-border) 60%, transparent);
    color: var(--color-text-dim);
    font-weight: 600;
  }
  .tab.active .tab-count {
    background: color-mix(in srgb, var(--color-accent) 18%, transparent);
    color: var(--color-accent);
  }

  /* Empty state */
  .empty-state {
    margin-top: 3rem;
    text-align: center;
    color: var(--color-text-dim);
  }
  .empty-title { font-size: 1rem; color: var(--color-text-strong); margin: 0 0 0.3rem; font-weight: 600; }
  .empty-sub { font-size: 0.85rem; margin: 0 0 1.2rem; }

  .app-card {
    position: relative;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    padding: 0.6rem;
    display: flex;
    align-items: stretch;
    gap: 0.75rem;
    transition: transform 0.15s, border-color 0.15s, box-shadow 0.15s;
    height: 108px;
    overflow: hidden;
    cursor: pointer;
    color: inherit;
    text-decoration: none;
  }
  .app-card:hover {
    transform: translateY(-2px);
    border-color: var(--color-accent);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  }
  .app-card.state-installed { border-color: color-mix(in srgb, var(--color-success) 45%, var(--color-border)); }
  .app-card.state-installing { border-color: color-mix(in srgb, var(--color-accent) 55%, var(--color-border)); }
  .app-card.state-failed { border-color: color-mix(in srgb, var(--color-danger) 55%, var(--color-border)); }
  /* Backing-service cards: same dimensions, dashed border to signal
     they're infra, not top-level apps. Click-through still goes to the
     detail page where the connection info lives. Issue #112. */
  .app-card.is-service {
    border-style: dashed;
    opacity: 0.9;
  }
  .app-card.is-service:hover { opacity: 1; }
  .chip-service {
    background: color-mix(in srgb, var(--color-accent) 14%, transparent);
    color: var(--color-accent);
    font-weight: 600;
    letter-spacing: 0.04em;
  }

  .app-logo {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .app-icon {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    display: inline-flex; align-items: center; justify-content: center;
    flex-shrink: 0; color: #fff; font-size: 1.3rem; font-weight: 700;
  }

  .app-body {
    flex: 1; min-width: 0;
    display: flex; flex-direction: column; gap: 0.25rem;
    padding-right: 4.5rem; /* room for bottom-right status chip + top-right actions */
    overflow: hidden;
  }
  .app-top { display: flex; align-items: baseline; gap: 0.5rem; }
  .app-name {
    color: var(--color-text-strong); font-size: 0.92rem; font-weight: 600; line-height: 1.2;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    flex: 1 1 auto; min-width: 0;
  }
  .app-cat {
    color: var(--color-text-dim); font-size: 0.68rem; text-transform: capitalize;
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    padding: 0.1rem 0.4rem; border-radius: 3px;
  }
  .app-desc {
    margin: 0; color: var(--color-text); font-size: 0.78rem; line-height: 1.45;
    display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .app-chips {
    margin-top: 0.25rem;
    display: flex;
    flex-wrap: nowrap;
    gap: 0.25rem;
    overflow: hidden;
    mask-image: linear-gradient(to right, #000 85%, transparent);
    -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
    min-height: 1.4rem;
  }
  .chip {
    display: inline-flex; align-items: center; padding: 0.1rem 0.45rem;
    border-radius: 999px; font-size: 0.65rem; font-weight: 600; line-height: 1.4; white-space: nowrap;
  }
  .chip-free { background: color-mix(in srgb, var(--color-success) 14%, transparent); color: var(--color-success); }
  .chip-dep { background: color-mix(in srgb, var(--color-accent) 12%, transparent); color: var(--color-accent); font-weight: 500; }
  .chip-service { background: color-mix(in srgb, var(--color-text-dim) 14%, transparent); color: var(--color-text-dim); letter-spacing: 0.04em; }

  /* Backing services — visually quieter than business cards. */
  .services-section { margin-top: 2rem; }
  .services-toggle {
    display: flex; align-items: center; gap: 0.5rem;
    background: transparent; border: 0; padding: 0.5rem 0;
    font-size: 0.78rem; font-weight: 600; color: var(--color-text-dim);
    cursor: pointer; letter-spacing: 0.02em;
  }
  .services-toggle:hover { color: var(--color-text); }
  .services-toggle .caret { transition: transform 0.15s ease; }
  .services-toggle .caret.open { transform: rotate(90deg); }
  .services-label { text-transform: uppercase; }
  .services-hint { margin-left: 0.25rem; font-weight: 400; font-style: italic; opacity: 0.7; }
  .services-blurb {
    margin: 0.25rem 0 0.75rem;
    font-size: 0.78rem; color: var(--color-text-dim); max-width: 48rem;
  }
  .services-grid .app-card.is-service {
    background: color-mix(in srgb, var(--color-surface) 60%, transparent);
    border-style: dashed;
    opacity: 0.92;
  }
  .services-grid .app-card.is-service:hover { opacity: 1; }

  /* Status chip — pinned bottom-right */
  .status-corner {
    position: absolute;
    bottom: 0.5rem;
    right: 0.55rem;
    pointer-events: none;
  }
  .status-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.15rem 0.55rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    line-height: 1.4;
  }
  .status-chip .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block; }
  .status-chip .dot-spin { animation: pulse 1.3s ease-in-out infinite; }
  .s-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
  .s-installing { background: color-mix(in srgb, var(--color-accent) 16%, transparent); color: var(--color-accent); }
  .s-pending { background: color-mix(in srgb, var(--color-text-dim) 16%, transparent); color: var(--color-text-dim); }
  .s-failed { background: color-mix(in srgb, var(--color-danger) 16%, transparent); color: var(--color-danger); }

  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.35; }
  }

  /* Hover action buttons — top-right */
  .actions-corner {
    position: absolute;
    top: 0.45rem;
    right: 0.45rem;
    display: flex;
    gap: 0.25rem;
    opacity: 0;
    transform: scale(0.9);
    transition: opacity 0.15s, transform 0.15s;
  }
  .app-card:hover .actions-corner { opacity: 1; transform: scale(1); }
  .icon-btn {
    width: 28px; height: 28px;
    border-radius: 50%;
    border: none; cursor: pointer;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--color-bg);
    color: var(--color-text-dim);
    box-shadow: 0 2px 6px rgba(0, 0, 0, 0.25);
  }
  .icon-btn svg { width: 14px; height: 14px; }
  .icon-btn.add { background: var(--color-accent); color: #fff; }
  .icon-btn.add:hover { filter: brightness(0.88); }
  .icon-btn.open:hover { color: var(--color-accent); }
  .icon-btn.del:hover { color: #EF4444; }

  /* Modal */
  .modal-backdrop {
    position: fixed; inset: 0; z-index: 100;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: center; justify-content: center;
    padding: 1rem;
  }
  .modal {
    width: 100%;
    max-width: 440px;
    background: var(--color-bg-2, var(--color-surface));
    border: 1px solid var(--color-border);
    border-radius: 14px;
    padding: 1.4rem 1.3rem 1.1rem;
    box-shadow: 0 12px 48px rgba(0, 0, 0, 0.4);
  }
  .modal-title { margin: 0 0 0.6rem; font-size: 1.05rem; font-weight: 600; color: var(--color-text-strong); }
  .modal-body { margin: 0.35rem 0; color: var(--color-text); font-size: 0.88rem; line-height: 1.5; }
  .modal-body.muted { color: var(--color-text-dim); font-size: 0.8rem; }
  .info-banner {
    margin-top: 0.75rem;
    padding: 0.55rem 0.75rem;
    background: color-mix(in srgb, var(--color-accent) 10%, transparent);
    color: var(--color-accent);
    border-radius: 8px;
    font-size: 0.8rem;
  }
  .modal-actions {
    display: flex; gap: 0.5rem; justify-content: flex-end;
    margin-top: 1rem;
  }
  .adv-toggle {
    margin-top: 0.75rem; padding: 0.35rem 0;
    background: none; border: none; color: var(--color-text-dim);
    font: inherit; font-size: 0.82rem; cursor: pointer;
    display: inline-flex; align-items: center; gap: 0.4rem;
  }
  .adv-toggle:hover { color: var(--color-text); }
  .adv-toggle .caret { display: inline-block; transition: transform 0.15s ease; font-size: 0.7rem; }
  .adv-toggle .caret.open { transform: rotate(90deg); }
  .adv-panel {
    margin-top: 0.5rem; padding: 0.85rem;
    background: color-mix(in srgb, var(--color-border) 25%, transparent);
    border: 1px dashed var(--color-border);
    border-radius: 10px;
  }
  .adv-hint { margin: 0 0 0.75rem; font-size: 0.8rem; color: var(--color-text-dim); line-height: 1.5; }
  .dep-picker { padding: 0.6rem 0; border-top: 1px solid var(--color-border); }
  .dep-picker:first-child { border-top: none; padding-top: 0; }
  .dep-picker-head { display: flex; gap: 0.6rem; align-items: center; margin-bottom: 0.5rem; }
  .dep-logo { width: 28px; height: 28px; border-radius: 6px; object-fit: cover; }
  .dep-dot { width: 28px; height: 28px; border-radius: 6px; display: inline-block; }
  .dep-name { font-size: 0.88rem; font-weight: 600; color: var(--color-text-strong); }
  .dep-tagline { font-size: 0.75rem; color: var(--color-text-dim); }
  .dep-options { display: flex; flex-direction: column; gap: 0.45rem; }
  .dep-option {
    display: flex; gap: 0.55rem; padding: 0.5rem 0.6rem;
    border: 1px solid var(--color-border); border-radius: 8px;
    cursor: pointer; font-size: 0.82rem;
    background: var(--color-bg, var(--color-surface));
  }
  .dep-option:hover:not(.disabled) { border-color: var(--color-accent); }
  .dep-option input[type="radio"] { margin-top: 0.15rem; flex-shrink: 0; }
  .dep-option strong { color: var(--color-text-strong); font-weight: 600; display: block; }
  .dep-sub { font-size: 0.75rem; color: var(--color-text-dim); display: block; }
  .dep-option.disabled { opacity: 0.55; cursor: not-allowed; }
  .btn {
    padding: 0.5rem 1rem; border-radius: 8px; border: none;
    font: inherit; font-size: 0.85rem; font-weight: 600; cursor: pointer;
    text-decoration: none;
  }
  .btn-primary { background: var(--color-accent); color: #fff; }
  .btn-primary:hover { filter: brightness(0.9); }
  .btn-secondary { background: transparent; color: var(--color-text-dim); border: 1px solid var(--color-border); }
  .btn-secondary:hover { color: var(--color-text); }
  .btn-danger { background: #EF4444; color: #fff; }
  .btn-danger:hover { filter: brightness(0.9); }
  .btn:disabled { opacity: 0.5; cursor: wait; }

  /* Toasts */
  .toast-container {
    position: fixed; top: 4rem; right: 1.25rem; z-index: 120;
    display: flex; flex-direction: column; gap: 0.4rem; pointer-events: none;
  }
  .toast {
    padding: 0.55rem 0.85rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    font-size: 0.82rem;
    color: var(--color-text-strong);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
    animation: toast-in 0.22s ease-out;
  }
  .toast.ok { border-color: var(--color-success); }
  .toast.error { border-color: var(--color-danger); }
  @keyframes toast-in {
    from { transform: translateY(-12px); opacity: 0; }
    to { transform: translateY(0); opacity: 1; }
  }
</style>
