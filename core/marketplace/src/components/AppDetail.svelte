<script lang="ts">
  import { getApps, type App } from '../lib/api';
  import { readCart, toggleApp } from '../lib/cart';

  interface Props {
    slug?: string;
  }

  let { slug: propSlug = '' }: Props = $props();
  let app = $state<App | null>(null);
  let relatedApps = $state<App[]>([]);
  let dependencyApps = $state<App[]>([]);
  let loading = $state(true);
  let cart = $state(readCart());

  const inCart = $derived(app ? cart.apps.includes(app.id) : false);
  const isService = $derived(app ? (app.system === true || app.kind === 'service') : false);
  const comingSoon = $derived(app ? (app.deployable === false && !isService) : false);

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
      loading = false;
    }).catch(() => { loading = false; });
  });

  function toggle() {
    if (!app) return;
    if (comingSoon) return;
    cart = toggleApp(app.id);
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
