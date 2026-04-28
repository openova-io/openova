<script lang="ts">
  import { getPlans, getApps, getAddons, type Plan, type App, type AddOn } from '../lib/api';
  import { readCart, toggleAddon, writeCart } from '../lib/cart';
  import { formatOMR, formatOMRAmount } from '../lib/currency';

  let cart = $state(readCart());
  let plans = $state<Plan[]>([]);
  let apps = $state<App[]>([]);
  let addons = $state<AddOn[]>([]);
  let loading = $state(true);
  let concurrency = $state<'small' | 'medium' | 'large'>('small');

  const selectedPlan = $derived(plans.find(p => p.id === cart.plan));
  const selectedApps = $derived(apps.filter(a => cart.apps.includes(a.id)));
  const paidAddons = $derived(addons.filter(a => !a.included));
  const selectedAddons = $derived(addons.filter(a => cart.addons.includes(a.id)));

  const planCost = $derived(selectedPlan?.monthly_price ?? 0);
  const addonCost = $derived(selectedAddons.reduce((sum, a) => sum + a.monthly_price, 0));
  const totalCost = $derived(planCost + addonCost);

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

  const concOptions = [
    { id: 'small' as const, label: 'Low', range: '1–10 users', multiplier: 1.0 },
    { id: 'medium' as const, label: 'Medium', range: '10–30 users', multiplier: 1.5 },
    { id: 'large' as const, label: 'High', range: '30–100 users', multiplier: 2.2 },
  ];

  // Plan slug → capacity in numeric units
  const planCapMap: Record<string, { ram: number; cpu: number; disk: number }> = {
    s: { ram: 4096, cpu: 2000, disk: 25 },
    m: { ram: 8192, cpu: 4000, disk: 50 },
    l: { ram: 16384, cpu: 8000, disk: 100 },
    xl: { ram: 32768, cpu: 16000, disk: 200 },
    flexi: { ram: 65536, cpu: 32000, disk: 500 },
  };

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

  const planCap = $derived(planCapMap[selectedPlan?.slug ?? ''] ?? { ram: 0, cpu: 0, disk: 0 });
  const ramPct = $derived(planCap.ram > 0 ? Math.round((grossRam / planCap.ram) * 100) : 0);
  const cpuPct = $derived(planCap.cpu > 0 ? Math.round((grossCpu / planCap.cpu) * 100) : 0);
  const diskPct = $derived(planCap.disk > 0 ? Math.round((grossDisk / planCap.disk) * 100) : 0);
  const maxPct = $derived(Math.max(ramPct, cpuPct, diskPct));

  // Find the smallest plan that fits
  const suggestedPlan = $derived.by(() => {
    const order = ['s', 'm', 'l', 'xl', 'flexi'];
    for (const slug of order) {
      const cap = planCapMap[slug];
      if (grossRam <= cap.ram && grossCpu <= cap.cpu && grossDisk <= cap.disk) {
        return plans.find(p => p.slug === slug) ?? null;
      }
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

  $effect(() => {
    Promise.all([getPlans(), getApps(), getAddons()])
      .then(([p, a, ad]) => { plans = p; apps = a.filter(x => !x.system); addons = ad; loading = false; })
      .catch(() => { loading = false; });
  });

  function toggleAddonItem(id: string) {
    cart = toggleAddon(id);
  }

  // #85 — shared helper. `formatOMRAmount` is used where the "OMR" label is
  // already rendered as a separate span (plan-opt-price hero); `formatOMR`
  // prefixes "OMR " and is used everywhere else so every baisa figure in the
  // review sidebar matches the checkout and the console.

  function upgradePlan() {
    if (suggestedPlan) {
      cart.plan = suggestedPlan.id;
      writeCart(cart);
      cart = readCart();
    }
  }

  // Addon icons by slug
  const addonIcons: Record<string, string> = {
    'waf': '🔥', 'ips': '🚨', 'vuln-scan': '🔍',
    'custom-domain': '🌐', 'log-management': '📋', 'priority-support': '⚡',
    'daily-backup': '🛡️', 'api-access': '🔌', 'dedicated-ip': '🌍',
  };
</script>

<div class="review">
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
          <div class="plan-row">
            {#each plans as plan}
              {@const isChecked = cart.plan === plan.id}
              <label class="plan-option {plan.popular ? 'popular' : ''} {isChecked ? 'checked' : ''} {suggestedPlan?.id === plan.id && maxPct > 100 ? 'suggested' : ''}">
                <input
                  type="radio"
                  name="plan"
                  value={plan.id}
                  checked={isChecked}
                  onchange={() => { cart.plan = plan.id; writeCart(cart); cart = readCart(); }}
                />
                <span class="plan-opt-body">
                  <span class="plan-opt-name">{plan.name}</span>
                  <span class="plan-opt-price">
                    {#if plan.slug === 'flexi' || plan.name === 'Flexi'}
                      <strong>2</strong> OMR/CU/mo
                    {:else}
                      <strong>{formatOMRAmount(plan.monthly_price)}</strong> OMR/mo
                    {/if}
                  </span>
                  <span class="plan-opt-specs">{plan.resources.cpu} · {plan.resources.memory} · {plan.resources.storage}</span>
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
                  <span>{opt.range}</span>
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
                  <div class="cap-row">
                    <span class="cap-metric">RAM</span>
                    <span class="cap-val">{grossRam} / {planCap.ram} MiB</span>
                  </div>
                  <div class="cap-row">
                    <span class="cap-metric">CPU</span>
                    <span class="cap-val">{grossCpu} / {planCap.cpu} m</span>
                  </div>
                  <div class="cap-row">
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
                <h2>Tenant</h2>
                <a href="/addons" class="rv-link">Edit</a>
              </div>
              <div class="ws-preview">
                <div class="ws-row"><span>URL</span><strong class="font-mono">{cart.subdomain}.omani.rest</strong></div>
              </div>
            </section>
          {/if}
        </div>

        <!-- Optional extras (sme2-style tile grid) -->
        <section class="rv-section">
          <div class="rv-head">
            <h2>Optional extras</h2>
            <span class="rv-note">Skip any you don't need</span>
          </div>
          <div class="addon-grid">
            {#each paidAddons as addon}
              {@const isChecked = cart.addons.includes(addon.id)}
              <label class="addon-tile {isChecked ? 'checked' : ''}">
                <input
                  type="checkbox"
                  checked={isChecked}
                  onchange={() => toggleAddonItem(addon.id)}
                />
                <span class="addon-icon">{addonIcons[addon.slug] || addon.icon || '📦'}</span>
                <div class="addon-body">
                  <strong>{addon.name}</strong>
                  <p>{addon.tagline}</p>
                </div>
                <span class="addon-price">{addon.monthly_price === 0 ? 'Free' : `+${formatOMR(addon.monthly_price)}`}</span>
              </label>
            {/each}
          </div>
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
            {#if selectedPlan}
              <div class="breakdown-row">
                <span>{selectedPlan.name} plan</span>
                <span>{formatOMR(planCost)}</span>
              </div>
            {/if}
            {#each selectedAddons as addon}
              <div class="breakdown-row">
                <span>{addon.name}</span>
                <span>+{formatOMR(addon.monthly_price)}</span>
              </div>
            {/each}
          </div>
          <div class="total-row">
            <span>Total</span>
            <strong>{formatOMR(totalCost)}</strong>
          </div>
          <small>per month · first month prorated · cancel anytime</small>
          <a href="/checkout" class="checkout-cta">
            Proceed to Checkout &rarr;
          </a>
        </div>
      </aside>
    </div>

    <div class="float-nav">
      <a href="/addons" class="float-back">&larr; Setup</a>
    </div>
  {/if}
</div>

<style>
  .review { max-width: 1100px; margin: 0 auto; padding: 0.5rem 1.25rem 4.5rem; }
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

  /* Plan row — all 5 in one line */
  .plan-row {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 0.4rem;
  }
  @media (max-width: 700px) { .plan-row { grid-template-columns: repeat(3, 1fr); } }
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
  .plan-opt-body { display: flex; flex-direction: column; gap: 0.1rem; flex: 1; }
  .plan-opt-name { color: var(--color-text-strong); font-weight: 600; font-size: 0.82rem; }
  .plan-opt-price strong { color: var(--color-text-strong); font-size: 0.95rem; font-weight: 700; }
  .plan-opt-price { font-size: 0.7rem; color: var(--color-text-dim); }
  .plan-opt-specs { color: var(--color-text-dim); font-size: 0.68rem; }
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
  .conc-btn span { color: var(--color-text-dim); font-size: 0.72rem; }

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

  /* Add-ons grid — sme2 tile style */
  .addon-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 0.5rem;
  }
  .addon-tile {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.7rem;
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    cursor: pointer;
    transition: border-color 0.15s;
  }
  .addon-tile:hover { border-color: var(--color-text-dim); }
  .addon-tile.checked {
    border-color: var(--color-accent);
    background: color-mix(in srgb, var(--color-accent) 4%, var(--color-bg));
  }
  .addon-tile input { accent-color: var(--color-accent); }
  .addon-icon { font-size: 1.1rem; }
  .addon-body { flex: 1; }
  .addon-body strong { display: block; color: var(--color-text-strong); font-size: 0.85rem; }
  .addon-body p { margin: 0.1rem 0 0; color: var(--color-text-dim); font-size: 0.72rem; }
  .addon-price { color: var(--color-text-strong); font-weight: 600; font-size: 0.85rem; white-space: nowrap; }

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
  .checkout-cta {
    display: flex; align-items: center; justify-content: center; gap: 0.5rem;
    margin-top: 1rem; padding: 0.65rem 1rem;
    background: var(--color-accent); color: #fff;
    border-radius: 7px; text-decoration: none;
    font-weight: 600; font-size: 0.9rem;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--color-accent) 25%, transparent);
  }
  .checkout-cta:hover { filter: brightness(0.9); }

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
</style>
