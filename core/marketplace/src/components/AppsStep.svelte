<script lang="ts">
  import { getApps, getIndustries, type App, type Industry } from '../lib/api';
  import { readCart, toggleApp, writeCart } from '../lib/cart';

  let apps = $state<App[]>([]);
  let allApps = $state<App[]>([]); // includes system apps, for dependency name lookup
  let industries = $state<Industry[]>([]);
  let cart = $state(readCart());
  let loading = $state(true);
  let query = $state('');
  let selectedIndustry = $state<string | null>(null);
  let activeCategory = $state<string | null>(null);
  let toasts = $state<Array<{ id: number; name: string; added: boolean }>>([]);
  let toastId = 0;

  const categories = $derived([...new Set(apps.map(a => a.category))].sort());

  // Sort: selected apps float to top, then alphabetical
  const sorted = $derived.by(() => {
    let result = [...apps];
    if (activeCategory) {
      result = result.filter(a => a.category === activeCategory);
    }
    if (query) {
      const q = query.toLowerCase();
      result = result.filter(a => a.name.toLowerCase().includes(q) || a.tagline.toLowerCase().includes(q) || a.category.toLowerCase().includes(q) || a.description.toLowerCase().includes(q));
    }
    result.sort((a, b) => {
      const aIn = cart.apps.includes(a.id) ? 0 : 1;
      const bIn = cart.apps.includes(b.id) ? 0 : 1;
      if (aIn !== bIn) return aIn - bIn;
      return a.name.localeCompare(b.name);
    });
    return result;
  });

  $effect(() => {
    Promise.all([getApps(), getIndustries()])
      .then(([a, i]) => {
        allApps = a;
        // Show every catalog entry — backing services (postgres/mysql/redis)
        // included — in the same grid as business apps. They get a 'SERVICE'
        // pill + muted styling so users can tell them apart at a glance, and
        // the toggle is disabled so they can't be chosen directly (they
        // always come bundled with the app that needs them). #106 / #112.
        apps = a;
        industries = i;
        loading = false;
      })
      .catch(() => {
        apps = fallbackApps;
        industries = fallbackIndustries;
        loading = false;
      });
  });

  function toggle(e: Event, appId: string) {
    e.preventDefault();
    e.stopPropagation();
    const app = apps.find(a => a.id === appId);
    // Non-deployable apps carry a 'Coming soon' overlay and the button is
    // disabled, but guard here too so keyboard-activation can't bypass it.
    if (app?.deployable === false) return;
    // Backing services (postgres / mysql / redis) are never selected
    // directly — they come bundled with whichever app needs them. Ignore
    // clicks on the toggle; user browses them for info only.
    if (app?.system === true || app?.kind === 'service') return;
    const wasIn = cart.apps.includes(appId);
    cart = toggleApp(appId);
    if (app) showToast(app.name, !wasIn);
  }

  function selectIndustry(id: string) {
    const ind = industries.find(i => i.id === id);
    if (!ind) return;
    selectedIndustry = id;
    const resolvedIds = ind.app_ids
      .map(slug => apps.find(a => a.slug === slug)?.id)
      .filter((v): v is string => !!v);
    cart.apps = resolvedIds;
    writeCart(cart);
    cart = readCart();
  }

  function isInCart(appId: string): boolean {
    return cart.apps.includes(appId);
  }

  function depName(slug: string): string {
    return allApps.find(x => x.slug === slug)?.name ?? slug;
  }

  function showToast(name: string, added: boolean) {
    const id = ++toastId;
    toasts = [...toasts, { id, name, added }];
    setTimeout(() => {
      toasts = toasts.filter(t => t.id !== id);
    }, 2500);
  }

  // Static fallback data
  const fallbackApps: App[] = [
    { id: '1', name: 'WordPress', slug: 'wordpress', tagline: 'Website & blog platform', description: 'Create blogs, websites, and online stores with the most widely used content management system.', category: 'cms', icon: 'W', color: '#21759b', logo: '', free: true, popular: true, features: ['Drag-and-drop editor', 'Thousands of themes', 'Plugin ecosystem'], website: 'https://wordpress.org', license: 'GPL-2.0' },
    { id: '2', name: 'Ghost', slug: 'ghost', tagline: 'Professional publishing', description: 'Modern publishing platform for blogs and newsletters with built-in memberships and subscriptions.', category: 'cms', icon: 'G', color: '#15171A', logo: '', free: true, features: ['Rich editor', 'Memberships', 'Newsletters'], website: 'https://ghost.org', license: 'MIT' },
    { id: '3', name: 'Nextcloud', slug: 'nextcloud', tagline: 'File sync & share', description: 'Store, share, and collaborate on files, calendars, and contacts from any device.', category: 'productivity', icon: 'N', color: '#0082c9', logo: '', free: true, popular: true, features: ['File sync across devices', 'Calendar & contacts', 'Document collaboration'], website: 'https://nextcloud.com', license: 'AGPL-3.0' },
    { id: '4', name: 'Twenty CRM', slug: 'twenty', tagline: 'Open-source CRM', description: 'Customer relationship management with a beautiful interface, pipeline views, and API-first design.', category: 'crm', icon: 'T', color: '#000000', logo: '', free: true, features: ['Contact management', 'Deal pipeline', 'Email integration'], website: 'https://twenty.com', license: 'AGPL-3.0' },
    { id: '5', name: 'Rocket.Chat', slug: 'rocketchat', tagline: 'Team messaging', description: 'Secure team communication with channels, direct messages, video calls, and integrations.', category: 'communication', icon: 'R', color: '#F5455C', logo: '', free: true, features: ['Channels & DMs', 'Video conferencing', 'File sharing'], website: 'https://rocket.chat', license: 'MIT' },
    { id: '6', name: 'Cal.com', slug: 'calcom', tagline: 'Scheduling & bookings', description: 'Scheduling platform for appointments, meetings, and events with calendar integrations.', category: 'scheduling', icon: 'C', color: '#292929', logo: '', free: true, features: ['Appointment scheduling', 'Calendar sync', 'Custom booking pages'], website: 'https://cal.com', license: 'AGPL-3.0' },
  ];

  const fallbackIndustries: Industry[] = [
    { id: '1', name: 'Restaurant & Hospitality', icon: '🍽️', app_ids: ['1', '6', '5'] },
    { id: '2', name: 'Retail & E-commerce', icon: '🛍️', app_ids: ['1', '4'] },
    { id: '3', name: 'Professional Services', icon: '💼', app_ids: ['4', '6', '5'] },
  ];
