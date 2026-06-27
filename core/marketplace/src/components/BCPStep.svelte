<script lang="ts">
  // TBD-V57 (#2133) — Pillar 2 BCP topology picker. This is the
  // marketplace wizard surface that lets the customer signal "I want
  // active-hot-standby across two regions" at signup. The backend
  // round-trip is ALREADY canonical via snake_case
  // cart.appConfigs.postgres.{active_hot_standby, primary_region,
  // replica_region} — every layer below (catalog seed, tenant publisher
  // wire format, provisioning gitops generator, bp-cnpg-pair chart)
  // consumes those exact field keys today. The previous audit at
  // /tmp/audit-pillar3-cnpg-2026-05-20.md confirmed end-to-end wiring;
  // the only missing piece was THIS UX surface so the customer could
  // populate the fields. Adding a sibling Order.enable_hot_standby
  // schema field (proposed in the original #2133 plan) would create two
  // sources of truth and break the round-trip test in
  // core/services/tenant/handlers/tenant_created_wire_test.go — the
  // commit 56e04ac8a explicitly locked the contract.
  //
  // Files this component touches:
  //   - cart.appConfigs.postgres.active_hot_standby (bool)
  //   - cart.appConfigs.postgres.primary_region    (string)
  //   - cart.appConfigs.postgres.replica_region    (string)
  //
  // The downstream consumer at
  // core/services/provisioning/gitops/gitops.go:240-251 reads exactly
  // these three keys; an invalid pair (identical regions, missing
  // primary, missing replica) falls back to the single-cluster shape
  // and logs a Warn — symmetric with the operator-opt-in InvalidRegionPair
  // test in appconfigs_test.go.
  //
  // Regions list — #4525: fetched from the catalog service's
  // GET /api/catalog/regions (the Sovereign's REAL configured regions,
  // sourced from CATALYST_CONFIGURED_REGIONS). The hardcoded list below
  // is the loading/offline FALLBACK only — on a Huawei Sovereign running
  // me-east-215-a/b the fetched set replaces it so the picker never
  // offers a region the Sovereign cannot honor (which would route the
  // customer's choice into the gitops InvalidRegionPair single-cluster
  // fallback silently).
  import { onMount } from 'svelte';
  import { readCart, setAppConfig } from '../lib/cart';

  // Fallback Sovereign region keys — matches the values gitops
  // appconfigs_test.go::TestPostgres_AppConfigs_ActiveHotStandby_GenericApp
  // exercises ("hz-fsn-rtz-prod" / "hz-hel-rtz-prod") + nbg as a third.
  // These are ONLY used while the /api/catalog/regions fetch is in flight
  // or when it fails (offline / older Sovereign without the endpoint).
  // hz-fsn = Falkenstein, hz-hel = Helsinki, hz-nbg = Nuremberg.
  const FALLBACK_REGIONS: { key: string; label: string }[] = [
    { key: 'hz-fsn-rtz-prod', label: 'Falkenstein (hz-fsn)' },
    { key: 'hz-hel-rtz-prod', label: 'Helsinki (hz-hel)' },
    { key: 'hz-nbg-rtz-prod', label: 'Nuremberg (hz-nbg)' },
  ];

  // The live region list shown in the <select>s. Starts as the fallback
  // so the form is fully populated on first paint; replaced on mount by
  // the Sovereign's real regions when the fetch succeeds with a non-empty
  // set. A failed fetch or empty response leaves the fallback in place.
  let REGIONS = $state<{ key: string; label: string }[]>(FALLBACK_REGIONS);

  onMount(async () => {
    try {
      const res = await fetch('/api/catalog/regions', {
        headers: { Accept: 'application/json' },
      });
      if (!res.ok) return; // keep fallback
      const data = (await res.json()) as { key?: string; label?: string }[];
      if (!Array.isArray(data) || data.length === 0) return; // keep fallback
      const fetched = data
        .filter((r) => typeof r.key === 'string' && r.key !== '')
        .map((r) => ({ key: r.key as string, label: (r.label as string) || (r.key as string) }));
      if (fetched.length === 0) return; // keep fallback
      REGIONS = fetched;
      // Re-anchor the picks onto the fetched set so a stale cart default
      // (e.g. a hardcoded hz-* key from a prior session) can't survive as
      // a region the Sovereign can't honor. Only nudge when the current
      // pick isn't in the fetched set.
      const keys = new Set(fetched.map((r) => r.key));
      if (!keys.has(primaryRegion)) primaryRegion = fetched[0].key;
      if (!keys.has(replicaRegion)) {
        replicaRegion = fetched[1]?.key ?? fetched[0].key;
      }
    } catch {
      // Network error — keep the static fallback so the picker is never
      // blocked. The region-pair "must differ" validation still applies.
    }
  });

  // Helper: pull the current postgres slot from cart.appConfigs and
  // apply defaults so the form is always populated even on first
  // visit. Single source of truth: every read + write goes through
  // setAppConfig() (which merges, not replaces), so AppDetail.svelte's
  // separate seed of replicas/disk_gb/backups_enabled isn't clobbered.
  let cart = $state(readCart());
  const initialPg = (cart.appConfigs ?? {}).postgres ?? {};
  let enabled = $state<boolean>(Boolean(initialPg.active_hot_standby));
  let primaryRegion = $state<string>(
    typeof initialPg.primary_region === 'string' && initialPg.primary_region !== ''
      ? initialPg.primary_region
      : REGIONS[0].key,
  );
  let replicaRegion = $state<string>(
    typeof initialPg.replica_region === 'string' && initialPg.replica_region !== ''
      ? initialPg.replica_region
      : REGIONS[1].key,
  );

  // Mirror gitops's InvalidRegionPair check so the customer can't ship
  // a config the provisioning generator would silently degrade. This
  // surface fires a hard validation error on the wizard rather than
  // letting the customer pay for active-hot-standby + then discover
  // post-checkout that the provisioner fell back to single-cluster.
  const regionsValid = $derived(!enabled || (primaryRegion !== '' && replicaRegion !== '' && primaryRegion !== replicaRegion));

  // Persist on every change. setAppConfig MERGES with the existing
  // postgres slot (AppDetail.svelte may have already seeded
  // replicas/disk_gb/backups_enabled) so we never blow those away.
  // The cart is the buffer between wizard steps — no submit needed.
  function persist() {
    const current = (readCart().appConfigs ?? {}).postgres ?? {};
    const next: Record<string, number | string | boolean> = { ...current };
    next.active_hot_standby = enabled;
    if (enabled) {
      next.primary_region = primaryRegion;
      next.replica_region = replicaRegion;
    } else {
      // When OFF, drop the region picks so the gitops fallback path
      // (legacy single-cluster generatePostgres) runs cleanly. Empty
      // values match the OFF path in
      // appconfigs_test.go::TestPostgres_AppConfigs_ActiveHotStandby_OFF.
      delete (next as Record<string, unknown>).primary_region;
      delete (next as Record<string, unknown>).replica_region;
    }
    cart = setAppConfig('postgres', next);
  }

  // Re-persist whenever the form mutates. $effect runs after every
  // mutation of the tracked $state values, which is exactly the cart
  // discipline the rest of the marketplace follows (mirror of
  // AddonsStep.persistTLD on TLD select).
  $effect(() => {
    void enabled;
    void primaryRegion;
    void replicaRegion;
    persist();
  });
