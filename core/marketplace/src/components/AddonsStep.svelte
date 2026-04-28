<script lang="ts">
  import { getAddons, getApps, checkSlug, type AddOn, type App } from '../lib/api';
  import { readCart, toggleAddon, setOrgDetails, writeCart } from '../lib/cart';
  import { formatOMR } from '../lib/currency';

  let addons = $state<AddOn[]>([]);
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
  let selectedTLD = $state('omani.rest');
  let byodDomain = $state('');

  const tlds = ['omani.rest', 'omani.works', 'omani.trade', 'omani.homes'];

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

  $effect(() => {
    getAddons()
      .then((data) => { addons = data; loading = false; })
      .catch(() => {
        addons = [
          { id: 'daily-backup', name: 'Daily Backup', slug: 'daily-backup', tagline: 'Automated daily backups with 30-day retention', icon: '🛡️', monthly_price: 3000, included: false },
          { id: 'waf', name: 'Web Application Firewall', slug: 'waf', tagline: 'Coraza WAF — OWASP CRS protection', icon: '🔥', monthly_price: 4000, included: false },
          { id: 'ips', name: 'Intrusion Prevention', slug: 'ips', tagline: 'Community-powered threat intelligence — CrowdSec', icon: '🚨', monthly_price: 3000, included: false },
          { id: 'vuln-scan', name: 'Vulnerability Scanner', slug: 'vuln-scan', tagline: 'Weekly CVE scans + remediation reports', icon: '🔍', monthly_price: 2000, included: false },
          { id: 'custom-domain', name: 'Custom Domain', slug: 'custom-domain', tagline: 'Your brand, your domain — with automatic TLS', icon: '🌐', monthly_price: 2000, included: false },
          { id: 'log-management', name: 'Log Management', slug: 'log-management', tagline: 'Search and analyze all your app logs — Grafana Loki', icon: '📋', monthly_price: 3000, included: false },
          { id: 'priority-support', name: 'Priority Support', slug: 'priority-support', tagline: '4-hour response SLA + dedicated channel', icon: '⚡', monthly_price: 5000, included: false },
        ];
        loading = false;
      });
  });

  const paidAddons = $derived(addons.filter(a => !a.included));

  function toggle(id: string) {
    cart = toggleAddon(id);
  }

  function saveSubdomain() {
    cart = setOrgDetails(cart.orgName, subdomain, cart.email);
  }

  // #85 — shared helper renders "OMR 3.000". Previously we rounded to whole
  // OMR here while Review and Checkout used different precision; now every
  // addon price on every step shows the same baisa-precise value.

  // Addon icons by slug
  const addonIcons: Record<string, string> = {
    'waf': '🔥', 'ips': '🚨', 'vuln-scan': '🔍',
    'custom-domain': '🌐', 'log-management': '📋', 'priority-support': '⚡',
    'daily-backup': '🛡️',
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
            <select bind:value={selectedTLD} class="domain-tld">
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

    <!-- Optional extras — tile grid -->
    <section class="ao-section">
      <div class="ao-head">
        <h2>Optional extras</h2>
        <span class="ao-note">Skip any you don't need</span>
      </div>
      <div class="extras-grid">
        {#each paidAddons as addon}
          {@const isChecked = cart.addons.includes(addon.id)}
          <button
            type="button"
            onclick={() => toggle(addon.id)}
            class="extra-tile clickable {isChecked ? 'checked' : ''}"
          >
            <span class="extra-icon">{addonIcons[addon.slug] || addon.icon || '📦'}</span>
            <div class="extra-body">
              <strong>{addon.name}</strong>
              <p>{addon.tagline}</p>
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
    </section>
  {/if}
</div>

<div class="float-nav">
  <a href="/apps" class="float-back">&larr; Apps</a>
  <a href="/review" class="float-cta">Review Order &rarr;</a>
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
  .extra-price { color: var(--color-text-strong); font-weight: 600; font-size: 0.82rem; white-space: nowrap; flex-shrink: 0; }
  .extra-check { flex-shrink: 0; width: 20px; height: 20px; display: flex; align-items: center; justify-content: center; }
  .extra-check svg { width: 20px; height: 20px; background: var(--color-accent); color: #fff; border-radius: 4px; padding: 2px; }
  .extra-box { display: block; width: 18px; height: 18px; border: 1.5px solid var(--color-border); border-radius: 4px; }

  .bs-hint { color: var(--color-text-dim); font-size: 0.78rem; margin: 0 0 0.55rem; }
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
