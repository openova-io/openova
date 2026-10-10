<script lang="ts">
  import { getAddons, getApps, getPlans, checkSlug, type AddOn, type App, type Plan } from '../lib/api';
  import { readCart, toggleAddon, setOrgDetails, setPackage, setTLD, writeCart, DEFAULT_TLD } from '../lib/cart';
  import { formatOMR } from '../lib/currency';
  import { chargebackBaseURL } from '../lib/config';
  import {
    addonsLadderFor,
    catalogPlanIdForPackage,
    funnelAddonsFor,
    isLadderDocument,
    loadPublicPackages,
    packageForCart,
    pruneAddonsForPackage,
    stepUpHint,
    PACKAGE_STRINGS as PS,
    type AddonsLadder,
    type IncludedFeature,
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

  // Today's stand-in when the catalog is down — unchanged.
  const FALLBACK_ADDONS: AddOn[] = [
    { id: 'daily-backup', name: 'Daily Backup', slug: 'daily-backup', tagline: 'Automated daily backups with 30-day retention', icon: '🛡️', monthly_price: 3000, included: false },
    { id: 'waf', name: 'Web Application Firewall', slug: 'waf', tagline: 'Coraza WAF — OWASP CRS protection', icon: '🔥', monthly_price: 4000, included: false },
    { id: 'ips', name: 'Intrusion Prevention', slug: 'ips', tagline: 'Community-powered threat intelligence — CrowdSec', icon: '🚨', monthly_price: 3000, included: false },
    { id: 'vuln-scan', name: 'Vulnerability Scanner', slug: 'vuln-scan', tagline: 'Weekly CVE scans + remediation reports', icon: '🔍', monthly_price: 2000, included: false },
    { id: 'custom-domain', name: 'Custom Domain', slug: 'custom-domain', tagline: 'Your brand, your domain — with automatic TLS', icon: '🌐', monthly_price: 2000, included: false },
    { id: 'log-management', name: 'Log Management', slug: 'log-management', tagline: 'Search and analyze all your app logs — Grafana Loki', icon: '📋', monthly_price: 3000, included: false },
    { id: 'priority-support', name: 'Priority Support', slug: 'priority-support', tagline: '4-hour response SLA + dedicated channel', icon: '⚡', monthly_price: 5000, included: false },
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
        applyPackage(d, pkg.sku, catalog);
      } else {
        addons = catalog;
      }
      loading = false;
    });
  });

  // The step's lists for a package of the document in hand. A v2 document
  // yields the ladder blocks; a v1 one the included group + the merged list.
  function applyPackage(d: PublicPackages, sku: string, catalog: AddOn[]) {
    const l = isLadderDocument(d) ? addonsLadderFor(d, sku, catalog) : null;
    if (l) {
      ladder = l;
      addons = l.addons;
      bssIncluded = l.included.map(i => ({ key: i.key, name: i.name, blurb: i.value ?? i.blurb }));
      packageName = l.packageName;
      return;
    }
    ladder = null;
    const merged = funnelAddonsFor(d, sku, catalog);
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
      addons: pruneAddonsForPackage(doc, pkg.sku, cart.addons),
    });
    applyPackage(doc, pkg.sku, catalogAddons);
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

  // Addon icons by slug
  const addonIcons: Record<string, string> = {
    'waf': '🔥', 'ips': '🚨', 'vuln-scan': '🔍',
    'custom-domain': '🌐', 'log-management': '📋', 'priority-support': '⚡',
    'daily-backup': '🛡️',
    // #6971 — BSS feature keys (the `slug` of a document-sourced add-on):
    // the v1 document's hyphenated keys and the v2 document's underscored ones.
    'backup': '🛡️', 'domain': '🌐', 'dedicated-ip': '🌍', 'ai-seo': '🔎',
    'ai-website-builder': '🪄', 'ssl': '🔒', 'sso': '🔑', 'ddos': '🛡️',
    'malware-scanner': '🔍', 'support': '💬', 'mail': '✉️', 'databases': '🗄️',
    'applications': '📦',
    'dedicated_ip': '🌍', 'ai_seo': '🔎', 'ai_builder': '🪄', 'bandwidth': '📶',
  };
</script>

<div class="addons-page">
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
              <span class="in-pkg-tick" aria-hidden="true">✓</span>
              <span class="in-pkg-name">{f.name}</span>
              {#if f.value}<span class="in-pkg-val">{f.value}</span>{/if}
            </li>
          {/each}
        </ul>
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
              <span class="extra-icon">{addonIcons[f.key] || '✓'}</span>
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
            class="extra-tile clickable {isChecked ? 'checked' : ''}"
            data-testid="addon-tile-{addon.id}"
          >
            <span class="extra-icon">{addonIcons[addon.slug] || addon.icon || '📦'}</span>
            <div class="extra-body">
              <strong>{addon.name}</strong>
              <p>{addon.tagline}</p>
              {#if addon.hint}<p class="extra-hint">{addon.hint}</p>{/if}
            </div>
            <span class="extra-price">+{formatOMR(addon.monthly_price)}</span>
            <span class="extra-check">
              {#if isChecked}
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 13l4 4L19 7"/></svg>
              {:else}
                <span class="extra-box"></span>
              {/if}
            </span>
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
              <span class="extra-icon missing-icon" aria-hidden="true">—</span>
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

<div class="float-nav">
  <a href="/apps" class="float-back">&larr; Apps</a>
  <a href="/bcp" class="float-cta">Continue &rarr;</a>
</div>

<style>
  .addons-page { max-width: 900px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }

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
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.65rem;
  }
  .ao-head h2 { font-size: 0.95rem; color: var(--color-text-strong); margin: 0; font-weight: 600; }
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
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
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
    align-items: baseline;
    gap: 0.35rem;
    padding: 0.3rem 0.6rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 999px;
    font-size: 0.76rem;
    color: var(--color-text);
  }
  .in-pkg-tick { color: var(--color-success); font-weight: 700; }
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

  /* Floating nav pill */
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
</style>
