<!--
  G117.3 — Endpoints tab.

  Per-Application endpoint CRUD UI. Every mutation hits the catalyst-api
  endpoint-CRUD routes which open a PR against gitea.<sov>/<org>/iac
  (locked decision #4). The UI shows a confirmation dialog about the
  PR-auto-merge consequence on every Create/Update/Delete.

  Props:
    appId — Application UID
    initialEndpoints — server-rendered first list (avoid initial render
                       loading-spinner flicker; falls back to live fetch)

  Children of /apps/{id}/endpoints/+page.svelte but also embeddable as a tab
  in /apps/{id}/+page.svelte via the tabbed AppDetail view.
-->
<script lang="ts">
  import { catalystApi } from '../lib/api/catalystApi';
  import LaunchButton from './LaunchButton.svelte';
  import type {
    EndpointMutationRequest,
    EndpointProtocol,
    EndpointVisibility,
    ResolvedEndpoint,
  } from '../lib/api/types';

  let {
    appId,
    initialEndpoints = [],
  }: {
    appId: string;
    initialEndpoints?: ResolvedEndpoint[];
  } = $props();

  // NOTE: `initialEndpoints` is a prop; $state(initialEndpoints) captures
  // only the initial value (Svelte warning state_referenced_locally). That
  // matches our intent — the prop is a server-rendered first cut; subsequent
  // reactivity flows through the locally-owned `endpoints` $state via load().
  let initial = initialEndpoints;
  let endpoints = $state<ResolvedEndpoint[]>(initial);
  let loading = $state(initial.length === 0);
  let error = $state<string | null>(null);
  let dialogMode = $state<'closed' | 'create' | 'edit' | 'delete'>('closed');
  let working = $state<EndpointMutationRequest>({ name: '' });
  let editingName = $state<string | null>(null);
  let deleteTarget = $state<string | null>(null);
  let lastPRUrl = $state<string | null>(null);

  async function load(): Promise<void> {
    loading = true;
    error = null;
    try {
      endpoints = await catalystApi.listAppEndpoints(appId);
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'load failed';
    } finally {
      loading = false;
    }
  }

  function openCreate(): void {
    working = {
      name: '',
      hostname: '',
      port: 443,
      protocol: 'https',
      tls: true,
      visibility: 'public',
      ssoEnabled: true,
    };
    editingName = null;
    dialogMode = 'create';
  }

  function openEdit(ep: ResolvedEndpoint): void {
    working = {
      name: ep.name,
      hostname: ep.hostname,
      port: ep.port,
      protocol: ep.protocol,
      tls: ep.tls,
      visibility: ep.visibility,
      ssoEnabled: ep.ssoEnabled,
    };
    editingName = ep.name;
    dialogMode = 'edit';
  }

  function openDelete(name: string): void {
    deleteTarget = name;
    dialogMode = 'delete';
  }

  function cancel(): void {
    dialogMode = 'closed';
    editingName = null;
    deleteTarget = null;
  }

  async function submit(): Promise<void> {
    error = null;
    try {
      const pr =
        dialogMode === 'edit' && editingName
          ? await catalystApi.patchAppEndpoint(appId, editingName, working)
          : await catalystApi.createAppEndpoint(appId, working);
      lastPRUrl = pr.prURL;
      dialogMode = 'closed';
      editingName = null;
      await load();
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'mutation failed';
    }
  }

  async function confirmDelete(): Promise<void> {
    if (!deleteTarget) return;
    error = null;
    try {
      const pr = await catalystApi.deleteAppEndpoint(appId, deleteTarget);
      lastPRUrl = pr.prURL;
      dialogMode = 'closed';
      deleteTarget = null;
      await load();
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'delete failed';
    }
  }

  const PROTOCOLS: EndpointProtocol[] = ['https', 'http', 'grpc', 'tcp', 'udp'];
  const VISIBILITIES: EndpointVisibility[] = ['public', 'private', 'internal'];

  $effect(() => {
    if (initial.length === 0) load();
  });
</script>

<section class="endpoints-tab" data-testid="endpoints-tab">
  <header class="tab-header">
    <h2>Endpoints ({endpoints.length})</h2>
    <button
      type="button"
      class="primary"
      data-testid="endpoint-new"
      onclick={openCreate}
    >
      + New endpoint
    </button>
  </header>

  {#if lastPRUrl}
    <p class="pr-banner" data-testid="endpoint-pr-banner">
      Change submitted as PR
      <a href={lastPRUrl} target="_blank" rel="noopener noreferrer">{lastPRUrl}</a>
      — auto-merges on green Kyverno + cert-manager + DNS-conflict checks.
    </p>
  {/if}

  {#if loading}
    <p>Loading endpoints…</p>
  {:else if error}
    <p class="error" data-testid="endpoints-error">{error}</p>
  {:else if endpoints.length === 0}
    <p class="empty">No endpoints. Click "+ New endpoint" to add one.</p>
  {:else}
    <ul class="endpoint-list">
      {#each endpoints as ep (ep.name)}
        <li class="endpoint-card" data-testid={`endpoint-${ep.name}`}>
          <div class="card-head">
            <strong>{ep.name}</strong>
            <code class="hostname">{ep.hostname}</code>
            <span class="proto">{ep.protocol}{ep.port ? ':' + ep.port : ''}</span>
            {#if ep.tls}<span class="tls">TLS</span>{/if}
            {#if ep.ssoEnabled}<span class="sso">SSO</span>{/if}
            {#if ep.launchDefault}<span class="default">default</span>{/if}
            <span class={`status status-${ep.status.toLowerCase()}`}>{ep.status}</span>
          </div>
          {#if ep.certificateStatus}
            <div class="meta">cert: {ep.certificateStatus} · visibility: {ep.visibility ?? 'public'}</div>
          {/if}
          {#if ep.pendingPR}
            <div class="meta">
              Pending PR:
              <a href={ep.pendingPR.prURL} target="_blank" rel="noopener noreferrer">
                {ep.pendingPR.status}
              </a>
            </div>
          {/if}
          <div class="actions">
            {#if ep.ssoEnabled && ep.status === 'Ready'}
              <LaunchButton {appId} endpointName={ep.name} />
            {:else if !ep.ssoEnabled}
              <a
                class="raw-link"
                href={`${ep.tls === false ? 'http' : 'https'}://${ep.hostname}`}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open endpoint
              </a>
            {/if}
            <button type="button" onclick={() => openEdit(ep)}>Edit</button>
            <button type="button" class="danger" onclick={() => openDelete(ep.name)}>
              Delete
            </button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  {#if dialogMode === 'create' || dialogMode === 'edit'}
    <div class="dialog" role="dialog" aria-modal="true" data-testid="endpoint-dialog">
      <h3>
        {dialogMode === 'create' ? 'New endpoint' : `Edit endpoint ${editingName}`}
      </h3>
      <p class="consequence">
        Submitting this opens a <strong>PR</strong> against
        <code>gitea.&lt;sov&gt;/&lt;org&gt;/iac</code>. Kyverno + cert-manager +
        DNS-conflict checks gate auto-merge. If a check fails, the PR stays open
        for operator review.
      </p>
      <form
        onsubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label>
          Name
          <input
            type="text"
            required
            pattern="^[a-z][a-z0-9-]{0,30}[a-z0-9]$"
            bind:value={working.name}
            disabled={dialogMode === 'edit'}
          />
        </label>
        <label>
          Hostname
          <input type="text" required bind:value={working.hostname} />
        </label>
        <label>
          Protocol
          <select bind:value={working.protocol}>
            {#each PROTOCOLS as p (p)}<option value={p}>{p}</option>{/each}
          </select>
        </label>
        <label>
          Port
          <input type="number" min="1" max="65535" bind:value={working.port} />
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={working.tls} /> TLS
        </label>
        <label class="check">
          <input type="checkbox" bind:checked={working.ssoEnabled} /> SSO enabled
        </label>
        <label>
          Visibility
          <select bind:value={working.visibility}>
            {#each VISIBILITIES as v (v)}<option value={v}>{v}</option>{/each}
          </select>
        </label>
        <div class="dialog-actions">
          <button type="submit" class="primary" data-testid="endpoint-save">
            Save — open PR
          </button>
          <button type="button" onclick={cancel}>Cancel</button>
        </div>
      </form>
    </div>
  {:else if dialogMode === 'delete'}
    <div class="dialog" role="dialog" aria-modal="true" data-testid="endpoint-delete-dialog">
      <h3>Delete endpoint {deleteTarget}?</h3>
      <p class="consequence">
        This opens a PR against <code>gitea.&lt;sov&gt;/&lt;org&gt;/iac</code>
        removing the endpoint manifest. The cert-manager Certificate +
        Gateway HTTPRoute are removed on merge.
      </p>
      <div class="dialog-actions">
        <button
          type="button"
          class="danger"
          onclick={confirmDelete}
          data-testid="endpoint-delete-confirm"
        >
          Delete — open PR
        </button>
        <button type="button" onclick={cancel}>Cancel</button>
      </div>
    </div>
  {/if}
</section>

<style>
  .endpoints-tab {
    margin-top: 1rem;
  }
  .tab-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }
  h2 {
    margin: 0;
    font-size: 1.1rem;
  }
  button {
    padding: 0.4rem 0.8rem;
    background: #e2e8f0;
    color: #0f172a;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.85rem;
  }
  button.primary {
    background: #1d4ed8;
    color: white;
  }
  button.danger {
    background: #b00020;
    color: white;
  }
  .endpoint-list {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .endpoint-card {
    border: 1px solid #e2e8f0;
    border-radius: 6px;
    padding: 0.75rem 1rem;
    margin: 0.5rem 0;
    background: white;
  }
  .card-head {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: center;
  }
  .hostname {
    background: #f1f5f9;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .proto,
  .tls,
  .sso,
  .default {
    font-size: 0.7rem;
    padding: 0.1rem 0.35rem;
    border-radius: 3px;
  }
  .proto {
    background: #eee;
  }
  .tls {
    background: #d1fae5;
    color: #047857;
  }
  .sso {
    background: #1d4ed8;
    color: white;
  }
  .default {
    background: #fef3c7;
    color: #b45309;
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
  .status-provisioning {
    background: #fef3c7;
    color: #b45309;
  }
  .status-failed,
  .status-degraded {
    background: #fee2e2;
    color: #b00020;
  }
  .meta {
    font-size: 0.8rem;
    color: #64748b;
    margin: 0.4rem 0;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .raw-link {
    padding: 0.4rem 0.8rem;
    background: #f1f5f9;
    color: #1d4ed8;
    border-radius: 4px;
    text-decoration: none;
    font-size: 0.85rem;
  }
  .dialog {
    border: 1px solid #e2e8f0;
    background: #f8fafc;
    padding: 1rem;
    border-radius: 6px;
    margin-top: 1rem;
  }
  .dialog h3 {
    margin-top: 0;
  }
  .consequence {
    background: #fef3c7;
    border-left: 3px solid #b45309;
    padding: 0.5rem 0.75rem;
    font-size: 0.85rem;
    border-radius: 3px;
  }
  .dialog label {
    display: block;
    margin: 0.5rem 0;
    font-size: 0.85rem;
  }
  .dialog label.check {
    display: inline-flex;
    gap: 0.3rem;
    margin-right: 1rem;
  }
  .dialog input[type='text'],
  .dialog input[type='number'],
  .dialog select {
    width: 100%;
    padding: 0.4rem;
    border: 1px solid #cbd5e1;
    border-radius: 4px;
    margin-top: 0.2rem;
  }
  .dialog-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }
  .pr-banner {
    background: #ddd6fe;
    color: #5b21b6;
    padding: 0.5rem 0.75rem;
    border-radius: 4px;
    font-size: 0.85rem;
  }
  .pr-banner a {
    color: #5b21b6;
    font-weight: 600;
  }
  .empty {
    color: #64748b;
    font-style: italic;
  }
  .error {
    color: #b00020;
  }
</style>
