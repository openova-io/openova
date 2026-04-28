<script lang="ts">
  import { readCart, cartItemCount, type CartState } from '../lib/cart';
  import { getMe, logout as apiLogout, AUTH_CHANGED_EVENT, type User } from '../lib/api';
  import { consoleHref } from '../lib/config';

  let { currentStep = 0 }: { currentStep?: number } = $props();

  let cart = $state<CartState>(readCart());
  let count = $derived(cartItemCount(cart));
  let user = $state<User | null>(null);
  let menuOpen = $state(false);
  let theme = $state<'light' | 'dark'>('dark');

  $effect(() => {
    if (typeof document === 'undefined') return;
    theme = (document.documentElement.getAttribute('data-theme') as 'light' | 'dark') || 'dark';
  });

  function toggleTheme() {
    const next = theme === 'dark' ? 'light' : 'dark';
    theme = next;
    document.documentElement.setAttribute('data-theme', next);
    try { localStorage.setItem('sme-theme', next); } catch {}
  }

  const steps = [
    { num: 1, label: 'Plan', href: '/plans' },
    { num: 2, label: 'Stack', href: '/apps' },
    { num: 3, label: 'Add-ons', href: '/addons' },
    { num: 4, label: 'Review', href: '/review' },
    { num: 5, label: 'Checkout', href: '/checkout' },
  ];

  $effect(() => {
    const handler = (e: Event) => {
      cart = (e as CustomEvent).detail ?? readCart();
    };
    window.addEventListener('cart-updated', handler);
    return () => window.removeEventListener('cart-updated', handler);
  });

  // Re-read auth state on mount AND whenever another component dispatches
  // `sme-auth-changed` (login, logout, token refresh, active-org switch).
  // `localStorage.setItem` does NOT fire the `storage` event in the same tab,
  // so a custom event is the only reliable cross-component signal — see #51.
  $effect(() => {
    if (typeof localStorage === 'undefined') return;
    const sync = () => {
      const token = localStorage.getItem('sme-token');
      if (!token) {
        user = null;
        return;
      }
      getMe()
        .then((u) => { user = u; })
        .catch(() => { user = null; });
    };
    sync();
    window.addEventListener(AUTH_CHANGED_EVENT, sync);
    // Cross-tab sign-out should also clear the profile widget.
    const onStorage = (e: StorageEvent) => {
      if (e.key === 'sme-token' || e.key === null) sync();
    };
    window.addEventListener('storage', onStorage);
    return () => {
      window.removeEventListener(AUTH_CHANGED_EVENT, sync);
      window.removeEventListener('storage', onStorage);
    };
  });

  $effect(() => {
    const onDocClick = (e: MouseEvent) => {
      if (!menuOpen) return;
      const target = e.target as HTMLElement | null;
      if (target?.closest('[data-profile-menu]')) return;
      menuOpen = false;
    };
    window.addEventListener('click', onDocClick);
    return () => window.removeEventListener('click', onDocClick);
  });

  async function handleLogout() {
    menuOpen = false;
    await apiLogout();
    user = null;
    window.location.href = '/';
  }

  function initials(u: User): string {
    if (u.name) return u.name.trim().split(/\s+/).map(p => p[0]).slice(0, 2).join('').toUpperCase();
    return (u.email[0] || '?').toUpperCase();
  }

  function portalHref(): string {
    const t = typeof localStorage !== 'undefined' ? localStorage.getItem('sme-token') : null;
    const r = typeof localStorage !== 'undefined' ? localStorage.getItem('sme-refresh-token') : null;
    if (!t) return consoleHref();
    const params: Record<string, string> = { token: t };
    if (r) params.refresh_token = r;
    return consoleHref('', params);
  }
</script>

