<script lang="ts">
  // Step 1 of the wizard with a v2 package document (#6971): the LADDER.
  // Four package cards side by side (name, tagline, price, the annual line,
  // the shape with its guarantee in small type, the disk), then ONE grouped
  // comparison — a header row per group, its features under it — and the
  // floor strip once beneath. Choosing a package is the parent's job
  // (PackageTable.svelte): it stamps the cart exactly as the legacy deck did
  // and continues to Stack. Nothing is ticked here; add-ons are step 3.
  //
  // No <style> block, deliberately: this component is reached only after the
  // document arrives in the browser, never by the server render, so a scoped
  // stylesheet here would be dropped from the production bundle with the
  // unreachable branch. Its classes are styled by src/styles/package-ladder.css,
  // imported by plans.astro as a page stylesheet (bundled unconditionally).
  import { PACKAGE_STRINGS as S, type LadderModel } from '../lib/packages';

  let {
    model,
    selectedSku,
    onchoose,
  }: {
    model: LadderModel;
    selectedSku: string | null;
    onchoose: (sku: string) => void;
  } = $props();

  const colClass = (sku: string) =>
    `${sku === model.recommendedSku ? 'recommended' : ''} ${sku === selectedSku ? 'selected' : ''}`;
</script>

<div class="ld-page" data-testid="package-ladder">
  <h1 class="ld-title">{S.title}</h1>
  <p class="ld-sub">{S.subtitle}</p>

  <div class="ld-scroll">
    <div
      class="ld-grid"
      role="table"
      aria-label={S.title}
      style="--ld-cols: {model.cards.length}"
      data-testid="package-ladder-grid"
    >
      <!-- The cards -->
      <div role="row" class="ld-row ld-head-row">
        <div role="columnheader" class="ld-corner" aria-label={S.featureColumn}></div>
        {#each model.cards as card (card.sku)}
          <div
            role="columnheader"
            class="ld-card {colClass(card.sku)}"
            data-testid="package-card-{card.sku}"
            data-recommended={card.recommended ? 'true' : 'false'}
            data-selected={card.sku === selectedSku ? 'true' : 'false'}
          >
            <div class="ld-hat">
              {#if card.recommended}
                <span class="ld-hat-pill">{S.recommended}</span>
              {/if}
            </div>
            <div class="ld-name">{card.name}</div>
            {#if card.tagline}
              <div class="ld-tagline">{card.tagline}</div>
            {/if}
            <div class="ld-price">
              <span class="ld-cur">{model.currency}</span>
              <strong>{card.priceMonth}</strong>
              <span class="ld-per">{S.perMonth}</span>
            </div>
            {#if card.annualLine}
              <div class="ld-annual">{card.annualLine}</div>
            {/if}
            {#if card.shapeHeadline}
              <div class="ld-shape">{card.shapeHeadline}</div>
            {/if}
            {#if card.shapeGuarantee}
              <small class="ld-guarantee">{card.shapeGuarantee}</small>
            {/if}
            {#if card.diskLine}
              <div class="ld-disk">{card.diskLine}</div>
            {/if}
            <button
              type="button"
              class="ld-cta {card.sku === selectedSku ? 'primary' : 'ghost'}"
              data-testid="package-choose-{card.sku}"
              onclick={() => onchoose(card.sku)}
            >
              {card.sku === selectedSku ? S.continueWith(card.name) : S.choose(card.name)}
            </button>
          </div>
        {/each}
      </div>

      <!-- One grouped comparison -->
      {#each model.groups as group (group.key)}
        <div role="row" class="ld-row ld-group-row" data-testid="package-group-{group.key}">
          <div role="columnheader" class="ld-group" aria-colspan={model.cards.length + 1}>{group.name}</div>
        </div>
        {#each group.rows as row (row.key)}
          <div role="row" class="ld-row" data-testid="package-row-{row.key}">
            <div role="rowheader" class="ld-feature">
              <span class="ld-feature-name">{row.name}</span>
              {#if row.blurb}<small class="ld-blurb">{row.blurb}</small>{/if}
            </div>
            {#each row.cells as cell (cell.sku)}
              <div
                role="cell"
                class="ld-cell {cell.state} {colClass(cell.sku)}"
                data-testid="package-cell-{row.key}-{cell.sku}"
                data-state={cell.state}
              >
                {#if cell.state === 'optional'}
                  <span class="ld-addon" aria-label="{cell.stateLabel}: {cell.label}">
                    <span class="ld-addon-tag">{S.ladder.addonTag}</span>
                    <span class="ld-addon-price">{cell.label}</span>
                  </span>
                {:else}
                  <span class="ld-glyph" role="img" aria-label="{cell.stateLabel}{cell.label !== S.includedGlyph && cell.label !== S.notOfferedGlyph ? `: ${cell.label}` : ''}">{cell.label}</span>
                {/if}
                {#if cell.hint}
                  <div class="ld-hint">{cell.hint}</div>
                {/if}
              </div>
            {/each}
          </div>
        {/each}
      {/each}
    </div>
  </div>

  <!-- The floor, once -->
  {#if model.floor.length > 0}
    <p class="ld-floor" data-testid="package-floor">
      <span class="ld-floor-lead">{S.ladder.floorLead}</span>
      {#each model.floor as item, i (item)}
        {#if i > 0}<span class="ld-floor-sep" aria-hidden="true">·</span>{/if}
        <span class="ld-floor-item">{item}</span>
      {/each}
    </p>
  {/if}

  <p class="ld-meta">
    <span>{S.ladder.addonsNote}</span>
    {#if model.pricesAsOf}
      <span class="ld-meta-sep">·</span>
      <span>{S.pricesAsOf(model.pricesAsOf)}{model.priceBook ? ` — ${model.priceBook}` : ''}</span>
    {/if}
  </p>
</div>

<div class="ld-nav">
  <a href="/apps" class="ld-nav-cta">{S.continueCta}</a>
</div>
