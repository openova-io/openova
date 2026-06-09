<script lang="ts">
  // Operator-console "Data instances" panel (ADR-0010).
  //
  // Renders the three wireframe surfaces of the reusable, shareable
  // backing-services model:
  //   1. ONE PostgreSQL ENGINE-CLASS card (the engine, shown once) —
  //      under Platform/engines, with the instance count.
  //   2. The N DATA-INSTANCE App cards (bp-postgres installs), each with
  //      a Consumers (bindings) table: app · database · role · mode · secret.
  //   3. (consumerBackingRow is exported from ../lib/dataInstances for the
  //      consumer AppDetail "Backing services" row that names its instance.)
  //
  // Fully driven by the declarative binding model — the bp-postgres
  // instance's databases[] values. NO controller, NO Crossplane.
  import {
    type DataInstance,
    postgresClass,
    isShared,
  } from '../lib/dataInstances';

  let { instances = [] }: { instances?: DataInstance[] } = $props();

  const engine = $derived(postgresClass(instances));
</script>

<section class="panel">
  <header class="panel-head">
    <h2>Data instances</h2>
    <p class="sub">
      Shareable PostgreSQL engines. Each instance is one CNPG cluster; apps share
      an instance via isolated databases + per-app roles. Pure Flux-applied YAML —
      no controller, no Crossplane.
    </p>
  </header>

  <!-- 1. Engine CLASS card (shown once) -->
  <div class="engines">
    <div class="engine-card" data-testid="engine-class-card">
      <span class="engine-icon">🐘</span>
      <div class="engine-body">
        <span class="engine-title">{engine.title}</span>
        <span class="engine-sub">Engine · {engine.blueprint}</span>
      </div>
      <span class="engine-count" title="data-instances running this engine">
        {engine.instanceCount} instance{engine.instanceCount === 1 ? '' : 's'}
      </span>
    </div>
  </div>

  {#if instances.length === 0}
    <div class="empty">No PostgreSQL data instances yet.</div>
  {:else}
    <!-- 2. Data-instance App cards, each with a Consumers (bindings) table -->
    <div class="instances">
      {#each instances as inst (inst.name)}
        <div class="instance-card" data-testid="data-instance-card">
          <div class="instance-head">
            <span class="instance-name">{inst.name}</span>
            <span class="pill pill-bp">{inst.blueprint}</span>
            <span class="pill pill-topo">{inst.topology}</span>
            {#if isShared(inst)}
              <span class="pill pill-shared" title="Shared by more than one app">SHARED</span>
            {/if}
          </div>

          <div class="consumers">
            <div class="consumers-head">Consumers (bindings)</div>
            {#if inst.bindings.length === 0}
              <div class="no-consumers">No consumers bound yet.</div>
            {:else}
              <table class="bindings" data-testid="consumers-table">
                <thead>
                  <tr>
                    <th>App</th>
                    <th>Database</th>
                    <th>Role</th>
                    <th>Mode</th>
                    <th>Secret</th>
                  </tr>
                </thead>
                <tbody>
                  {#each inst.bindings as b (b.consumer + '/' + b.database)}
                    <tr data-testid="binding-row">
                      <td>{b.consumer}</td>
                      <td><code>{b.database}</code></td>
                      <td><code>{b.role}</code></td>
                      <td>
                        <span class="mode mode-{b.mode}">{b.mode}</span>
                      </td>
                      <td><code class="secret">{b.secret}</code></td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .panel { margin-top: 1.5rem; }
  .panel-head h2 {
    margin: 0; font-size: 0.95rem; font-weight: 600;
    color: var(--color-text-strong);
  }
  .panel-head .sub {
    margin: 0.25rem 0 0.75rem;
    font-size: 0.78rem; color: var(--color-text-dim);
    max-width: 52rem;
  }

  /* Engine class card — distinct from instance cards (it's the engine). */
  .engines { margin-bottom: 1rem; }
  .engine-card {
    display: flex; align-items: center; gap: 0.75rem;
    background: var(--color-surface);
    border: 1.5px dashed var(--color-border);
    border-radius: 12px;
    padding: 0.6rem 0.9rem;
    max-width: 28rem;
  }
  .engine-icon { font-size: 1.4rem; }
  .engine-body { display: flex; flex-direction: column; min-width: 0; }
  .engine-title { font-size: 0.92rem; font-weight: 600; color: var(--color-text-strong); }
  .engine-sub {
    font-size: 0.7rem; color: var(--color-text-dim);
    font-family: var(--font-mono, ui-monospace, monospace);
  }
  .engine-count {
    margin-left: auto;
    font-size: 0.72rem; color: var(--color-text-dim);
    font-variant-numeric: tabular-nums;
  }

  .empty {
    padding: 0.8rem 1rem;
    background: var(--color-surface);
    border: 1px dashed var(--color-border);
    border-radius: 10px;
    color: var(--color-text-dim);
    font-size: 0.82rem;
  }

  /* Data-instance App cards. */
  .instances {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
    gap: 0.9rem;
  }
  .instance-card {
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    padding: 0.75rem 0.9rem;
  }
  .instance-head {
    display: flex; flex-wrap: wrap; align-items: center; gap: 0.5rem;
    margin-bottom: 0.6rem;
  }
  .instance-name {
    font-size: 0.95rem; font-weight: 600; color: var(--color-text-strong);
  }
  .pill {
    padding: 0.05rem 0.45rem; border-radius: 4px; font-size: 0.62rem;
    font-weight: 600; letter-spacing: 0.03em; text-transform: uppercase;
  }
  .pill-bp {
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    color: var(--color-text-dim);
    font-family: var(--font-mono, ui-monospace, monospace);
  }
  .pill-topo {
    background: color-mix(in srgb, var(--color-accent) 14%, transparent);
    color: var(--color-accent);
  }
  .pill-shared {
    background: color-mix(in srgb, var(--color-success) 16%, transparent);
    color: var(--color-success);
  }

  .consumers-head {
    font-size: 0.72rem; font-weight: 600; letter-spacing: 0.02em;
    color: var(--color-text-dim); text-transform: uppercase;
    margin-bottom: 0.35rem;
  }
  .no-consumers { font-size: 0.8rem; color: var(--color-text-dim); }

  table.bindings {
    width: 100%; border-collapse: collapse; font-size: 0.78rem;
  }
  table.bindings th {
    text-align: left; font-weight: 600; color: var(--color-text-dim);
    padding: 0.25rem 0.5rem 0.25rem 0;
    border-bottom: 1px solid var(--color-border);
  }
  table.bindings td {
    padding: 0.3rem 0.5rem 0.3rem 0;
    border-bottom: 1px solid color-mix(in srgb, var(--color-border) 40%, transparent);
    color: var(--color-text-strong);
  }
  table.bindings code {
    font-family: var(--font-mono, ui-monospace, monospace);
    font-size: 0.72rem;
  }
  .secret { color: var(--color-text-dim); }
  .mode {
    padding: 0.04rem 0.4rem; border-radius: 4px; font-size: 0.62rem;
    font-weight: 600; text-transform: uppercase;
  }
  .mode-shared {
    background: color-mix(in srgb, var(--color-success) 16%, transparent);
    color: var(--color-success);
  }
  .mode-private {
    background: color-mix(in srgb, var(--color-text-dim) 14%, transparent);
    color: var(--color-text-dim);
  }
</style>