</script>

<div class="apps-page">
  <div class="apps-hero">
    <h1>Build your stack</h1>
    <p>Every app is <strong class="free-badge">FREE</strong> — pick as many as you need</p>
  </div>

  {#if loading}
    <div class="flex justify-center py-20">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else}
    <!-- Toolbar: Search + Industry + Category chips -->
    <div class="apps-toolbar">
      <div class="toolbar-row">
        <div class="search-wrap">
          <svg class="search-icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
          <input
            type="text"
            placeholder="Search {apps.length} apps..."
            bind:value={query}
            class="search-input"
          />
        </div>
        <div class="industry-wrap">
          <select
            onchange={(e) => selectIndustry((e.target as HTMLSelectElement).value)}
            class="industry-select"
          >
            <option value="">Industry template...</option>
            {#each industries as ind}
              <option value={ind.id}>{ind.icon} {ind.name}</option>
            {/each}
          </select>
        </div>
      </div>
      <div class="chips-row">
        <button
          onclick={() => activeCategory = null}
          class="cat-chip {activeCategory === null ? 'active' : ''}"
        >
          All
        </button>
        {#each categories as cat}
          <button
            onclick={() => activeCategory = activeCategory === cat ? null : cat}
            class="cat-chip {activeCategory === cat ? 'active' : ''}"
          >
            {cat}
          </button>
        {/each}
      </div>
    </div>

    <!-- Section head -->
    <div class="section-head">
      <h2>All apps</h2>
      <span class="count-label">{sorted.length} available · {cart.apps.length} selected</span>
    </div>

    <!-- App Grid — horizontal 2-column cards -->
    <div class="apps-grid">
      {#each sorted as app}
        {@const inCart = isInCart(app.id)}
        {@const isService = app.system === true || app.kind === 'service'}
        {@const comingSoon = app.deployable === false && !isService}
        <a
          href="/app?slug={app.slug}"
          class="app-card {inCart ? 'in-cart' : ''} {comingSoon ? 'coming-soon' : ''} {isService ? 'is-service' : ''}"
        >
          {#if app.logo}
            <img src={app.logo} alt={app.name} class="app-logo" loading="lazy" />
          {:else}
            <span class="app-icon" style="background: {app.color}">{app.icon}</span>
          {/if}
          <div class="app-body">
            <div class="app-top">
              <span class="app-name">{app.name}</span>
              <span class="app-cat">{app.category}</span>
            </div>
            <p class="app-desc">{app.description || app.tagline}</p>
            <div class="app-chips">
              <span class="chip chip-free">FREE</span>
              {#if isService}
                <span class="chip chip-service" title="Backing service — comes bundled with apps that need it">SERVICE</span>
              {/if}
              {#if comingSoon}
                <span class="chip chip-soon" title="Provisioning template not yet wired">COMING SOON</span>
              {/if}
              {#if app.dependencies && app.dependencies.length > 0}
                {#each app.dependencies as dep}
                  <span class="chip chip-dep" title="Bundled dependency">+ {depName(dep)}</span>
                {/each}
              {/if}
            </div>
          </div>

          <!-- Status chip: bottom-right (unified corner across views) -->
          {#if inCart}
            <div class="status-corner">
              <span class="status-chip s-selected">
                <span class="dot"></span> SELECTED
              </span>
            </div>
          {/if}

          <!-- Add/Remove button: top-right + circle (unified pattern) -->
          <button
            type="button"
            class="app-add-btn {inCart ? 'added' : ''}"
            onclick={(e) => toggle(e, app.id)}
            disabled={comingSoon}
            title={comingSoon ? 'Coming soon — provisioning template pending' : (inCart ? 'Remove from stack' : 'Add to stack')}
          >
            {#if inCart}
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/></svg>
            {:else}
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
            {/if}
          </button>
        </a>
      {/each}
    </div>
  {/if}
</div>

<div class="float-nav">
  <a href="/plans" class="float-back">&larr; Plans</a>
  <a
    href="/addons"
    class="float-cta {cart.apps.length > 0 ? '' : 'disabled'}"
  >
    Continue &rarr;
  </a>
</div>

<!-- Toast notifications -->
<div class="toast-container">
  {#each toasts as toast (toast.id)}
    <div class="toast-card show">
      {#if toast.added}
        <span class="toast-check">&#10003;</span> {toast.name} added <span class="toast-price">OMR 0</span>
      {:else}
        <span class="toast-x">&times;</span> {toast.name} removed
      {/if}
    </div>
  {/each}
</div>

<style>
  .apps-page { max-width: 1280px; margin: 0 auto; padding: 0 1.25rem 4.5rem; }

  .apps-hero { text-align: center; margin-bottom: 0.75rem; }
  .apps-hero h1 {
    font-size: clamp(1.2rem, 2.2vw, 1.5rem);
    color: var(--color-text-strong);
    margin: 0.25rem 0 0.2rem;
    font-weight: 700;
  }
  .apps-hero p {
    color: var(--color-text-dim);
    font-size: 0.85rem;
    margin: 0;
  }
  .free-badge { color: var(--color-success); font-weight: 700; }

  /* Toolbar */
  .apps-toolbar {
    background: color-mix(in srgb, var(--color-bg) 92%, transparent);
    backdrop-filter: blur(10px);
    border: 1px solid var(--color-border);
    border-radius: 12px;
    padding: 0.75rem 1rem;
    margin-bottom: 0.75rem;
  }
  .toolbar-row {
    display: flex;
    gap: 0.75rem;
    margin-bottom: 0.65rem;
  }
  .search-wrap {
    position: relative;
    flex: 2;
    min-width: 200px;
  }
  .search-icon {
    position: absolute;
    left: 12px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--color-text-dim);
    opacity: 0.5;
  }
  .search-input {
    width: 100%;
    padding: 0.6rem 0.85rem 0.6rem 2.2rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.88rem;
  }
  .search-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }

  .industry-wrap { flex: 1; min-width: 160px; }
  .industry-select {
    width: 100%;
    padding: 0.6rem 0.85rem;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    color: var(--color-text);
    font: inherit;
    font-size: 0.85rem;
    appearance: auto;
  }
  .industry-select:focus { outline: 2px solid var(--color-accent); border-color: transparent; }

  .chips-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
  }
  .cat-chip {
    padding: 0.4rem 0.7rem;
    border-radius: 999px;
    background: var(--color-surface);
    border: 1px solid var(--color-border);
    color: var(--color-text-dim);
    font: inherit;
    font-size: 0.78rem;
    cursor: pointer;
    transition: all 0.15s;
  }
  .cat-chip:hover { color: var(--color-text-strong); border-color: var(--color-text-dim); }
  .cat-chip.active {
    background: var(--color-accent);
    color: #fff;
    border-color: var(--color-accent);
  }

  /* Section head */
  .section-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.6rem;
    padding-bottom: 0.4rem;
    border-bottom: 1px solid var(--color-border);
  }
  .section-head h2 { color: var(--color-text-strong); font-size: 0.95rem; margin: 0; font-weight: 600; }
  .count-label { color: var(--color-text-dim); font-size: 0.78rem; }

  /* App grid — fixed 3 per row on desktop, 1 per row on mobile */
  .apps-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.65rem;
  }
  @media (max-width: 700px) { .apps-grid { grid-template-columns: 1fr; } }

  /* App card — horizontal layout, deterministic height, overflow clipped */
  .app-card {
    position: relative;
    background: var(--color-surface);
    border: 1.5px solid var(--color-border);
    border-radius: 12px;
    padding: 0.6rem;
    display: flex;
    align-items: stretch;
    gap: 0.75rem;
    cursor: pointer;
    transition: transform 0.15s, border-color 0.15s, background 0.15s;
    color: inherit;
    text-decoration: none;
    height: 108px;
    overflow: hidden;
  }
  .app-card:hover {
    transform: translateY(-2px);
    border-color: var(--color-accent);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  }
  .app-card.in-cart {
    border-color: var(--color-success);
    background: color-mix(in srgb, var(--color-success) 4%, var(--color-surface));
  }

  /* Column 1: Logo / icon — perfectly square, covers full card height */
  .app-logo {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    object-fit: cover;
    flex-shrink: 0;
  }
  .app-icon {
    align-self: stretch;
    aspect-ratio: 1 / 1;
    height: auto;
    border-radius: 10px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    color: #fff;
    font-size: 1.3rem;
    font-weight: 700;
  }

  /* Column 2: Rich description */
  .app-body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding-right: 4.5rem; /* room for bottom-right SELECTED chip + top-right +/− button */
    overflow: hidden;
  }
  .app-top {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
  }
  .app-name {
    color: var(--color-text-strong);
    font-size: 0.92rem;
    font-weight: 600;
    line-height: 1.2;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 1 1 auto;
    min-width: 0;
  }
  .app-cat {
    color: var(--color-text-dim);
    font-size: 0.68rem;
    text-transform: capitalize;
    background: color-mix(in srgb, var(--color-border) 50%, transparent);
    padding: 0.1rem 0.4rem;
    border-radius: 3px;
  }
  .app-desc {
    margin: 0;
    color: var(--color-text);
    font-size: 0.78rem;
    line-height: 1.45;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .app-chips {
    margin-top: 0.25rem;
    display: flex;
    flex-wrap: nowrap;
    gap: 0.25rem;
    overflow: hidden;
    mask-image: linear-gradient(to right, #000 85%, transparent);
    -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
    min-height: 1.4rem;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    line-height: 1.4;
    white-space: nowrap;
  }
  .chip-free {
    background: color-mix(in srgb, var(--color-success) 14%, transparent);
    color: var(--color-success);
  }
  .chip-dep {
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent);
    font-weight: 500;
  }
  .chip-soon {
    background: color-mix(in srgb, var(--color-warning, #f59e0b) 14%, transparent);
    color: var(--color-warning, #f59e0b);
    font-weight: 600;
  }
  .chip-service {
    background: color-mix(in srgb, var(--color-accent) 14%, transparent);
    color: var(--color-accent);
    font-weight: 600;
    letter-spacing: 0.04em;
  }
  .app-card.coming-soon {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .app-card.coming-soon .app-add-btn,
  .app-card.is-service .app-add-btn {
    display: none;
  }
  /* Backing-service cards: same dimensions as business apps, subtly muted
     so users see them as infra, not selectable. Card still click-through to
     the detail page for connection info. */
  .app-card.is-service {
    border-style: dashed;
    opacity: 0.88;
  }
  .app-card.is-service:hover {
    opacity: 1;
  }
  /* Status chip — pinned bottom-right (unified corner across views) */
  .status-corner {
    position: absolute;
    bottom: 0.5rem;
    right: 0.55rem;
    pointer-events: none;
  }
  .status-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.15rem 0.55rem;
    border-radius: 999px;
    font-size: 0.65rem;
    font-weight: 600;
    line-height: 1.4;
    letter-spacing: 0.03em;
  }
  .status-chip .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; display: inline-block; }
  .s-selected {
    background: color-mix(in srgb, var(--color-success) 16%, transparent);
    color: var(--color-success);
  }

  /* Floating add/remove button — appears on hover */
  .app-add-btn {
    position: absolute;
    top: 0.6rem;
    right: 0.6rem;
    width: 32px; height: 32px;
    border-radius: 50%;
    border: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    opacity: 0;
    transform: scale(0.8);
    transition: opacity 0.15s, transform 0.15s, background 0.15s;
    background: var(--color-accent);
    color: #fff;
    z-index: 2;
  }
  .app-add-btn svg { width: 16px; height: 16px; }
  .app-card:hover .app-add-btn {
    opacity: 1;
    transform: scale(1);
  }
  .app-add-btn.added {
    background: var(--color-success);
    opacity: 1;
    transform: scale(1);
  }
  .app-add-btn:hover {
    filter: brightness(0.85);
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
  .float-cta.disabled {
    background: var(--color-border);
    color: var(--color-text-dim);
    cursor: not-allowed;
    pointer-events: none;
    box-shadow: none;
  }

  /* Toast — top-right below cart icon */
  .toast-container {
    position: fixed;
    top: 4rem;
    right: 1.25rem;
    z-index: 200;
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    pointer-events: none;
  }
  .toast-card {
    background: var(--color-surface);
    border: 1px solid var(--color-success);
    border-radius: 8px;
    padding: 0.5rem 0.85rem;
    font-size: 0.82rem;
    color: var(--color-text-strong);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
    display: flex;
    align-items: center;
    gap: 0.4rem;
    white-space: nowrap;
    animation: toast-in 0.25s ease-out;
  }
  .toast-check { color: var(--color-success); font-weight: 700; }
  .toast-x { color: var(--color-text-dim); }
  .toast-price { color: var(--color-success); font-weight: 600; font-size: 0.75rem; }

  @keyframes toast-in {
    from { transform: translateY(-16px); opacity: 0; }
    to { transform: translateY(0); opacity: 1; }
  }
</style>
