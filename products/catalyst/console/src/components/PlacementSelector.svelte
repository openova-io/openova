<!--
  #3373 — Instance-level placement selector (advanced options).

  GENERIC by design (#3370 pattern): everything rendered here is driven by
  the Blueprint declaration (CatalogBlueprint.allowedPlacements +
  CatalogBlueprint.defaultPlacement) — never per-app special-casing.

  Emits `value = null` until the user actually changes something away from
  the Blueprint defaults. Once changed, `value` contains ONLY the fields the
  user changed (vcluster always included when changed), so the create request
  silently accepts Blueprint defaults for everything untouched.

  `regions` / `clusters` are the available choices supplied by the parent;
  an empty list hides that selector entirely (Wave-2 wires the sovereign
  regions endpoint).
-->
<script lang="ts">
  import type {
    CatalogBlueprint,
    InstancePlacement,
    VClusterTier,
  } from '../lib/api/types';

  let {
    blueprint,
    regions = [],
    clusters = [],
    value = $bindable<InstancePlacement | null>(null),
  }: {
    blueprint: CatalogBlueprint;
    regions?: string[];
    clusters?: string[];
    value?: InstancePlacement | null;
  } = $props();

  const ALL_TIERS: VClusterTier[] = ['host', 'mgmt', 'dmz', 'rtz'];

  const tiers = $derived<VClusterTier[]>(
    blueprint.allowedPlacements && blueprint.allowedPlacements.length > 0
      ? blueprint.allowedPlacements
      : ALL_TIERS,
  );

  const defaultTier = $derived<VClusterTier>(
    blueprint.defaultPlacement?.vcluster ?? 'host',
  );

  let selectedTier = $state<VClusterTier | ''>('');
  let selectedRegions = $state<string[]>([]);
  let selectedClusters = $state<string[]>([]);

  function describe(t: VClusterTier): string {
    switch (t) {
      case 'host':
        return 'host cluster (substrate only)';
      case 'mgmt':
        return 'control-plane vCluster';
      case 'dmz':
        return 'edge/WAF vCluster';
      case 'rtz':
        return 'regulated Organization vCluster';
    }
  }

  // Recompute the bound value from the current selections: null while
  // everything still matches the Blueprint defaults, else only the deltas.
  function sync(): void {
    const out: InstancePlacement = {};
    if (selectedTier && selectedTier !== defaultTier) out.vcluster = selectedTier;
    if (selectedRegions.length > 0) out.regions = [...selectedRegions];
    if (selectedClusters.length > 0) out.clusters = [...selectedClusters];
    value = Object.keys(out).length > 0 ? out : null;
  }

  function toggle(list: string[], name: string): string[] {
    return list.includes(name) ? list.filter((n) => n !== name) : [...list, name];
  }

  // Pre-select the Blueprint-default tier on mount or whenever the
  // blueprint flips (e.g. the allowed set no longer contains the choice).
  $effect(() => {
    if (!selectedTier || !tiers.includes(selectedTier)) {
      selectedTier = tiers.includes(defaultTier) ? defaultTier : tiers[0];
      sync();
    }
  });
</script>

<fieldset class="placement-selector" data-testid="placement-selector">
  <legend>Placement</legend>
  <p class="hint">
    Where this Application runs. Leave untouched to accept the Blueprint
    defaults.
  </p>

  <div class="group">
    <span class="group-label">vCluster</span>
    {#each tiers as t (t)}
      <label class="row">
        <input
          type="radio"
          name="placement-vcluster"
          value={t}
          checked={selectedTier === t}
          data-testid={`placement-vcluster-${t}`}
          onchange={() => {
            selectedTier = t;
            sync();
          }}
        />
        <span class="label">
          <code>{t}</code>
          {#if t === defaultTier}
            <em class="default-badge">blueprint default</em>
          {/if}
        </span>
        <span class="desc">{describe(t)}</span>
      </label>
    {/each}
  </div>

  {#if regions.length > 0}
    <div class="group">
      <span class="group-label">Regions</span>
      {#each regions as r (r)}
        <label class="row">
          <input
            type="checkbox"
            checked={selectedRegions.includes(r)}
            data-testid={`placement-region-${r}`}
            onchange={() => {
              selectedRegions = toggle(selectedRegions, r);
              sync();
            }}
          />
          <span class="label"><code>{r}</code></span>
          <span class="desc"></span>
        </label>
      {/each}
    </div>
  {/if}

  {#if clusters.length > 0}
    <div class="group">
      <span class="group-label">Clusters</span>
      {#each clusters as c (c)}
        <label class="row">
          <input
            type="checkbox"
            checked={selectedClusters.includes(c)}
            data-testid={`placement-cluster-${c}`}
            onchange={() => {
              selectedClusters = toggle(selectedClusters, c);
              sync();
            }}
          />
          <span class="label"><code>{c}</code></span>
          <span class="desc"></span>
        </label>
      {/each}
    </div>
  {/if}
</fieldset>

<style>
  .placement-selector {
    border: 1px solid #e2e8f0;
    padding: 0.75rem 1rem;
    border-radius: 6px;
    margin: 0.75rem 0;
  }
  .placement-selector legend {
    padding: 0 0.4rem;
    font-weight: 500;
  }
  .hint {
    color: #64748b;
    font-size: 0.85rem;
    margin: 0 0 0.5rem 0;
  }
  .group {
    margin: 0.5rem 0;
  }
  .group-label {
    display: block;
    font-size: 0.8rem;
    font-weight: 500;
    color: #475569;
    margin-bottom: 0.2rem;
  }
  .row {
    display: grid;
    grid-template-columns: 1.2rem auto 1fr;
    gap: 0.5rem;
    align-items: center;
    padding: 0.35rem 0;
    cursor: pointer;
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