</script>

<div class="bcp-page">
  <div class="bcp-hero">
    <h1>Business continuity</h1>
    <p>Pick how your database should survive a regional outage</p>
  </div>

  <!-- Topology radio: single-region (default) vs active-hot-standby -->
  <section class="bcp-section">
    <div class="bcp-head">
      <h2>Topology</h2>
      <span class="bcp-note">Optional</span>
    </div>
    <div class="topology-grid">
      <label class="topology-card {!enabled ? 'selected' : ''}">
        <input
          type="radio"
          name="topology"
          checked={!enabled}
          onchange={() => { enabled = false; }}
        />
        <div class="topology-body">
          <div class="topology-title-row">
            <strong>Single-region</strong>
            <span class="topology-price free">FREE</span>
          </div>
          <p>One Postgres cluster in your primary region. Daily backups via the optional add-on. Recovery time after a regional outage: hours.</p>
        </div>
      </label>

      <label class="topology-card {enabled ? 'selected' : ''}">
        <input
          type="radio"
          name="topology"
          checked={enabled}
          onchange={() => { enabled = true; }}
        />
        <div class="topology-body">
          <div class="topology-title-row">
            <strong>Active-hot-standby</strong>
            <span class="topology-price">+OMR 5.000 / mo</span>
          </div>
          <p>Primary + synchronous replica across two distinct regions over Cilium ClusterMesh. RTO 30s, RPO 5s. Zero-tx-loss failover when a region goes dark.</p>
        </div>
      </label>
    </div>
  </section>

  <!-- Region pickers — only rendered when active-hot-standby is on -->
  {#if enabled}
    <section class="bcp-section">
      <div class="bcp-head">
        <h2>Regions</h2>
        <span class="bcp-note">Primary and replica must differ</span>
      </div>
      <div class="region-grid">
        <div class="region-field">
          <label for="primary-region">Primary region</label>
          <select
            id="primary-region"
            class="region-select"
            bind:value={primaryRegion}
          >
            {#each REGIONS as r}
              <option value={r.key}>{r.label}</option>
            {/each}
          </select>
          <p class="region-hint">Writes land here. Apps connect to the primary's read-write endpoint.</p>
        </div>
        <div class="region-field">
          <label for="replica-region">Replica region</label>
          <select
            id="replica-region"
            class="region-select"
            bind:value={replicaRegion}
          >
            {#each REGIONS as r}
              <option value={r.key}>{r.label}</option>
            {/each}
          </select>
          <p class="region-hint">Hot standby ready to take writes within 30 seconds if the primary region goes dark.</p>
        </div>
      </div>
      {#if !regionsValid}
        <p class="region-error" role="alert">Primary and replica regions must differ — pick two distinct regions to enable active-hot-standby.</p>
      {/if}
    </section>
  {/if}
</div>

<div class="float-nav">
  <a href="/addons" class="float-back">&larr; Add-ons</a>
  <a
    href={regionsValid ? '/review' : '#'}
    class="float-cta {regionsValid ? '' : 'disabled'}"
    aria-disabled={!regionsValid}
    onclick={(e) => { if (!regionsValid) e.preventDefault(); }}
  >
    Review Order &rarr;
  </a>
</div>

<style>
  .bcp-page { max-width: 900px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }

  .bcp-hero { text-align: center; margin-bottom: 0.75rem; }
  .bcp-hero h1 {
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.2rem;
    font-weight: 700;
  }
  .bcp-hero p {
    color: var(--color-text-dim);
    font-size: 0.85rem;
    margin: 0;
  }

  /* Sections — mirrors AddonsStep .ao-section so the design system
     stays consistent. No bespoke chrome. */
  .bcp-section {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 12px;
    padding: 1rem 1.1rem;
    margin-bottom: 0.75rem;
  }
  .bcp-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.65rem;
  }
  .bcp-head h2 { font-size: 0.95rem; color: var(--color-text-strong); margin: 0; font-weight: 600; }
  .bcp-note { color: var(--color-text-dim); font-size: 0.78rem; }

  /* Topology cards — mirrors .extra-tile pattern. Two-card grid that
     collapses to single column on narrow viewports. */
  .topology-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.6rem;
  }
  @media (max-width: 640px) { .topology-grid { grid-template-columns: 1fr; } }
  .topology-card {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    padding: 0.85rem 0.95rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 10px;
    cursor: pointer;
    transition: border-color 0.15s, background 0.15s;
  }
  .topology-card:hover { border-color: var(--color-text-dim); }
  .topology-card.selected {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 4%, var(--color-bg));
  }
  .topology-card input[type="radio"] {
    margin-top: 0.25rem;
    accent-color: var(--color-accent);
    flex-shrink: 0;
  }
  .topology-body { flex: 1; min-width: 0; }
  .topology-title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.25rem;
  }
  .topology-body strong {
    color: var(--color-text-strong);
    font-size: 0.92rem;
  }
  .topology-body p {
    margin: 0;
    color: var(--color-text-dim);
    font-size: 0.78rem;
    line-height: 1.45;
  }
  .topology-price {
    padding: 0.15rem 0.55rem;
    border-radius: 4px;
    font-size: 0.7rem;
    font-weight: 700;
    letter-spacing: 0.04em;
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent);
  }
  .topology-price.free {
    background: color-mix(in srgb, var(--color-success) 15%, transparent);
    color: var(--color-success);
  }

  /* Region pickers */
  .region-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }
  @media (max-width: 640px) { .region-grid { grid-template-columns: 1fr; } }
  .region-field { display: flex; flex-direction: column; gap: 0.35rem; }
  .region-field label {
    color: var(--color-text-strong);
    font-size: 0.82rem;
    font-weight: 600;
  }
  .region-select {
    padding: 0.5rem 0.65rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.85rem;
    appearance: auto;
  }
  .region-select:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
  .region-hint {
    margin: 0;
    color: var(--color-text-dim);
    font-size: 0.74rem;
    line-height: 1.45;
  }
  .region-error {
    margin: 0.6rem 0 0;
    color: var(--color-danger, #ef4444);
    font-size: 0.8rem;
    font-weight: 500;
  }

  /* Floating nav pill — duplicated from AddonsStep so this surface
     inherits the same chrome. No new tokens. */
  .float-nav {
    position: fixed;
    bottom: 1.25rem;
    left: 50%;
    transform: translateX(-50%);
    z-index: 100;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    background: color-mix(in srgb, var(--color-surface) 95%, transparent);
    backdrop-filter: blur(12px);
    border: 1px solid var(--color-border);
    border-radius: 999px;
    padding: 0.35rem 0.4rem 0.35rem 0.6rem;
    box-shadow: 0 4px 24px rgba(0, 0, 0, 0.2);
  }
  .float-back {
    color: var(--color-text-dim);
    text-decoration: none;
    font-size: 0.82rem;
    font-weight: 500;
    padding: 0.4rem 0.6rem;
    white-space: nowrap;
  }
  .float-back:hover { color: var(--color-text-strong); }
  .float-cta {
    padding: 0.55rem 1.4rem;
    background: var(--color-accent);
    color: #fff;
    border-radius: 999px;
    text-decoration: none;
    font-weight: 600;
    font-size: 0.88rem;
    white-space: nowrap;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--color-accent) 25%, transparent);
    transition: filter 0.15s;
  }
  .float-cta:hover { filter: brightness(0.9); }
  .float-cta.disabled {
    background: color-mix(in srgb, var(--color-text-dim) 35%, transparent);
    color: var(--color-text-dim);
    cursor: not-allowed;
    box-shadow: none;
    pointer-events: auto;
  }
  .float-cta.disabled:hover { filter: none; }
</style>
