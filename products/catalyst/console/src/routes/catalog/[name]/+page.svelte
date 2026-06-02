<!--
  G117.2 — Catalog drill-down page.

  Backed by:
    GET /catalog/{blueprint}            → CatalogBlueprint
    GET /catalog/{blueprint}/instances  → ApplicationSummary[]

  Wave-1 (G117.2) task: replace the mock fetch with the real catalyst-api
  client (see ../../../lib/api/catalystApi.ts which the same agent should
  add to mirror docs/api/catalyst-api-openapi.yaml).

  Mock data shape: products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml
-->
<script lang="ts">
  // SvelteKit-style $page store fallback for non-SvelteKit (Astro) runners.
  // When ported to SvelteKit proper, replace with `import { page } from '$app/stores'`.
  type Endpoint = { name: string; hostnameTemplate: string; protocol: string; ssoEnabled: boolean; launchDefault: boolean };
  type CatalogBlueprint = {
    name: string;
    version: string;
    title: string;
    description: string;
    family: string;
    instanceCount: number;
    supportedTopologies: string[];
    defaultTopology: string;
    multiInstance: { enabled: boolean; maxPerOrg?: number };
    endpoints?: Endpoint[];
  };
  type Instance = { id: string; name: string; org: string; topology: string; status: string; createdAt: string };

  // In a real SvelteKit page this is `export let data` from +page.ts.
  // In the mock harness we read from window.__MOCK__ injected by the dev shim.
  let blueprintName = $state('grafana');
  let blueprint = $state<CatalogBlueprint | null>(null);
  let instances = $state<Instance[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function load() {
    loading = true;
    try {
      // Resolve route param. In SvelteKit this is `params.name`; here we
      // read it from the URL or a globally-injected mock context.
      if (typeof window !== 'undefined') {
        const m = window.location.pathname.match(/\/catalog\/([^/]+)/);
        if (m) blueprintName = m[1];
      }
      const bp = await fetchJSON(`/catalyst/v1/catalog/${blueprintName}`);
      const list = await fetchJSON(`/catalyst/v1/catalog/${blueprintName}/instances`);
      blueprint = bp;
      instances = list.items ?? [];
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  async function fetchJSON(url: string): Promise<any> {
    // MOCK_API mode: read from globally-injected fixture.
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      const fixture = (window as any).__MOCK_API__;
      if (url.endsWith(`/catalog/${blueprintName}`)) return fixture.blueprints[blueprintName];
      if (url.endsWith(`/instances`)) return { items: fixture.instances[blueprintName] ?? [] };
    }
    const r = await fetch(url, { headers: { accept: 'application/json' } });
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  $effect(() => { load(); });
</script>

<section class="g117-catalog-drilldown">
  {#if loading}
    <p>Loading {blueprintName}…</p>
  {:else if error}
    <p class="error">{error}</p>
  {:else if blueprint}
    <header>
      <h1>{blueprint.title}</h1>
      <span class="version">v{blueprint.version}</span>
      <span class="family">{blueprint.family}</span>
    </header>
    <p>{blueprint.description}</p>

    <h2>Supported topologies</h2>
    <ul>
      {#each blueprint.supportedTopologies as t}
        <li>
          <code>{t}</code>
          {#if t === blueprint.defaultTopology}<em>(default)</em>{/if}
        </li>
      {/each}
    </ul>

    <h2>Instances ({blueprint.instanceCount})</h2>
    {#if blueprint.multiInstance.enabled}
      <a class="btn-new" href="/catalog/{blueprintName}/new">+ New instance</a>
    {:else if instances.length === 0}
      <a class="btn-new" href="/catalog/{blueprintName}/new">Install</a>
    {:else}
      <p><em>This Blueprint is singleton-per-Org.</em></p>
    {/if}

    {#if instances.length > 0}
      <table>
        <thead>
          <tr>
            <th>Name</th><th>Org</th><th>Topology</th><th>Status</th><th>Created</th><th></th>
          </tr>
        </thead>
        <tbody>
          {#each instances as inst}
            <tr>
              <td><a href="/apps/{inst.id}">{inst.name}</a></td>
              <td>{inst.org}</td>
              <td><code>{inst.topology}</code></td>
              <td>{inst.status}</td>
              <td>{inst.createdAt}</td>
              <td><a href="/apps/{inst.id}/endpoints">Endpoints</a></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}
</section>

<style>
  .g117-catalog-drilldown { padding: 1.5rem; max-width: 960px; margin: 0 auto; }
  header { display: flex; align-items: baseline; gap: 1rem; }
  .version, .family { font-size: 0.85rem; color: #666; }
  table { width: 100%; border-collapse: collapse; margin-top: 1rem; }
  th, td { padding: 0.5rem; border-bottom: 1px solid #eee; text-align: left; }
  .btn-new { display: inline-block; padding: 0.4rem 0.8rem; background: #1d4ed8; color: white; border-radius: 4px; text-decoration: none; }
  .error { color: #b00020; }
</style>
