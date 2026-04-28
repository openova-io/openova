<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import {
    getApps, getProvisionStatus, getMyOrgs, installApp, uninstallApp, getUninstallPreview,
    getBackingServices,
    type User, type Org, type CatalogApp, type Provision, type ConfigField, type UninstallPreview,
    type BackingService,
  } from '../lib/api';
  import { getAppStateStore } from '../lib/stores/appState.svelte';
  import { path } from '../lib/config';

  const ACTIVE_ORG_KEY = 'sme-active-org';

  let slug = $state('');
  let app = $state<CatalogApp | null>(null);
  let catalog = $state<CatalogApp[]>([]);
  let provision = $state<Provision | null>(null);
  let activeOrg = $state<Org | null>(null);
  let loading = $state(true);
  // Shared store — source of truth for apps/jobs/app_states. #64
  let store = $state<ReturnType<typeof getAppStateStore> | null>(null);
  let modal = $state<null | { mode: 'add' | 'remove' | 'capacity'; message?: string; upgrade?: string; pending?: boolean }>(null);
  let toast = $state<{ text: string; kind: 'ok' | 'error' } | null>(null);
  // Uninstall confirm flow (#62): preview + user-typed slug friction. The
  // user must tick the box AND type the slug before Remove is enabled.
  let uninstallPreview = $state<UninstallPreview | null>(null);
  let previewLoading = $state(false);
  let confirmUnderstood = $state(false);
  let typedSlug = $state('');
  // Backing-service connection info — only fetched + rendered when this
  // detail page is for a service app (postgres / mysql / redis). #112.
  let backing = $state<BackingService | null>(null);

  function pickActiveOrg(list: Org[]): Org | null {
    if (!list.length) return null;
    const saved = localStorage.getItem(ACTIVE_ORG_KEY);
    if (saved) {
      const match = list.find((o) => o.id === saved);
      if (match) return match;
    }
    return list[0];
  }

  $effect(() => {
    slug = new URLSearchParams(window.location.search).get('slug') ?? '';
    Promise.all([getApps(), getMyOrgs()]).then(([apps, orgs]) => {
      catalog = apps;
      app = apps.find((a) => a.slug === slug) ?? null;
      const picked = pickActiveOrg(orgs || []);
      activeOrg = picked;
      if (picked) {
        // Subscribe this page to the shared store — one poller across apps,
        // app-detail, and jobs so the three views never disagree. #64
        store = getAppStateStore(picked.id);
        const dispose = store.subscribe();
        getProvisionStatus(picked.id).then((p) => { provision = p; }).catch(() => {});
        loading = false;
        return () => dispose();
      }
      loading = false;
    }).catch(() => { loading = false; });
  });

  // Re-derive activeOrg from the shared store whenever it updates. Replaces
  // the previous per-page 3s poll — one source of truth, no drift. #64
  $effect(() => {
    if (!store) return;
    const o = store.state.org;
    if (o && activeOrg && o.id === activeOrg.id) {
      activeOrg = o;
    }
  });

  // Lifecycle state for the app on this workspace, driven by tenant.app_states.
  const appState = $derived(app && activeOrg?.app_states ? (activeOrg.app_states[app.id] ?? null) : null);
  const isServiceApp = $derived(!!app && (app.kind === 'service' || app.system === true));

  // Fetch backing-service connection info when this page is for a service
  // app AND the tenant actually has it installed. Re-runs on org change.
  $effect(() => {
    if (!activeOrg || !app || !isServiceApp) { backing = null; return; }
    getBackingServices(activeOrg.id)
      .then(list => { backing = list.find(b => b.slug === app!.slug) ?? null; })
      .catch(() => { backing = null; });
  });

  const isInstalling = $derived(appState === 'installing');
  const isUninstalling = $derived(appState === 'uninstalling');
  const isFailed = $derived(appState === 'failed');
  const installedIds = $derived<string[]>(activeOrg?.apps ?? provision?.apps ?? []);
  const isInstalled = $derived(app ? installedIds.includes(app.id) && !isUninstalling && !isFailed : false);
  const deps = $derived.by(() => (app?.dependencies ?? []).map((d) => catalog.find((c) => c.slug === d) ?? { name: d, slug: d }));

  const configSchema = $derived<ConfigField[]>(app?.config_schema ?? []);
  const basicFields = $derived(configSchema.filter((f) => !f.advanced));
  const advancedFields = $derived(configSchema.filter((f) => f.advanced));

  let configValues = $state<Record<string, any>>({});
  let showAdvanced = $state(false);

  $effect(() => {
    if (!configSchema.length) { configValues = {}; return; }
    const next: Record<string, any> = {};
    for (const f of configSchema) {
      next[f.key] = f.default ?? (f.type === 'bool' ? false : f.type === 'int' ? 0 : '');
    }
    configValues = next;
  });

  function reloadProvision() {
    if (!activeOrg) return;
    getProvisionStatus(activeOrg.id).then((p) => { provision = p; }).catch(() => {});
    // Re-hydrate shared store → sibling tabs (apps list, jobs page) flip
    // immediately rather than waiting for the next poll tick. #64
    void store?.refreshNow();
  }

  function flash(text: string, kind: 'ok' | 'error' = 'ok') {
    toast = { text, kind };
    setTimeout(() => { toast = null; }, 3500);
  }

  async function confirmAdd(orgId: string) {
    if (!app || !modal) return;
    modal.pending = true;
    try {
      await installApp(orgId, app.slug);
      flash(`${app.name} queued for install`, 'ok');
      modal = null;
      reloadProvision();
    } catch (e: any) {
      const msg = e?.message ?? '';
      if (msg.startsWith('409')) {
        try {
          const body = JSON.parse(msg.slice(msg.indexOf(':') + 1).trim());
          modal = { mode: 'capacity', upgrade: body.upgrade_suggestion, message: body.message };
          return;
        } catch {}
      }
      if (msg.startsWith('501')) {
        modal = { mode: 'add', message: 'Day-2 installs are launching soon — we will email you when live.' };
        modal.pending = false;
        return;
      }
      flash(`Install failed: ${msg}`, 'error');
      modal = null;
    }
  }

  async function confirmRemove(orgId: string) {
    if (!app || !modal) return;
    modal.pending = true;
    try {
      await uninstallApp(orgId, app.slug);
      flash(`${app.name} queued for uninstall — tracking progress on Jobs page`, 'ok');
      modal = null;
      confirmUnderstood = false;
      typedSlug = '';
      uninstallPreview = null;
      reloadProvision();
    } catch (e: any) {
      const msg = e?.message ?? '';
      if (msg.startsWith('501')) {
        modal = { mode: 'remove', message: 'Day-2 removal is launching soon — we will email you when live.' };
        modal.pending = false;
        return;
      }
      flash(`Remove failed: ${msg}`, 'error');
      modal = null;
    }
  }

  // Load the uninstall preview when the remove modal opens so the UI can
  // show exactly which data will be deleted vs retained (shared DBs).
  function openRemoveModal(orgId: string) {
    modal = { mode: 'remove' };
    uninstallPreview = null;
    confirmUnderstood = false;
    typedSlug = '';
    if (!app) return;
    previewLoading = true;
    getUninstallPreview(orgId, app.slug)
      .then((p) => { uninstallPreview = p; })
      .catch(() => { uninstallPreview = null; })
      .finally(() => { previewLoading = false; });
  }

  // Remove button is only enabled once the user acknowledges the purge AND
  // types the app slug exactly. Typing friction is light enough to not be
  // annoying but enough to prevent muscle-memory clicks.
  const canConfirmRemove = $derived(
    confirmUnderstood && app && typedSlug.trim() === app.slug
  );

  function openRunning(org: Org) {
    if (!app) return;
    window.open(`https://${org.slug}.omani.rest/${app.slug}`, '_blank');
  }
