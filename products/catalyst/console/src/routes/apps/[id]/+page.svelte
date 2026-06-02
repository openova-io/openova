<!--
  G117.3 / G117.4 — Application detail page.

  Backed by:
    GET /apps/{id}              → Application (incl. perCluster[] + endpoints[])
    GET /apps/{id}/launch-url   → Launch URL (silent OIDC)

  The Launch button opens window.open(launchURL, '_blank') with
  prompt=none&kc_idp_hint=catalyst-pin URL params per locked decision #3.
  Target time-to-tab: <500ms.
-->
<script lang="ts">
  type ClusterStatus = { cluster: string; role: 'active'|'passive'|'singleton'; status: string; hr: string; message?: string };
  type Endpoint = { name: string; hostname: string; protocol: string; ssoEnabled: boolean; launchDefault: boolean; status: string; launchURL?: string };
  type Application = {
    id: string;
    name: string;
    blueprint: string;
    org: string;
    topology: string;
    status: string;
    perCluster: ClusterStatus[];
    endpoints: Endpoint[];
  };

  let appId = $state('');
  let app = $state<Application | null>(null);
  let loading = $state(true);
  let launching = $state(false);
  let error = $state<string | null>(null);

  async function load() {
    if (typeof window !== 'undefined') {
      const m = window.location.pathname.match(/\/apps\/([^/]+)/);
      if (m) appId = m[1];
    }
    try {
      app = await fetchJSON(`/catalyst/v1/apps/${appId}`);
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  async function launch(endpointName?: string) {
    if (!app) return;
    launching = true;
    try {
      const url = endpointName
        ? `/catalyst/v1/apps/${app.id}/launch-url?endpoint=${encodeURIComponent(endpointName)}`
        : `/catalyst/v1/apps/${app.id}/launch-url`;
      const { url: target } = await fetchJSON(url);
      window.open(target, '_blank', 'noopener,noreferrer');
    } catch (e) {
      error = String(e);
    } finally {
      launching = false;
    }
  }

  async function fetchJSON(url: string): Promise<any> {
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      const fx = (window as any).__MOCK_API__;
      if (url.includes('/launch-url')) {
        return { url: `https://grafana.example.com/?prompt=none&kc_idp_hint=catalyst-pin&t=${Date.now()}`, expiresAt: new Date(Date.now() + 60000).toISOString(), endpoint: 'ui' };
      }
      if (url.match(/\/apps\/[^/]+$/)) return fx.apps[appId] ?? fx.apps['mock-app-1'];
    }
    const r = await fetch(url);
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  $effect(() => { load(); });
</script>

<section class="g117-app-detail">
  {#if loading}
    <p>Loading…</p>
  {:else if error}
    <p class="error">{error}</p>
  {:else if app}
    <header>
      <h1>{app.name}</h1>
      <span class="badge">{app.blueprint}</span>
      <span class="badge">{app.org}</span>
      <span class="badge topology">{app.topology}</span>
      <span class="status status-{app.status.toLowerCase()}">{app.status}</span>
    </header>

    <div class="actions">
      <button class="launch" on:click={() => launch()} disabled={launching}>
        {launching ? 'Launching…' : 'Launch'}
      </button>
      <a href="/apps/{app.id}/endpoints">Endpoints</a>
    </div>

    <h2>Per-cluster fan-out</h2>
    <table>
      <thead>
        <tr><th>Cluster</th><th>Role</th><th>Status</th><th>HelmRelease</th></tr>
      </thead>
      <tbody>
        {#each app.perCluster as pc}
          <tr>
            <td>{pc.cluster}</td>
            <td><code>{pc.role}</code></td>
            <td class="status-{pc.status.toLowerCase()}">{pc.status}</td>
            <td><code>{pc.hr}</code></td>
          </tr>
        {/each}
      </tbody>
    </table>

    <h2>Endpoints</h2>
    <ul class="endpoints">
      {#each app.endpoints as ep}
        <li>
          <strong>{ep.name}</strong>
          <code>{ep.hostname}</code>
          <span class="proto">{ep.protocol}</span>
          {#if ep.ssoEnabled}<span class="sso">SSO</span>{/if}
          {#if ep.ssoEnabled}
            <button on:click={() => launch(ep.name)} disabled={launching}>Launch</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .g117-app-detail { padding: 1.5rem; max-width: 960px; margin: 0 auto; }
  header { display: flex; align-items: baseline; gap: 0.75rem; flex-wrap: wrap; }
  .badge { font-size: 0.85rem; padding: 0.15rem 0.4rem; background: #eee; border-radius: 3px; }
  .badge.topology { background: #e0e7ff; color: #1d4ed8; }
  .status { padding: 0.15rem 0.4rem; border-radius: 3px; font-size: 0.85rem; }
  .status-ready { background: #d1fae5; color: #006400; }
  .status-degraded { background: #fef3c7; color: #92400e; }
  .status-failed { background: #fee2e2; color: #991b1b; }
  .actions { margin: 1rem 0; display: flex; gap: 1rem; align-items: center; }
  .launch { padding: 0.5rem 1rem; background: #1d4ed8; color: white; border: none; border-radius: 4px; cursor: pointer; }
  table { width: 100%; border-collapse: collapse; }
  th, td { padding: 0.5rem; border-bottom: 1px solid #eee; text-align: left; }
  .endpoints li { margin: 0.5rem 0; display: flex; gap: 0.75rem; align-items: center; }
  .sso { font-size: 0.75rem; padding: 0.1rem 0.3rem; background: #1d4ed8; color: white; border-radius: 2px; }
  .error { color: #b00020; }
</style>
