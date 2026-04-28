<script lang="ts">
  // Per-tenant "Backing services" panel. Self-contained: fetches on mount,
  // refetches whenever the parent hands it a new orgId. Keeps its own
  // reveal-details toggle so owners can grab the connection string without
  // having to read our docs.
  import { getBackingServices, type BackingService } from '../lib/api';

  let { orgId, compact = false }: { orgId: string; compact?: boolean } = $props();

  let services = $state<BackingService[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  // Per-row "show connection details" toggle. Closed by default to keep the
  // row dense; one click reveals host+port the user would wire into their app.
  let revealed = $state<Record<string, boolean>>({});

  // Cache the last fetched slice so a transient 429 / 500 (issue #106)
  // doesn't wipe the list and make the whole panel flicker back to
  // empty-state copy. Only the id that triggered the last successful
  // fetch changes the list.
  let lastLoadedId = $state<string>('');

  async function load(id: string) {
    // First mount shows the spinner; subsequent refreshes for the SAME
    // org keep the last good list rendered and update in place. This
    // kills the 1.5s flicker the user reported.
    if (lastLoadedId !== id) {
      loading = true;
      services = [];
    }
    error = null;
    try {
      const fresh = await getBackingServices(id);
      services = fresh;
      lastLoadedId = id;
    } catch (e: any) {
      if (lastLoadedId !== id) {
        error = e?.message || 'failed to load backing services';
      }
      // If we had a cached list for this id, keep showing it silently.
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (!orgId) return;
    // Guard against parent reassigning activeOrg to a fresh object
    // instance with the same id (shared store poll) — don't re-run the
    // effect when the observable id hasn't actually changed.
    if (orgId === lastLoadedId) return;
    load(orgId);
  });

  function statusLabel(s: BackingService): { label: string; cls: string } {
    switch (s.pod_status) {
      case 'Running':
        return { label: 'RUNNING', cls: 's-installed' };
      case 'Pending':
        return { label: 'PENDING', cls: 's-installing' };
      case 'Failed':
        return { label: 'FAILED', cls: 's-failed' };
      case 'not_found':
        // 'not_found' means total_replicas=0 — the service is known to
        // the tenant but no pod has been scheduled yet. Friendlier label
        // than the previous 'NOT FOUND' which read like a 404. #106.
        return { label: 'DEPLOYING', cls: 's-installing' };
      default:
        return { label: 'DEPLOYING', cls: 's-installing' };
    }
  }

  function toggle(slug: string) {
    revealed = { ...revealed, [slug]: !revealed[slug] };
  }
</script>

<section class="panel" class:compact>
  <header class="panel-head">
    <h2>Backing services</h2>
    <p class="sub">
      Databases, caches, and queues that run behind your apps. Status mirrors the
      live pods in your tenant. Connection details live on each app's detail page.
    </p>
  </header>

  {#if loading}
    <div class="loading">
      <div class="spinner"></div>
    </div>
  {:else if error}
    <div class="err">{error}</div>
  {:else if services.length === 0}
    <div class="empty">No backing services — your installed apps don't use a database or cache.</div>
  {:else}
    <!-- Same grid / card dimensions as the marketplace app cards (issue #106);
         the only difference is a small BACKING SERVICE pill so users can tell
         them apart at a glance. -->
    <div class="apps-grid">
      {#each services as s (s.id)}
        {@const st = statusLabel(s)}
        <div class="app-card is-service">
          <span class="app-icon" style="background: var(--color-surface-strong, #374151)">
            {s.category === 'database' ? 'DB' : s.category === 'cache' ? 'C' : 'S'}
          </span>
          <div class="app-body">
            <div class="app-top">
              <span class="app-name">{s.name}</span>
              {#if s.version}<span class="app-ver">{s.version}</span>{/if}
            </div>
            <div class="app-meta">
              <span class="pill pill-service">BACKING SERVICE</span>
              <span class="pill pill-cat">{s.category}</span>
            </div>
            <div class="app-meta">
              <span class="status-chip {st.cls}"><span class="dot"></span> {st.label}</span>
              {#if s.total_replicas > 0}
                <span class="replicas">{s.ready_replicas}/{s.total_replicas}</span>
              {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .panel { margin-top: 1.5rem; }
  .panel.compact { margin-top: 0.75rem; }
  .panel-head h2 {
    margin: 0; font-size: 0.95rem; font-weight: 600;
    color: var(--color-text-strong);
  }
  .panel-head .sub {
    margin: 0.25rem 0 0.75rem;
    font-size: 0.78rem; color: var(--color-text-dim);
    max-width: 52rem;
  }
  .loading { display: flex; justify-content: center; padding: 1rem; }
  .spinner {
    width: 1.25rem; height: 1.25rem;
    border-radius: 999px;
    border: 2px solid var(--color-accent);
    border-top-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  .err {
    padding: 0.6rem 0.75rem;
    border: 1px solid color-mix(in srgb, var(--color-danger) 40%, transparent);
    color: var(--color-danger);
    border-radius: 8px;
    font-size: 0.82rem;
  }
  .empty {
    padding: 0.8rem 1rem;
    background: var(--color-surface);
    border: 1px dashed var(--color-border);
    border-radius: 10px;
    color: var(--color-text-dim);
    font-size: 0.82rem;
  }

  /* Same dimensions as the marketplace .app-card in AppsPage.svelte so the
     backing services blend visually into the same row/column layout.
     Only visual differentiator: the BACKING SERVICE pill. #106. */
  .apps-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 0.75rem;
  }
  .app-card.is-service {
    position: relative;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    padding: 0.6rem;
    display: flex;
    align-items: stretch;
    gap: 0.75rem;
    height: 108px;
    overflow: hidden;
    color: inherit;
    opacity: 0.96;
  }
  .app-icon {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    border-radius: 10px;
    display: inline-flex; align-items: center; justify-content: center;
    flex-shrink: 0; color: #fff; font-size: 1rem; font-weight: 700;
  }
  .app-body {
    flex: 1 1 auto; display: flex; flex-direction: column; gap: 0.25rem;
    min-width: 0;
  }
  .app-top { display: flex; gap: 0.5rem; align-items: baseline; min-width: 0; }
  .app-name {
    font-size: 0.9rem; font-weight: 600; color: var(--color-text-strong);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .app-ver {
    font-size: 0.68rem;
    font-family: var(--font-mono, ui-monospace, monospace);
    color: var(--color-text-dim);
  }
  .app-meta {
    display: flex; flex-wrap: wrap; gap: 0.4rem; align-items: center;
    font-size: 0.72rem;
  }
  .pill {
    padding: 0.05rem 0.45rem; border-radius: 4px; font-size: 0.62rem;
    font-weight: 600; letter-spacing: 0.03em; text-transform: uppercase;
  }
  .pill-service {
    background: color-mix(in srgb, var(--color-accent) 15%, transparent);
    color: var(--color-accent);
  }
  .pill-cat {
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    color: var(--color-text-dim);
  }
  .replicas {
    color: var(--color-text-dim);
    font-variant-numeric: tabular-nums;
  }
  .status-chip {
    display: inline-flex; align-items: center; gap: 0.3rem;
    padding: 0.12rem 0.5rem; border-radius: 999px;
    font-size: 0.62rem; font-weight: 600; letter-spacing: 0.02em;
  }
  .status-chip .dot {
    width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block;
  }
  .s-installed { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
  .s-installing { background: color-mix(in srgb, var(--color-accent) 16%, transparent); color: var(--color-accent); }
  .s-failed { background: color-mix(in srgb, var(--color-danger) 16%, transparent); color: var(--color-danger); }
</style>
