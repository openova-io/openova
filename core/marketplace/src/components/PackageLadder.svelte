<script lang="ts">
  // Step 1 of the wizard with a v2 package document (#6971): the LADDER.
  // Four package cards side by side (icon, name, badge, tagline, price, the
  // annual line, the shape with its guarantee in small type, the disk), then
  // the floor — "Included in every package", a block of tiles right under the
  // cards — then ONE grouped comparison: a header row per group, its features
  // under it. Choosing a package is the parent's job
  // (PackageTable.svelte): it stamps the cart exactly as the legacy deck did
  // and continues to Stack. Nothing is ticked here; add-ons are step 3.
  //
  // Branding comes from the document only: a package's `icon`, `accent` and
  // `badge`, a group's / floor item's / feature's `icon` (validated and
  // resolved by packages.ts::parseIcon). Nothing is keyed by sku or feature
  // key here; with none published the ladder renders exactly as it did
  // without branding — no empty box, no placeholder. Every icon sits beside
  // the name it depicts, so every <img> is decorative (alt="").
  //
  // No <style> block, deliberately: this component is reached only after the
  // document arrives in the browser, never by the server render, so a scoped
  // stylesheet here would be dropped from the production bundle with the
  // unreachable branch. Its classes are styled by src/styles/package-ladder.css,
  // imported by plans.astro as a page stylesheet (bundled unconditionally).
  import { PACKAGE_STRINGS as S, type LadderCard, type LadderModel } from '../lib/packages';

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
    `${sku === model.recommendedSku ? 'recommended' : ''} ${sku === selectedSku ? 'selected' : ''} ${accentOf(sku) ? 'has-accent' : ''}`;

  const accentOf = (sku: string): LadderCard | null => {
    const c = model.cards.find(x => x.sku === sku);
    return c && c.accent ? c : null;
  };
  // The package's brand colour and its readable foreground as custom
  // properties on every element of its column; the stylesheet decides where
  // they show (stripe, ring, chip, CTA).
  const colStyle = (sku: string) => {
    const c = accentOf(sku);
    return c ? `--pk-accent: ${c.accent}; --pk-accent-fg: ${c.accentFg};` : '';
  };
  const tileStyle = (bg: string | undefined) => (bg ? `background: ${bg}` : '');
</script>

