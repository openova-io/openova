<script lang="ts">
  import { getPlans, getApps, getAddons, getQuote, type Plan, type App, type AddOn, type QuoteResponse } from '../lib/api';
  import { readCart, setPlan, setPackage, setOverage } from '../lib/cart';
  import { formatOMR, formatOMRAmount } from '../lib/currency';
  import { chargebackBaseURL } from '../lib/config';
  import { documentQuote, overageSummary, quoteRequestFor, quoteable, lineAmountLabel, QUOTE_STRINGS } from '../lib/quote';
  import {
    catalogPlanIdForPackage,
    drTopologyFor,
    funnelAddonsFor,
    growModelFor,
    growSelectionFor,
    loadPublicPackages,
    packageCapacity,
    packageForCart,
    packageSpecsLine,
    pruneAddonsForPackage,
    readableOn,
    skuTail,
    PACKAGE_STRINGS,
    type PackageCapacity,
    type PackageIcon,
    type PublicPackage,
    type PublicPackages,
  } from '../lib/packages';

  // One row of the plan picker. With the BSS document every option IS a
  // package of the document — in its order, with its price, shape, icon and
  // accent — and a catalog plan the document does not publish is not shown.
  // Without the document the options are the catalog plans, as before.
  type PlanOption = {
    id: string;
    slug: string;
    name: string;
    popular: boolean;
    pkg: PublicPackage | null;
    plan: Plan | null;
  };

  let cart = $state(readCart());
  let plans = $state<Plan[]>([]);
  let apps = $state<App[]>([]);
  let addons = $state<AddOn[]>([]);
  // #6971 — the raw catalog list and the BSS package document, kept so a plan
  // change here can re-derive the add-on list for the new package. Null
  // document → `addons` is the catalog list, exactly as before.
  let catalogAddons = $state<AddOn[]>([]);
  let doc = $state<PublicPackages | null>(null);
  let loading = $state(true);
  let concurrency = $state<'small' | 'medium' | 'large'>('small');

  const planOptions = $derived.by((): PlanOption[] => {
    if (doc) {
      return doc.packages.map(pkg => ({
        id: catalogPlanIdForPackage(pkg, plans),
        slug: skuTail(pkg.sku),
        name: pkg.name,
        popular: pkg.recommended === true,
        pkg,
        plan: plans.find(p => p.id === catalogPlanIdForPackage(pkg, plans)) ?? null,
      }));
    }
    return plans.map(p => ({ id: p.id, slug: p.slug, name: p.name, popular: Boolean(p.popular), pkg: null, plan: p }));
  });
  const selectedPlan = $derived(
    (doc && cart.packageSku ? planOptions.find(o => o.pkg?.sku === cart.packageSku) : undefined)
      ?? planOptions.find(o => o.id === cart.plan),
  );
  const selectedPackage = $derived(selectedPlan?.pkg ?? null);
  const selectedApps = $derived(apps.filter(a => cart.apps.includes(a.id)));
  const selectedAddons = $derived(addons.filter(a => cart.addons.includes(a.id)));

  // --- Pillar-2 BCP topology (#4524) ---------------------------------------
  // The /bcp step persists the customer's database business-continuity choice
  // to cart.appConfigs.postgres.{active_hot_standby, primary_region,
  // replica_region} (BCPStep.svelte — the same snake_case keys the
  // provisioning gitops generator consumes). This review surface MUST reflect
  // both the chosen topology AND its price so the customer confirms checkout
  // seeing what they picked. The checkout payload is unchanged — it already
  // carries app_configs end-to-end. The surcharge amount is the server's
  // (quote.topology_amount_baisa), not a constant mirrored here (#6971).

  // Human labels for the canonical Sovereign region keys, mirroring
  // BCPStep.svelte's REGIONS list. Falls back to the raw key so a region the
  // customer's Sovereign exposes that isn't in this static map still renders
  // (it just shows the key) rather than disappearing from the summary.
  const REGION_LABELS: Record<string, string> = {
    'hz-fsn-rtz-prod': 'Falkenstein (hz-fsn)',
    'hz-hel-rtz-prod': 'Helsinki (hz-hel)',
    'hz-nbg-rtz-prod': 'Nuremberg (hz-nbg)',
  };
  function regionLabel(key: unknown): string {
    if (typeof key !== 'string' || key === '') return '—';
    return REGION_LABELS[key] ?? key;
  }

  const pgConfig = $derived((cart.appConfigs ?? {}).postgres ?? {});
  const hotStandby = $derived(Boolean(pgConfig.active_hot_standby));
  const primaryRegion = $derived(regionLabel(pgConfig.primary_region));
  const replicaRegion = $derived(regionLabel(pgConfig.replica_region));

  // #6971 — every figure in the cost sidebar comes from POST /billing/quote,
  // the pricing seam /billing/checkout bills through: the plan (from the BSS
  // package or the catalog), one line per add-on in the cart's list (a BSS
  // SKU from the package's cell, a catalog id from /catalog/addons,
  // "Included" when the package already has it), the topology surcharge and
  // the total. The package table, this page, the checkout and the receipt
  // therefore show ONE number; nothing here sums money. Re-quoted whenever a
  // priced input changes (changePlan / toggleAddon rewrite `cart`); when the
  // quote is unavailable the total says so.
  let quote = $state<QuoteResponse | null>(null);
  let quoteError = $state<string | null>(null);
  $effect(() => {
    // With the document in hand the grow fields are made consistent with the
    // package first (a package that cannot grow is quoted capped).
    const req = quoteRequestFor(cart, doc);
    if (!quoteable(req)) { quote = null; quoteError = null; return; }
    let stale = false;
    getQuote(req)
      .then(q => { if (!stale) { quote = q; quoteError = null; } })
      .catch(e => { if (!stale) { quote = null; quoteError = e instanceof Error ? e.message : String(e); } });
    return () => { stale = true; };
  });
  // When the quote cannot answer (billing 503s "prices unavailable" while
  // /plans priced the same cart from the same document a step ago), the
  // sidebar prices it from that document by the quote's own rules
  // (quote.ts::documentQuote) and says so; checkout still bills through the
  // quote. Unavailable only when neither can price it.
  const totalCost = $derived(quote?.amount_baisa ?? 0);
  const docQuote = $derived(documentQuote(doc, cart));
  const shownQuote = $derived(quote ?? (quoteError ? docQuote : null));
  const fromDocument = $derived(!quote && Boolean(shownQuote));

  // #6971 — "When you reach your package", read back: the quote's echo when
  // it answered (overage_mode / grow_ceiling / spend_limit_month), else the
  // cart's choice. Shown when the package can grow or the cart chose grow.
  const canGrow = $derived(Boolean(doc && cart.packageSku && growModelFor(doc, cart.packageSku)));
  const overage = $derived(overageSummary(shownQuote, cart, doc, formatOMR));
  // Active-passive on a grow_only package: the standby is usage, not a line.
  const standbyAsUsage = $derived(
    overage.mode === 'grow' && Boolean(doc && cart.packageSku && drTopologyFor(doc, cart.packageSku)?.growOnly),
  );

  // --- Per-app resource estimates (MiB RAM, milli-CPU, GiB disk) ---
  const appRam: Record<string, number> = {
    wordpress: 600, ghost: 300, 'stalwart-mail': 400, 'rocket-chat': 750,
    nextcloud: 800, twenty: 500, umami: 250, medusa: 500,
    plane: 500, erpnext: 900, invoiceshelf: 400, listmonk: 250,
    'cal-com': 450, gitea: 350, 'uptime-kuma': 200, librechat: 500,
    documenso: 300, vaultwarden: 200, bookstack: 350, formbricks: 300,
    dify: 900, openclaw: 300, chatwoot: 550, postiz: 300,
    nocodb: 450, 'jitsi-meet': 600, immich: 700,
  };
  const appCpu: Record<string, number> = {
    wordpress: 400, ghost: 200, 'stalwart-mail': 250, 'rocket-chat': 500,
    nextcloud: 500, twenty: 300, umami: 150, medusa: 350,
    plane: 350, erpnext: 700, invoiceshelf: 250, listmonk: 200,
    'cal-com': 300, gitea: 250, 'uptime-kuma': 120, librechat: 400,
    documenso: 200, vaultwarden: 120, bookstack: 250, formbricks: 200,
    dify: 700, openclaw: 200, chatwoot: 400, postiz: 200,
    nocodb: 350, 'jitsi-meet': 500, immich: 500,
  };
  const appDisk: Record<string, number> = {
    wordpress: 3, ghost: 2, 'stalwart-mail': 3, 'rocket-chat': 3,
    nextcloud: 5, twenty: 2, umami: 2, medusa: 3,
    plane: 3, erpnext: 4, invoiceshelf: 2, listmonk: 2,
    'cal-com': 2, gitea: 3, 'uptime-kuma': 1, librechat: 3,
    documenso: 2, vaultwarden: 1, bookstack: 2, formbricks: 2,
    dify: 5, openclaw: 2, chatwoot: 3, postiz: 2,
    nocodb: 2, 'jitsi-meet': 2, immich: 10,
  };
  const overheadRam = 500, overheadCpu = 250, overheadDisk = 3;

  // Three usage buckets, labelled only — no user counts, which were not
  // measured anywhere.
  const concOptions = [
    { id: 'small' as const, label: 'Low', multiplier: 1.0 },
    { id: 'medium' as const, label: 'Medium', multiplier: 1.5 },
    { id: 'large' as const, label: 'High', multiplier: 2.2 },
  ];

  // Plan slug → capacity in numeric units: the catalog's shape, used ONLY when
  // there is no package document (today's fallback).
  const planCapMap: Record<string, PackageCapacity> = {
    s: { ram: 4096, cpu: 2000, disk: 25 },
    m: { ram: 8192, cpu: 4000, disk: 50 },
    l: { ram: 16384, cpu: 8000, disk: 100 },
    xl: { ram: 32768, cpu: 16000, disk: 200 },
    flexi: { ram: 65536, cpu: 32000, disk: 500 },
  };

  // #6971 — ONE shape source for this page: the package document's `shape`
  // (packageCapacity) feeds the plan cards' price + specs, the headroom ring
  // and the "fits your N apps" hint alike; the catalog shape is the fallback
  // without a document.
  function capFor(opt: PlanOption): PackageCapacity {
    const fromDoc = opt.pkg ? packageCapacity(opt.pkg) : null;
    return fromDoc ?? planCapMap[opt.slug] ?? { ram: 0, cpu: 0, disk: 0 };
  }
  function priceLineFor(opt: PlanOption): string {
    if (opt.pkg) return opt.pkg.price_month;
    return opt.plan ? formatOMRAmount(opt.plan.monthly_price) : '';
  }
  function specsLineFor(opt: PlanOption): string {
    if (opt.pkg) {
      const line = packageSpecsLine(opt.pkg);
      if (line) return line;
    }
    const r = opt.plan?.resources;
    return r ? `${r.cpu} · ${r.memory} · ${r.storage}` : '';
  }
  /** The accent custom properties for a package option; empty without one. */
  function accentStyle(pkg: PublicPackage | null): string {
    return pkg?.accent ? `--pk-accent: ${pkg.accent}; --pk-accent-fg: ${readableOn(pkg.accent)};` : '';
  }

  const multiplier = $derived(concOptions.find(o => o.id === concurrency)?.multiplier ?? 1.0);

  const grossRam = $derived(Math.round(
    selectedApps.reduce((s, a) => s + (appRam[a.slug] ?? 300), 0) * multiplier
  ) + overheadRam);
  const grossCpu = $derived(Math.round(
    selectedApps.reduce((s, a) => s + (appCpu[a.slug] ?? 200), 0) * multiplier
  ) + overheadCpu);
  const grossDisk = $derived(
    selectedApps.reduce((s, a) => s + (appDisk[a.slug] ?? 2), 0) + overheadDisk
  );

  const planCap = $derived(selectedPlan ? capFor(selectedPlan) : { ram: 0, cpu: 0, disk: 0 });
  const ramPct = $derived(planCap.ram > 0 ? Math.round((grossRam / planCap.ram) * 100) : 0);
  const cpuPct = $derived(planCap.cpu > 0 ? Math.round((grossCpu / planCap.cpu) * 100) : 0);
  const diskPct = $derived(planCap.disk > 0 ? Math.round((grossDisk / planCap.disk) * 100) : 0);
  const maxPct = $derived(Math.max(ramPct, cpuPct, diskPct));

  // Find the smallest plan that fits — the plans in the document's order
  // (by shape) when there is one, else the catalog's fixed ladder.
  const suggestedPlan = $derived.by(() => {
    const ordered: PlanOption[] = doc
      ? [...planOptions].filter(p => capFor(p).ram > 0).sort((a, b) => capFor(a).ram - capFor(b).ram)
      : ['s', 'm', 'l', 'xl', 'flexi'].map(slug => planOptions.find(p => p.slug === slug)).filter((p): p is PlanOption => Boolean(p));
    for (const plan of ordered) {
      const cap = capFor(plan);
      if (grossRam <= cap.ram && grossCpu <= cap.cpu && grossDisk <= cap.disk) return plan;
    }
    return null; // nothing fits — contact sales
  });

  // Recommended plan: factor in both app count AND resource usage
  const recommendedPlan = $derived.by(() => {
    if (suggestedPlan) return suggestedPlan.name;
    const appCount = selectedApps.length;
    if (appCount <= 5) return 'S';
    if (appCount <= 12) return 'M';
    if (appCount <= 20) return 'L';
    return 'XL';
  });

  // #6971 — the add-on list the cart's ids resolve against: the catalog list,
  // or, with the BSS document and a package to stand on, that package's
  // optional features plus the catalog add-ons with no BSS twin. Same shape,
  // same arithmetic below.
  function addonsForCart(catalog: AddOn[], d: PublicPackages | null): AddOn[] {
    const pkg = d ? packageForCart(d, cart) : null;
    return d && pkg ? funnelAddonsFor(d, pkg.sku).addons : catalog;
  }

  $effect(() => {
    Promise.all([getPlans(), getApps(), getAddons(), loadPublicPackages(chargebackBaseURL())])
      .then(([p, a, ad, d]) => {
        plans = p;
        apps = a.filter(x => !x.system);
        catalogAddons = ad;
        doc = d;
        const pkg = d ? packageForCart(d, cart) : null;
        if (d && pkg) {
          // With a document the cart's add-ons are BSS SKUs this package
          // sells; a catalog id from before the document existed is carried
          // over to its BSS twin or dropped (it is not in the price book).
          const pruned = pruneAddonsForPackage(d, pkg.sku, cart.addons, ad);
          if (pruned.join('\u0000') !== cart.addons.join('\u0000')) {
            cart = setPackage({
              planId: cart.plan || catalogPlanIdForPackage(pkg, p),
              planName: pkg.name,
              packageSku: pkg.sku,
              addons: pruned,
            });
          }
        }
        addons = addonsForCart(ad, d);
        // A grow choice the package cannot honour (XL, or a package switched
        // to since) is capped here, so the checkout POST — built from the
        // cart — carries what this page quoted.
        if (cart.overageMode === 'grow' && growSelectionFor(d, cart.packageSku, cart).mode === 'capped' && d) {
          cart = setOverage({ mode: 'capped' });
        }
        loading = false;
      })
      .catch(() => { loading = false; });
  });

  // A plan change here is a package change when the document is in hand: stamp
  // the matching package, drop a BSS add-on the new package no longer offers
  // as optional, and re-derive the add-on list. Without the document it is
  // today's setPlan.
  function changePlan(plan: PlanOption) {
    const pkg = plan.pkg;
    if (doc && pkg) {
      cart = setPackage({
        planId: plan.id,
        planName: pkg.name,
        packageSku: pkg.sku,
        addons: pruneAddonsForPackage(doc, pkg.sku, cart.addons, catalogAddons),
      });
      addons = funnelAddonsFor(doc, pkg.sku).addons;
    } else {
      cart = setPlan(plan.id, plan.name);
    }
  }

  // #85 — shared helper. `formatOMRAmount` is used where the "OMR" label is
  // already rendered as a separate span (plan-opt-price hero); `formatOMR`
  // prefixes "OMR " and is used everywhere else so every baisa figure in the
  // review sidebar matches the checkout and the console.

  function upgradePlan() {
    if (suggestedPlan) changePlan(suggestedPlan);
  }

  // The chosen add-ons, for the summary: from the add-on list the cart's ids
  // resolve against (the package's BSS add-ons with the document's icons, or
  // the catalog list without a document). Nothing is picked here — the
  // summary links back to the Add-ons step.
  const chosenAddons = $derived(addons.filter(a => !a.included && cart.addons.includes(a.id)));
  function addonImage(a: AddOn): PackageIcon | null {
    return a.image ?? null;
  }

  // The step bar's measured height pads the page (and the document's
  // scroll-padding), the same as the Add-ons step.
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

