<script lang="ts">
  // Admin-side mirror of the console's BackingServices panel. Renders the
  // same catalog+kube-API join, but hits the superadmin endpoint so cross-
  // tenant inspection works without impersonating the owner. Kept visually
  // aligned with the console so an operator can recognize the same data.
  import { getAdminBackingServices, type BackingService } from '../lib/api';

  let { tenantId }: { tenantId: string } = $props();

  let services = $state<BackingService[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function load(id: string) {
    loading = true;
    error = null;
    try {
      services = await getAdminBackingServices(id);
    } catch (e: any) {
      error = e?.message || 'failed to load backing services';
      services = [];
    }
    loading = false;
  }

  $effect(() => {
    if (!tenantId) return;
    load(tenantId);
  });

  function statusLabel(s: BackingService): { label: string; cls: string } {
    switch (s.pod_status) {
      case 'Running':
        return { label: 'RUNNING', cls: 's-ok' };
      case 'Pending':
        return { label: 'PENDING', cls: 's-pending' };
      case 'Failed':
        return { label: 'FAILED', cls: 's-fail' };
      case 'not_found':
        return { label: 'NOT FOUND', cls: 's-fail' };
      default:
        return { label: 'UNKNOWN', cls: 's-unknown' };
    }
  }
</script>

<div class="wrap">
  {#if loading}
    <div class="loading"><div class="spinner"></div></div>
  {:else if error}
    <div class="err">{error}</div>
  {:else if services.length === 0}
    <div class="empty">No backing services.</div>
  {:else}
    <table class="tbl">
      <thead>
        <tr>
          <th>Service</th>
          <th>Version</th>
          <th>Status</th>
          <th>Replicas</th>
          <th>Endpoint</th>
          <th>Image</th>
        </tr>
      </thead>
      <tbody>
        {#each services as s (s.id)}
          {@const st = statusLabel(s)}
          <tr>
            <td><span class="name">{s.name}</span> <span class="cat">{s.category}</span></td>
            <td>{s.version || '—'}</td>
            <td><span class="chip {st.cls}">{st.label}</span></td>
            <td class="num">{s.ready_replicas}/{s.total_replicas}</td>
            <td><code>{s.endpoint_host}:{s.endpoint_port}</code></td>
            <td><code class="dim">{s.image || '—'}</code></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .wrap { padding: 0.25rem 0; }
  .loading { display: flex; justify-content: center; padding: 0.75rem; }
  .spinner {
    width: 1rem; height: 1rem;
    border-radius: 999px;
    border: 2px solid var(--color-accent);
    border-top-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  .err {
    padding: 0.5rem 0.75rem;
    color: var(--color-danger);
    font-size: 0.8rem;
  }
  .empty {
    padding: 0.6rem 0.75rem;
    color: var(--color-text-dim);
    font-size: 0.8rem;
    font-style: italic;
  }
  .tbl { width: 100%; font-size: 0.8rem; }
  .tbl th {
    text-align: left;
    font-weight: 500;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--color-text-dim);
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid var(--color-border);
  }
  .tbl td {
    padding: 0.5rem 0.6rem;
    border-bottom: 1px solid var(--color-border);
    color: var(--color-text);
  }
  .tbl tr:last-child td { border-bottom: 0; }
  .name { font-weight: 600; color: var(--color-text-strong); }
  .cat {
    margin-left: 0.35rem;
    font-size: 0.68rem;
    color: var(--color-text-dim);
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    padding: 0.05rem 0.35rem;
    border-radius: 3px;
    text-transform: capitalize;
  }
  .num { font-variant-numeric: tabular-nums; color: var(--color-text-dim); }
  code {
    font-size: 0.78rem;
    font-family: var(--font-mono, ui-monospace, monospace);
    color: var(--color-text);
  }
  code.dim { color: var(--color-text-dim); }
  .chip {
    display: inline-flex; align-items: center;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    letter-spacing: 0.04em;
  }
  .s-ok { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
  .s-pending { background: color-mix(in srgb, var(--color-accent) 16%, transparent); color: var(--color-accent); }
  .s-fail { background: color-mix(in srgb, var(--color-danger) 16%, transparent); color: var(--color-danger); }
  .s-unknown { background: color-mix(in srgb, var(--color-text-dim) 16%, transparent); color: var(--color-text-dim); }
</style>
