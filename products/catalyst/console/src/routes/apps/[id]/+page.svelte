<!--
  G117.3 / G117.4 — Application detail page with tabbed view.

  Backed by:
    GET /apps/{id}             → Application (perCluster[] + endpoints[])

  Tabs (locked architecture):
    [Overview] [Endpoints] [Storage] [Secrets] [Events] [YAML]

  Wave-1 ships Overview + Endpoints. Storage/Secrets/Events/YAML render
  "coming in Wave-2" cards but are present in the tab strip so the
  navigation shape is locked from day one.

  Launch button (G117.4) lives on the Overview tab and on each endpoint
  card in the Endpoints tab. Replaces the raw endpoint URL display.
-->
<script lang="ts">
  import { catalystApi } from '../../../lib/api/catalystApi';
  import LaunchButton from '../../../components/LaunchButton.svelte';
  import EndpointsTab from '../../../components/EndpointsTab.svelte';
  import type { Application } from '../../../lib/api/types';

  let { appId = '' }: { appId?: string } = $props();

  let app = $state<Application | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let tab = $state<'overview' | 'endpoints' | 'storage' | 'secrets' | 'events' | 'yaml'>(
    'overview',
  );

  async function load(): Promise<void> {
    loading = true;
    try {
      if (typeof window !== 'undefined') {
        const m = window.location.pathname.match(/\/apps\/([^/]+)/);
        if (m) appId = m[1];
      }
      app = await catalystApi.getApp(appId);
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'load failed';
    } finally {
      loading = false;
    }
  }

  function isLaunchable(): boolean {
    return !!app?.endpoints?.some((e) => e.ssoEnabled);
  }

  function launchDefaultEndpoint(): string | undefined {
    return app?.endpoints?.find((e) => e.launchDefault && e.ssoEnabled)?.name;
  }

  $effect(() => {
    load();
  });
</script>