<header class="sticky top-0 z-50 border-b border-[var(--color-border)] bg-[var(--color-bg)]/95 backdrop-blur-sm">
  <div class="mx-auto flex h-14 max-w-7xl items-center justify-between px-4">
    <!-- Logo -->
    <a href="/" class="flex items-center gap-2 text-[var(--color-text-strong)] no-underline">
      <svg viewBox="0 0 700 400" class="h-5 w-8" fill="none">
        <defs>
          <linearGradient id="logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
            <stop offset="0%" stop-color="#3B82F6"/>
            <stop offset="100%" stop-color="#818CF8"/>
          </linearGradient>
        </defs>
        <path d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
              fill="none" stroke="url(#logo-grad)" stroke-width="100" stroke-linecap="butt"/>
      </svg>
      <span class="text-sm font-semibold">OpenOva</span>
    </a>

    <!-- Wizard Steps -->
    <nav class="hidden items-center gap-1 md:flex">
      {#each steps as step}
        {@const isActive = step.num === currentStep}
        {@const isDone = step.num < currentStep}
        <a
          href={step.href}
          class="flex items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors no-underline
            {isActive ? 'bg-[var(--color-accent)] text-white' : isDone ? 'text-[var(--color-success)]' : 'text-[var(--color-text-dim)] hover:text-[var(--color-text)]'}"
        >
          <span class="flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-bold
            {isActive ? 'bg-white/20' : isDone ? 'bg-[var(--color-success)]/20' : 'bg-[var(--color-border)]'}">
            {#if isDone}
              <svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3">
                <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
              </svg>
            {:else}
              {step.num}
            {/if}
          </span>
          <span class="hidden lg:inline">{step.label}</span>
        </a>
        {#if step.num < steps.length}
          <span class="text-[var(--color-border-strong)]">·</span>
        {/if}
      {/each}
    </nav>

    <!-- Right: Theme toggle + Cart + Profile -->
    <div class="flex items-center gap-3">
      <button
        type="button"
        onclick={toggleTheme}
        aria-label="Toggle light / dark theme"
        title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
        class="rounded-lg p-2 text-[var(--color-text-dim)] transition-colors hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
      >
        {#if theme === 'dark'}
          <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <circle cx="12" cy="12" r="4"/>
            <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/>
          </svg>
        {:else}
          <svg class="h-5 w-5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/>
          </svg>
        {/if}
      </button>
      <a href="/review" class="relative rounded-lg p-2 text-[var(--color-text-dim)] transition-colors hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)] no-underline">
        <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
          <path stroke-linecap="round" stroke-linejoin="round" d="M2.25 3h1.386c.51 0 .955.343 1.087.835l.383 1.437M7.5 14.25a3 3 0 00-3 3h15.75m-12.75-3h11.218c1.121-2.3 2.1-4.684 2.924-7.138a60.114 60.114 0 00-16.536-1.84M7.5 14.25L5.106 5.272M6 20.25a.75.75 0 11-1.5 0 .75.75 0 011.5 0zm12.75 0a.75.75 0 11-1.5 0 .75.75 0 011.5 0z"/>
        </svg>
        {#if count > 0}
          <span class="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--color-accent)] px-1 text-[10px] font-bold text-white">
            {count}
          </span>
        {/if}
      </a>

      {#if user}
        <div class="relative" data-profile-menu>
          <button
            type="button"
            onclick={(e) => { e.stopPropagation(); menuOpen = !menuOpen; }}
            aria-label="My account"
            class="flex items-center gap-2 rounded-lg p-1.5 pl-2 text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)]"
          >
            <span class="hidden text-xs font-medium sm:inline">{user.name || user.email.split('@')[0]}</span>
            <span class="flex h-7 w-7 items-center justify-center rounded-full bg-[var(--color-accent)] text-[11px] font-bold text-white">
              {initials(user)}
            </span>
          </button>
          {#if menuOpen}
            <div class="absolute right-0 top-11 w-64 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] py-2 shadow-xl" role="menu">
              <div class="border-b border-[var(--color-border)] px-4 pb-3 pt-1">
                <p class="truncate text-sm font-semibold text-[var(--color-text-strong)]">{user.name || 'My Account'}</p>
                <p class="truncate text-xs text-[var(--color-text-dim)]">{user.email}</p>
              </div>
              <a href={portalHref()} class="flex items-center gap-2 px-4 py-2 text-sm text-[var(--color-text)] no-underline hover:bg-[var(--color-surface-hover)]" role="menuitem">
                <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M3.75 6A2.25 2.25 0 016 3.75h2.25A2.25 2.25 0 0110.5 6v2.25a2.25 2.25 0 01-2.25 2.25H6a2.25 2.25 0 01-2.25-2.25V6zM3.75 15.75A2.25 2.25 0 016 13.5h2.25a2.25 2.25 0 012.25 2.25V18a2.25 2.25 0 01-2.25 2.25H6A2.25 2.25 0 013.75 18v-2.25zM13.5 6a2.25 2.25 0 012.25-2.25H18A2.25 2.25 0 0120.25 6v2.25A2.25 2.25 0 0118 10.5h-2.25a2.25 2.25 0 01-2.25-2.25V6zM13.5 15.75a2.25 2.25 0 012.25-2.25H18a2.25 2.25 0 012.25 2.25V18A2.25 2.25 0 0118 20.25h-2.25A2.25 2.25 0 0113.5 18v-2.25z"/></svg>
                Console (my tenants)
              </a>
              <a href="/checkout" class="flex items-center gap-2 px-4 py-2 text-sm text-[var(--color-text)] no-underline hover:bg-[var(--color-surface-hover)]" role="menuitem">
                <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z"/></svg>
                Continue checkout
              </a>
              <button
                type="button"
                onclick={handleLogout}
                class="flex w-full items-center gap-2 px-4 py-2 text-left text-sm text-[var(--color-danger)] hover:bg-[var(--color-surface-hover)]"
                role="menuitem"
              >
                <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M15.75 9V5.25A2.25 2.25 0 0013.5 3h-6a2.25 2.25 0 00-2.25 2.25v13.5A2.25 2.25 0 007.5 21h6a2.25 2.25 0 002.25-2.25V15m3 0l3-3m0 0l-3-3m3 3H9"/></svg>
                Sign out
              </button>
            </div>
          {/if}
        </div>
      {:else}
        <a href="/checkout" class="flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs font-medium text-[var(--color-text)] transition-colors hover:bg-[var(--color-surface-hover)] no-underline">
          <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
            <path stroke-linecap="round" stroke-linejoin="round" d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z"/>
          </svg>
          Sign in
        </a>
      {/if}
    </div>
  </div>
</header>
