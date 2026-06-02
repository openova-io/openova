<!--
  G117.2 — Catalog drill-down page (class/instance split).

  Backed by:
    GET /catalog/{blueprint}            → CatalogBlueprint
    GET /catalog/{blueprint}/instances  → ApplicationSummary[]

  Click on an instance row → /apps/{id}.
  Click "+ New instance" → /catalog/{blueprint}/new (topology picker).

  Locked architecture: this is NOT a single card per blueprint — it is a
  per-blueprint LIST of instances + a header summarising the Blueprint
  class. Multi-instance vs singleton drives whether "+ New" is offered.
-->
<script lang="ts">
  import { catalystApi } from '../../../lib/api/catalystApi';
  import type { ApplicationSummary, CatalogBlueprint } from '../../../lib/api/types';

  let { blueprintName = 'grafana' }: { blueprintName?: string } = $props();

  let blueprint = $state<CatalogBlueprint | null>(null);
  let instances = $state<ApplicationSummary[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function load(): Promise<void> {
    loading = true;
    error = null;
    try {
      if (typeof window !== 'undefined') {
        const m = window.location.pathname.match(/\/catalog\/([^/]+)/);
        if (m) blueprintName = m[1];
      }
      const [bp, list] = await Promise.all([
        catalystApi.getBlueprint(blueprintName),
        catalystApi.listBlueprintInstances(blueprintName),
      ]);
      blueprint = bp;
      instances = list;
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'load failed';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });
</script>

<section class="g117-catalog-drilldown" data-testid="catalog-drilldown" data-blueprint={blueprintName}>
  {#if loading}
    <p data-testid="catalog-loading">Loading {blueprintName}…</p>
  {:else if error}
    <p class="error" data-testid="catalog-error">{error}</p>
  {:else if blueprint}
    <header class="bp-header">
      <div>
        <h2>{blueprint.title}</h2>
        <p class="desc">{blueprint.description ?? ''}</p>
      </div>
      <div class="badges">
        <span class="badge">v{blueprint.version}</span>
        {#if blueprint.family}<span class="badge">{blueprint.family}</span>{/if}
        <span class="badge">
          {blueprint.multiInstance.enabled ? 'multi-instance' : 'singleton-per-Org'}
        </span>
      </div>
    </header>

    <section class="topologies">
      <h3>Supported topologies</h3>
      <ul>
        {#each blueprint.supportedTopologies as t (t)}
          <li>
            <code>{t}</code>
            {#if t === blueprint.defaultTopology}<em>(default)</em>{/if}
          </li>
        {/each}
      </ul>
    </section>

    <section class="instances">
      <header>
        <h3>Instances ({blueprint.instanceCount})</h3>
        {#if blueprint.multiInstance.enabled}
          <a
            class="btn-new"
            href={`/catalog/${blueprintName}/new`}
            data-testid="btn-new-instance"
          >
            + New instance
          </a>
        {:else if instances.length === 0}
          <a
            class="btn-new"
            href={`/catalog/${blueprintName}/new`}
            data-testid="btn-install-singleton"
          >
            Install
          </a>
        {:else}
          <span class="hint">Singleton-per-Org — already installed.</span>
        {/if}
      </header>

      {#if instances.length === 0}
        <p class="empty">No instances yet.</p>
      {:else}
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Org</th>
              <th>Topology</th>
              <th>Status</th>
              <th>Created</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each instances as inst (inst.id)}
              <tr data-testid={`instance-row-${inst.id}`}>
                <td>
                  <a href={`/apps/${inst.id}`} data-testid={`instance-link-${inst.id}`}>
                    {inst.name}
                  </a>
                </td>
                <td>{inst.org}</td>
                <td><code>{inst.topology}</code></td>
                <td>
                  <span class={`status status-${inst.status.toLowerCase()}`}>
                    {inst.status}
                  </span>
                </td>
                <td>{inst.createdAt ?? ''}</td>
                <td>
                  <a href={`/apps/${inst.id}/endpoints`}>Endpoints</a>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  {/if}
</section>

<style>
  .g117-catalog-drilldown {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
  }
  .bp-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    background: white;
    padding: 1rem 1.25rem;
    border: 1px solid #e2e8f0;
    border-radius: 6px;
  }
  .bp-header h2 {
    margin: 0;
  }
  .desc {
    color: #64748b;
    margin: 0.4rem 0 0;
  }
  .badges {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .badge {
    background: #f1f5f9;
    color: #475569;
    padding: 0.15rem 0.5rem;
    border-radius: 3px;
    font-size: 0.75rem;
  }
  .topologies ul {
    list-style: none;
    padding: 0;
    display: flex;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .topologies li code {
    background: #f1f5f9;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .instances header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .btn-new {
    display: inline-block;
    padding: 0.45rem 0.9rem;
    background: #1d4ed8;
    color: white;
    border-radius: 4px;
    text-decoration: none;
    font-size: 0.85rem;
  }
  .btn-new:hover {
    background: #1e40af;
  }
  .hint {
    color: #64748b;
    font-style: italic;
    font-size: 0.85rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 0.5rem;
    background: white;
  }
  th,
  td {
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid #e2e8f0;
    text-align: left;
    font-size: 0.9rem;
  }
  .status {
    font-size: 0.75rem;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .status-ready {
    background: #d1fae5;
    color: #047857;
  }
  .status-pending,
  .status-reconciling {
    background: #fef3c7;
    color: #b45309;
  }
  .status-failed,
  .status-degraded {
    background: #fee2e2;
    color: #b00020;
  }
  .empty {
    color: #64748b;
    font-style: italic;
  }
  .error {
    color: #b00020;
  }
</style>