<div class="review" style={stepBarH > 0 ? `--step-bar-h: ${stepBarH + 16}px` : ''}>
  <h1 class="review-title">Review & launch</h1>

  {#if loading}
    <div class="flex justify-center py-20">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else}
    <div class="review-layout">
      <!-- LEFT: Stack + Plan + Workspace + Optional extras -->
      <div class="review-main">
        <!-- Stack summary — compact card grid -->
        <section class="rv-section">
          <div class="rv-head">
            <h2>Your stack</h2>
            <a href="/apps" class="rv-link">Edit</a>
          </div>
          {#if selectedApps.length > 0}
            <div class="stack-grid">
              {#each selectedApps as app}
                <a href="/app?slug={app.slug}" class="stack-card">
                  {#if app.logo}
                    <img src={app.logo} alt={app.name} class="stack-logo" loading="lazy" />
                  {:else}
                    <span class="stack-icon" style="background: {app.color}">{app.icon}</span>
                  {/if}
                  <div class="stack-body">
                    <span class="stack-name">{app.name}</span>
                    <span class="stack-cat">{app.category}</span>
                    <p class="stack-desc">{app.description || app.tagline}</p>
                  </div>
                </a>
              {/each}
            </div>
          {:else}
            <div class="stack-empty">
              <p>Your stack is empty.</p>
              <a href="/apps">Browse apps &rarr;</a>
            </div>
          {/if}
        </section>

        <!-- Plan selection (radio buttons like sme2) -->
        <section class="rv-section">
          <div class="rv-head">
            <h2>Plan</h2>
            {#if selectedApps.length > 0 && selectedPlan}
              <span class="rv-note {maxPct > 100 ? 'rv-warn' : ''}">
                {#if maxPct > 100}
                  Upgrade recommended
                {:else}
                  {recommendedPlan} fits your {selectedApps.length} app{selectedApps.length === 1 ? '' : 's'}
                {/if}
              </span>
            {:else if selectedApps.length > 0}
              <span class="rv-note">Recommended: {recommendedPlan}</span>
            {/if}
          </div>
          <div class="plan-row" style="--plan-cols: {Math.max(planOptions.length, 1)}" data-testid="review-plan-row">
            {#each planOptions as plan (plan.pkg?.sku ?? plan.id)}
              {@const isChecked = selectedPlan === plan}
              <label
                class="plan-option {plan.popular ? 'popular' : ''} {isChecked ? 'checked' : ''} {suggestedPlan === plan && maxPct > 100 ? 'suggested' : ''} {plan.pkg?.accent ? 'has-accent' : ''}"
                style={accentStyle(plan.pkg)}
                data-testid="review-plan-{plan.pkg?.sku ?? plan.id}"
              >
                <input
                  type="radio"
                  name="plan"
                  value={plan.id}
                  checked={isChecked}
                  onchange={() => changePlan(plan)}
                />
                <span class="plan-opt-body">
                  <span class="plan-opt-head">
                    {#if plan.pkg?.icon}
                      <span class="plan-opt-icon" style={plan.pkg.icon.bg ? `background: ${plan.pkg.icon.bg}` : ''}>
                        <img src={plan.pkg.icon.src} alt="" width="16" height="16" loading="lazy" decoding="async" />
                      </span>
                    {/if}
                    <span class="plan-opt-name">{plan.name}</span>
                  </span>
                  <span class="plan-opt-price">
                    {#if !plan.pkg && (plan.slug === 'flexi' || plan.name === 'Flexi')}
                      <strong>2</strong> OMR/CU/mo
                    {:else}
                      <strong>{priceLineFor(plan)}</strong> OMR/mo
                    {/if}
                  </span>
                  <span class="plan-opt-specs" data-testid="review-plan-specs-{plan.id}">{specsLineFor(plan)}</span>
                </span>
              </label>
            {/each}
          </div>
        </section>

        <!-- Expected usage + Workspace — side by side -->
        <div class="rv-two-col">
          <!-- Expected usage / capacity estimation -->
          <section class="rv-section">
            <div class="rv-head">
              <h2>Expected usage</h2>
              <span class="rv-note">Helps size your plan</span>
            </div>
            <div class="conc-row">
              {#each concOptions as opt}
                <button
                  type="button"
                  class="conc-btn {concurrency === opt.id ? 'active' : ''}"
                  onclick={() => concurrency = opt.id}
                >
                  <strong>{opt.label}</strong>
                </button>
              {/each}
            </div>

            {#if selectedPlan && selectedApps.length > 0}
              {@const gaugeColor = maxPct > 100 ? '#EF4444' : maxPct >= 80 ? '#F59E0B' : '#22C55E'}
              {@const dashArray = `${Math.min(maxPct, 100) * 2.51327} ${251.327 - Math.min(maxPct, 100) * 2.51327}`}
              <div class="capacity-compact">
                <div class="cap-gauge-wrap">
                  <svg viewBox="0 0 100 100" class="cap-gauge">
                    <circle cx="50" cy="50" r="40" fill="none" stroke="var(--color-border)" stroke-width="8" />
                    <circle cx="50" cy="50" r="40" fill="none" stroke={gaugeColor} stroke-width="8"
                      stroke-dasharray={dashArray}
                      stroke-linecap="round"
                      transform="rotate(-90 50 50)" />
                  </svg>
                  <div class="cap-gauge-label">
                    <strong style="color: {gaugeColor}">{maxPct}%</strong>
                    <span>used</span>
                  </div>
                </div>
                <div class="cap-details">
                  <div class="cap-row" data-testid="review-cap-ram">
                    <span class="cap-metric">RAM</span>
                    <span class="cap-val">{grossRam} / {planCap.ram} MiB</span>
                  </div>
                  <div class="cap-row" data-testid="review-cap-cpu">
                    <span class="cap-metric">CPU</span>
                    <span class="cap-val">{grossCpu} / {planCap.cpu} m</span>
                  </div>
                  <div class="cap-row" data-testid="review-cap-disk">
                    <span class="cap-metric">Disk</span>
                    <span class="cap-val">{grossDisk} / {planCap.disk} GiB</span>
                  </div>
                  {#if maxPct > 100}
                    <div class="cap-msg cap-over">
                      Exceeds {selectedPlan.name} —
                      {#if suggestedPlan}
                        <button type="button" class="cs-upgrade" onclick={upgradePlan}>Upgrade to {suggestedPlan.name}</button>
                      {:else}
                        contact us
                      {/if}
                    </div>
                  {:else if maxPct >= 80}
                    <div class="cap-msg cap-warn">Tight fit — consider upgrading</div>
                  {:else}
                    <div class="cap-msg cap-ok">Plenty of headroom</div>
                  {/if}
                </div>
              </div>
            {:else if !selectedPlan}
              <p class="cs-hint">Select a plan above to see capacity estimation.</p>
            {/if}
          </section>

          <!-- Workspace info -->
          {#if cart.subdomain}
            <section class="rv-section">
              <div class="rv-head">
                <h2>Organization</h2>
                <a href="/addons" class="rv-link">Edit</a>
              </div>
              <div class="ws-preview">
                <div class="ws-row"><span>URL</span><strong class="font-mono">{cart.subdomain}.{cart.tld}</strong></div>
              </div>
            </section>
          {/if}
        </div>

        <!-- Business continuity (Pillar-2 BCP topology) — #4524. Reflects the
             choice made on /bcp so the customer confirms checkout seeing the
             topology they picked + its price. -->
        <section class="rv-section">
          <div class="rv-head">
            <h2>Business continuity</h2>
            <a href="/bcp" class="rv-link">Edit</a>
          </div>
          <div class="bcp-summary {hotStandby ? 'bcp-hot' : ''}">
            <div class="bcp-summary-head">
              <strong>{hotStandby ? 'Active-hot-standby' : 'Single-region'}</strong>
              <span class="bcp-summary-price {hotStandby ? '' : 'free'}">
                {hotStandby && standbyAsUsage ? PACKAGE_STRINGS.grow.billedAsUsage : hotStandby ? (shownQuote ? (shownQuote.topology_amount_baisa > 0 ? `+${formatOMR(shownQuote.topology_amount_baisa)} / mo` : QUOTE_STRINGS.included) : QUOTE_STRINGS.pending) : 'FREE'}
              </span>
            </div>
            {#if hotStandby}
              <div class="bcp-region-row">
                <span class="bcp-region-label">Primary</span>
                <span class="bcp-region-val">{primaryRegion}</span>
              </div>
              <div class="bcp-region-row">
                <span class="bcp-region-label">Replica</span>
                <span class="bcp-region-val">{replicaRegion}</span>
              </div>
              <p class="bcp-summary-note">Synchronous replica across two regions. RTO 30s, RPO 5s.</p>
            {:else}
              <p class="bcp-summary-note">One Postgres cluster in your primary region.</p>
            {/if}
          </div>
        </section>

        {#if canGrow || overage.mode === 'grow'}
          <!-- #6971 — what happens when the Organization reaches its package. -->
          <section class="rv-section" data-testid="review-overage" data-mode={overage.mode}>
            <div class="rv-head">
              <h2>{PACKAGE_STRINGS.grow.title}</h2>
              <a href="/addons#grow" class="rv-link" data-testid="review-overage-edit">Edit</a>
            </div>
            <div class="ov-summary {overage.mode === 'grow' ? 'grow' : ''}">
              <span class="ov-ico" aria-hidden="true">
                {#if overage.mode === 'grow'}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 17l6-6 4 4 8-8"/><path d="M15 7h6v6"/></svg>
                {:else}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v5c0 4.5-3 8.3-7 9.9-4-1.6-7-5.4-7-9.9V6l7-3z"/><path d="M9 12l2 2 4-4"/></svg>
                {/if}
              </span>
              <span class="ov-text">
                <strong data-testid="review-overage-title">{overage.title}</strong>
                {#if overage.detail.length > 0}
                  <span class="ov-detail" data-testid="review-overage-detail">{overage.detail.join(' · ')}</span>
                {/if}
              </span>
            </div>
          </section>
        {/if}

        <!-- The package and its add-ons, summarised: picked on the Add-ons
             step, so this is a read-back with a way back, not a second picker. -->
        <section class="rv-section" data-testid="review-addons-summary">
          <div class="rv-head">
            <h2>Add-ons</h2>
            <a href="/addons" class="rv-link" data-testid="review-addons-edit">Edit</a>
          </div>
          {#if selectedPackage}
            <div class="sum-pkg {selectedPackage.accent ? 'has-accent' : ''}" style={accentStyle(selectedPackage)} data-testid="review-package-summary">
              {#if selectedPackage.icon}
                <span class="sum-icon" style={selectedPackage.icon.bg ? `background: ${selectedPackage.icon.bg}` : ''}>
                  <img src={selectedPackage.icon.src} alt="" width="22" height="22" loading="lazy" decoding="async" />
                </span>
              {/if}
              <span class="sum-name">{PACKAGE_STRINGS.ladder.packageLine(selectedPackage.name)}</span>
              {#if selectedPackage.badge}<span class="sum-badge">{selectedPackage.badge}</span>{/if}
              <span class="sum-price">{doc?.currency ?? 'OMR'} {selectedPackage.price_month} <small>{PACKAGE_STRINGS.perMonth}</small></span>
            </div>
          {/if}
          {#if chosenAddons.length > 0}
            <ul class="sum-list">
              {#each chosenAddons as addon (addon.id)}
                {@const img = addonImage(addon)}
                <li class="sum-item" data-testid="review-addon-{addon.id}">
                  {#if img}
                    <span class="sum-icon small" style={img.bg ? `background: ${img.bg}` : ''}>
                      <img src={img.src} alt="" width="16" height="16" loading="lazy" decoding="async" />
                    </span>
                  {/if}
                  <span class="sum-item-name">{addon.name}</span>
                  <span class="sum-item-price">{addon.monthly_price === 0 ? 'Free' : `+${formatOMR(addon.monthly_price)}`}</span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="sum-empty" data-testid="review-addons-none">No add-ons. <a href="/addons" class="rv-link">Add some</a></p>
          {/if}
          {#if doc?.floor && doc.floor.length > 0}
            <!-- #6971 — what every package includes (the document's floor). -->
            <div class="sum-floor" data-testid="review-floor">
              <span class="sum-floor-lead">{PACKAGE_STRINGS.ladder.floorTitle}</span>
              <ul class="sum-floor-list">
                {#each doc.floor as f (f.key)}
                  <li class="sum-floor-item">
                    {#if f.icon}
                      <span class="sum-icon tiny" style={f.icon.bg ? `background: ${f.icon.bg}` : ''}>
                        <img src={f.icon.src} alt="" width="13" height="13" loading="lazy" decoding="async" />
                      </span>
                    {/if}
                    <span>{f.name}</span>
                  </li>
                {/each}
              </ul>
            </div>
          {/if}
        </section>
      </div>

      <!-- RIGHT: Cost sidebar -->
      <aside class="review-side">
        <div class="side-card">
          <h3>Monthly total</h3>
          <div class="total-breakdown">
            {#if selectedApps.length > 0}
              <div class="breakdown-row">
                <span>{selectedApps.length} app{selectedApps.length === 1 ? '' : 's'}</span>
                <span class="free-label">{formatOMR(0)}</span>
              </div>
            {/if}
            {#if shownQuote}
              <div class="breakdown-row" data-testid="review-total-plan">
                <span>{selectedPlan?.name || cart.planName || 'Plan'} plan</span>
                <span>{formatOMR(shownQuote.plan_amount_baisa)}</span>
              </div>
              {#each shownQuote.lines as line (line.sku)}
                <div class="breakdown-row" data-testid="review-total-package-addon-{line.sku}">
                  <span>{line.name}</span>
                  <span>{lineAmountLabel(line, formatOMR)}</span>
                </div>
              {/each}
              {#if shownQuote.topology_amount_baisa > 0}
                <div class="breakdown-row">
                  <span>Active-hot-standby</span>
                  <span>+{formatOMR(shownQuote.topology_amount_baisa)}</span>
                </div>
              {/if}
            {/if}
            {#if overage.mode === 'grow'}
              <div class="breakdown-row usage" data-testid="review-total-usage">
                <span class="usage-label">{PACKAGE_STRINGS.grow.sidebarUsage}<small>{PACKAGE_STRINGS.grow.sidebarUsageValue}</small></span>
                <span>{PACKAGE_STRINGS.grow.sidebarUsagePer}</span>
              </div>
            {/if}
          </div>
          <div class="total-row">
            <span>Total</span>
            <strong data-testid="review-total" data-source={fromDocument ? 'document' : 'quote'}>{shownQuote ? formatOMR(shownQuote.amount_baisa) : QUOTE_STRINGS.pending}</strong>
          </div>
          {#if fromDocument}
            <p class="quote-doc" data-testid="review-total-from-document">{QUOTE_STRINGS.fromDocument}</p>
          {:else if quoteError}
            <p class="quote-error" data-testid="review-quote-error">{QUOTE_STRINGS.unavailable}</p>
          {/if}
          <small>per month · first month prorated · cancel anytime</small>
          <a href="/checkout" class="checkout-cta">
            Proceed to Checkout &rarr;
          </a>
        </div>
      </aside>
    </div>

  {/if}
</div>

<!-- The step bar, docked like every other step's: the page is padded by its
     measured height, so it never covers the last row (the floating
     "← Topology" pill used to sit over the last add-on line). -->
<div class="step-bar" data-testid="step-bar" bind:this={stepBar}>
  <div class="step-bar-inner">
    <a href="/bcp" class="step-back" data-testid="review-back">&larr; Topology</a>
    <a href="/checkout" class="step-cta">Checkout &rarr;</a>
  </div>
</div>

<style>
  .review {
    --step-bar-h: 4.5rem;
    max-width: 1100px;
    margin: 0 auto;
    padding: 0.5rem 1.25rem calc(var(--step-bar-h) + env(safe-area-inset-bottom, 0px));
  }
  .review-title {
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.65rem;
    font-weight: 700;
    text-align: center;
  }

  /* Two-column layout */
  .review-layout {
    display: grid;
    grid-template-columns: 1fr 300px;
    gap: 1rem;
    align-items: start;
  }
  @media (max-width: 900px) { .review-layout { grid-template-columns: 1fr; } }

  /* Sections */
  .rv-section {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 10px;
    padding: 0.85rem 1rem;
    margin-bottom: 0.6rem;
  }
  .rv-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.55rem;
  }
  .rv-head h2 { font-size: 0.95rem; color: var(--color-text-strong); margin: 0; font-weight: 600; }
  .rv-note { color: var(--color-accent); font-size: 0.82rem; }
  .rv-note.rv-warn { color: #EF4444; }
  .rv-link { color: var(--color-accent); font-size: 0.82rem; text-decoration: none; }
  .rv-link:hover { text-decoration: underline; }

  /* Stack grid — horizontal cards matching app cards */
  .stack-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 0.5rem;
  }
  @media (max-width: 700px) { .stack-grid { grid-template-columns: 1fr; } }
  .stack-card {
    display: flex;
    align-items: flex-start;
    gap: 0.65rem;
    padding: 0.65rem;
    background: var(--color-bg);
    border-radius: 8px;
    border: 1px solid var(--color-border);
    text-decoration: none;
    color: inherit;
    transition: border-color 0.15s;
  }
  .stack-card:hover { border-color: var(--color-accent); }
  .stack-logo {
    width: 40px; height: 40px;
    border-radius: 10px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .stack-icon {
    width: 40px; height: 40px; min-width: 40px;
    border-radius: 10px;
    display: inline-flex; align-items: center; justify-content: center;
    color: #fff; font-size: 0.85rem; font-weight: 700; flex-shrink: 0;
  }
  .stack-body { flex: 1; min-width: 0; }
  .stack-name {
    color: var(--color-text-strong); font-size: 0.82rem; font-weight: 600;
    line-height: 1.2; margin-right: 0.4rem;
  }
  .stack-cat {
    color: var(--color-text-dim); font-size: 0.62rem; text-transform: capitalize;
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    padding: 0.08rem 0.35rem; border-radius: 3px;
  }
  .stack-desc {
    margin: 0.2rem 0 0; color: var(--color-text-dim); font-size: 0.72rem;
    line-height: 1.4;
    display: -webkit-box; -webkit-line-clamp: 1; -webkit-box-orient: vertical; overflow: hidden;
  }
  .stack-empty { text-align: center; padding: 2rem; color: var(--color-text-dim); }
  .stack-empty a { color: var(--color-accent); text-decoration: none; font-weight: 600; }

  /* Plan row — one card per package (or catalog plan) in one line */
  .plan-row {
    display: grid;
    grid-template-columns: repeat(var(--plan-cols, 5), minmax(0, 1fr));
    gap: 0.4rem;
  }
  @media (max-width: 700px) { .plan-row { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  .plan-option {
    display: flex; align-items: center; gap: 0.5rem;
    padding: 0.55rem 0.7rem;
    background: var(--color-bg);
    border: 1.5px solid var(--color-border);
    border-radius: 8px;
    cursor: pointer;
    transition: all 0.15s;
  }
  .plan-option:hover { border-color: var(--color-text-dim); }
  .plan-option.checked {
    border-color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 6%, var(--color-surface));
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-success) 18%, transparent);
  }
  .plan-option.popular { border-color: color-mix(in srgb, var(--color-success) 30%, var(--color-border)); }
  .plan-option input { accent-color: var(--color-success); }
  .plan-opt-body { display: flex; flex-direction: column; gap: 0.1rem; flex: 1; min-width: 0; }
  .plan-opt-name { color: var(--color-text-strong); font-weight: 600; font-size: 0.82rem; }
  .plan-opt-price strong { color: var(--color-text-strong); font-size: 0.95rem; font-weight: 700; }
  .plan-opt-price { font-size: 0.7rem; color: var(--color-text-dim); }
  .plan-opt-specs { color: var(--color-text-dim); font-size: 0.68rem; }
  /* The package's accent: a top stripe, and the ring when chosen. */
  .plan-option.has-accent { box-shadow: inset 0 3px 0 var(--pk-accent); }
  .plan-option.has-accent.checked {
    border-color: var(--pk-accent);
    background: color-mix(in srgb, var(--pk-accent) 7%, var(--color-surface));
    box-shadow: inset 0 3px 0 var(--pk-accent), 0 0 0 2px color-mix(in srgb, var(--pk-accent) 22%, transparent);
  }
  .plan-option.has-accent input { accent-color: var(--pk-accent); }
  .plan-opt-icon, .sum-icon {
    display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
    width: 30px; height: 30px; border-radius: 8px;
    background: color-mix(in srgb, var(--color-text) 7%, transparent);
  }
  .plan-opt-icon img, .sum-icon img { display: block; }
  .plan-opt-head { display: flex; align-items: center; gap: 0.4rem; }
  .plan-opt-icon { width: 24px; height: 24px; border-radius: 6px; }
  .sum-icon.small { width: 24px; height: 24px; border-radius: 6px; }
  .plan-option.suggested { border-color: var(--color-accent); animation: pulse-border 1.5s ease-in-out infinite; }
  @keyframes pulse-border { 0%, 100% { box-shadow: 0 0 0 0 transparent; } 50% { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 20%, transparent); } }

  /* Concurrency selector */
  .conc-row { display: grid; grid-template-columns: repeat(3, 1fr); gap: 0.4rem; margin-bottom: 0.75rem; }
  .conc-btn {
    display: flex; flex-direction: column; align-items: center; gap: 0.1rem;
    padding: 0.55rem 0.5rem;
    background: var(--color-bg);
    border: 1.5px solid var(--color-border);
    border-radius: 8px; cursor: pointer;
    font: inherit; color: inherit;
    transition: all 0.15s;
  }
  .conc-btn:hover { border-color: var(--color-text-dim); }
  .conc-btn.active {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 6%, var(--color-surface));
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 18%, transparent);
  }
  .conc-btn strong { color: var(--color-text-strong); font-size: 0.82rem; }

  /* Capacity — compact donut gauge */
  .capacity-compact {
    display: flex;
    align-items: center;
    gap: 1rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 10px;
    padding: 0.75rem 1rem;
  }
  .cap-gauge-wrap {
    position: relative;
    width: 80px; height: 80px;
    flex-shrink: 0;
  }
  .cap-gauge { width: 80px; height: 80px; }
  .cap-gauge-label {
    position: absolute; inset: 0;
    display: flex; flex-direction: column;
    align-items: center; justify-content: center;
  }
  .cap-gauge-label strong { font-size: 1rem; font-weight: 800; line-height: 1; }
  .cap-gauge-label span { font-size: 0.6rem; color: var(--color-text-dim); }
  .cap-details { flex: 1; display: flex; flex-direction: column; gap: 0.3rem; }
  .cap-row {
    display: flex; justify-content: space-between;
    font-size: 0.78rem;
  }
  .cap-metric { color: var(--color-text-dim); font-weight: 500; }
  .cap-val {
    color: var(--color-text); font-family: 'JetBrains Mono', monospace;
    font-size: 0.72rem;
  }
  .cap-msg {
    margin-top: 0.2rem; font-size: 0.75rem; font-weight: 600;
    display: flex; align-items: center; gap: 0.4rem;
  }
  .cap-ok { color: #22C55E; }
  .cap-warn { color: #F59E0B; }
  .cap-over { color: #EF4444; }
  .cs-upgrade {
    background: var(--color-accent); color: #fff;
    border: none; border-radius: 5px;
    padding: 0.25rem 0.5rem;
    font: inherit; font-size: 0.75rem; font-weight: 600;
    cursor: pointer;
  }
  .cs-upgrade:hover { filter: brightness(0.9); }
  .cs-hint { color: var(--color-text-dim); font-size: 0.82rem; text-align: center; padding: 0.5rem; margin: 0; }

  /* Two-column row for Expected Usage + Workspace */
  .rv-two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.6rem;
    align-items: start;
  }
  .rv-two-col > .rv-section { margin-bottom: 0; }
  @media (max-width: 700px) { .rv-two-col { grid-template-columns: 1fr; } }

  /* Workspace preview */
  .ws-preview { display: flex; flex-direction: column; }
  .ws-row {
    display: flex; justify-content: space-between; padding: 0.35rem 0;
    font-size: 0.85rem;
  }
  .ws-row > span { color: var(--color-text-dim); }
  .ws-row strong { color: var(--color-text-strong); font-size: 0.78rem; }

  /* Business-continuity summary (Pillar-2 BCP) — #4524 */
  .bcp-summary {
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    padding: 0.7rem 0.85rem;
  }
  .bcp-summary.bcp-hot {
    border-color: color-mix(in srgb, var(--color-accent) 35%, var(--color-border));
    background: color-mix(in srgb, var(--color-accent) 4%, var(--color-bg));
  }
  .bcp-summary-head {
    display: flex; align-items: center; justify-content: space-between; gap: 0.5rem;
  }
  .bcp-summary-head strong { color: var(--color-text-strong); font-size: 0.88rem; }
  .bcp-summary-price {
    padding: 0.12rem 0.5rem; border-radius: 4px;
    font-size: 0.7rem; font-weight: 700; letter-spacing: 0.03em;
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent); white-space: nowrap;
  }
  .bcp-summary-price.free {
    background: color-mix(in srgb, var(--color-success) 15%, transparent);
    color: var(--color-success);
  }
  .bcp-region-row {
    display: flex; justify-content: space-between; gap: 0.5rem;
    padding: 0.25rem 0 0; font-size: 0.78rem;
  }
  .bcp-region-label { color: var(--color-text-dim); }
  .bcp-region-val {
    color: var(--color-text); font-family: 'JetBrains Mono', monospace; font-size: 0.72rem;
  }
  .bcp-summary-note {
    margin: 0.4rem 0 0; color: var(--color-text-dim); font-size: 0.72rem; line-height: 1.4;
  }

  /* The package + add-ons summary */
  .sum-pkg {
    display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap;
    padding: 0.6rem 0.75rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
  }
  .sum-pkg.has-accent { border-left: 3px solid var(--pk-accent); }
  .sum-name { color: var(--color-text-strong); font-weight: 600; font-size: 0.88rem; }
  .sum-badge {
    padding: 0.1rem 0.45rem; border-radius: 999px;
    font-size: 0.66rem; font-weight: 700; letter-spacing: 0.02em;
    background: color-mix(in srgb, var(--color-text) 10%, transparent); color: var(--color-text);
  }
  .sum-pkg.has-accent .sum-badge { background: var(--pk-accent); color: var(--pk-accent-fg); }
  .sum-price { margin-left: auto; color: var(--color-text-strong); font-weight: 700; font-size: 0.88rem; white-space: nowrap; }
  .sum-price small { color: var(--color-text-dim); font-weight: 500; font-size: 0.72rem; }
  .sum-list { list-style: none; margin: 0.5rem 0 0; padding: 0; display: flex; flex-direction: column; }
  .sum-item {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.45rem 0.75rem;
    border-bottom: 1px dashed var(--color-border);
    font-size: 0.82rem;
  }
  .sum-item:last-child { border-bottom: 0; }
  .sum-item-name { flex: 1; min-width: 0; color: var(--color-text); }
  .sum-item-price { color: var(--color-text-strong); font-weight: 600; white-space: nowrap; }
  .sum-floor { margin-top: 0.7rem; padding-top: 0.6rem; border-top: 1px dashed var(--color-border); }
  .sum-floor-lead { display: block; color: var(--color-text-dim); font-size: 0.72rem; font-weight: 600; margin-bottom: 0.4rem; }
  .sum-floor-list { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .sum-floor-item {
    display: inline-flex; align-items: center; gap: 0.35rem;
    padding: 0.2rem 0.55rem 0.2rem 0.3rem;
    border: 1px solid var(--color-border); border-radius: 999px;
    background: var(--color-bg); color: var(--color-text); font-size: 0.72rem;
  }
  .sum-icon.tiny { width: 18px; height: 18px; border-radius: 999px; }
  .sum-empty { margin: 0.5rem 0 0; color: var(--color-text-dim); font-size: 0.8rem; }

  /* Sidebar */
  .review-side { position: sticky; top: 5rem; }
  .side-card {
    background: var(--color-surface); border: 1px solid var(--color-border);
    border-radius: 10px; padding: 1.1rem;
  }
  .side-card h3 {
    color: var(--color-text-dim); font-size: 0.75rem; margin: 0 0 0.75rem;
    font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase;
  }
  .total-breakdown { border-bottom: 1px dashed var(--color-border); padding-bottom: 0.5rem; }
  .breakdown-row {
    display: flex; justify-content: space-between;
    padding: 0.2rem 0; color: var(--color-text-dim); font-size: 0.82rem;
  }
  .free-label { color: var(--color-success); font-weight: 600; }
  .total-row {
    display: flex; justify-content: space-between; align-items: baseline; padding: 0.55rem 0 0.2rem;
  }
  .total-row span { color: var(--color-text-strong); font-weight: 600; }
  .total-row strong { color: var(--color-text-strong); font-size: 1.4rem; font-weight: 800; }
  .side-card small { color: var(--color-text-dim); font-size: 0.78rem; }
  .quote-doc { margin: 0.4rem 0 0; color: var(--color-text-dim); font-size: 0.72rem; line-height: 1.4; }
  .quote-error { margin: 0.4rem 0 0; color: #EF4444; font-size: 0.75rem; line-height: 1.4; }
  .checkout-cta {
    display: flex; align-items: center; justify-content: center; gap: 0.5rem;
    margin-top: 1rem; padding: 0.65rem 1rem;
    background: var(--color-accent); color: #fff;
    border-radius: 7px; text-decoration: none;
    font-weight: 600; font-size: 0.9rem;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--color-accent) 25%, transparent);
  }
  .checkout-cta:hover { filter: brightness(0.9); }

  /* #6971 — "When you reach your package", read back */
  .ov-summary {
    display: flex; align-items: flex-start; gap: 0.65rem;
    padding: 0.65rem 0.8rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
  }
  .ov-summary.grow {
    border-color: color-mix(in srgb, var(--color-success) 35%, var(--color-border));
    background: color-mix(in srgb, var(--color-success) 5%, var(--color-bg));
  }
  .ov-ico {
    display: inline-flex; align-items: center; justify-content: center; flex-shrink: 0;
    width: 28px; height: 28px; border-radius: 8px;
    color: var(--color-accent); background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  }
  .ov-summary.grow .ov-ico { color: var(--color-success); background: color-mix(in srgb, var(--color-success) 13%, transparent); }
  .ov-ico svg { width: 16px; height: 16px; }
  .ov-text { display: flex; flex-direction: column; gap: 0.15rem; min-width: 0; }
  .ov-text strong { color: var(--color-text-strong); font-size: 0.88rem; }
  .ov-detail { color: var(--color-text-dim); font-size: 0.76rem; line-height: 1.45; }
  .breakdown-row.usage span:last-child { color: var(--color-success); font-weight: 600; white-space: nowrap; }
  .usage-label { display: flex; flex-direction: column; }
  .usage-label small { color: var(--color-text-dimmer); font-size: 0.7rem; }

  /* The step bar — the same chrome as the Add-ons and Topology steps. */
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
    max-width: 1100px;
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
  }
  .step-cta:hover { filter: brightness(0.9); }
</style>
