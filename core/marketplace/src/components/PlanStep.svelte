<script lang="ts">
  import { getPlans, type Plan } from '../lib/api';
  import { readCart, setPlan } from '../lib/cart';
  import { formatOMRAmount } from '../lib/currency';

  let plans = $state<Plan[]>([]);
  let selected = $state<string | null>(readCart().plan);
  let loading = $state(true);

  // Extended capability rows — hardcoded per tier
  const capsByName: Record<string, Record<string, string>> = {
    'S':     { bandwidth: '500 GB / mo', backupRetention: '7 days', responseSla: '—', support: 'Email' },
    'M':     { bandwidth: '2 TB / mo', backupRetention: '15 days', responseSla: '8h', support: 'Email + Chat' },
    'L':     { bandwidth: '10 TB / mo', backupRetention: '30 days', responseSla: '4h', support: 'Email + Chat + Phone' },
    'XL':    { bandwidth: 'Unlimited', backupRetention: '90 days', responseSla: '1h', support: 'Dedicated account manager' },
    'Flexi': { bandwidth: 'Metered', backupRetention: 'Configurable', responseSla: 'Configurable', support: 'Email + Chat' },
  };

  const capRows = [
    { label: 'vCPU', key: 'cpu' },
    { label: 'RAM', key: 'memory' },
    { label: 'Disk', key: 'storage' },
    { label: 'Bandwidth', key: 'bandwidth' },
    { label: 'Backup retention', key: 'backupRetention' },
    { label: 'SSL certificates', key: 'ssl' },
    { label: 'SSO (SAML / OIDC)', key: 'sso' },
    { label: 'Response SLA', key: 'responseSla' },
    { label: 'Support', key: 'support' },
  ];

  $effect(() => {
    getPlans()
      .then((data) => {
        plans = data;
        if (!selected) {
          const pop = data.find(p => p.popular);
          if (pop) { selected = pop.id; setPlan(pop.id, pop.name); }
        } else {
          // Ensure planName is set for existing selection.
          const cur = data.find(p => p.id === selected);
          if (cur) setPlan(cur.id, cur.name);
        }
        loading = false;
      })
      .catch(() => {
        plans = [
          { id: 's', slug: 's', name: 'S', tagline: '', resources: { cpu: '2 vCPU', memory: '4 GB', storage: '25 GB' }, monthly_price: 5000, features: [], popular: false },
          { id: 'm', slug: 'm', name: 'M', tagline: '', resources: { cpu: '4 vCPU', memory: '8 GB', storage: '50 GB' }, monthly_price: 9000, features: [], popular: true },
          { id: 'l', slug: 'l', name: 'L', tagline: '', resources: { cpu: '8 vCPU', memory: '16 GB', storage: '100 GB' }, monthly_price: 16000, features: [], popular: false },
          { id: 'xl', slug: 'xl', name: 'XL', tagline: '', resources: { cpu: '16 vCPU', memory: '32 GB', storage: '200 GB' }, monthly_price: 30000, features: [], popular: false },
          { id: 'flexi', slug: 'flexi', name: 'Flexi', tagline: '', resources: { cpu: 'On demand', memory: 'On demand', storage: 'On demand' }, monthly_price: 0, features: [], popular: false },
        ];
        if (!selected) { selected = 'm'; setPlan('m', 'M'); }
        loading = false;
      });
  });

  function selectPlan(id: string) {
    selected = id;
    const plan = plans.find(p => p.id === id);
    setPlan(id, plan?.name);
  }

  function cellValue(plan: Plan, key: string): string {
    if (key === 'cpu') return plan.resources.cpu;
    if (key === 'memory') return plan.resources.memory;
    if (key === 'storage') return plan.resources.storage;
    if (key === 'ssl' || key === 'sso') return '✓';
    return capsByName[plan.name]?.[key] ?? '—';
  }

  // #85 — "OMR" is rendered as a styled span next to the amount, so we use
  // `formatOMRAmount` (numeric portion only, 3-decimal baisa precision). When
  // the plan price is 0 we still show an em dash — the free tier doesn't have
  // a numeric price to display.
  function formatPrice(baisa: number): string {
    if (baisa === 0) return '—';
    return formatOMRAmount(baisa);
  }

  const selectedPlan = $derived(plans.find(p => p.id === selected));
