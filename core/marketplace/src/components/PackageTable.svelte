<script lang="ts">
  // Step 1 of the wizard — the package comparison table (#6971). Columns are
  // the packages BSS publishes, rows are its features, cells are ✓ / a muted
  // "add-on" with its price and the "Included from XL" hint / —. Choosing a
  // package does exactly what the legacy deck did: it sets the cart plan
  // (catalog plan id mapped from the sku, plus the sku for billing) and
  // continues to Stack. Add-ons are picked on step 3, where they always were.
  // Fed ONLY from GET /api/v1/public/packages on the Sovereign's chargeback
  // host; when that is unreachable or empty the legacy deck renders unchanged.
  //
  // The legacy deck is this step's FIRST render — server-rendered into the page
  // and hydrated exactly as plans.astro mounted it before the table existed —
  // and the table replaces it only once the document has arrived. That order
  // is load-bearing, not cosmetic: the production build renders this component
  // on the server with its initial state only (`$effect` never runs there), so
  // a branch the initial state cannot reach is dead code to Rollup. With a
  // spinner as the first render, PlanStep was tree-shaken out of the server
  // bundle, its component-scoped stylesheet (which Vite keeps only while the
  // component is rendered) never reached /_astro/plans.*.css, and the fallback
  // on a Sovereign without the endpoint drew the deck completely unstyled. The
  // Playwright spec asserts the deck's computed layout against the production
  // build with the request failing, so that cannot come back unnoticed.
  import PlanStep from './PlanStep.svelte';
  import { getPlans, type Plan } from '../lib/api';
  import { readCart, setPackage } from '../lib/cart';
  import { chargebackBaseURL } from '../lib/config';
  import {
    buildPackageTable,
    catalogPlanIdForPackage,
    includesLines,
    loadPublicPackages,
    pruneAddonsForPackage,
    PACKAGE_STRINGS as S,
    type PackageTableModel,
    type PublicPackages,
  } from '../lib/packages';

  // 'deck' until the document is in hand; it stays 'deck' when the document
  // never comes (loadPublicPackages has already warned once by then).
  let status = $state<'deck' | 'table'>('deck');
  let data = $state<PublicPackages | null>(null);
  let model = $state<PackageTableModel | null>(null);
  let plans = $state<Plan[]>([]);
  let selectedSku = $state<string | null>(readCart().packageSku);

  $effect(() => {
    let cancelled = false;
    (async () => {
      const [pk, pl] = await Promise.all([
        loadPublicPackages(chargebackBaseURL()),
        getPlans().catch(() => [] as Plan[]),
      ]);
      if (cancelled) return;
      // No document: the deck already on screen is the step, as before #6971.
      if (!pk) return;
      plans = pl;
      data = pk;
      const query = new URLSearchParams(window.location.search).get('recommended');
      model = buildPackageTable(pk, { recommended: query });
      if (!selectedSku || !pk.packages.some(p => p.sku === selectedSku)) {
        // Same posture as the legacy deck, which pre-selects the popular plan:
        // land with the recommended package in the cart so "Continue" works.
        selectedSku = model.recommendedSku;
      }
      if (selectedSku) persist(selectedSku);
      status = 'table';
    })();
    return () => { cancelled = true; };
  });

  function persist(sku: string) {
    if (!data) return;
    const pkg = data.packages.find(p => p.sku === sku);
    if (!pkg) return;
    setPackage({
      planId: catalogPlanIdForPackage(pkg, plans),
      planName: pkg.name,
      packageSku: pkg.sku,
      // A BSS add-on picked on step 3 for another package stays only where this
      // package still offers it as optional; catalog add-ons are untouched.
      addons: pruneAddonsForPackage(data, pkg.sku, readCart().addons),
    });
  }

  function choose(sku: string) {
    selectedSku = sku;
    persist(sku);
    window.location.assign('/apps');
  }
</script>

