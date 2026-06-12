<!--
  G117.2 — New-instance dialog with topology picker.

  Backed by:
    GET  /catalog/{blueprint}  → CatalogBlueprint
    POST /apps/instances       → Application

  The topology picker honors spec.topology.supported (locked architecture).
  Auto-selects default per Sovereign region count (locked decision #7).

  The new-instance dialog accepts:
    - custom name      (per-Org Application name)
    - org              (Organization slug)
    - topology         (radio over supported set)
    - endpoint hostname override (optional; falls back to Blueprint template)
-->
<script lang="ts">
  import { catalystApi } from '../../../../lib/api/catalystApi';
  import TopologyPicker from '../../../../components/TopologyPicker.svelte';
  import PlacementSelector from '../../../../components/PlacementSelector.svelte';
  import type {
    BcpTopology,
    CatalogBlueprint,
    InstancePlacement,
  } from '../../../../lib/api/types';

  let {
    blueprintName = 'grafana',
    regionsCount = 1,
  }: { blueprintName?: string; regionsCount?: number } = $props();

  let blueprint = $state<CatalogBlueprint | null>(null);
  let instanceName = $state('');
  let org = $state('');
  let topology = $state<BcpTopology | ''>('');
  // #3373 — null until the user changes advanced placement away from the
  // Blueprint defaults; then it carries only the changed fields.
  let placement = $state<InstancePlacement | null>(null);
  let endpointHostname = $state('');
  let submitting = $state(false);
  let error = $state<string | null>(null);

  async function load(): Promise<void> {
    if (typeof window !== 'undefined') {
      const m = window.location.pathname.match(/\/catalog\/([^/]+)\/new/);
      if (m) blueprintName = m[1];
    }
    try {
      blueprint = await catalystApi.getBlueprint(blueprintName);
    } catch (e: unknown) {
      error = (e as { message?: string }).message ?? 'load failed';
    }
  }

  async function submit(): Promise<void> {
    if (!blueprint) return;
    if (!instanceName || !org || !topology) {
      error = 'Fill name, org, and topology';
      return;
    }
    submitting = true;
    error = null;
    try {
      const values: Record<string, unknown> = {};
      if (endpointHostname) {
        // Pass-through into the per-Blueprint values map; per-Blueprint
        // configSchema (G117.1) gates which keys are accepted.
        values.endpoints = [{ name: 'ui', hostname: endpointHostname }];
      }
      const app = await catalystApi.createInstance({
        blueprint: blueprint.name,
        org,
        name: instanceName,
        topology: topology || undefined,
        values: Object.keys(values).length > 0 ? values : undefined,
        // Omitted entirely when untouched — silent-accept of Blueprint
        // placement defaults (#3373).
        placement: placement ?? undefined,
      });
      if (typeof window !== 'undefined') {
        window.location.href = `/apps/${app.id}`;
      }
    } catch (e: unknown) {
      const err = e as { code?: string; message?: string };
      error =
        err.code === 'conflict'
          ? `${err.message} — switch to a different Org or pick another instance name.`
          : err.message ?? 'create failed';
    } finally {
      submitting = false;
    }
  }

  $effect(() => {
    load();
  });
</script>

<section class="g117-new-instance" data-testid="new-instance-form">
  {#if !blueprint}
    <p>Loading…</p>
  {:else}
    <p class="meta">
      Blueprint <code>{blueprint.name}</code> v{blueprint.version}
      {#if blueprint.multiInstance.enabled}
        · multi-instance (max {blueprint.multiInstance.maxPerOrg ?? '∞'} per Org)
      {:else}
        · singleton-per-Org
      {/if}
    </p>

    <form
      onsubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <label>
        Instance name
        <input
          type="text"
          bind:value={instanceName}
          required
          pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$"
          data-testid="instance-name"
          placeholder="e.g. metrics-primary"
        />
      </label>

      <label>
        Organization slug
        <input
          type="text"
          bind:value={org}
          required
          pattern="^[a-z0-9][a-z0-9-]{0,40}[a-z0-9]$"
          data-testid="instance-org"
          placeholder="e.g. acme"
        />
      </label>

      <TopologyPicker {blueprint} bind:value={topology} {regionsCount} />

      <!--
        #3373 — advanced placement, collapsed by default. The default flow
        never mentions vClusters; regions/clusters lists arrive in Wave-2
        (empty arrays hide those selectors).
      -->
      <details class="advanced">
        <summary data-testid="advanced-options-toggle">Advanced options</summary>
        <PlacementSelector
          {blueprint}
          regions={[]}
          clusters={[]}
          bind:value={placement}
        />
      </details>

      <label>
        Endpoint hostname <span class="opt">(optional)</span>
        <input
          type="text"
          bind:value={endpointHostname}
          data-testid="instance-endpoint-hostname"
          placeholder="leave blank to use Blueprint template"
        />
      </label>

      {#if error}<p class="error" data-testid="instance-error">{error}</p>{/if}

      <div class="actions">
        <button
          type="submit"
          class="primary"
          disabled={submitting}
          data-testid="instance-submit"
        >
          {submitting ? 'Creating…' : 'Create instance'}
        </button>
        <a href={`/catalog/${blueprintName}`}>Cancel</a>
      </div>
    </form>
  {/if}
</section>

<style>
  .g117-new-instance {
    max-width: 640px;
    background: white;
    border: 1px solid #e2e8f0;
    padding: 1.25rem 1.5rem;
    border-radius: 6px;
  }
  .meta {
    color: #64748b;
    font-size: 0.85rem;
    margin: 0 0 1rem;
  }
  .meta code {
    background: #f1f5f9;
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
  }
  form label {
    display: block;
    margin: 0.6rem 0;
    font-size: 0.9rem;
  }
  .opt {
    color: #64748b;
    font-weight: 400;
    font-size: 0.8rem;
  }
  input[type='text'] {
    width: 100%;
    padding: 0.45rem;
    border: 1px solid #cbd5e1;
    border-radius: 4px;
    margin-top: 0.2rem;
  }
  .advanced {
    margin: 0.75rem 0;
  }
  .advanced summary {
    cursor: pointer;
    color: #64748b;
    font-size: 0.9rem;
  }
  .advanced summary:hover {
    color: #1d4ed8;
  }
  .actions {
    display: flex;
    gap: 1rem;
    align-items: center;
    margin-top: 1rem;
  }
  .actions a {
    color: #64748b;
    text-decoration: none;
  }
  button.primary {
    padding: 0.5rem 1rem;
    background: #1d4ed8;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
  }
  button.primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .error {
    color: #b00020;
  }
</style>