</script>

<div class="plans-page">
  <h1 class="plans-title">Pick a plan</h1>

  {#if loading}
    <div class="flex justify-center py-20">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else}
    <div class="pd">
      <!-- Left gutter — capability labels -->
      <aside class="pd-gutter">
        <div class="pd-g-head"></div>
        {#each capRows as row}
          <div class="pd-g-row">{row.label}</div>
        {/each}
        <div class="pd-g-foot"></div>
      </aside>

      <!-- Plan columns -->
      <div class="pd-cards" style="grid-template-columns: repeat({plans.length}, 1fr)">
        {#each plans as plan}
          {@const isSelected = selected === plan.id}
          {@const isFlexi = plan.slug === 'flexi' || plan.name === 'Flexi'}
          <div class="pcard-wrapper">
            {#if plan.popular}
              <div class="pcard-hat">Popular</div>
            {/if}
            <article class="pcard {isSelected ? 'selected' : ''} {isFlexi ? 'flexi' : ''}">
              <div class="pcard-head">
                <h3>{plan.name}</h3>
                <div class="pcard-price-wrap">
                  {#if isFlexi}
                    <div class="pcard-cu-note">1 CU = 1 vCPU + 2 GB RAM</div>
                    <div class="pcard-price">
                      <span class="pcard-currency">OMR</span>
                      <strong>2</strong>
                      <span class="pcard-per">/ CU / mo</span>
                    </div>
                  {:else}
                    <div class="pcard-price">
                      <span class="pcard-currency">OMR</span>
                      <strong>{formatPrice(plan.monthly_price)}</strong>
                      <span class="pcard-per">/ mo</span>
                    </div>
                  {/if}
                </div>
              </div>

              {#each capRows as row}
                <div class="pcard-cell {(row.key === 'ssl' || row.key === 'sso') ? 'included' : ''}">{cellValue(plan, row.key)}</div>
              {/each}

              <div class="pcard-foot">
                <button
                  onclick={() => selectPlan(plan.id)}
                  class="pcard-cta {isSelected ? 'primary' : 'ghost'}"
                >
                  {isSelected ? 'Selected' : 'Select'}
                </button>
              </div>
            </article>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<div class="float-nav">
  <a href="/apps" class="float-cta">Continue to Stack &rarr;</a>
</div>

<style>
  .plans-page { max-width: 1280px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }
  .plans-title {
    text-align: center;
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.75rem;
    font-weight: 700;
    letter-spacing: -0.01em;
  }

  /* -- Pricing Deck -------------------------------- */
  .pd {
    display: grid;
    grid-template-columns: 150px 1fr;
    gap: 0;
    max-width: 1200px;
    margin: 0 auto;
    align-items: start;
  }

  /* Gutter */
  .pd-gutter {
    display: flex;
    flex-direction: column;
    gap: 0;
  }
  .pd-g-head { min-height: 110px; }
  .pd-g-row {
    min-height: 30px;
    padding: 0 0.9rem;
    display: flex;
    align-items: center;
    color: var(--color-text-dim);
    font-size: 0.8rem;
    border-bottom: 1px dashed var(--color-border);
  }
  .pd-g-row:last-of-type { border-bottom: 0; }
  .pd-g-foot { min-height: 62px; }

  /* Cards row */
  .pd-cards {
    display: grid;
    gap: 0.55rem;
  }

  /* Card wrapper — bottom-aligned so Popular hat adds height above */
  .pcard-wrapper {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    justify-content: flex-end;
  }

  /* Popular hat */
  .pcard-hat {
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

  /* Card */
  .pcard {
    position: relative;
    display: flex;
    flex-direction: column;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    overflow: visible;
    transition: transform 0.15s, box-shadow 0.15s, border-color 0.15s;
  }
  .pcard-hat + .pcard {
    border-radius: 0 0 12px 12px;
    border-top: none;
  }

  .pcard:hover {
    transform: translateY(-2px);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.15);
    border-color: var(--color-success);
  }
  .pcard.selected {
    border-color: var(--color-success);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-success) 20%, transparent), 0 4px 16px rgba(0, 0, 0, 0.1);
    background: color-mix(in srgb, var(--color-success) 5%, var(--color-surface));
  }
  .pcard.flexi {
    background: color-mix(in srgb, var(--color-accent) 3%, var(--color-surface));
    border-style: dashed;
  }

  .pcard-head {
    min-height: 110px;
    padding: 0.7rem 0.9rem;
    text-align: center;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 0.25rem;
    border-bottom: 1px solid var(--color-border);
  }
  .pcard-head h3 {
    margin: 0;
    color: var(--color-text-strong);
    font-size: 1.1rem;
    font-weight: 700;
  }
  .pcard-price-wrap {
    margin-top: auto;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.1rem;
  }
  .pcard-price {
    display: flex;
    align-items: baseline;
    justify-content: center;
    gap: 0.2rem;
    white-space: nowrap;
  }
  .pcard-price strong {
    font-size: 1.9rem;
    font-weight: 800;
    color: var(--color-text-strong);
    line-height: 1;
  }
  .pcard-currency, .pcard-per {
    color: var(--color-text-dim);
    font-size: 0.78rem;
    font-weight: 600;
  }
  .pcard-cu-note {
    color: var(--color-text-dim);
    font-size: 0.68rem;
    font-weight: 500;
    margin-top: 0.15rem;
  }

  /* Capability cells */
  .pcard-cell {
    min-height: 30px;
    padding: 0 0.6rem;
    display: flex;
    align-items: center;
    justify-content: center;
    text-align: center;
    color: var(--color-text);
    font-size: 0.8rem;
    border-bottom: 1px dashed var(--color-border);
  }
  .pcard-cell.included {
    color: var(--color-success);
    font-weight: 600;
  }

  /* Foot — CTA */
  .pcard-foot {
    min-height: 62px;
    padding: 0.7rem;
    display: flex;
    align-items: center;
    justify-content: center;
    border-top: 1px solid var(--color-border);
    background: var(--color-bg);
    border-radius: 0 0 12px 12px;
  }
  .pcard-cta {
    width: 100%;
    padding: 0.6rem 0.7rem;
    border-radius: 7px;
    border: none;
    font-weight: 600;
    font-size: 0.85rem;
    text-align: center;
    cursor: pointer;
    transition: background 0.15s, border-color 0.15s, color 0.15s;
  }
  .pcard-cta.primary {
    background: var(--color-success);
    color: #fff;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--color-success) 30%, transparent);
  }
  .pcard-cta.primary:hover { filter: brightness(0.95); }
  .pcard-cta.ghost {
    background: transparent;
    color: var(--color-text);
    border: 1.5px solid var(--color-border-strong, var(--color-border));
  }
  .pcard-cta.ghost:hover {
    border-color: var(--color-success);
    color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 8%, transparent);
  }

  /* Floating navigation pill */
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

  /* Tablet */
  @media (max-width: 1000px) {
    .pd { grid-template-columns: 120px 1fr; }
    .pd-g-row { padding: 0 0.55rem; font-size: 0.76rem; }
    .pd-cards { gap: 0.35rem; }
    .pcard-head { padding: 0.7rem 0.45rem; }
    .pcard-head h3 { font-size: 0.95rem; }
    .pcard-price strong { font-size: 1.45rem; }
    .pcard-cta { font-size: 0.74rem; padding: 0.45rem 0.35rem; }
    .pcard-cell { font-size: 0.74rem; padding: 0 0.3rem; }
  }

  /* Mobile: stack cards */
  @media (max-width: 640px) {
    .pd { grid-template-columns: 1fr; }
    .pd-gutter { display: none; }
    .pd-cards { grid-template-columns: 1fr !important; }
  }
</style>
