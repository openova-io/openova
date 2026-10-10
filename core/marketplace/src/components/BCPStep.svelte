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
  //
  // #6971 — with the BSS package document in hand, the package's DR level
  // decides what this step offers (packages.ts::drTopologyFor): on a package
  // whose level is "single region" the hot-standby card is locked — "Included
  // from XL" with a switch that keeps the apps and add-ons — and on a package
  // that includes it the card is selectable and reads INCLUDED. Single-region
  // is free everywhere. Without a document (or a document with no DR level
  // feature) the step is exactly as before.
  import { onMount } from 'svelte';
  import { getPlans, type Plan } from '../lib/api';
  import { readCart, setAppConfig, setOverage, setPackage } from '../lib/cart';
  import { chargebackBaseURL } from '../lib/config';
  import {
    catalogPlanIdForPackage,
    drTopologyFor,
    growSelectionFor,
    loadPublicPackages,
    packageForCart,
    pruneAddonsForPackage,
    PACKAGE_STRINGS as PS,
    type DrTopology,
    type PublicPackages,
  } from '../lib/packages';

  let doc = $state<PublicPackages | null>(null);
  let plans = $state<Plan[]>([]);
  let dr = $state<DrTopology | null>(null);
  let packageName = $state('');

  function applyDocument(d: PublicPackages) {
    const c = readCart();
    const pkg = packageForCart(d, c);
    if (!pkg) { dr = null; packageName = ''; return; }
    packageName = pkg.name;
    dr = drTopologyFor(d, pkg.sku);
    // A topology this package does not offer cannot stay in the cart — it was
    // picked on a larger package (or in grow mode) the customer has since left.
    const mode = growSelectionFor(d, pkg.sku, c).mode;
    if (dr && !dr.activePassive && !(dr.growOnly && mode === 'grow') && enabled) enabled = false;
  }

  function switchToGrow() {
    cart = setOverage({ mode: 'grow' });
  }

  onMount(async () => {
    const [d, pl] = await Promise.all([
      loadPublicPackages(chargebackBaseURL()),
      getPlans().catch(() => [] as Plan[]),
    ]);
    plans = pl;
    doc = d;
    if (d) applyDocument(d);
  });

  // "Switch to XL" on the locked card — the same move as the Add-ons step's
  // "Upgrade to": catalog plan id + sku, apps untouched, add-ons the new
  // package still sells kept.
  function switchPackage(sku: string) {
    if (!doc) return;
    const pkg = doc.packages.find(p => p.sku === sku);
    if (!pkg) return;
    cart = setPackage({
      planId: catalogPlanIdForPackage(pkg, plans),
      planName: pkg.name,
      packageSku: pkg.sku,
      addons: pruneAddonsForPackage(doc, pkg.sku, cart.addons),
    });
    applyDocument(doc);
  }

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
  // #6971 — the grow choice made on /addons. On a package whose DR cell is
  // `grow_only` (S / M / L), active-passive is selectable only in grow mode,
  // the standby billed as usage; "Switch to Grow" here sets the mode.
  const growMode = $derived(doc ? growSelectionFor(doc, packageForCart(doc, cart)?.sku ?? null, cart).mode : 'capped');
  const hotSelectable = $derived(!dr || dr.activePassive || (dr.growOnly && growMode === 'grow'));

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
      <label class="topology-card {!enabled ? 'selected' : ''}" data-testid="topology-card-single">
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
          <p>One Postgres cluster in your primary region. Backups via the optional add-on. Recovery time after a regional outage: hours.</p>
        </div>
      </label>

      {#if dr && dr.growOnly && !hotSelectable}
        <!-- #6971 — on this package active-passive needs Grow: the standby is
             billed as usage. One click switches the mode; the rung that
             includes it stays named as the other way. -->
        <div class="topology-card locked needs-grow" data-testid="topology-card-hot" data-locked="true" data-needs-grow="true">
          <span class="topology-lock" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/></svg>
          </span>
          <div class="topology-body">
            <div class="topology-title-row">
              <strong>Active-hot-standby</strong>
              <span class="topology-price from" data-testid="topology-needs-grow">{PS.grow.needsGrow}</span>
            </div>
            <p>Primary + synchronous replica across two distinct regions over Cilium ClusterMesh. Zero-tx-loss failover when a region goes dark.</p>
            <p class="topology-locked-note">{PS.grow.needsGrowBody(packageName)}</p>
            <div class="topology-actions">
              <button type="button" class="topology-switch" data-testid="topology-switch-grow" onclick={switchToGrow}>
                {PS.grow.switchToGrow} &rarr;
              </button>
              {#if dr.activePassiveFrom}
                <button
                  type="button"
                  class="topology-link"
                  data-testid="topology-switch-{dr.activePassiveFrom.sku}"
                  onclick={() => switchPackage(dr!.activePassiveFrom!.sku)}
                >
                  {PS.grow.orSwitchTo(dr.activePassiveFrom.name)}
                </button>
              {/if}
            </div>
          </div>
        </div>
      {:else if dr && !hotSelectable}
        <!-- Locked: this package's DR level is single region. -->
        <div class="topology-card locked" data-testid="topology-card-hot" data-locked="true">
          <span class="topology-lock" aria-hidden="true">—</span>
          <div class="topology-body">
            <div class="topology-title-row">
              <strong>Active-hot-standby</strong>
              {#if dr.activePassiveFrom}
                <span class="topology-price from">Included from {dr.activePassiveFrom.name}</span>
              {/if}
            </div>
            <p>Primary + synchronous replica across two distinct regions over Cilium ClusterMesh. Zero-tx-loss failover when a region goes dark.</p>
            {#if dr.activePassiveFrom}
              <p class="topology-locked-note">{PS.ladder.topologyLocked(dr.activePassiveFrom.name)}</p>
              <button
                type="button"
                class="topology-switch"
                data-testid="topology-switch-{dr.activePassiveFrom.sku}"
                onclick={() => switchPackage(dr!.activePassiveFrom!.sku)}
              >
                {PS.ladder.switchTo(dr.activePassiveFrom.name)} &rarr;
              </button>
            {/if}
          </div>
        </div>
      {:else}
        <label class="topology-card {enabled ? 'selected' : ''}" data-testid="topology-card-hot" data-locked="false">
          <input
            type="radio"
            name="topology"
            checked={enabled}
            onchange={() => { enabled = true; }}
          />
          <div class="topology-body">
            <div class="topology-title-row">
              <strong>Active-hot-standby</strong>
              {#if dr?.activePassive}
                <span class="topology-price free">{packageName ? `${packageName} · ` : ''}{PS.ladder.includedBadge}</span>
              {:else if dr?.growOnly}
                <span class="topology-price usage" data-testid="topology-billed-as-usage">{PS.grow.billedAsUsage}</span>
              {:else}
                <span class="topology-price">+OMR 5.000 / mo</span>
              {/if}
            </div>
            <p>Primary + synchronous replica across two distinct regions over Cilium ClusterMesh. RTO 30s, RPO 5s. Zero-tx-loss failover when a region goes dark.</p>
          </div>
        </label>
      {/if}
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

<!-- The step bar — the same fixed bottom bar as the Add-ons step: the page is
     padded by its height, so it never covers the cards. -->
<div class="step-bar" data-testid="step-bar">
  <div class="step-bar-inner">
    <a href="/addons" class="step-back">&larr; Add-ons</a>
    <a
      href={regionsValid ? '/review' : '#'}
      class="step-cta {regionsValid ? '' : 'disabled'}"
      aria-disabled={!regionsValid}
      onclick={(e) => { if (!regionsValid) e.preventDefault(); }}
    >
      Review Order &rarr;
    </a>
  </div>
</div>

<style>
  .bcp-page {
    --step-bar-h: 4.5rem;
    max-width: 900px;
    margin: 0 auto;
    padding: 0 1.25rem calc(var(--step-bar-h) + env(safe-area-inset-bottom, 0px));
  }
  :global(html) { scroll-padding-bottom: calc(4.5rem + env(safe-area-inset-bottom, 0px)); }

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
  .topology-price.from {
    background: color-mix(in srgb, var(--color-warn, #f59e0b) 15%, transparent);
    color: var(--color-text);
  }

  /* #6971 — the locked hot-standby card: not a choice on this package. */
  .topology-card.locked { cursor: default; opacity: 0.92; }
  .topology-card.locked:hover { border-color: var(--color-border); }
  .topology-lock { margin-top: 0.1rem; color: var(--color-text-dimmer); flex-shrink: 0; }
  .topology-locked-note { margin-top: 0.4rem !important; color: var(--color-text-dimmer) !important; font-style: italic; }
  .topology-switch {
    margin-top: 0.45rem;
    padding: 0.4rem 0.9rem;
    border: none;
    border-radius: 7px;
    background: var(--color-accent);
    color: #fff;
    font: inherit;
    font-size: 0.78rem;
    font-weight: 600;
    cursor: pointer;
  }
  .topology-switch:hover { filter: brightness(0.92); }
  /* #6971 — active-passive with Grow */
  .topology-card.needs-grow .topology-lock { color: var(--color-success); }
  .topology-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.35rem 0.8rem; }
  .topology-actions .topology-switch { background: var(--color-success); }
  .topology-link {
    margin-top: 0.45rem; padding: 0; border: 0; background: none;
    color: var(--color-accent); font: inherit; font-size: 0.76rem; font-weight: 600; cursor: pointer; text-align: left;
  }
  .topology-link:hover { text-decoration: underline; }
  .topology-price.usage {
    background: color-mix(in srgb, var(--color-success) 15%, transparent);
    color: var(--color-success);
  }
  .topology-title-row { flex-wrap: wrap; }

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

  /* The step bar — the same chrome as the Add-ons step. */
  .step-bar {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 100;
    padding: 0.6rem 1.25rem calc(0.6rem + env(safe-area-inset-bottom, 0px));
    background: color-mix(in srgb, var(--color-surface) 96%, transparent);
    backdrop-filter: blur(12px);
    border-top: 1px solid var(--color-border);
    box-shadow: 0 -4px 24px rgba(0, 0, 0, 0.08);
  }
  .step-bar-inner {
    max-width: 900px;
    margin: 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
  }
  .step-back {
    color: var(--color-text-dim);
    text-decoration: none;
    font-size: 0.85rem;
    font-weight: 500;
    padding: 0.4rem 0.2rem;
    white-space: nowrap;
  }
  .step-back:hover { color: var(--color-text-strong); }
  .step-cta {
    padding: 0.6rem 1.5rem;
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
  .step-cta:hover { filter: brightness(0.9); }
  .step-cta.disabled {
    background: color-mix(in srgb, var(--color-text-dim) 35%, transparent);
    color: var(--color-text-dim);
    cursor: not-allowed;
    box-shadow: none;
    pointer-events: auto;
  }
  .step-cta.disabled:hover { filter: none; }
</style>