{#if status !== 'table' || !model}
  <!-- The legacy deck: the server-rendered first paint, the whole step when
       the document is unreachable, and replaced in place once it arrives. -->
  <PlanStep />
{:else}
  <div class="pk-page">
    <h1 class="pk-title">{S.title}</h1>
    <p class="pk-sub">{S.subtitle}</p>

    <div class="pk-scroll">
      <table class="pk-table" data-testid="package-table">
        <thead>
          <tr class="pk-hat-row">
            <td></td>
            {#each model.columns as col (col.sku)}
              <td class="pk-hat-cell">
                {#if col.sku === model.recommendedSku}
                  <span class="pk-hat">{S.recommended}</span>
                {/if}
              </td>
            {/each}
          </tr>
          <tr>
            <th scope="col" class="pk-feature-head">{S.featureColumn}</th>
            {#each model.columns as col (col.sku)}
              <th
                scope="col"
                class="pk-col {col.sku === model.recommendedSku ? 'recommended' : ''} {col.sku === selectedSku ? 'selected' : ''}"
                data-testid="package-col-{col.sku}"
                data-recommended={col.sku === model.recommendedSku ? 'true' : 'false'}
                data-selected={col.sku === selectedSku ? 'true' : 'false'}
              >
                <div class="pk-name">{col.name}</div>
                <div class="pk-price">
                  <span class="pk-cur">{model.currency}</span>
                  <strong>{col.priceMonth}</strong>
                  <span class="pk-per">{S.perMonth}</span>
                </div>
                <ul class="pk-includes">
                  {#each includesLines(col.includes) as line}
                    <li>{line}</li>
                  {/each}
                </ul>
              </th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each model.rows as row (row.key)}
            <tr data-testid="package-row-{row.key}">
              <th scope="row" class="pk-feature">
                <span class="pk-feature-name">{row.name}</span>
                {#if row.blurb}<small class="pk-blurb">{row.blurb}</small>{/if}
              </th>
              {#each row.cells as cell (cell.sku)}
                <td
                  class="pk-cell {cell.state} {cell.sku === model.recommendedSku ? 'recommended' : ''} {cell.sku === selectedSku ? 'selected' : ''}"
                  data-testid="package-cell-{row.key}-{cell.sku}"
                  data-state={cell.state}
                >
                  {#if cell.state === 'optional'}
                    <span class="pk-addon" aria-label="{cell.stateLabel}: {cell.label}">
                      <span class="pk-addon-tag">{S.addonTag}</span>
                      <span class="pk-addon-price">{cell.label}</span>
                    </span>
                  {:else}
                    <span class="pk-glyph" role="img" aria-label="{cell.stateLabel}{cell.quantity !== null ? `: ${cell.label}` : ''}">{cell.label}</span>
                  {/if}
                  {#if cell.hint}
                    <div class="pk-hint">{cell.hint}</div>
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
        <tfoot>
          <tr>
            <td></td>
            {#each model.columns as col (col.sku)}
              <td class="pk-foot {col.sku === model.recommendedSku ? 'recommended' : ''} {col.sku === selectedSku ? 'selected' : ''}">
                <button
                  type="button"
                  class="pk-cta {col.sku === selectedSku ? 'primary' : 'ghost'}"
                  data-testid="package-choose-{col.sku}"
                  onclick={() => choose(col.sku)}
                >
                  {col.sku === selectedSku ? S.continueWith(col.name) : S.choose(col.name)}
                </button>
              </td>
            {/each}
          </tr>
        </tfoot>
      </table>
    </div>

    <p class="pk-meta">
      <span>{S.addonsNote}</span>
      {#if model.pricesAsOf}
        <span class="pk-meta-sep">·</span>
        <span>{S.pricesAsOf(model.pricesAsOf)}{model.priceBook ? ` — ${model.priceBook}` : ''}</span>
      {/if}
    </p>
  </div>

  <div class="float-nav">
    <a href="/apps" class="float-cta">{S.continueCta}</a>
  </div>
{/if}

<style>
  .pk-page { max-width: 1280px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }
  .pk-title {
    text-align: center;
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.2rem;
    font-weight: 700;
    letter-spacing: -0.01em;
  }
  .pk-sub {
    text-align: center;
    color: var(--color-text-dim);
    font-size: 0.85rem;
    margin: 0 0 0.9rem;
  }

  /* The table may be wider than a phone; it scrolls inside its own box so the
     page body never scrolls sideways. */
  .pk-scroll { overflow-x: auto; -webkit-overflow-scrolling: touch; }

  .pk-table {
    width: 100%;
    min-width: 760px;
    border-collapse: separate;
    border-spacing: 0.45rem 0;
    table-layout: fixed;
  }
  .pk-table th, .pk-table td { vertical-align: middle; }

  /* Gutter column — feature labels */
  .pk-feature-head {
    text-align: left;
    color: var(--color-text-dimmer);
    font-size: 0.72rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    padding: 0 0.6rem 0.5rem;
    width: 220px;
  }
  .pk-feature {
    text-align: left;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px dashed var(--color-border);
    font-weight: 500;
  }
  .pk-feature-name { display: block; color: var(--color-text); font-size: 0.82rem; }
  .pk-blurb { display: block; color: var(--color-text-dimmer); font-size: 0.68rem; line-height: 1.3; margin-top: 0.1rem; }

  /* Recommended hat row */
  .pk-hat-row td { padding: 0; height: 1.7rem; }
  .pk-hat {
    display: block;
    background: var(--color-warn, #f59e0b);
    color: #000;
    text-align: center;
    padding: 0.3rem 0;
    font-size: 0.68rem;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    border-radius: 10px 10px 0 0;
    white-space: nowrap;
  }

  /* Package header cells */
  .pk-col {
    text-align: center;
    padding: 0.8rem 0.6rem 0.7rem;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-bottom: 1px solid var(--color-border);
    border-radius: 12px 12px 0 0;
  }
  .pk-col.recommended { border-top-left-radius: 0; border-top-right-radius: 0; border-top-color: var(--color-warn, #f59e0b); }
  .pk-col.selected { border-color: var(--color-success); background: color-mix(in srgb, var(--color-success) 5%, var(--color-surface)); }
  .pk-name { color: var(--color-text-strong); font-size: 1.1rem; font-weight: 700; }
  .pk-price {
    display: flex;
    align-items: baseline;
    justify-content: center;
    gap: 0.2rem;
    white-space: nowrap;
    margin: 0.25rem 0 0.4rem;
  }
  .pk-price strong { font-size: 1.7rem; font-weight: 800; color: var(--color-text-strong); line-height: 1; }
  .pk-cur, .pk-per { color: var(--color-text-dim); font-size: 0.76rem; font-weight: 600; }
  .pk-includes {
    list-style: none;
    margin: 0;
    padding: 0;
    color: var(--color-text-dim);
    font-size: 0.74rem;
    line-height: 1.45;
  }

  /* Body cells */
  .pk-cell {
    text-align: center;
    padding: 0.45rem 0.5rem;
    background: var(--color-surface);
    border-left: 1.5px solid var(--color-border);
    border-right: 1.5px solid var(--color-border);
    border-bottom: 1px dashed var(--color-border);
    color: var(--color-text);
    font-size: 0.8rem;
  }
  .pk-cell.selected { border-left-color: var(--color-success); border-right-color: var(--color-success); background: color-mix(in srgb, var(--color-success) 5%, var(--color-surface)); }
  .pk-cell.included .pk-glyph { color: var(--color-success); font-weight: 700; }
  .pk-cell.not_offered .pk-glyph { color: var(--color-text-dimmer); }
  .pk-glyph { display: inline-block; min-width: 1.2em; }

  /* Optional: a muted "add-on" tag with the price beside it — nothing to tick
     here; the pick happens on the Add-ons step. */
  .pk-addon {
    display: inline-flex;
    align-items: baseline;
    gap: 0.4rem;
    white-space: nowrap;
  }
  .pk-addon-tag {
    color: var(--color-text-dimmer);
    font-size: 0.66rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .pk-addon-price { color: var(--color-text); font-size: 0.78rem; font-weight: 600; }

  .pk-hint {
    margin-top: 0.15rem;
    color: var(--color-text-dimmer);
    font-size: 0.68rem;
    font-style: italic;
    white-space: nowrap;
  }

  /* Foot — CTA */
  .pk-foot {
    padding: 0.7rem 0.6rem;
    background: var(--color-bg);
    border: 1.5px solid var(--color-border);
    border-top: 1px solid var(--color-border);
    border-radius: 0 0 12px 12px;
  }
  .pk-foot.selected { border-color: var(--color-success); }
  .pk-cta {
    width: 100%;
    padding: 0.6rem 0.5rem;
    border-radius: 7px;
    border: none;
    font-weight: 600;
    font-size: 0.82rem;
    text-align: center;
    cursor: pointer;
    white-space: nowrap;
    transition: background 0.15s, border-color 0.15s, color 0.15s;
  }
  .pk-cta.primary {
    background: var(--color-success);
    color: #fff;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--color-success) 30%, transparent);
  }
  .pk-cta.primary:hover { filter: brightness(0.95); }
  .pk-cta.ghost {
    background: transparent;
    color: var(--color-text);
    border: 1.5px solid var(--color-border-strong, var(--color-border));
  }
  .pk-cta.ghost:hover {
    border-color: var(--color-success);
    color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 8%, transparent);
  }

  .pk-meta {
    margin: 0.9rem 0 0;
    text-align: center;
    color: var(--color-text-dimmer);
    font-size: 0.72rem;
  }
  .pk-meta-sep { margin: 0 0.4rem; }

  /* Floating navigation pill — same as the other funnel steps */
  .float-nav {
    position: fixed;
    bottom: 1.25rem;
    left: 50%;
    transform: translateX(-50%);
    z-index: 100;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    background: color-mix(in srgb, var(--color-surface) 95%, transparent);
    backdrop-filter: blur(12px);
    border: 1px solid var(--color-border);
    border-radius: 999px;
    padding: 0.35rem 0.4rem;
    box-shadow: 0 4px 24px rgba(0, 0, 0, 0.2);
  }
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

  @media (max-width: 1000px) {
    .pk-feature-head { width: 170px; }
    .pk-name { font-size: 0.95rem; }
    .pk-price strong { font-size: 1.35rem; }
    .pk-cta { font-size: 0.74rem; padding: 0.45rem 0.35rem; }
    .pk-cell { font-size: 0.74rem; padding: 0.4rem 0.3rem; }
  }
</style>
