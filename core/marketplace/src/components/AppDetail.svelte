<script lang="ts">
  import { getApps, type App, type ConfigField } from '../lib/api';
  import { readCart, toggleApp, toggleAgent, setAppConfig, SANDBOX_AGENTS } from '../lib/cart';

  interface Props {
    slug?: string;
  }

  let { slug: propSlug = '' }: Props = $props();
  let app = $state<App | null>(null);
  let relatedApps = $state<App[]>([]);
  let dependencyApps = $state<App[]>([]);
  let loading = $state(true);
  let cart = $state(readCart());
  // TBD-V18 (#2026) — local form state for the per-instance tunables
  // declared on app.configSchema. Initialised from each field's
  // `default` so the rendered form is always populated for the
  // canonical Postgres-backed bundle (replicas=1, disk_gb=5,
  // backups_enabled=false). TBD-V18-D follow-up to PR #2038: every
  // mutation now also persists to cart.appConfigs[app.slug] via
  // setAppConfig(), so CheckoutStep can thread the values into the
  // install POST body (createTenant /api/tenant/orgs `app_configs`).
  let configValues = $state<Record<string, number | string | boolean>>({});

  const inCart = $derived(app ? cart.apps.includes(app.id) : false);
  const isService = $derived(app ? (app.system === true || app.kind === 'service') : false);
  const comingSoon = $derived(app ? (app.deployable === false && !isService) : false);
  // Schema fields render below the Description / Features sections so
  // operators get the configuration surface immediately after the
  // marketing context. Empty/missing schema = section is skipped (Postgres
  // is a System app that ships ConfigSchema; per-Pillar-1-step-2 the
  // bundle UI surfaces these tunables to the customer).
  const configSchemaFields = $derived<ConfigField[]>(app?.configSchema ?? []);
  const hasConfigSchema = $derived(configSchemaFields.length > 0);
  // Sandbox product — render the 6-agent pre-select grid below the
  // features section. Cards reuse the .related-card chrome verbatim
  // (design-system inheritance rule from Wave 4 brief: no bespoke
  // components). Picks land on cart.agents via toggleAgent().
  const isSandbox = $derived(app?.slug === 'sandbox');

  $effect(() => {
    // Read slug from URL query param (static site can't use dynamic route params)
    const urlSlug = propSlug || new URLSearchParams(window.location.search).get('slug') || '';
    if (!urlSlug) { loading = false; return; }

    getApps().then((apps) => {
      app = apps.find(a => a.slug === urlSlug) ?? null;
      relatedApps = apps
        .filter(a => !a.system && a.category === app?.category && a.slug !== urlSlug)
        .slice(0, 4);
      const depSlugs = app?.dependencies ?? [];
      dependencyApps = depSlugs
        .map(slug => apps.find(a => a.slug === slug))
        .filter((a): a is App => !!a);
      // Seed configValues from per-field defaults. Falls back to a
      // type-appropriate zero when `default` is missing so the form
      // always has a coherent initial state. TBD-V18-D: when the
      // operator already visited this AppDetail in the current cart
      // session (e.g. navigated forward to /addons then back), prefer
      // their previously-saved values from cart.appConfigs[slug] so
      // we don't blow away their edits on every mount.
      const fields = app?.configSchema ?? [];
      const seeded: Record<string, number | string | boolean> = {};
      const previouslySaved = (cart.appConfigs ?? {})[app?.slug ?? ''] ?? {};
      for (const f of fields) {
        if (Object.prototype.hasOwnProperty.call(previouslySaved, f.key)) {
          seeded[f.key] = previouslySaved[f.key];
        } else if (f.default !== undefined && f.default !== null) {
          seeded[f.key] = f.default;
        } else {
          seeded[f.key] = f.type === 'int' ? 0 : f.type === 'bool' ? false : '';
        }
      }
      configValues = seeded;
      // Persist the freshly-seeded values back so the cart has a
      // coherent snapshot from the moment the AppDetail mounts, even
      // when the customer never mutates a field (silent acceptance of
      // defaults still needs to thread through the install POST).
      if (app?.slug && fields.length > 0) {
        cart = setAppConfig(app.slug, seeded);
      }
      loading = false;
    }).catch(() => { loading = false; });
  });

  // Cast helpers — Svelte 5 + TS doesn't narrow $state<Record<string,...>>
  // values to a single primitive when bound to <input>, so these helpers
  // keep the binding strictly typed.
  function numValue(key: string): number {
    const v = configValues[key];
    return typeof v === 'number' ? v : Number(v) || 0;
  }
  function strValue(key: string): string {
    const v = configValues[key];
    return typeof v === 'string' ? v : v == null ? '' : String(v);
  }
  function boolValue(key: string): boolean {
    return configValues[key] === true;
  }
  function setValue(key: string, v: number | string | boolean): void {
    configValues = { ...configValues, [key]: v };
    // TBD-V18-D — persist on every change so the cart matches the
    // on-screen form when the customer leaves AppDetail (no submit
    // button on this surface: the cart IS the buffer). Guarded on
    // `app?.slug` so we never write a stub `undefined` key when the
    // detail page is still loading.
    if (app?.slug) {
      cart = setAppConfig(app.slug, configValues);
    }
  }

  function toggle() {
    if (!app) return;
    if (comingSoon) return;
    cart = toggleApp(app.id);
  }

  function pickAgent(slug: string) {
    cart = toggleAgent(slug);
  }

  function agentPicked(slug: string): boolean {
    return cart.agents.includes(slug);
  }