</script>

<PortalShell activePage="apps">
  {#snippet children(user: User, org: Org | null)}
    <div class="detail-page">
      <a href={path('apps')} class="back-link">&larr; Back to apps</a>

      {#if loading}
        <div class="flex justify-center py-20">
          <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
        </div>
      {:else if !app}
        <div class="not-found">
          <h1>App not found</h1>
          <a href={path('apps')}>Back to apps</a>
        </div>
      {:else}
        <div class="hero">
          {#if app.logo}
            <img src={app.logo} alt={app.name} class="hero-logo" />
          {:else}
            <span class="hero-icon" style="background: {app.color}">{app.icon}</span>
          {/if}
          <div class="hero-body">
            <h1>{app.name}</h1>
            <p class="hero-tagline">{app.tagline}</p>
            <div class="hero-meta">
              <span class="chip chip-cat">{app.category}</span>
              <span class="chip chip-free">FREE</span>
              {#if isInstalling}
                <span class="chip chip-pending"><span class="spinner"></span> Installing…</span>
              {:else if isUninstalling}
                <span class="chip chip-pending"><span class="spinner"></span> Uninstalling…</span>
              {:else if isFailed}
                <span class="chip chip-failed">Failed</span>
              {:else if isInstalled}
                <span class="chip chip-installed"><span class="dot"></span> Installed</span>
              {/if}
            </div>
          </div>
          <div class="hero-actions">
            {#if isInstalling || isUninstalling}
              <button class="btn btn-secondary" disabled>
                {isInstalling ? 'Installing…' : 'Uninstalling…'}
              </button>
            {:else if isInstalled}
              <button class="btn btn-primary" onclick={() => org && openRunning(org)}>Open app &rarr;</button>
              <button class="btn btn-danger-outline" onclick={() => org && openRemoveModal(org.id)}>Remove</button>
            {:else}
              <button class="btn btn-primary" onclick={() => (modal = { mode: 'add' })}>+ Install</button>
            {/if}
          </div>
        </div>

        <section class="section">
          <h2>About</h2>
          <p class="desc">{app.description || app.tagline}</p>
        </section>

        {#if isServiceApp}
          <section class="section">
            <h2>Connection</h2>
            <p class="section-hint">
              Your apps reach this service inside the tenant vCluster. Credentials are
              injected at deploy time — no manual wiring needed.
            </p>
            {#if backing}
              <dl class="conn-grid">
                <div class="conn-row">
                  <dt>Host</dt>
                  <dd><code>{backing.endpoint_host}</code></dd>
                </div>
                <div class="conn-row">
                  <dt>Port</dt>
                  <dd><code>{backing.endpoint_port}</code></dd>
                </div>
                {#if backing.image}
                  <div class="conn-row">
                    <dt>Image</dt>
                    <dd><code>{backing.image}</code></dd>
                  </div>
                {/if}
                <div class="conn-row">
                  <dt>Credentials</dt>
                  <dd>
                    Kubernetes secret <code>{backing.slug}-credentials</code> in the
                    <code>apps</code> namespace of your vCluster.
                  </dd>
                </div>
                <div class="conn-row">
                  <dt>Status</dt>
                  <dd>
                    <span class="status-chip s-{backing.pod_status === 'Running' ? 'installed' : 'installing'}">
                      <span class="dot"></span>
                      {backing.pod_status === 'Running' ? 'RUNNING' :
                        (backing.pod_status === 'Failed' ? 'FAILED' : 'DEPLOYING')}
                    </span>
                    {#if backing.total_replicas > 0}
                      <span class="replicas">{backing.ready_replicas}/{backing.total_replicas} ready</span>
                    {/if}
                  </dd>
                </div>
              </dl>
            {:else}
              <p class="desc">Not yet deployed on this tenant — it will come up automatically when an app that needs it is installed.</p>
            {/if}
          </section>
        {/if}

        {#if deps.length}
          <section class="section">
            <h2>Bundled dependencies</h2>
            <p class="section-hint">These are auto-installed alongside {app.name}:</p>
            <ul class="dep-list">
              {#each deps as d}
                <li>{d.name}</li>
              {/each}
            </ul>
          </section>
        {/if}

        <section class="section">
          <h2>Tenant</h2>
          <p class="desc">
            {org ? `Installing into ${org.name} — currently ${installedIds.length} apps installed.` : 'No tenant selected.'}
          </p>
        </section>

        {#if configSchema.length}
          <section class="section">
            <h2>Configuration</h2>
            <p class="section-hint">Tune {app.name} for your workload. Defaults work for most teams.</p>

            <div class="info-banner soft">Day-2 configuration is launching soon — edits will be enabled then.</div>

            <div class="config-grid">
              {#each basicFields as f (f.key)}
                <div class="config-row">
                  <label class="config-label" for={`cfg-${f.key}`}>
                    {f.label}
                    {#if f.description}<span class="config-hint">{f.description}</span>{/if}
                  </label>
                  <div class="config-control">
                    {#if f.type === 'bool'}
                      <input id={`cfg-${f.key}`} type="checkbox" checked={!!configValues[f.key]} disabled />
                    {:else if f.type === 'enum'}
                      <select id={`cfg-${f.key}`} bind:value={configValues[f.key]} disabled>
                        {#each (f.options ?? []) as opt}<option value={opt}>{opt}</option>{/each}
                      </select>
                    {:else if f.type === 'int'}
                      <input id={`cfg-${f.key}`} type="number" min={f.min} max={f.max} bind:value={configValues[f.key]} disabled />
                    {:else if f.type === 'size'}
                      <input id={`cfg-${f.key}`} type="text" bind:value={configValues[f.key]} placeholder="e.g. 10Gi" disabled />
                    {:else}
                      <input id={`cfg-${f.key}`} type="text" bind:value={configValues[f.key]} disabled />
                    {/if}
                  </div>
                </div>
              {/each}
            </div>

            {#if advancedFields.length}
              <button class="adv-toggle" onclick={() => (showAdvanced = !showAdvanced)} aria-expanded={showAdvanced}>
                <span class="caret" class:open={showAdvanced}>▸</span>
                {showAdvanced ? 'Hide advanced' : `Show advanced (${advancedFields.length})`}
              </button>

              {#if showAdvanced}
                <div class="config-grid advanced">
                  {#each advancedFields as f (f.key)}
                    <div class="config-row">
                      <label class="config-label" for={`cfg-${f.key}`}>
                        {f.label}
                        {#if f.description}<span class="config-hint">{f.description}</span>{/if}
                      </label>
                      <div class="config-control">
                        {#if f.type === 'bool'}
                          <input id={`cfg-${f.key}`} type="checkbox" checked={!!configValues[f.key]} disabled />
                        {:else if f.type === 'enum'}
                          <select id={`cfg-${f.key}`} bind:value={configValues[f.key]} disabled>
                            {#each (f.options ?? []) as opt}<option value={opt}>{opt}</option>{/each}
                          </select>
                        {:else if f.type === 'int'}
                          <input id={`cfg-${f.key}`} type="number" min={f.min} max={f.max} bind:value={configValues[f.key]} disabled />
                        {:else if f.type === 'size'}
                          <input id={`cfg-${f.key}`} type="text" bind:value={configValues[f.key]} placeholder="e.g. 10Gi" disabled />
                        {:else}
                          <input id={`cfg-${f.key}`} type="text" bind:value={configValues[f.key]} disabled />
                        {/if}
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            {/if}

            <div class="config-actions">
              <button class="btn btn-primary" disabled>Save changes</button>
            </div>
          </section>
        {/if}
      {/if}
    </div>

    {#if modal && app}
      <div class="modal-backdrop" onclick={() => (modal = null)} role="presentation">
        <div class="modal" onclick={(e) => e.stopPropagation()} role="dialog">
          {#if modal.mode === 'capacity'}
            <h3 class="modal-title">Plan upgrade required</h3>
            <p class="modal-body">Installing <strong>{app.name}</strong> exceeds your plan's capacity.</p>
            {#if modal.message}<p class="modal-body muted">{modal.message}</p>{/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={() => (modal = null)}>Cancel</button>
              <a class="btn btn-primary" href={path('billing')}>{modal.upgrade ? `Upgrade to ${modal.upgrade}` : 'Upgrade plan'} &rarr;</a>
            </div>
          {:else if modal.mode === 'add'}
            <h3 class="modal-title">Install {app.name}?</h3>
            <p class="modal-body">{app.description || app.tagline}</p>
            {#if modal.message}<div class="info-banner">{modal.message}</div>{/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={() => (modal = null)} disabled={modal.pending}>Cancel</button>
              <button class="btn btn-primary" onclick={() => org && confirmAdd(org.id)} disabled={modal.pending}>
                {modal.pending ? 'Installing…' : 'Install'}
              </button>
            </div>
          {:else if modal.mode === 'remove'}
            <h3 class="modal-title">Uninstall {app.name}?</h3>
            <p class="modal-body">This will remove <strong>{app.name}</strong> from your tenant.</p>

            {#if previewLoading}
              <div class="preview-loading">Checking dependencies…</div>
            {:else if uninstallPreview}
              {#if uninstallPreview.dependents.length > 0}
                <div class="info-banner danger">
                  <strong>Blocked:</strong> {uninstallPreview.dependents.join(', ')} depend on {app.name}. Remove {uninstallPreview.dependents.length === 1 ? 'it' : 'them'} first.
                </div>
              {:else}
                <div class="purge-section">
                  <p class="purge-label">Data that will be permanently deleted:</p>
                  <ul class="purge-list">
                    <li><span class="purge-dot"></span>App data and persistent volumes for {app.name}</li>
                    {#each uninstallPreview.purged_services as s}
                      <li><span class="purge-dot"></span>{s.name} database (dedicated to {app.name})</li>
                    {/each}
                  </ul>
                </div>
                {#if uninstallPreview.retained_services.length > 0}
                  <div class="retain-section">
                    <p class="retain-label">Shared backing services (kept because other apps use them):</p>
                    <ul class="retain-list">
                      {#each uninstallPreview.retained_services as s}
                        <li><span class="retain-dot"></span>{s.name}</li>
                      {/each}
                    </ul>
                  </div>
                {/if}
              {/if}
            {/if}

            {#if !uninstallPreview?.dependents?.length}
              <label class="confirm-check">
                <input type="checkbox" bind:checked={confirmUnderstood} disabled={modal.pending} />
                <span>I understand this data cannot be recovered.</span>
              </label>
              <label class="confirm-type">
                <span class="confirm-type-label">Type <code>{app.slug}</code> to confirm:</span>
                <input
                  type="text"
                  bind:value={typedSlug}
                  placeholder={app.slug}
                  autocomplete="off"
                  spellcheck="false"
                  disabled={modal.pending}
                />
              </label>
            {/if}

            {#if modal.message}<div class="info-banner">{modal.message}</div>{/if}
            <div class="modal-actions">
              <button class="btn btn-secondary" onclick={() => (modal = null)} disabled={modal.pending}>Cancel</button>
              <button
                class="btn btn-danger"
                onclick={() => org && confirmRemove(org.id)}
                disabled={modal.pending || !canConfirmRemove || (uninstallPreview?.dependents?.length ?? 0) > 0}
              >
                {modal.pending ? 'Removing…' : 'Remove'}
              </button>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    {#if toast}
      <div class="toast {toast.kind}">{toast.text}</div>
    {/if}
  {/snippet}
</PortalShell>

<style>
  .detail-page { max-width: 860px; margin: 0 auto; padding: 1rem 0 4rem; }
  .back-link {
    display: inline-block; margin-bottom: 1rem;
    color: var(--color-text-dim); font-size: 0.85rem; text-decoration: none;
  }
  .back-link:hover { color: var(--color-text-strong); }

  .not-found { text-align: center; padding: 4rem 0; color: var(--color-text-dim); }
  .not-found h1 { color: var(--color-text-strong); font-size: 1.4rem; margin-bottom: 1rem; }
  .not-found a { color: var(--color-accent); text-decoration: none; }

  .hero {
    display: flex; align-items: flex-start; gap: 1.1rem;
    padding: 1.4rem 0; border-bottom: 1px solid var(--color-border);
  }
  .hero-logo { width: 80px; height: 80px; border-radius: 18px; object-fit: cover; flex-shrink: 0; }
  .hero-icon {
    width: 80px; height: 80px; border-radius: 18px;
    display: inline-flex; align-items: center; justify-content: center;
    color: #fff; font-size: 1.8rem; font-weight: 700; flex-shrink: 0;
  }
  .hero-body { flex: 1; min-width: 0; }
  .hero-body h1 { margin: 0; color: var(--color-text-strong); font-size: 1.4rem; font-weight: 700; }
  .hero-tagline { margin: 0.25rem 0 0.6rem; color: var(--color-text-dim); font-size: 0.9rem; }
  .hero-meta { display: flex; gap: 0.4rem; flex-wrap: wrap; }

  .chip { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.18rem 0.55rem; border-radius: 999px; font-size: 0.7rem; font-weight: 600; white-space: nowrap; }
  .chip-cat { background: color-mix(in srgb, var(--color-border) 50%, transparent); color: var(--color-text-dim); text-transform: capitalize; }
  .chip-free { background: color-mix(in srgb, var(--color-success) 14%, transparent); color: var(--color-success); }
  .chip-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
  .chip-installed .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
  .chip-pending { background: color-mix(in srgb, var(--color-accent) 14%, transparent); color: var(--color-accent); }
  .chip-pending .spinner {
    width: 10px; height: 10px; border-radius: 50%;
    border: 2px solid currentColor; border-top-color: transparent;
    animation: cd-spin 0.7s linear infinite;
  }
  .chip-failed { background: color-mix(in srgb, var(--color-danger) 14%, transparent); color: var(--color-danger); }
  @keyframes cd-spin { to { transform: rotate(360deg); } }

  .hero-actions { display: flex; gap: 0.5rem; flex-shrink: 0; flex-direction: column; align-items: flex-end; }

  .section { padding: 1.1rem 0; border-bottom: 1px solid var(--color-border); }
  .section:last-of-type { border-bottom: none; }
  .section h2 { margin: 0 0 0.5rem; font-size: 0.98rem; font-weight: 600; color: var(--color-text-strong); }
  .section-hint { margin: 0 0 0.5rem; font-size: 0.82rem; color: var(--color-text-dim); }
  .desc { margin: 0; color: var(--color-text); font-size: 0.9rem; line-height: 1.6; }
  .conn-grid { margin: 0.4rem 0 0; padding: 0; display: grid; gap: 0.35rem; }
  .conn-row { display: grid; grid-template-columns: 6rem 1fr; gap: 0.6rem; align-items: baseline; }
  .conn-row dt {
    margin: 0;
    color: var(--color-text-dim);
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .conn-row dd { margin: 0; font-size: 0.88rem; color: var(--color-text); }
  .conn-row code {
    font-size: 0.82rem;
    background: var(--color-surface);
    padding: 0.1rem 0.4rem;
    border-radius: 4px;
    border: 1px solid var(--color-border);
  }
  .status-chip {
    display: inline-flex; align-items: center; gap: 0.3rem;
    padding: 0.12rem 0.5rem; border-radius: 999px;
    font-size: 0.62rem; font-weight: 600; letter-spacing: 0.03em;
  }
  .status-chip .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block; }
  .s-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
  .s-installing { background: color-mix(in srgb, var(--color-accent) 16%, transparent); color: var(--color-accent); }
  .replicas { margin-left: 0.5rem; color: var(--color-text-dim); font-size: 0.78rem; font-variant-numeric: tabular-nums; }
  .dep-list { list-style: none; padding: 0; margin: 0; display: flex; flex-wrap: wrap; gap: 0.4rem; }
  .dep-list li {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    padding: 0.25rem 0.7rem;
    font-size: 0.8rem;
    color: var(--color-text);
  }

  .btn {
    padding: 0.55rem 1rem; border-radius: 8px; border: none;
    font: inherit; font-size: 0.85rem; font-weight: 600; cursor: pointer;
    text-decoration: none; white-space: nowrap;
    display: inline-flex; align-items: center; justify-content: center;
  }
  .btn-primary { background: var(--color-accent); color: #fff; }
  .btn-primary:hover { filter: brightness(0.9); }
  .btn-secondary { background: transparent; color: var(--color-text-dim); border: 1px solid var(--color-border); }
  .btn-secondary:hover { color: var(--color-text); }
  .btn-danger { background: #EF4444; color: #fff; }
  .btn-danger:hover { filter: brightness(0.9); }
  .btn-danger-outline { background: transparent; color: var(--color-text-dim); border: 1px solid var(--color-border); }
  .btn-danger-outline:hover { color: #EF4444; border-color: #EF4444; }
  .btn:disabled { opacity: 0.5; cursor: wait; }

  .modal-backdrop {
    position: fixed; inset: 0; z-index: 100;
    background: rgba(0, 0, 0, 0.55);
    display: flex; align-items: center; justify-content: center;
    padding: 1rem;
  }
  .modal {
    width: 100%; max-width: 440px;
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
    margin-top: 0.75rem; padding: 0.55rem 0.75rem;
    background: color-mix(in srgb, var(--color-accent) 10%, transparent);
    color: var(--color-accent); border-radius: 8px; font-size: 0.8rem;
  }
  .modal-actions { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 1rem; }

  .info-banner.soft {
    margin: 0 0 1rem;
    background: color-mix(in srgb, var(--color-text-dim) 10%, transparent);
    color: var(--color-text-dim);
  }
  .info-banner.danger {
    background: color-mix(in srgb, var(--color-danger) 14%, transparent);
    color: var(--color-danger);
  }

  .preview-loading {
    margin: 0.7rem 0;
    padding: 0.5rem 0.7rem;
    font-size: 0.8rem;
    color: var(--color-text-dim);
    background: color-mix(in srgb, var(--color-border) 40%, transparent);
    border-radius: 8px;
  }
  .purge-section, .retain-section { margin: 0.8rem 0 0; }
  .purge-label, .retain-label {
    margin: 0 0 0.4rem;
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--color-text-strong);
  }
  .purge-list, .retain-list {
    list-style: none; padding: 0; margin: 0;
    display: flex; flex-direction: column; gap: 0.3rem;
  }
  .purge-list li, .retain-list li {
    display: flex; align-items: center; gap: 0.5rem;
    font-size: 0.82rem; color: var(--color-text);
  }
  .purge-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: var(--color-danger);
    flex-shrink: 0;
  }
  .retain-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: var(--color-success);
    flex-shrink: 0;
  }

  .confirm-check {
    display: flex; align-items: center; gap: 0.55rem;
    margin: 1rem 0 0.65rem;
    font-size: 0.85rem; color: var(--color-text);
    cursor: pointer;
  }
  .confirm-check input { margin: 0; }
  .confirm-type {
    display: flex; flex-direction: column; gap: 0.35rem;
    font-size: 0.82rem; color: var(--color-text);
  }
  .confirm-type-label { color: var(--color-text-dim); }
  .confirm-type-label code {
    background: color-mix(in srgb, var(--color-border) 60%, transparent);
    padding: 0.05rem 0.35rem; border-radius: 4px;
    font-family: var(--font-mono, ui-monospace, SFMono-Regular, monospace);
    color: var(--color-text-strong);
  }
  .confirm-type input {
    width: 100%; padding: 0.45rem 0.6rem;
    border: 1px solid var(--color-border);
    border-radius: 6px;
    background: var(--color-bg, var(--color-surface));
    color: var(--color-text);
    font-size: 0.85rem;
  }
  .confirm-type input:focus {
    outline: none;
    border-color: var(--color-danger);
  }
  .config-grid { display: flex; flex-direction: column; gap: 0.75rem; }
  .config-grid.advanced { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px dashed var(--color-border); }
  .config-row {
    display: grid; grid-template-columns: 1fr 220px; gap: 1rem; align-items: start;
    padding: 0.4rem 0;
  }
  .config-label { display: flex; flex-direction: column; gap: 0.15rem; font-size: 0.85rem; color: var(--color-text); }
  .config-hint { font-size: 0.75rem; color: var(--color-text-dim); font-weight: 400; }
  .config-control input[type="text"],
  .config-control input[type="number"],
  .config-control select {
    width: 100%; padding: 0.4rem 0.55rem; border-radius: 6px;
    background: var(--color-bg, var(--color-surface)); color: var(--color-text);
    border: 1px solid var(--color-border); font: inherit; font-size: 0.85rem;
  }
  .config-control input[type="checkbox"] { transform: scale(1.15); }
  .config-control :disabled { opacity: 0.65; cursor: not-allowed; }
  .adv-toggle {
    margin-top: 0.75rem; padding: 0.35rem 0;
    background: none; border: none; color: var(--color-text-dim);
    font: inherit; font-size: 0.82rem; cursor: pointer;
    display: inline-flex; align-items: center; gap: 0.4rem;
  }
  .adv-toggle:hover { color: var(--color-text); }
  .adv-toggle .caret { display: inline-block; transition: transform 0.15s ease; font-size: 0.7rem; }
  .adv-toggle .caret.open { transform: rotate(90deg); }
  .config-actions { display: flex; justify-content: flex-end; margin-top: 1rem; }

  .toast {
    position: fixed; top: 4rem; right: 1.25rem; z-index: 120;
    padding: 0.55rem 0.85rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    font-size: 0.82rem;
    color: var(--color-text-strong);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
  }
  .toast.ok { border-color: var(--color-success); }
  .toast.error { border-color: var(--color-danger); }
</style>
