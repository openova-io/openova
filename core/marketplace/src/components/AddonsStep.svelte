<script lang="ts">
  import { getAddons, getApps, getPlans, checkSlug, type AddOn, type App, type Plan } from '../lib/api';
  import { readCart, toggleAddon, setOrgDetails, setPackage, setTLD, setOverage, setAppConfig, writeCart, DEFAULT_TLD } from '../lib/cart';
  import { formatOMR } from '../lib/currency';
  import { chargebackBaseURL } from '../lib/config';
  import {
    addonsLadderFor,
    catalogPlanIdForPackage,
    clampGrowCeiling,
    drTopologyFor,
    funnelAddonsFor,
    growModelFor,
    growSelectionFor,
    growUpgradeHint,
    isLadderDocument,
    isPackageCeiling,
    loadPublicPackages,
    minorUnits,
    normalizeSpendLimit,
    packageForCart,
    pruneAddonsForPackage,
    stepGrow,
    stepUpHint,
    PACKAGE_STRINGS as PS,
    type AddonsLadder,
    type GrowDim,
    type IncludedFeature,
    type OverageMode,
    type PublicPackages,
  } from '../lib/packages';

  let addons = $state<AddOn[]>([]);
  // #6971 — when the Sovereign's BSS publishes the package document, the
  // chosen package's INCLUDED boolean features render here read-only, above
  // the optional extras (which are then its OPTIONAL features). Empty without
  // the document, so this step is exactly today's.
  let bssIncluded = $state<IncludedFeature[]>([]);
  let packageName = $state('');
  // #6971, v2 document — the ladder's three blocks for the chosen package:
  // "In your package" (block A), "Add-ons" (block B: the optional cells and
  // the next-level add-ons, the tiles below), "Not on <pkg>" (block C: the
  // teaser / not-offered features with the rung that has them), plus the
  // step-up hint and the running total. Null with a v1 document or none, so
  // this step is then exactly as before.
  let doc = $state<PublicPackages | null>(null);
  let catalogAddons = $state<AddOn[]>([]);
  let plans = $state<Plan[]>([]);
  let ladder = $state<AddonsLadder | null>(null);
  let allApps = $state<App[]>([]);
  let cart = $state(readCart());
  let loading = $state(true);

  // Resolve backing services: for each selected app, pull the app record whose
  // `kind === 'service'` appears in its dependencies list. A service may be
  // shared across multiple apps — dedupe by slug so the card grid lists each
  // exactly once.
  const backingServices = $derived.by(() => {
    const selected = allApps.filter(a => cart.apps.includes(a.id));
    const depSlugs = new Set<string>();
    for (const app of selected) {
      for (const dep of app.dependencies ?? []) depSlugs.add(dep);
    }
    const out: App[] = [];
    for (const slug of depSlugs) {
      const svc = allApps.find(a => a.slug === slug && a.kind === 'service');
      if (svc) out.push(svc);
    }
    return out;
  });
  let subdomain = $state(cart.subdomain);
  // Hydrate TLD from cart so a user who picked .omani.homes, navigated
  // forward, then came back, sees their choice — not the default. The
  // change handler below persists every flip immediately (no blur required)
  // so /review + /checkout always read the latest value.
  let selectedTLD = $state(cart.tld || DEFAULT_TLD);
  let byodDomain = $state('');

  const tlds = ['omani.rest', 'omani.works', 'omani.trade', 'omani.homes'];

  function persistTLD() {
    cart = setTLD(selectedTLD);
  }

  // Subdomain availability check (same logic as CheckoutStep so the state is
  // consistent — user doesn't re-learn at checkout that their subdomain is taken).
  let slugStatus = $state<'idle' | 'checking' | 'available' | 'taken' | 'invalid'>('idle');
  let slugChecked = $state('');
  let slugTimer: ReturnType<typeof setTimeout> | null = null;

  function normalizeSlug(s: string): string {
    return s.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
  }

  $effect(() => {
    void subdomain; // re-run when subdomain changes
    if (slugTimer) { clearTimeout(slugTimer); slugTimer = null; }
    const s = normalizeSlug(subdomain);
    if (!s) { slugStatus = 'idle'; slugChecked = ''; return; }
    if (s.length < 3) { slugStatus = 'invalid'; slugChecked = s; return; }
    slugStatus = 'checking';
    slugChecked = s;
    slugTimer = setTimeout(async () => {
      try {
        const { available } = await checkSlug(s);
        if (slugChecked !== s) return;
        slugStatus = available ? 'available' : 'taken';
      } catch {
        if (slugChecked !== s) return;
        slugStatus = 'idle';
      }
    }, 400);
  });

  $effect(() => {
    getApps().then(apps => { allApps = apps; }).catch(() => {});
  });

  // The stand-in when BOTH the catalog and the package document are down.
  // Price-free: a catalog add-on carries no price of its own (the catalog's
  // add-ons are free; only a BSS add-on from the package document shows a
  // price anywhere in the journey), so nothing here can put a number on the
  // page that is not in the price book.
  const FALLBACK_ADDONS: AddOn[] = [
    { id: 'daily-backup', name: 'Daily Backup', slug: 'daily-backup', tagline: 'Scheduled backups of your sites and databases', icon: '🛡️', monthly_price: 0, included: false },
    { id: 'waf', name: 'Web Application Firewall', slug: 'waf', tagline: 'Coraza WAF — OWASP CRS protection', icon: '🔥', monthly_price: 0, included: false },
    { id: 'ips', name: 'Intrusion Prevention', slug: 'ips', tagline: 'Community-powered threat intelligence — CrowdSec', icon: '🚨', monthly_price: 0, included: false },
    { id: 'vuln-scan', name: 'Vulnerability Scanner', slug: 'vuln-scan', tagline: 'Scheduled CVE scans + remediation reports', icon: '🔍', monthly_price: 0, included: false },
    { id: 'custom-domain', name: 'Custom Domain', slug: 'custom-domain', tagline: 'Your brand, your domain — with automatic TLS', icon: '🌐', monthly_price: 0, included: false },
    { id: 'log-management', name: 'Log Management', slug: 'log-management', tagline: 'Search and analyze all your app logs — Grafana Loki', icon: '📋', monthly_price: 0, included: false },
  ];

  // #6971 — the catalog list as today; then, when the BSS package document is
  // available and the cart stands on one of its packages, that package's
  // OPTIONAL features become the add-ons (BSS `addon.*` SKUs, same AddOn shape),
  // its INCLUDED boolean features render read-only, not-offered ones never
  // appear, and a catalog add-on that twins a BSS feature yields to it. No
  // document → `addons` is the catalog list, exactly as before.
  $effect(() => {
    Promise.all([
      getAddons().catch(() => FALLBACK_ADDONS),
      loadPublicPackages(chargebackBaseURL()),
      getPlans().catch(() => [] as Plan[]),
    ]).then(([catalog, d, pl]) => {
      catalogAddons = catalog;
      doc = d;
      plans = pl;
      const pkg = d ? packageForCart(d, cart) : null;
      if (d && pkg) {
        // With a document the cart's add-ons are BSS SKUs this package sells;
        // a catalog id left from a session before the document existed is
        // carried over to its BSS twin or dropped — it is not in the price
        // book, so it is not in the journey.
        const pruned = pruneAddonsForPackage(d, pkg.sku, cart.addons, catalog);
        if (pruned.join('\u0000') !== cart.addons.join('\u0000')) {
          cart = setPackage({
            planId: cart.plan || catalogPlanIdForPackage(pkg, pl),
            planName: pkg.name,
            packageSku: pkg.sku,
            addons: pruned,
          });
        }
        applyPackage(d, pkg.sku);
      } else {
        addons = catalog;
      }
      loading = false;
    });
  });

  // The step's lists for a package of the document in hand. A v2 document
  // yields the ladder blocks; a v1 one the included group + the BSS list.
  function applyPackage(d: PublicPackages, sku: string) {
    const l = isLadderDocument(d) ? addonsLadderFor(d, sku) : null;
    if (l) {
      ladder = l;
      addons = l.addons;
      bssIncluded = l.included.map(i => ({ key: i.key, name: i.name, blurb: i.value ?? i.blurb }));
      packageName = l.packageName;
      return;
    }
    ladder = null;
    const merged = funnelAddonsFor(d, sku);
    addons = merged.addons;
    bssIncluded = merged.included;
    packageName = merged.packageName;
  }

  // Block C's "Upgrade to L" and the step-up card both switch the package in
  // the cart the way step 1 does — catalog plan id + sku — keeping the chosen
  // apps and every add-on the new package still offers (a BSS add-on it
  // includes as standard is dropped: billing would refuse it as redundant).
  function switchPackage(sku: string) {
    if (!doc) return;
    const pkg = doc.packages.find(p => p.sku === sku);
    if (!pkg) return;
    cart = setPackage({
      planId: catalogPlanIdForPackage(pkg, plans),
      planName: pkg.name,
      packageSku: pkg.sku,
      addons: pruneAddonsForPackage(doc, pkg.sku, cart.addons, catalogAddons),
    });
    applyPackage(doc, pkg.sku);
  }

  const paidAddons = $derived(addons.filter(a => !a.included));
  const stepUp = $derived(doc && ladder ? stepUpHint(doc, ladder, cart.addons) : null);
  // The running total here is a preview from the document's own prices; the
  // figure the customer agrees to is the server's quote on Review / Checkout.
  const tickedBaisa = $derived(addons.filter(a => !a.included && cart.addons.includes(a.id)).reduce((s, a) => s + a.monthly_price, 0));
  const runningTotalBaisa = $derived(ladder ? ladder.packagePriceBaisa + tickedBaisa : 0);

  function toggle(id: string) {
    cart = toggleAddon(id);
  }

  // #6971 — "When you reach your package": Capped (the default; the bill never
  // exceeds the package + add-ons) or Grow with me (usage above the allowance
  // billed after the month at the package's own rates, up to a ceiling, with
  // an optional monthly spend limit). Everything shown comes from the
  // document: the rates, the allowance, the ceiling, the "the next package is
  // cheaper" arithmetic. No grow on the package (or no document) → no block,
  // and the order stays capped.
  const cartSku = $derived(doc ? packageForCart(doc, cart)?.sku ?? null : null);
  const growModel = $derived(doc && cartSku ? growModelFor(doc, cartSku) : null);
  const growSel = $derived(growSelectionFor(doc, cartSku, cart));
  const growMode = $derived<OverageMode>(growSel.mode);
  const shownCeiling = $derived(growModel ? (growSel.ceiling ?? growModel.ceiling) : null);
  const growHint = $derived(doc && cartSku && growModel ? growUpgradeHint(doc, cartSku) : null);
  // What "Capped" promises: the package + the ticked add-ons, from the
  // document's own prices (the quote on Review is the billed figure).
  const cappedBaisa = $derived(
    (doc && cartSku ? minorUnits(doc.packages.find(p => p.sku === cartSku)?.price_month) : 0) + tickedBaisa,
  );
  let spendText = $state(cart.spendLimitMonth ?? '');
  let spendError = $state(false);

  function chooseMode(mode: OverageMode) {
    cart = setOverage({ mode });
    // Active-passive on a package where it exists only with Grow cannot stay
    // in the cart once the customer goes back to Capped.
    if (mode === 'capped' && doc && cartSku && drTopologyFor(doc, cartSku)?.growOnly) {
      const pg = (cart.appConfigs ?? {}).postgres ?? {};
      if (pg.active_hot_standby) {
        const next: Record<string, number | string | boolean> = { ...pg, active_hot_standby: false };
        delete (next as Record<string, unknown>).primary_region;
        delete (next as Record<string, unknown>).replica_region;
        cart = setAppConfig('postgres', next);
      }
    }
  }

  function stepCeiling(dim: GrowDim, direction: 1 | -1) {
    if (!growModel || !shownCeiling) return;
    const next = clampGrowCeiling(growModel, { ...shownCeiling, [dim.key]: stepGrow(dim, shownCeiling[dim.key], direction) });
    cart = setOverage({ growCeiling: isPackageCeiling(growModel, next) ? null : next });
  }

  function onSpendInput(value: string) {
    spendText = value;
    const n = normalizeSpendLimit(value);
    spendError = n === undefined;
    if (n !== undefined) cart = setOverage({ spendLimitMonth: n });
  }
  function onSpendBlur() {
    const n = normalizeSpendLimit(spendText);
    if (typeof n === 'string') spendText = n;
  }

  function rateLine(r: { price_month: string; key: string }): string {
    return PS.grow.rateLine(r.price_month, doc?.currency ?? 'OMR', PS.grow.unitWord[r.key as keyof typeof PS.grow.unitWord] ?? r.key);
  }
  function hintExtra(h: NonNullable<typeof growHint>): string {
    return PS.grow.upgradeExtra(h.deltas.map(d => PS.grow.upgradeDelta(d.delta, d.unit)));
  }

  function saveSubdomain() {
    cart = setOrgDetails(cart.orgName, subdomain, cart.email);
    // Belt-and-braces: TLD is already persisted on <select> onchange, but
    // also flush here so a leftover stale value in localStorage from a
    // pre-fix session can't outlive a fresh subdomain save.
    if (cart.tld !== selectedTLD) cart = setTLD(selectedTLD);
  }

  // #85 — shared helper renders "OMR 3.000". Previously we rounded to whole
  // OMR here while Review and Checkout used different precision; now every
  // addon price on every step shows the same baisa-precise value.

  // Icons: nothing visual is keyed by feature key in this tree. A BSS add-on
  // or feature shows the icon the document publishes (`image` / `icon`,
  // validated and resolved by packages.ts::parseIcon) or none at all; only a
  // catalog add-on (no document) shows the catalog's own `icon` glyph.

  // The step bar's real height, measured: the page is padded by it and the
  // document's scroll-padding matches, so the bar never covers the last block
  // whatever its height at this width (the CSS 4.5rem is the first paint).
  let stepBar = $state<HTMLElement | null>(null);
  let stepBarH = $state(0);
  $effect(() => {
    const el = stepBar;
    if (!el || typeof ResizeObserver !== 'function') return;
    const measure = () => {
      stepBarH = Math.ceil(el.getBoundingClientRect().height);
      document.documentElement.style.scrollPaddingBottom = `${stepBarH + 16}px`;
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => { ro.disconnect(); document.documentElement.style.scrollPaddingBottom = ''; };
  });
</script>

<div class="addons-page" style={stepBarH > 0 ? `--step-bar-h: ${stepBarH + 16}px` : ''}>
  <div class="addons-hero">
    <h1>Setup & extras</h1>
    <p>Pick your domain and optional add-ons</p>
  </div>

  {#if loading}
    <div class="flex justify-center py-20">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else}
    <!-- Domain Section -->
    <section class="ao-section">
      <div class="ao-head">
        <h2>Your domain</h2>
        <span class="ao-badge">FREE</span>
      </div>
      <div class="domain-row">
        <div class="domain-sub">
          <label class="domain-label">Subdomain</label>
          <div class="domain-input-row">
            <input
              type="text"
              bind:value={subdomain}
              onblur={saveSubdomain}
              placeholder="my-company"
              class="domain-input"
            />
            <select bind:value={selectedTLD} onchange={persistTLD} class="domain-tld">
              {#each tlds as tld}
                <option value={tld}>.{tld}</option>
              {/each}
            </select>
          </div>
          {#if subdomain}
            <p class="domain-preview">
              Your URL: <span class="domain-url">{slugChecked || normalizeSlug(subdomain)}.{selectedTLD}</span>
            </p>
          {/if}
          <div class="slug-status">
            {#if slugStatus === 'checking'}
              <span class="ss-dim">Checking availability…</span>
            {:else if slugStatus === 'available'}
              <span class="ss-ok">✓ {slugChecked}.{selectedTLD} is available</span>
            {:else if slugStatus === 'taken'}
              <span class="ss-err">✗ {slugChecked}.{selectedTLD} is already taken</span>
            {:else if slugStatus === 'invalid'}
              <span class="ss-err">Subdomain must be at least 3 characters</span>
            {/if}
          </div>
        </div>
        <div class="domain-byod">
          <label class="domain-label">Bring your own domain <span class="domain-optional">(optional)</span></label>
          <input
            type="text"
            bind:value={byodDomain}
            placeholder="app.yourcompany.com"
            class="domain-input"
          />
          {#if byodDomain}
            <p class="domain-preview">
              We'll guide you through DNS setup after checkout.
            </p>
          {/if}
        </div>
      </div>
    </section>

    {#if backingServices.length > 0}
      <!-- Backing services — auto-installed dependencies of selected apps -->
      <section class="ao-section">
        <div class="ao-head">
          <h2>Backing services</h2>
          <span class="ao-badge">INCLUDED</span>
        </div>
        <p class="bs-hint">These services are automatically provisioned to power your apps — no setup required.</p>
        <div class="extras-grid">
          {#each backingServices as svc}
            <div class="extra-tile svc-tile">
              {#if svc.logo}
                <img src={svc.logo} alt={svc.name} class="svc-logo" />
              {:else}
                <span class="extra-icon">{svc.icon || '⚙️'}</span>
              {/if}
              <div class="extra-body">
                <strong>{svc.name}</strong>
                <p>{svc.tagline || svc.description || 'Backing service'}</p>
              </div>
              <span class="extra-price svc-price">Included</span>
            </div>
          {/each}
        </div>
      </section>
    {/if}

    {#if ladder}
      <!-- #6971, v2 — block A: what the package has, compact and read-only. -->
      <section class="ao-section" data-testid="addons-included">
        <div class="ao-head">
          <h2>{PS.ladder.inYourPackage}</h2>
          <span class="ao-badge">{ladder.packageName} · INCLUDED</span>
        </div>
        <p class="bs-hint">{PS.ladder.inYourPackageHint}</p>
        <ul class="in-pkg">
          {#each ladder.included as f (f.key)}
            <li class="in-pkg-item" data-testid="addons-included-{f.key}" title={f.blurb}>
              {#if f.icon}
                <span class="ico-tile chip" style={f.icon.bg ? `background: ${f.icon.bg}` : ''}>
                  <img src={f.icon.src} alt="" width="14" height="14" loading="lazy" decoding="async" />
                </span>
              {:else}
                <span class="in-pkg-tick" aria-hidden="true">✓</span>
              {/if}
              <span class="in-pkg-name">{f.name}</span>
              {#if f.value}<span class="in-pkg-val">{f.value}</span>{/if}
            </li>
          {/each}
        </ul>
        {#if doc?.floor && doc.floor.length > 0}
          <!-- What every package includes (the document's floor) — folded,
               so the package's own features stay the headline. -->
          <details class="in-pkg-floor" data-testid="addons-included-floor">
            <summary>{PS.ladder.floorMore(doc.floor.length)}</summary>
            <ul class="in-pkg">
              {#each doc.floor as f (f.key)}
                <li class="in-pkg-item" data-testid="addons-floor-{f.key}" title={f.blurb ?? ''}>
                  {#if f.icon}
                    <span class="ico-tile chip" style={f.icon.bg ? `background: ${f.icon.bg}` : ''}>
                      <img src={f.icon.src} alt="" width="14" height="14" loading="lazy" decoding="async" />
                    </span>
                  {:else}
                    <span class="in-pkg-tick" aria-hidden="true">✓</span>
                  {/if}
                  <span class="in-pkg-name">{f.name}</span>
                </li>
              {/each}
            </ul>
          </details>
        {/if}
      </section>
    {:else if bssIncluded.length > 0}
      <!-- #6971 — the chosen package's included features, read-only: no price,
           no toggle. Rendered only when the BSS document is available. -->
      <section class="ao-section" data-testid="addons-included">
        <div class="ao-head">
          <h2>{PS.includedInPackage}</h2>
          <span class="ao-badge">{packageName ? `${packageName} · INCLUDED` : 'INCLUDED'}</span>
        </div>
        <p class="bs-hint">{PS.includedHint}</p>
        <div class="extras-grid">
          {#each bssIncluded as f (f.key)}
            <div class="extra-tile svc-tile" data-testid="addons-included-{f.key}">
              {#if f.icon}
                <span class="ico-tile" style={f.icon.bg ? `background: ${f.icon.bg}` : ''}>
                  <img src={f.icon.src} alt="" width="18" height="18" loading="lazy" decoding="async" />
                </span>
              {:else}
                <span class="extra-icon in-pkg-tick" aria-hidden="true">✓</span>
              {/if}
              <div class="extra-body">
                <strong>{f.name}</strong>
                <p>{f.blurb}</p>
              </div>
              <span class="extra-price svc-price">Included</span>
            </div>
          {/each}
        </div>
      </section>
    {/if}

    <!-- Optional extras — tile grid (block B, "Add-ons", with a v2 document) -->
    <section class="ao-section" data-testid="addons-optional">
      <div class="ao-head">
        <h2>{ladder ? PS.ladder.addons : 'Optional extras'}</h2>
        <span class="ao-note">{ladder ? PS.ladder.addonsHint : "Skip any you don't need"}</span>
      </div>
      <div class="extras-grid">
        {#each paidAddons as addon (addon.id)}
          {@const isChecked = cart.addons.includes(addon.id)}
          <button
            type="button"
            onclick={() => toggle(addon.id)}
            class="extra-tile addon-card clickable {isChecked ? 'checked' : ''}"
            data-testid="addon-tile-{addon.id}"
            aria-pressed={isChecked}
          >
            <span class="addon-card-head">
              {#if addon.image}
                <span class="ico-tile" style={addon.image.bg ? `background: ${addon.image.bg}` : ''}>
                  <img src={addon.image.src} alt="" width="18" height="18" loading="lazy" decoding="async" />
                </span>
              {:else if !doc && addon.icon}
                <span class="extra-icon" aria-hidden="true">{addon.icon}</span>
              {/if}
              <strong class="addon-card-name">{addon.name}</strong>
              <span class="extra-price">{addon.monthly_price === 0 ? 'Free' : `+${formatOMR(addon.monthly_price)}`}</span>
              <span class="extra-check">
                {#if isChecked}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 13l4 4L19 7"/></svg>
                {:else}
                  <span class="extra-box"></span>
                {/if}
              </span>
            </span>
            {#if addon.tagline}<span class="addon-card-desc">{addon.tagline}</span>{/if}
            {#if addon.hint}<span class="addon-card-hint">{addon.hint}</span>{/if}
          </button>
        {/each}
      </div>

      {#if ladder}
        <!-- The step-up hint: the ticked add-ons the next rung includes cost
             at least the gap to it. Switching keeps the apps, drops only what
             the next package bundles. -->
        {#if stepUp}
          <div class="step-up" data-testid="addons-stepup" data-next={stepUp.nextSku}>
            <div class="step-up-body">
              <strong class="step-up-title">{PS.ladder.stepUpTitle(stepUp.nextName, stepUp.gapMonth, doc?.currency ?? 'OMR')}</strong>
              <p class="step-up-text">{PS.ladder.stepUpBody(stepUp.bundledSumMonth, doc?.currency ?? 'OMR', stepUp.nextName)}</p>
              <p class="step-up-list">
                {#each stepUp.bundled as b, i (b.id)}{#if i > 0}{' · '}{/if}<span>{b.name}</span>{/each}
              </p>
            </div>
            <button type="button" class="step-up-cta" data-testid="addons-stepup-switch" onclick={() => switchPackage(stepUp.nextSku)}>
              {PS.ladder.stepUpCta(stepUp.nextName)} &rarr;
            </button>
          </div>
        {/if}

        <!-- The running total: package + ticked add-ons, from the document's
             own prices. The quoted total on Review / Checkout is the one billed. -->
        <div class="running-total" data-testid="addons-running-total" data-baisa={runningTotalBaisa}>
          <span class="rt-label">{PS.ladder.runningTotal}</span>
          <span class="rt-parts">
            <span>{PS.ladder.packageLine(ladder.packageName)} {formatOMR(ladder.packagePriceBaisa)}</span>
            <span class="rt-sep">+</span>
            <span>{PS.ladder.addonsLine} {formatOMR(tickedBaisa)}</span>
          </span>
          <strong class="rt-total">{formatOMR(runningTotalBaisa)} <small>{PS.perMonth}</small></strong>
        </div>
      {/if}
    </section>

    {#if growModel && shownCeiling}
      <!-- #6971 — "When you reach your package": the package is a monthly
           allowance; Capped keeps it a hard limit, Grow lets it grow and bills
           the usage above it after the month. Every number is the document's. -->
      <section class="ao-section grow-section" id="grow" data-testid="addons-grow" data-mode={growMode}>
        <div class="ao-head">
          <h2>{PS.grow.title}</h2>
          <span class="ao-note">{PS.grow.hint}</span>
        </div>
        <div class="mode-grid" role="radiogroup" aria-label={PS.grow.title}>
          <button
            type="button"
            role="radio"
            aria-checked={growMode === 'capped'}
            class="mode-card {growMode === 'capped' ? 'on' : ''}"
            data-testid="mode-capped"
            onclick={() => chooseMode('capped')}
          >
            <span class="mode-top">
              <span class="mode-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v5c0 4.5-3 8.3-7 9.9-4-1.6-7-5.4-7-9.9V6l7-3z"/><path d="M9 12l2 2 4-4"/></svg>
              </span>
              <strong class="mode-title">{PS.grow.cappedTitle}</strong>
              <span class="mode-tag">{PS.grow.cappedTag}</span>
              <span class="mode-dot" aria-hidden="true"></span>
            </span>
            <span class="mode-body" data-testid="mode-capped-body">{PS.grow.cappedBody(formatOMR(cappedBaisa))}</span>
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={growMode === 'grow'}
            class="mode-card grow {growMode === 'grow' ? 'on' : ''}"
            data-testid="mode-grow"
            onclick={() => chooseMode('grow')}
          >
            <span class="mode-top">
              <span class="mode-ico" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/></svg>
              </span>
              <strong class="mode-title">{PS.grow.growTitle}</strong>
              <span class="mode-tag">{PS.grow.growTag}</span>
              <span class="mode-dot" aria-hidden="true"></span>
            </span>
            <span class="mode-body">{PS.grow.growBody}</span>
            <span class="mode-rates" data-testid="mode-grow-rates">
              {#each growModel.rates.slice(0, 2) as r (r.key)}<span>{rateLine(r)}</span>{/each}
            </span>
          </button>
        </div>

        {#if growMode === 'grow'}
          <div class="grow-panel" data-testid="grow-panel">
            <div class="grow-block">
              <span class="grow-label">{PS.grow.ratesOn(growModel.packageName)}</span>
              <ul class="grow-rates" data-testid="grow-rates">
                {#each growModel.rates as r (r.key)}
                  <li class="grow-rate" data-testid="grow-rate-{r.key}" data-price={r.price_month}>
                    <span class="grow-rate-name">{PS.grow.rateName[r.key]}</span>
                    <span class="grow-rate-val"><strong>+{r.price_month} {doc?.currency ?? 'OMR'}</strong> <small>per extra {PS.grow.unitWord[r.key]} / mo</small></span>
                  </li>
                {/each}
              </ul>
            </div>

            <div class="grow-block">
              <span class="grow-label">{PS.grow.ceilingTitle}</span>
              <p class="grow-sub">{PS.grow.ceilingHint(growModel.packageName)}</p>
              <div class="grow-steppers">
                {#each growModel.dims as d (d.key)}
                  {@const v = shownCeiling[d.key]}
                  <div class="stepper-row" data-testid="grow-ceiling-row-{d.key}">
                    <span class="stepper-name">
                      <strong>{d.label}</strong>
                      <small>{PS.grow.included(d.allowance, d.unit)}</small>
                    </span>
                    <span class="stepper">
                      <button type="button" class="step-btn" aria-label={PS.grow.decrease(d.label)} data-testid="grow-step-{d.key}-dec" disabled={v <= d.allowance} onclick={() => stepCeiling(d, -1)}>&minus;</button>
                      <output class="step-val" data-testid="grow-ceiling-{d.key}" data-value={v}>{v} <small>{d.unit}</small></output>
                      <button type="button" class="step-btn" aria-label={PS.grow.increase(d.label)} data-testid="grow-step-{d.key}-inc" disabled={v >= d.max} onclick={() => stepCeiling(d, 1)}>+</button>
                    </span>
                    <span class="stepper-bar" aria-hidden="true">
                      <span style="width: {d.max > d.allowance ? ((v - d.allowance) / (d.max - d.allowance)) * 100 : 0}%"></span>
                    </span>
                  </div>
                {/each}
              </div>
            </div>

            <div class="grow-block">
              <label class="grow-label" for="grow-spend">{PS.grow.spendTitle} <span class="grow-optional">({PS.grow.spendOptional})</span></label>
              <div class="spend-row">
                <span class="spend-cur">{doc?.currency ?? 'OMR'}</span>
                <input
                  id="grow-spend"
                  type="text"
                  inputmode="decimal"
                  placeholder="25.000"
                  class="spend-input {spendError ? 'invalid' : ''}"
                  data-testid="grow-spend"
                  aria-invalid={spendError}
                  bind:value={spendText}
                  oninput={(e) => onSpendInput(e.currentTarget.value)}
                  onblur={onSpendBlur}
                />
                <span class="spend-per">{PS.perMonth}</span>
              </div>
              <p class="grow-sub {spendError ? 'err' : ''}" data-testid="grow-spend-hint">{spendError ? PS.grow.spendInvalid : PS.grow.spendHint}</p>
            </div>

            {#if growModel.unlocksDr}
              <p class="grow-dr" data-testid="grow-dr-note">{PS.grow.drUnlocked}</p>
            {/if}
          </div>
        {/if}

        {#if growHint}
          <!-- The upgrade is the better deal when growth is regular: computed
               from the document, shown only when it is true. -->
          <div class="grow-upgrade" data-testid="grow-upgrade" data-next={growHint.nextSku} data-grown={growHint.grownBaisa}>
            <div class="grow-upgrade-body">
              <strong>{PS.grow.upgradeTitle(hintExtra(growHint), growHint.nextName)}</strong>
              <p>{PS.grow.upgradeBody(growModel.packageName, formatOMR(growHint.grownBaisa), growHint.nextName, formatOMR(growHint.nextPriceBaisa))}</p>
            </div>
            <button type="button" class="grow-upgrade-cta" data-testid="grow-upgrade-switch" onclick={() => switchPackage(growHint!.nextSku)}>
              {PS.grow.upgradeCta(growHint.nextName)} &rarr;
            </button>
          </div>
        {/if}
      </section>
    {/if}

    {#if ladder && ladder.missing.length > 0}
      <!-- #6971, v2 — block C: what this package does not have, and the rung
           that does. The link switches the package in the cart and keeps the
           chosen apps. -->
      <section class="ao-section" data-testid="addons-missing">
        <div class="ao-head">
          <h2>{PS.ladder.notOn(ladder.packageName)}</h2>
          <span class="ao-note">{PS.ladder.notOnHint}</span>
        </div>
        <div class="extras-grid">
          {#each ladder.missing as m (m.key)}
            <div class="extra-tile missing-tile" data-testid="addons-missing-{m.key}" data-state={m.state}>
              {#if m.icon}
                <span class="ico-tile missing-ico" style={m.icon.bg ? `background: ${m.icon.bg}` : ''}>
                  <img src={m.icon.src} alt="" width="18" height="18" loading="lazy" decoding="async" />
                </span>
              {:else}
                <span class="extra-icon missing-icon" aria-hidden="true">—</span>
              {/if}
              <div class="extra-body">
                <strong>{m.name}</strong>
                <p>{m.blurb}</p>
                {#if m.upgrade}
                  <button type="button" class="missing-upgrade" data-testid="addons-upgrade-{m.key}" data-target={m.upgrade.sku} onclick={() => switchPackage(m.upgrade!.sku)}>
                    {m.upgrade.state === 'included'
                      ? PS.ladder.upgradeTo(m.upgrade.name)
                      : PS.ladder.availableOn(m.upgrade.name, m.upgrade.priceMonth ?? '')} &rarr;
                  </button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </section>
    {/if}
  {/if}
</div>

<!-- The step bar: a fixed bottom bar with its own space — the page is padded
     by its height (safe-area aware) and the document's scroll-padding keeps
     anything scrolled into view above it, so it never covers the content the
     customer is reading. -->
<div class="step-bar" data-testid="step-bar" bind:this={stepBar}>
  <div class="step-bar-inner">
    <a href="/apps" class="step-back">&larr; Apps</a>
    <a href="/bcp" class="step-cta">Continue &rarr;</a>
  </div>
</div>

<style>
  .addons-page {
    --step-bar-h: 4.5rem;
    max-width: 900px;
    margin: 0 auto;
    padding: 0 1.25rem calc(var(--step-bar-h) + env(safe-area-inset-bottom, 0px));
  }
  :global(html) { scroll-padding-bottom: calc(4.5rem + env(safe-area-inset-bottom, 0px)); }

  .addons-hero { text-align: center; margin-bottom: 0.75rem; }
  .addons-hero h1 {
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.2rem;
    font-weight: 700;
  }
  .addons-hero p {
    color: var(--color-text-dim);
    font-size: 0.85rem;
    margin: 0;
  }

  /* Sections */
  .ao-section {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 12px;
    padding: 1rem 1.1rem;
    margin-bottom: 0.75rem;
  }
  .ao-head {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: baseline;
    gap: 0.25rem 0.75rem;
    margin-bottom: 0.65rem;
  }
  .ao-head h2 { font-size: 0.95rem; color: var(--color-text-strong); margin: 0; font-weight: 600; white-space: nowrap; }
  .ao-badge {
    background: color-mix(in srgb, var(--color-success) 15%, transparent);
    color: var(--color-success);
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    font-size: 0.68rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }
  .ao-note { color: var(--color-text-dim); font-size: 0.78rem; }

  /* Domain */
  .domain-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
  }
  @media (max-width: 640px) { .domain-row { grid-template-columns: 1fr; } }

  .domain-label {
    display: block;
    color: var(--color-text);
    font-size: 0.82rem;
    font-weight: 500;
    margin-bottom: 0.4rem;
  }
  .domain-optional { color: var(--color-text-dim); font-weight: 400; }
  .domain-input-row { display: flex; gap: 0.4rem; }
  .domain-input {
    flex: 1;
    min-width: 0;
    padding: 0.55rem 0.75rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.85rem;
  }
  .domain-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
  .domain-tld {
    padding: 0.55rem 0.5rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.82rem;
    appearance: auto;
  }
  .domain-preview {
    margin: 0.35rem 0 0;
    font-size: 0.78rem;
    color: var(--color-text-dim);
  }
  .slug-status {
    margin-top: 0.25rem;
    font-size: 0.76rem;
    min-height: 1.1em;
  }
  .ss-dim { color: var(--color-text-dim); }
  .ss-ok { color: var(--color-success); }
  .ss-err { color: var(--color-danger); }
  .domain-url {
    font-family: 'JetBrains Mono', monospace;
    color: var(--color-accent);
    font-weight: 500;
  }

  /* Extras tile grid — matches review page style */
  .extras-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(min(100%, 260px), 1fr));
    gap: 0.5rem;
  }
  .extra-tile {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.65rem 0.75rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    transition: border-color 0.15s;
    font: inherit;
    color: inherit;
    text-align: left;
  }
  .extra-tile.clickable { cursor: pointer; }
  .extra-tile.clickable:hover { border-color: var(--color-text-dim); }
  .extra-tile.checked {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 4%, var(--color-bg));
  }
  .extra-icon { font-size: 1.1rem; flex-shrink: 0; }
  .extra-body { flex: 1; min-width: 0; }
  .extra-body strong { display: block; color: var(--color-text-strong); font-size: 0.82rem; }
  .extra-body p { margin: 0.1rem 0 0; color: var(--color-text-dim); font-size: 0.7rem; line-height: 1.3; }
  .extra-body .extra-hint { color: var(--color-text-dimmer); font-style: italic; }
  .extra-price { color: var(--color-text-strong); font-weight: 600; font-size: 0.82rem; white-space: nowrap; flex-shrink: 0; }
  .extra-check { flex-shrink: 0; width: 20px; height: 20px; display: flex; align-items: center; justify-content: center; }
  .extra-check svg { width: 20px; height: 20px; background: var(--color-accent); color: #fff; border-radius: 4px; padding: 2px; }
  .extra-box { display: block; width: 18px; height: 18px; border: 1.5px solid var(--color-border); border-radius: 4px; }

  .bs-hint { color: var(--color-text-dim); font-size: 0.78rem; margin: 0 0 0.55rem; }

  /* An add-on card: the name, price and tick on one row; the description
     full width beneath — never squeezed beside the price. */
  .addon-card {
    flex-direction: column;
    align-items: stretch;
    gap: 0.3rem;
    padding: 0.7rem 0.8rem 0.75rem;
  }
  .addon-card-head { display: flex; align-items: center; gap: 0.55rem; min-width: 0; }
  .addon-card-name {
    flex: 1; min-width: 0;
    color: var(--color-text-strong); font-size: 0.85rem; font-weight: 600; line-height: 1.3;
  }
  .addon-card-desc { color: var(--color-text-dim); font-size: 0.74rem; line-height: 1.45; }
  .addon-card-hint { color: var(--color-text-dimmer); font-size: 0.7rem; font-style: italic; }

  /* A document icon on a rounded tile (the feature's `bg` when it sends one). */
  .ico-tile {
    display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
    width: 28px; height: 28px; border-radius: 7px;
    background: color-mix(in srgb, var(--color-text) 7%, transparent);
  }
  .ico-tile img { display: block; }
  .ico-tile.chip { width: 20px; height: 20px; border-radius: 5px; align-self: center; }
  .missing-ico { filter: grayscale(1); opacity: 0.55; }

  /* #6971, v2 — block A: compact chips */
  .in-pkg {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }
  .in-pkg-item {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.3rem 0.6rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 999px;
    font-size: 0.76rem;
    color: var(--color-text);
  }
  .in-pkg-tick { color: var(--color-success); font-weight: 700; }
  .in-pkg-floor { margin-top: 0.6rem; }
  .in-pkg-floor summary {
    cursor: pointer;
    color: var(--color-accent);
    font-size: 0.78rem;
    font-weight: 600;
    list-style-position: inside;
  }
  .in-pkg-floor[open] summary { margin-bottom: 0.45rem; }
  .in-pkg-name { font-weight: 500; }
  .in-pkg-val { color: var(--color-text-dim); }

  /* #6971, v2 — the step-up card */
  .step-up {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-top: 0.75rem;
    padding: 0.85rem 1rem;
    border: 1.5px solid var(--color-warn, #f59e0b);
    background: color-mix(in srgb, var(--color-warn, #f59e0b) 10%, var(--color-bg));
    border-radius: 10px;
  }
  .step-up-body { flex: 1; min-width: 0; }
  .step-up-title { display: block; color: var(--color-text-strong); font-size: 0.92rem; }
  .step-up-text { margin: 0.2rem 0 0; color: var(--color-text-dim); font-size: 0.76rem; }
  .step-up-list { margin: 0.2rem 0 0; color: var(--color-text); font-size: 0.74rem; font-weight: 500; }
  .step-up-cta {
    flex-shrink: 0;
    padding: 0.55rem 1rem;
    border: none;
    border-radius: 7px;
    background: var(--color-accent);
    color: #fff;
    font: inherit;
    font-size: 0.82rem;
    font-weight: 600;
    cursor: pointer;
    white-space: nowrap;
  }
  .step-up-cta:hover { filter: brightness(0.92); }
  @media (max-width: 640px) { .step-up { flex-direction: column; align-items: stretch; } }

  /* #6971, v2 — the running total */
  .running-total {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.4rem 1rem;
    margin-top: 0.75rem;
    padding: 0.6rem 0.8rem;
    border-top: 1px dashed var(--color-border);
    font-size: 0.8rem;
    color: var(--color-text-dim);
  }
  .rt-label { font-weight: 600; color: var(--color-text-strong); }
  .rt-parts { display: inline-flex; gap: 0.4rem; flex-wrap: wrap; }
  .rt-sep { color: var(--color-text-dimmer); }
  .rt-total { color: var(--color-text-strong); font-size: 1.05rem; font-weight: 800; margin-left: auto; }
  .rt-total small { color: var(--color-text-dim); font-size: 0.72rem; font-weight: 600; }

  /* #6971 — "When you reach your package": two radio cards, the grow panel */
  .grow-section { scroll-margin-top: 5rem; }
  .mode-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.6rem;
  }
  @media (max-width: 640px) { .mode-grid { grid-template-columns: 1fr; } }
  .mode-card {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 0.45rem;
    padding: 0.9rem 1rem 1rem;
    background: var(--color-bg);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    font: inherit;
    color: inherit;
    text-align: left;
    cursor: pointer;
    transition: border-color 0.15s, box-shadow 0.15s, background 0.15s;
  }
  .mode-card:hover { border-color: var(--color-text-dim); }
  .mode-card:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }
  .mode-card.on {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 6%, var(--color-bg));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 16%, transparent);
  }
  .mode-card.grow.on {
    border-color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 6%, var(--color-bg));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success) 16%, transparent);
  }
  .mode-top { display: flex; align-items: center; gap: 0.55rem; flex-wrap: wrap; }
  .mode-ico {
    display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
    width: 32px; height: 32px; border-radius: 9px;
    color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  }
  .mode-card.grow .mode-ico { color: var(--color-success); background: color-mix(in srgb, var(--color-success) 13%, transparent); }
  .mode-ico svg { width: 18px; height: 18px; }
  .mode-title { color: var(--color-text-strong); font-size: 0.98rem; font-weight: 700; }
  .mode-tag {
    padding: 0.1rem 0.45rem; border-radius: 999px;
    font-size: 0.66rem; font-weight: 600;
    color: var(--color-text-dim);
    background: color-mix(in srgb, var(--color-text) 8%, transparent);
  }
  .mode-dot {
    margin-left: auto; flex-shrink: 0;
    width: 18px; height: 18px; border-radius: 999px;
    border: 1.5px solid var(--color-border-strong, var(--color-border));
  }
  .mode-card.on .mode-dot { border: 5px solid var(--color-accent); }
  .mode-card.grow.on .mode-dot { border-color: var(--color-success); }
  .mode-body { color: var(--color-text); font-size: 0.8rem; line-height: 1.45; }
  .mode-rates { display: flex; flex-wrap: wrap; gap: 0.25rem 0.8rem; color: var(--color-text-dim); font-size: 0.74rem; font-weight: 600; }

  .grow-panel {
    margin-top: 0.75rem;
    padding: 0.9rem 1rem;
    border: 1px solid color-mix(in srgb, var(--color-success) 30%, var(--color-border));
    border-radius: 12px;
    background: color-mix(in srgb, var(--color-success) 4%, var(--color-bg));
    display: grid;
    gap: 1rem;
  }
  .grow-block { display: flex; flex-direction: column; gap: 0.4rem; min-width: 0; }
  .grow-label { color: var(--color-text-strong); font-size: 0.84rem; font-weight: 600; }
  .grow-optional { color: var(--color-text-dim); font-weight: 400; }
  .grow-sub { margin: 0; color: var(--color-text-dim); font-size: 0.74rem; line-height: 1.4; }
  .grow-sub.err { color: var(--color-danger); }
  .grow-rates {
    list-style: none; margin: 0; padding: 0;
    display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 0.4rem;
  }
  @media (max-width: 760px) { .grow-rates { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  .grow-rate {
    display: flex; flex-direction: column; gap: 0.1rem;
    padding: 0.5rem 0.65rem;
    border: 1px solid var(--color-border); border-radius: 8px;
    background: var(--color-surface);
  }
  .grow-rate-name { color: var(--color-text-dim); font-size: 0.7rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; }
  .grow-rate-val { display: flex; flex-direction: column; gap: 0.05rem; }
  .grow-rate-val strong { color: var(--color-text-strong); font-size: 0.95rem; font-weight: 700; font-variant-numeric: tabular-nums; }
  .grow-rate-val small { color: var(--color-text-dim); font-size: 0.7rem; line-height: 1.3; }
  .grow-steppers { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.5rem; }
  @media (max-width: 640px) { .grow-steppers { grid-template-columns: 1fr; } }
  .stepper-row {
    display: grid;
    grid-template-columns: 1fr auto;
    align-items: center;
    gap: 0.3rem 0.6rem;
    padding: 0.55rem 0.7rem 0.6rem;
    border: 1px solid var(--color-border); border-radius: 8px;
    background: var(--color-surface);
  }
  .stepper-name { display: flex; flex-direction: column; min-width: 0; }
  .stepper-name strong { color: var(--color-text-strong); font-size: 0.82rem; }
  .stepper-name small { color: var(--color-text-dim); font-size: 0.7rem; }
  .stepper { display: inline-flex; align-items: center; border: 1px solid var(--color-border); border-radius: 8px; overflow: hidden; }
  .step-btn {
    width: 32px; height: 32px;
    border: 0; background: var(--color-bg); color: var(--color-text-strong);
    font: inherit; font-size: 1.05rem; font-weight: 600; cursor: pointer;
  }
  .step-btn:hover:not(:disabled) { background: color-mix(in srgb, var(--color-accent) 10%, var(--color-bg)); }
  .step-btn:disabled { color: var(--color-text-dimmer); cursor: not-allowed; opacity: 0.55; }
  .step-btn:focus-visible { outline: 2px solid var(--color-accent); outline-offset: -2px; }
  .step-val {
    min-width: 5.2rem; text-align: center;
    color: var(--color-text-strong); font-size: 0.85rem; font-weight: 700; font-variant-numeric: tabular-nums;
    border-left: 1px solid var(--color-border); border-right: 1px solid var(--color-border);
    padding: 0 0.4rem; line-height: 32px;
  }
  .step-val small { color: var(--color-text-dim); font-weight: 500; font-size: 0.7rem; }
  .stepper-bar { grid-column: 1 / -1; height: 4px; border-radius: 999px; background: color-mix(in srgb, var(--color-text) 9%, transparent); overflow: hidden; }
  .stepper-bar span { display: block; height: 100%; background: var(--color-success); border-radius: 999px; transition: width 0.15s; }
  .spend-row {
    display: inline-flex; align-items: center; max-width: 260px;
    border: 1px solid var(--color-border); border-radius: 8px; background: var(--color-surface);
  }
  .spend-row:focus-within { outline: 2px solid var(--color-accent); border-color: transparent; }
  .spend-cur, .spend-per { color: var(--color-text-dim); font-size: 0.78rem; padding: 0 0.6rem; white-space: nowrap; }
  .spend-input {
    flex: 1; min-width: 0; width: 7rem;
    padding: 0.5rem 0.2rem;
    border: 0; outline: none; background: transparent;
    color: var(--color-text-strong); font: inherit; font-size: 0.88rem; font-weight: 600; font-variant-numeric: tabular-nums;
  }
  .spend-input.invalid { color: var(--color-danger); }
  .grow-dr {
    margin: 0; padding: 0.5rem 0.7rem; border-radius: 8px;
    color: var(--color-text); font-size: 0.76rem;
    background: color-mix(in srgb, var(--color-accent) 8%, transparent);
  }
  .grow-upgrade {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
    margin-top: 0.75rem; padding: 0.8rem 1rem;
    border: 1.5px dashed color-mix(in srgb, var(--color-warn, #f59e0b) 70%, var(--color-border));
    background: color-mix(in srgb, var(--color-warn, #f59e0b) 8%, var(--color-bg));
    border-radius: 10px;
  }
  .grow-upgrade-body { flex: 1; min-width: 0; }
  .grow-upgrade-body strong { display: block; color: var(--color-text-strong); font-size: 0.88rem; }
  .grow-upgrade-body p { margin: 0.2rem 0 0; color: var(--color-text-dim); font-size: 0.76rem; line-height: 1.45; }
  .grow-upgrade-cta {
    flex-shrink: 0; padding: 0.5rem 0.95rem;
    border: 1.5px solid var(--color-accent); border-radius: 7px;
    background: transparent; color: var(--color-accent);
    font: inherit; font-size: 0.8rem; font-weight: 700; cursor: pointer; white-space: nowrap;
  }
  .grow-upgrade-cta:hover { background: color-mix(in srgb, var(--color-accent) 10%, transparent); }
  @media (max-width: 640px) { .grow-upgrade { flex-direction: column; align-items: stretch; } }

  /* #6971, v2 — block C */
  .missing-tile { cursor: default; opacity: 0.92; }
  .missing-icon { color: var(--color-text-dimmer); }
  .missing-upgrade {
    margin-top: 0.3rem;
    padding: 0;
    border: none;
    background: none;
    color: var(--color-accent);
    font: inherit;
    font-size: 0.74rem;
    font-weight: 600;
    cursor: pointer;
    text-align: left;
  }
  .missing-upgrade:hover { text-decoration: underline; }

  .svc-tile { cursor: default; }
  .svc-tile:hover { border-color: var(--color-border); }
  .svc-logo { width: 22px; height: 22px; border-radius: 4px; flex-shrink: 0; }
  .svc-price {
    color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 12%, transparent);
    padding: 0.15rem 0.45rem;
    border-radius: 4px;
    font-size: 0.68rem;
    font-weight: 700;
    letter-spacing: 0.04em;
  }

  /* The step bar */
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
</style>