</script>

<div class="detail-page">
  {#if loading}
    <div class="flex justify-center py-20">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else if !app}
    <div class="not-found">
      <h1>App not found</h1>
      <a href="/apps">Back to apps</a>
    </div>
  {:else}
    <!-- Hero -->
    <div class="detail-hero">
      {#if app.logo}
        <img src={app.logo} alt={app.name} class="detail-logo" />
      {:else}
        <span class="detail-icon" style="background: {app.color}">{app.icon}</span>
      {/if}
      <div class="detail-hero-body">
        <h1>{app.name}</h1>
        <p class="detail-tagline">{app.tagline}</p>
        <div class="detail-meta">
          <span class="detail-cat">{app.category}</span>
          <span class="detail-free">FREE</span>
          {#if app.license}
            <span class="detail-license">{app.license}</span>
          {/if}
          {#if comingSoon}
            <span class="detail-soon" title="Provisioning template not yet wired">COMING SOON</span>
          {/if}
        </div>
      </div>
      <button
        onclick={toggle}
        class="detail-add {inCart ? 'added' : ''}"
        disabled={comingSoon}
        title={comingSoon ? 'Coming soon — provisioning template pending' : (inCart ? 'Remove from stack' : 'Add to stack')}
      >
        {comingSoon ? 'Coming soon' : (inCart ? 'Remove from stack' : 'Add to stack')}
      </button>
    </div>

    <!-- Description -->
    <section class="detail-section">
      <h2>About</h2>
      <p class="detail-desc">{app.description}</p>
    </section>

    <!-- Features -->
    {#if app.features && app.features.length > 0}
      <section class="detail-section">
        <h2>Features</h2>
        <ul class="detail-features">
          {#each app.features as feat}
            <li>{feat}</li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- Configuration schema — TBD-V18 (#2026). Renders per-instance
         tunables declared by the catalog (replicas/disk/backup for a
         Postgres-backed bundle, replicas/persistence for Redis, etc.).
         Unblocks Pillar 1 step 2 of the deterministic CLAUDE.md §0
         walk ("Click the canonical Postgres-backed bundle → app card
         opens; configSchema renders"). One input widget per
         ConfigField.type — matches the Go store.ConfigField contract
         exactly. TBD-V18-D follow-up to PR #2038: every mutation is
         persisted to cart.appConfigs[slug] so CheckoutStep can
         thread the values into the install POST (createTenant
         /api/tenant/orgs `app_configs`). The downstream HelmRelease-
         values binding is gated on TBD-V26 (#2040) Path A/B; this
         file ships the SHAPE end-to-end. -->
    {#if hasConfigSchema}
      <section class="detail-section" data-testid="config-schema-section">
        <h2>Configuration</h2>
        <p class="detail-dependencies-hint">Tune the per-instance defaults. You can change these any time from the app's admin tab after install.</p>
        <div class="config-grid" role="group" aria-label="App configuration">
          {#each configSchemaFields as field}
            <div class="config-field" data-config-key={field.key} data-config-type={field.type}>
              <label for={`cfg-${field.key}`}>
                <span class="config-label">{field.label}</span>
                {#if field.advanced}
                  <span class="config-badge">advanced</span>
                {/if}
              </label>
              {#if field.type === 'int'}
                <input
                  id={`cfg-${field.key}`}
                  class="config-input"
                  type="number"
                  min={field.min ?? undefined}
                  max={field.max ?? undefined}
                  value={numValue(field.key)}
                  oninput={(e) => setValue(field.key, Number((e.currentTarget as HTMLInputElement).value))}
                />
              {:else if field.type === 'bool'}
                <label class="config-toggle">
                  <input
                    id={`cfg-${field.key}`}
                    type="checkbox"
                    checked={boolValue(field.key)}
                    oninput={(e) => setValue(field.key, (e.currentTarget as HTMLInputElement).checked)}
                  />
                  <span class="config-toggle-text">{boolValue(field.key) ? 'Enabled' : 'Disabled'}</span>
                </label>
              {:else if field.type === 'enum' && field.options}
                <select
                  id={`cfg-${field.key}`}
                  class="config-input"
                  value={strValue(field.key)}
                  onchange={(e) => setValue(field.key, (e.currentTarget as HTMLSelectElement).value)}
                >
                  {#each field.options as opt}
                    <option value={opt}>{opt}</option>
                  {/each}
                </select>
              {:else}
                <input
                  id={`cfg-${field.key}`}
                  class="config-input"
                  type="text"
                  value={strValue(field.key)}
                  oninput={(e) => setValue(field.key, (e.currentTarget as HTMLInputElement).value)}
                />
              {/if}
              {#if field.description}
                <p class="config-desc">{field.description}</p>
              {/if}
            </div>
          {/each}
        </div>
      </section>
    {/if}

    <!-- Sandbox: pre-select agents (Wave 4). Reuses .related-card chrome
         so we don't add a bespoke component. The 6 entries match the
         Sandbox CRD enum (products/catalyst/chart/crds/sandbox.yaml ::
         spec.agentCatalogue). Picks land on cart.agents and travel
         through checkout into the tenant create-org payload. -->
    {#if isSandbox}
      <section class="detail-section">
        <h2>Pick your agents</h2>
        <p class="detail-dependencies-hint">Choose the coding agents your Sandbox should spawn. You can change this any time from the Sandbox admin tab.</p>
        <div class="related-grid">
          {#each SANDBOX_AGENTS as ag}
            {@const picked = agentPicked(ag.slug)}
            <button
              type="button"
              class="related-card agent-card {picked ? 'picked' : ''}"
              onclick={() => pickAgent(ag.slug)}
              aria-pressed={picked}
            >
              <span class="related-icon" style="background: var(--color-accent)" aria-hidden="true">
                {#if picked}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" width="18" height="18"><path d="M20 6L9 17l-5-5"/></svg>
                {:else}
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" width="18" height="18"><path d="M12 5v14M5 12h14"/></svg>
                {/if}
              </span>
              <div>
                <strong>{ag.name}</strong>
                <p>{ag.tagline}</p>
              </div>
            </button>
          {/each}
        </div>
      </section>
    {/if}

    <!-- Dependencies -->
    {#if app.dependencies && app.dependencies.length > 0}
      <section class="detail-section">
        <h2>Bundled dependencies</h2>
        <p class="detail-dependencies-hint">Auto-installed inside your tenant — no setup required:</p>
        <ul class="detail-dependencies">
          {#each app.dependencies as dep}
            {@const depApp = dependencyApps.find(d => d.slug === dep)}
            <li>{depApp ? depApp.name : dep}</li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- Related -->
    {#if relatedApps.length > 0}
      <section class="detail-section">
        <h2>Related apps</h2>
        <div class="related-grid">
          {#each relatedApps as ra}
            <a href="/app?slug={ra.slug}" class="related-card">
              {#if ra.logo}
                <img src={ra.logo} alt={ra.name} class="related-logo" />
              {:else}
                <span class="related-icon" style="background: {ra.color}">{ra.icon}</span>
              {/if}
              <div>
                <strong>{ra.name}</strong>
                <p>{ra.tagline}</p>
              </div>
            </a>
          {/each}
        </div>
      </section>
    {/if}
  {/if}
</div>

<div class="float-nav">
  <a href="/apps" class="float-back">&larr; All apps</a>
  <a href="/addons" class="float-cta">Continue &rarr;</a>
</div>

<style>
  .detail-page { max-width: 800px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }

  .not-found { text-align: center; padding: 4rem 0; color: var(--color-text-dim); }
  .not-found h1 { color: var(--color-text-strong); font-size: 1.5rem; margin-bottom: 1rem; }
  .not-found a { color: var(--color-accent); text-decoration: none; }

  /* Hero */
  .detail-hero {
    display: flex;
    align-items: flex-start;
    gap: 1.2rem;
    padding: 1.5rem 0;
    border-bottom: 1px solid var(--color-border);
  }
  .detail-logo {
    width: 80px; height: 80px;
    border-radius: 18px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .detail-icon {
    width: 80px; height: 80px;
    border-radius: 18px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    color: #fff;
    font-size: 1.8rem;
    font-weight: 700;
  }
  .detail-hero-body { flex: 1; }
  .detail-hero-body h1 {
    margin: 0;
    color: var(--color-text-strong);
    font-size: 1.5rem;
    font-weight: 700;
  }
  .detail-tagline {
    margin: 0.25rem 0 0.5rem;
    color: var(--color-text-dim);
    font-size: 0.9rem;
  }
  .detail-meta {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .detail-meta span {
    padding: 0.2rem 0.55rem;
    border-radius: 4px;
    font-size: 0.72rem;
    font-weight: 600;
  }
  .detail-cat {
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent);
    text-transform: capitalize;
  }
  .detail-free {
    background: color-mix(in srgb, var(--color-success) 12%, transparent);
    color: var(--color-success);
  }
  .detail-license {
    background: color-mix(in srgb, var(--color-text-dim) 12%, transparent);
    color: var(--color-text-dim);
  }
  .detail-soon {
    background: color-mix(in srgb, var(--color-warning, #f59e0b) 14%, transparent);
    color: var(--color-warning, #f59e0b);
    font-weight: 600;
  }
  .detail-add {
    padding: 0.65rem 1.5rem;
    border-radius: 8px;
    border: none;
    font-weight: 600;
    font-size: 0.88rem;
    cursor: pointer;
    white-space: nowrap;
    flex-shrink: 0;
    background: var(--color-accent);
    color: #fff;
    transition: all 0.15s;
  }
  .detail-add:hover { filter: brightness(0.9); }
  .detail-add.added {
    background: transparent;
    color: var(--color-text-dim);
    border: 1.5px solid var(--color-border);
  }
  .detail-add.added:hover { border-color: #EF4444; color: #EF4444; }
  .detail-add:disabled {
    background: color-mix(in srgb, var(--color-text-dim) 18%, transparent);
    color: var(--color-text-dim);
    cursor: not-allowed;
    filter: none;
  }
  .detail-add:disabled:hover { filter: none; }

  /* Sections */
  .detail-section {
    padding: 1.25rem 0;
    border-bottom: 1px solid var(--color-border);
  }
  .detail-section:last-of-type { border-bottom: none; }
  .detail-section h2 {
    margin: 0 0 0.6rem;
    font-size: 1rem;
    font-weight: 600;
    color: var(--color-text-strong);
  }
  .detail-desc {
    color: var(--color-text);
    font-size: 0.9rem;
    line-height: 1.7;
    margin: 0;
  }
  .detail-dependencies-hint {
    font-size: 0.85rem;
    color: var(--color-text-dim);
    margin-bottom: 0.5rem;
  }
  .detail-dependencies {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    list-style: none;
    padding: 0;
  }
  .detail-dependencies li {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 0.5rem;
    padding: 0.25rem 0.75rem;
    font-size: 0.8rem;
    color: var(--color-text);
    font-family: var(--font-mono);
  }
  .detail-features {
    list-style: none;
    padding: 0;
    margin: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 0.5rem;
  }
  .detail-features li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    color: var(--color-text);
    font-size: 0.85rem;
  }
  .detail-features li::before {
    content: '';
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--color-success);
    flex-shrink: 0;
  }

  /* Configuration schema — TBD-V18 (#2026). Reuses existing tokens
     (--color-surface, --color-border, --color-accent, --color-text*).
     Two-column responsive grid mirrors .detail-features so this
     surface inherits the marketplace's existing card aesthetic. */
  .config-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 0.85rem;
  }
  .config-field {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .config-field label {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    color: var(--color-text-strong);
    font-size: 0.82rem;
    font-weight: 600;
  }
  .config-label { color: var(--color-text-strong); }
  .config-badge {
    background: color-mix(in srgb, var(--color-text-dim) 12%, transparent);
    color: var(--color-text-dim);
    border-radius: 4px;
    padding: 0.1rem 0.4rem;
    font-size: 0.66rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .config-input {
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 6px;
    color: var(--color-text);
    font-size: 0.85rem;
    font-family: inherit;
    padding: 0.4rem 0.55rem;
    width: 100%;
  }
  .config-input:focus {
    outline: none;
    border-color: var(--color-accent);
  }
  .config-toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-weight: 500;
    color: var(--color-text);
    font-size: 0.85rem;
  }
  .config-toggle input[type="checkbox"] {
    width: 16px;
    height: 16px;
    accent-color: var(--color-accent);
  }
  .config-toggle-text { color: var(--color-text); }
  .config-desc {
    margin: 0;
    color: var(--color-text-dim);
    font-size: 0.74rem;
    line-height: 1.45;
  }

  /* Related */
  .related-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 0.55rem;
  }
  .related-card {
    display: flex;
    align-items: center;
    gap: 0.65rem;
    padding: 0.65rem 0.75rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    text-decoration: none;
    transition: border-color 0.15s;
  }
  .related-card:hover { border-color: var(--color-accent); }
  .related-logo {
    width: 36px; height: 36px;
    border-radius: 9px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .related-icon {
    width: 36px; height: 36px;
    border-radius: 9px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: #fff;
    font-size: 0.85rem;
    font-weight: 700;
    flex-shrink: 0;
  }
  .related-card strong {
    display: block;
    color: var(--color-text-strong);
    font-size: 0.82rem;
  }
  .related-card p {
    margin: 0.1rem 0 0;
    color: var(--color-text-dim);
    font-size: 0.72rem;
  }

  /* Agent picker — reuses .related-card chrome; the only delta is a
     pressed state (mirror of .app-card.in-cart from AppsStep). No new
     tokens — color-success is the existing "selected" channel. */
  .agent-card {
    border: 1px solid var(--color-border);
    background: var(--color-surface);
    cursor: pointer;
    font: inherit;
    text-align: left;
    width: 100%;
  }
  .agent-card:hover {
    border-color: var(--color-accent);
  }
  .agent-card.picked {
    border-color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 4%, var(--color-surface));
  }
  .agent-card.picked .related-icon {
    background: var(--color-success) !important;
    color: #fff;
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