<div class="ld-page" data-testid="package-ladder">
  <h1 class="ld-title">{S.title}</h1>
  <p class="ld-sub">{S.subtitle}</p>

  <div class="ld-scroll">
    <div
      class="ld-grid {model.rowIcons ? 'with-icons' : ''}"
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
            style={colStyle(card.sku)}
            data-testid="package-card-{card.sku}"
            data-recommended={card.recommended ? 'true' : 'false'}
            data-selected={card.sku === selectedSku ? 'true' : 'false'}
          >
            <div class="ld-hat">
              {#if card.recommended}
                <span class="ld-hat-pill">{S.recommended}</span>
              {/if}
            </div>
            {#if card.icon}
              <span class="ld-card-icon" style={tileStyle(card.icon.bg)}>
                <img src={card.icon.src} alt="" width="28" height="28" loading="lazy" decoding="async" data-testid="package-icon-{card.sku}" />
              </span>
            {/if}
            <div class="ld-name">{card.name}</div>
            {#if card.badge}
              <span class="ld-badge" data-testid="package-badge-{card.sku}">{card.badge}</span>
            {/if}
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

      <!-- #6971 — one light line under the cards: every package can grow
           (chosen on Add-ons), and — when the document's per-package rates
           differ — that bigger packages grow cheaper. Absent when no
           package in the document can grow. -->
      {#if model.growNote}
        <div role="row" class="ld-row ld-grow-row">
          <div role="cell" class="ld-grow-band" aria-colspan={model.cards.length + 1} data-testid="package-grow-note">
            <span class="ld-grow-inner">
              <svg class="ld-grow-ico" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/></svg>
              <strong>{S.grow.plansNote}</strong>
              <span>{S.grow.plansNoteBody}</span>
              {#if model.growCheapest}
                <span class="ld-grow-cheaper" data-testid="package-grow-cheapest">{S.grow.plansCheaper(model.growCheapest.priceMonth, model.currency, model.growCheapest.name)}</span>
              {/if}
            </span>
          </div>
        </div>
      {/if}

      <!-- The floor, first-class: right under the cards, above the
           comparison — what every package includes, as tiles (icon, name,
           blurb) rendered from the document's `floor` only. A new floor item
           appears here without a release. The tiles stick to the visible
           width when the comparison scrolls sideways on a phone. -->
      {#if model.floorItems.length > 0}
        <div role="row" class="ld-row ld-floor-row">
          <section role="cell" class="ld-floor-band" aria-colspan={model.cards.length + 1} data-testid="package-floor">
            <div class="ld-floor-inner">
              <div class="ld-floor-head">
                <h2 class="ld-floor-title">{S.ladder.floorTitle}</h2>
                <p class="ld-floor-sub">{S.ladder.floorSub}</p>
              </div>
              <ul class="ld-floor-grid">
                {#each model.floorItems as item (item.key)}
                  <li class="ld-floor-tile" data-testid="package-floor-{item.key}">
                    {#if item.icon}
                      <span class="ld-ico ld-floor-ico" style={tileStyle(item.icon.bg)}>
                        <img src={item.icon.src} alt="" width="18" height="18" loading="lazy" decoding="async" />
                      </span>
                    {/if}
                    <span class="ld-floor-text">
                      <span class="ld-floor-item">{item.name}</span>
                      {#if item.blurb}<small class="ld-floor-blurb">{item.blurb}</small>{/if}
                    </span>
                  </li>
                {/each}
              </ul>
            </div>
          </section>
        </div>
      {/if}

      <!-- One grouped comparison -->
      {#each model.groups as group (group.key)}
        <div role="row" class="ld-row ld-group-row" data-testid="package-group-{group.key}">
          <div role="columnheader" class="ld-group" aria-colspan={model.cards.length + 1}>
            {#if group.icon}
              <img class="ld-group-icon" src={group.icon.src} alt="" width="16" height="16" loading="lazy" decoding="async" />
            {/if}
            <span class="ld-group-name">{group.name}</span>
          </div>
        </div>
        {#each group.rows as row (row.key)}
          <div role="row" class="ld-row" data-testid="package-row-{row.key}">
            <div role="rowheader" class="ld-feature {row.icon ? 'has-ico' : ''}">
              {#if row.icon}
                <span class="ld-ico" style={tileStyle(row.icon.bg)}>
                  <img src={row.icon.src} alt="" width="16" height="16" loading="lazy" decoding="async" />
                </span>
              {/if}
              <span class="ld-feature-text">
                <span class="ld-feature-name">{row.name}</span>
                {#if row.blurb}<small class="ld-blurb">{row.blurb}</small>{/if}
              </span>
            </div>
            {#each row.cells as cell (cell.sku)}
              <div
                role="cell"
                class="ld-cell {cell.state} {colClass(cell.sku)}"
                style={colStyle(cell.sku)}
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

      <!-- The foot: the same Choose per column, so the customer who read the
           whole comparison need not scroll back up. There is no floating bar
           on this step — nothing ever covers a cell. -->
      <div role="row" class="ld-row ld-foot-row" data-testid="package-ladder-foot">
        <div role="cell" class="ld-foot-corner"></div>
        {#each model.cards as card (card.sku)}
          <div role="cell" class="ld-foot {colClass(card.sku)}" style={colStyle(card.sku)}>
            <button
              type="button"
              class="ld-cta {card.sku === selectedSku ? 'primary' : 'ghost'}"
              data-testid="package-choose-foot-{card.sku}"
              onclick={() => onchoose(card.sku)}
            >
              {card.sku === selectedSku ? S.continueWith(card.name) : S.choose(card.name)}
            </button>
          </div>
        {/each}
      </div>
    </div>
  </div>

  <p class="ld-meta">
    <span>{S.ladder.addonsNote}</span>
    {#if model.pricesAsOf}
      <span class="ld-meta-sep">·</span>
      <span>{S.pricesAsOf(model.pricesAsOf)}{model.priceBook ? ` — ${model.priceBook}` : ''}</span>
    {/if}
  </p>
</div>
