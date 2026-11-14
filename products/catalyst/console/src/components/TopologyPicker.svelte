<!--
  G117.2 — Topology picker.

  Reads a Blueprint's spec.topology.supported (from CatalogBlueprint.supportedTopologies)
  and renders a radio list. Default selection is computed:
    - If the Sovereign is multi-region (regionsCount > 1) → blueprint.defaultTopology
    - Else                                                → 'singleton' if in
      supportedTopologies, otherwise the first supported value

  Locked decision #7: `len(Sovereign.spec.regions) > 1` = multi-region default.

  Two-way bound via `value` + `onchange` (parent owns state). Disables choices
  that the Sovereign cannot satisfy (e.g. active-active on single-region).
-->
<script lang="ts">
  import type { BcpTopology, CatalogBlueprint } from '../lib/api/types';

  let {
    blueprint,
    value = $bindable<BcpTopology | ''>(''),
    regionsCount = 1,
  }: {
    blueprint: CatalogBlueprint;
    value?: BcpTopology | '';
    regionsCount?: number;
  } = $props();

  // Topologies that require multi-region. The catalyst-api validates this
  // server-side too (409 conflict on mismatch), but pre-disabling helps
  // operators see intent at design time.
  const MULTI_REGION_REQUIRED: BcpTopology[] = [
    'active-active',
    'active-hot-standby',
    'active-passive',
  ];

  function defaultFor(bp: CatalogBlueprint, regions: number): BcpTopology {
    if (regions > 1) {
      // Multi-region Sovereign: prefer blueprint's default.
      if (bp.supportedTopologies.includes(bp.defaultTopology)) {
        return bp.defaultTopology;
      }
      return bp.supportedTopologies[0];
    }
    // Single-region Sovereign: prefer singleton.
    if (bp.supportedTopologies.includes('singleton')) return 'singleton';
    return bp.supportedTopologies[0];
  }

  function isDisabled(t: BcpTopology): boolean {
    return regionsCount <= 1 && MULTI_REGION_REQUIRED.includes(t);
  }

  function describe(t: BcpTopology): string {
    switch (t) {
      case 'active-active':
        return 'Both regions serve live traffic; data syncs bidirectionally';
      case 'active-hot-standby':
        return 'Region-A serves; region-B mirrored and ready for failover';
      case 'active-passive':
        return 'Region-A serves; region-B cold standby (manual cutover)';
      case 'singleton':
        return 'One replica in one region; no failover';
    }
  }

  // Auto-select default on mount or whenever the blueprint flips.
  $effect(() => {
    if (!value || !blueprint.supportedTopologies.includes(value)) {
      value = defaultFor(blueprint, regionsCount);
    }
  });
</script>

<fieldset class="topology-picker" data-testid="topology-picker">
  <legend>Topology</legend>
  {#if regionsCount <= 1}
    <p class="hint">
      This Sovereign is single-region — multi-region topologies are disabled.
    </p>
  {/if}
  {#each blueprint.supportedTopologies as t (t)}
    {@const disabled = isDisabled(t)}
    <label class="row" class:disabled>
      <input
        type="radio"
        name="topology"
        value={t}
        checked={value === t}
        {disabled}
        data-testid={`topology-radio-${t}`}
        onchange={() => (value = t)}
      />
      <span class="label">
        <code>{t}</code>
        {#if t === blueprint.defaultTopology}
          <em class="default-badge">default</em>
        {/if}
      </span>
      <span class="desc">{describe(t)}</span>
    </label>
  {/each}
</fieldset>

<style>
  .topology-picker {
    border: 1px solid #e2e8f0;
    padding: 0.75rem 1rem;
    border-radius: 6px;
    margin: 0.75rem 0;
  }
  .topology-picker legend {
    padding: 0 0.4rem;
    font-weight: 500;
  }
  .hint {
    color: #64748b;
    font-size: 0.85rem;
    margin: 0 0 0.5rem 0;
  }
  .row {
    display: grid;
    grid-template-columns: 1.2rem auto 1fr;
    gap: 0.5rem;
    align-items: center;
    padding: 0.35rem 0;
    cursor: pointer;
  }
  .row.disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .label code {
    background: #f1f5f9;
    padding: 0.1rem 0.35rem;
    border-radius: 3px;
    font-size: 0.85rem;
  }
  .default-badge {
    margin-left: 0.3rem;
    font-style: normal;
    font-size: 0.7rem;
    background: #ddd6fe;
    color: #5b21b6;
    padding: 0.05rem 0.3rem;
    border-radius: 3px;
  }
  .desc {
    color: #64748b;
    font-size: 0.85rem;
  }
</style>
