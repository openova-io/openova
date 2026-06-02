<!--
  G117.2 — New-instance dialog (topology picker).

  Backed by:
    POST /apps/instances    → Application

  Topology radio is pre-selected per Sovereign shape:
    - len(regions) > 1  → CatalogBlueprint.defaultTopology (multi-region default)
    - else              → singleton (or Blueprint's single-region default)

  Wave-1 (G117.2): wire to real catalyst-api client; pull regions count
  from /sovereign/info; gate maxPerOrg before submit.
-->
<script lang="ts">
  type CatalogBlueprint = {
    name: string;
    title: string;
    supportedTopologies: string[];
    defaultTopology: string;
    multiInstance: { enabled: boolean; maxPerOrg?: number };
  };

  let blueprintName = $state('grafana');
  let blueprint = $state<CatalogBlueprint | null>(null);
  let instanceName = $state('');
  let org = $state('');
  let topology = $state('');
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let toast = $state<string | null>(null);

  async function load() {
    if (typeof window !== 'undefined') {
      const m = window.location.pathname.match(/\/catalog\/([^/]+)\/new/);
      if (m) blueprintName = m[1];
    }
    blueprint = await fetchJSON(`/catalyst/v1/catalog/${blueprintName}`);
    if (blueprint) topology = blueprint.defaultTopology;
  }

  async function submit() {
    if (!instanceName || !org || !topology || !blueprint) {
      error = 'Fill all fields'; return;
    }
    submitting = true;
    error = null;
    try {
      const body = { blueprint: blueprint.name, org, name: instanceName, topology };
      const app = await postJSON('/catalyst/v1/apps/instances', body);
      toast = `Instance ${app.id} created`;
      // Navigate to the new instance.
      window.location.href = `/apps/${app.id}`;
    } catch (e) {
      error = String(e);
    } finally {
      submitting = false;
    }
  }

  async function fetchJSON(url: string): Promise<any> {
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      return (window as any).__MOCK_API__.blueprints[blueprintName];
    }
    const r = await fetch(url);
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  async function postJSON(url: string, body: any): Promise<any> {
    if (typeof window !== 'undefined' && (window as any).__MOCK_API__) {
      return { id: 'mock-' + crypto.randomUUID(), ...body, status: 'Pending' };
    }
    const r = await fetch(url, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    if (!r.ok) throw new Error(`${url} → ${r.status}`);
    return await r.json();
  }

  $effect(() => { load(); });
</script>

<section class="g117-new-instance">
  <header>
    <h1>New {blueprint?.title ?? blueprintName} instance</h1>
    <a href="/catalog/{blueprintName}">← back</a>
  </header>

  {#if !blueprint}
    <p>Loading…</p>
  {:else}
    <form on:submit|preventDefault={submit}>
      <label>
        Instance name
        <input type="text" bind:value={instanceName} placeholder="e.g. metrics-primary" required pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$" />
      </label>

      <label>
        Organization slug
        <input type="text" bind:value={org} placeholder="e.g. acme" required pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$" />
      </label>

      <fieldset>
        <legend>Topology</legend>
        {#each blueprint.supportedTopologies as t}
          <label class="radio">
            <input type="radio" bind:group={topology} value={t} />
            <code>{t}</code>
            {#if t === blueprint.defaultTopology}<em>(default for this Sovereign)</em>{/if}
          </label>
        {/each}
      </fieldset>

      {#if error}<p class="error">{error}</p>{/if}
      {#if toast}<p class="ok">{toast}</p>{/if}

      <button type="submit" disabled={submitting}>{submitting ? 'Creating…' : 'Create'}</button>
    </form>
  {/if}
</section>

<style>
  .g117-new-instance { padding: 1.5rem; max-width: 640px; margin: 0 auto; }
  header { display: flex; align-items: baseline; justify-content: space-between; }
  form label { display: block; margin: 0.75rem 0; }
  input[type="text"] { width: 100%; padding: 0.5rem; border: 1px solid #ddd; border-radius: 4px; }
  fieldset { border: 1px solid #ddd; padding: 1rem; border-radius: 4px; }
  .radio { display: block; margin: 0.25rem 0; }
  button { padding: 0.5rem 1rem; background: #1d4ed8; color: white; border: none; border-radius: 4px; cursor: pointer; }
  .error { color: #b00020; }
  .ok { color: #006400; }
</style>
