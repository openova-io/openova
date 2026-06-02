<!--
  G117.3 — Endpoints tab (per-Application).

  Backed by:
    GET    /apps/{id}/endpoints
    POST   /apps/{id}/endpoints
    PATCH  /apps/{id}/endpoints/{name}
    DELETE /apps/{id}/endpoints/{name}

  Each mutation opens a PR against gitea.<sov>/<org>/iac (decision #4).
  The Endpoint card shows the PR status badge until the PR auto-merges.
-->
<script lang="ts">
  type Endpoint = {
    name: string;
    hostname: string;
    hostnameTemplate?: string;
    port?: number;
    protocol: string;
    tls?: boolean;
    visibility?: string;
    ssoEnabled: boolean;
    launchDefault?: boolean;
    status: string;
    certificateStatus?: string;
    pendingPR?: { prURL: string; status: string };
  };

  let appId = $state('');
  let endpoints = $state<Endpoint[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let showAdd = $state(false);
  let editing = $state<Endpoint | null>(null);

  async function load() {
    if (typeof window !== 'undefined') {
      const m = window.location.pathname.match(/\/apps\/([^/]+)\/endpoints/);
      if (m) appId = m[1];
    }
    try {
      const res = await fetchJSON(`/catalyst/v1/apps/${appId}/endpoints`);
      endpoints = res.items ?? [];
    } catch (e) {
      error = String(e);
    } finally {
      loading = false;
    }
  }

  async function save(ep: Endpoint, isNew: boolean) {
    try {
      const url = isNew
        ? `/catalyst/v1/apps/${appId}/endpoints`
        : `/catalyst/v1/apps/${appId}/endpoints/${ep.name}`;
      const method = isNew ? 'POST' : 'PATCH';
      const pr = await mutateJSON(url, method, ep);
      ep.pendingPR = { prURL: pr.prURL, status: pr.status };
      showAdd = false; editing = null;
      await load();
    } catch (e) {
      error = String(e);
    }
  }

  async function remove(ep: Endpoint) {
    if (!confirm(`Delete endpoint ${ep.name}?`)) return;
    try {
      await mutateJSON(`/catalyst/v1/apps/${appId}/endpoints/${ep.name}`, 'DELETE', null);
      await load();
    } catch (e) {
      error = String(e);
    }
  }

  async function fetchJSON(url: string): Promise<any> {
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      return { items: (window as any).__MOCK_API__.endpoints?.[appId] ?? [] };
    }
    const r = await fetch(url);
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  async function mutateJSON(url: string, method: string, body: any): Promise<any> {
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      return { prURL: 'https://gitea.example/org/iac/pulls/42', status: 'open' };
    }
    const r = await fetch(url, {
      method,
      headers: { 'content-type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  $effect(() => { load(); });
</script>

<section class="g117-endpoints">
  <header>
    <h1>Endpoints</h1>
    <a href="/apps/{appId}">← back to Application</a>
    <button on:click={() => { showAdd = true; editing = { name: '', hostname: '', protocol: 'https', ssoEnabled: true, status: 'Pending' }; }}>+ Add endpoint</button>
  </header>

  {#if loading}
    <p>Loading…</p>
  {:else if error}
    <p class="error">{error}</p>
  {:else}
    <ul class="endpoint-list">
      {#each endpoints as ep}
        <li class="card">
          <header>
            <strong>{ep.name}</strong>
            <code>{ep.hostname}</code>
            <span class="proto">{ep.protocol}{ep.port ? ':' + ep.port : ''}</span>
            {#if ep.ssoEnabled}<span class="sso">SSO</span>{/if}
            {#if ep.launchDefault}<span class="default">default</span>{/if}
            {#if ep.tls}<span class="tls">TLS</span>{/if}
            <span class="status status-{ep.status.toLowerCase()}">{ep.status}</span>
            {#if ep.pendingPR}
              <a class="pr" href={ep.pendingPR.prURL} target="_blank" rel="noopener">PR {ep.pendingPR.status}</a>
            {/if}
          </header>
          <div class="meta">
            {#if ep.certificateStatus}cert: {ep.certificateStatus} · {/if}
            {#if ep.visibility}visibility: {ep.visibility}{/if}
          </div>
          <div class="actions">
            <button on:click={() => editing = { ...ep }}>Edit</button>
            <button class="danger" on:click={() => remove(ep)}>Delete</button>
          </div>
        </li>
      {/each}
    </ul>

    {#if showAdd || editing}
      <div class="dialog">
        <h2>{showAdd ? 'New endpoint' : 'Edit endpoint'}</h2>
        <form on:submit|preventDefault={() => save(editing!, showAdd)}>
          <label>Name <input type="text" bind:value={editing!.name} required pattern="^[a-z][a-z0-9-]{0,30}[a-z0-9]$" /></label>
          <label>Hostname <input type="text" bind:value={editing!.hostname} required /></label>
          <label>Protocol
            <select bind:value={editing!.protocol}>
              <option>https</option><option>http</option><option>grpc</option><option>tcp</option><option>udp</option>
            </select>
          </label>
          <label>Port <input type="number" bind:value={editing!.port} min="1" max="65535" /></label>
          <label><input type="checkbox" bind:checked={editing!.tls} /> TLS</label>
          <label><input type="checkbox" bind:checked={editing!.ssoEnabled} /> SSO</label>
          <label>Visibility
            <select bind:value={editing!.visibility}>
              <option value="public">public</option>
              <option value="private">private</option>
              <option value="internal">internal</option>
            </select>
          </label>
          <div class="actions">
            <button type="submit">Save (opens PR)</button>
            <button type="button" on:click={() => { showAdd = false; editing = null; }}>Cancel</button>
          </div>
        </form>
      </div>
    {/if}
  {/if}
</section>

<style>
  .g117-endpoints { padding: 1.5rem; max-width: 960px; margin: 0 auto; }
  header { display: flex; align-items: baseline; gap: 0.75rem; flex-wrap: wrap; }
  .endpoint-list { list-style: none; padding: 0; }
  .card { border: 1px solid #ddd; border-radius: 6px; padding: 1rem; margin: 0.75rem 0; }
  .card header { gap: 0.5rem; }
  .proto, .tls, .sso, .default, .pr { font-size: 0.75rem; padding: 0.1rem 0.3rem; border-radius: 3px; }
  .proto { background: #eee; }
  .tls  { background: #d1fae5; color: #006400; }
  .sso  { background: #1d4ed8; color: white; }
  .default { background: #fef3c7; color: #92400e; }
  .pr { background: #ddd6fe; color: #5b21b6; text-decoration: none; }
  .status { padding: 0.1rem 0.3rem; border-radius: 3px; font-size: 0.85rem; }
  .status-ready { background: #d1fae5; color: #006400; }
  .meta { font-size: 0.85rem; color: #666; margin: 0.5rem 0; }
  .actions { display: flex; gap: 0.5rem; }
  button { padding: 0.4rem 0.8rem; background: #1d4ed8; color: white; border: none; border-radius: 4px; cursor: pointer; }
  .danger { background: #b00020; }
  .dialog { border: 1px solid #ddd; padding: 1rem; border-radius: 6px; margin: 1rem 0; background: #fafafa; }
  .dialog label { display: block; margin: 0.5rem 0; }
  .dialog input[type="text"], .dialog input[type="number"], .dialog select { width: 100%; padding: 0.4rem; border: 1px solid #ddd; border-radius: 4px; }
  .error { color: #b00020; }
</style>