<section class="g117-app-detail" data-testid="app-detail" data-app-id={appId}>
  {#if loading}
    <p>Loading…</p>
  {:else if error}
    <p class="error" data-testid="app-detail-error">{error}</p>
  {:else if app}
    <header class="app-header">
      <div>
        <h2>{app.name}</h2>
        <p class="meta">
          <span class="badge bp">{app.blueprint}</span>
          <span class="badge org">{app.org}</span>
          <span class="badge topology">{app.topology}</span>
          <span class={`status status-${app.status.toLowerCase()}`}>{app.status}</span>
        </p>
      </div>
      {#if isLaunchable()}
        <LaunchButton
          {appId}
          endpointName={launchDefaultEndpoint()}
          label="Launch"
        />
      {/if}
    </header>

    <div class="tabs" role="tablist" aria-label="Application sections">
      {#each ['overview', 'endpoints', 'storage', 'secrets', 'events', 'yaml'] as t (t)}
        <button
          type="button"
          role="tab"
          aria-selected={tab === t}
          class:active={tab === t}
          data-testid={`tab-${t}`}
          onclick={() => (tab = t as typeof tab)}
        >
          {t[0].toUpperCase() + t.slice(1)}
        </button>
      {/each}
    </div>

    <div class="tab-body" role="tabpanel">
      {#if tab === 'overview'}
        <section data-testid="tab-body-overview">
          <h3>Per-cluster fan-out</h3>
          {#if app.perCluster && app.perCluster.length > 0}
            <table>
              <thead>
                <tr>
                  <th>Cluster</th>
                  <th>Role</th>
                  <th>Status</th>
                  <th>HelmRelease</th>
                  <th>Message</th>
                </tr>
              </thead>
              <tbody>
                {#each app.perCluster as pc (pc.cluster)}
                  <tr>
                    <td>{pc.cluster}</td>
                    <td><code>{pc.role}</code></td>
                    <td>
                      <span class={`status status-${pc.status.toLowerCase()}`}>
                        {pc.status}
                      </span>
                    </td>
                    <td>{pc.hr ? `${pc.hr}` : ''}</td>
                    <td class="msg">{pc.message ?? ''}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {:else}
            <p class="empty">No clusters yet.</p>
          {/if}

          <h3>Endpoints</h3>
          {#if app.endpoints && app.endpoints.length > 0}
            <ul class="endpoint-summary">
              {#each app.endpoints as ep (ep.name)}
                <li>
                  <strong>{ep.name}</strong>
                  <code>{ep.hostname}</code>
                  <span class="proto">{ep.protocol}</span>
                  {#if ep.ssoEnabled && ep.status === 'Ready'}
                    <LaunchButton
                      {appId}
                      endpointName={ep.name}
                      label="Launch"
                    />
                  {:else if !ep.ssoEnabled}
                    <a
                      class="raw-link"
                      href={`${ep.tls === false ? 'http' : 'https'}://${ep.hostname}`}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      Open
                    </a>
                  {:else}
                    <span class="hint">waiting: {ep.status}</span>
                  {/if}
                </li>
              {/each}
            </ul>
          {:else}
            <p class="empty">No endpoints.</p>
          {/if}
        </section>
      {:else if tab === 'endpoints'}
        <EndpointsTab {appId} initialEndpoints={app.endpoints ?? []} />
      {:else}
        <section data-testid={`tab-body-${tab}`}>
          <p class="empty">
            <strong>{tab}</strong> tab — coming in Wave-2.
          </p>
        </section>
      {/if}
    </div>
  {/if}
</section>

<style>
  .g117-app-detail {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .app-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    background: white;
    border: 1px solid #e2e8f0;
    border-radius: 6px;
    padding: 1rem 1.25rem;
  }
  .app-header h2 {
    margin: 0;
  }
  .meta {
    display: flex;
    gap: 0.4rem;
    margin: 0.4rem 0 0;
    flex-wrap: wrap;
  }
  .badge {
    background: #f1f5f9;
    color: #475569;
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
    font-size: 0.75rem;
  }
  .badge.topology {
    background: #ddd6fe;
    color: #5b21b6;
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
  .status-reconciling,
  .status-provisioning {
    background: #fef3c7;
    color: #b45309;
  }
  .status-failed,
  .status-degraded,
  .status-uninstalling {
    background: #fee2e2;
    color: #b00020;
  }
  .tabs {
    display: flex;
    gap: 0.25rem;
    border-bottom: 1px solid #e2e8f0;
  }
  .tabs button {
    padding: 0.45rem 0.9rem;
    background: transparent;
    color: #64748b;
    border: none;
    border-bottom: 2px solid transparent;
    cursor: pointer;
    font-size: 0.85rem;
  }
  .tabs button:hover {
    color: #1d4ed8;
  }
  .tabs button.active {
    color: #1d4ed8;
    border-bottom-color: #1d4ed8;
    font-weight: 500;
  }
  .tab-body {
    background: white;
    border: 1px solid #e2e8f0;
    border-top: none;
    border-radius: 0 0 6px 6px;
    padding: 1rem 1.25rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    padding: 0.45rem 0.75rem;
    border-bottom: 1px solid #e2e8f0;
    text-align: left;
    font-size: 0.85rem;
  }
  .msg {
    color: #64748b;
    font-style: italic;
  }
  .endpoint-summary {
    list-style: none;
    padding: 0;
  }
  .endpoint-summary li {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    padding: 0.4rem 0;
    flex-wrap: wrap;
  }
  .endpoint-summary code {
    background: #f1f5f9;
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
  }
  .proto {
    background: #eee;
    font-size: 0.7rem;
    padding: 0.1rem 0.3rem;
    border-radius: 3px;
  }
  .raw-link {
    padding: 0.3rem 0.7rem;
    background: #f1f5f9;
    color: #1d4ed8;
    border-radius: 3px;
    text-decoration: none;
    font-size: 0.8rem;
  }
  .hint {
    color: #64748b;
    font-style: italic;
    font-size: 0.8rem;
  }
  .empty {
    color: #64748b;
    font-style: italic;
  }
  .error {
    color: #b00020;
  }
</style>
