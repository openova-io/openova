<script lang="ts">
  import { getMe, logout, logoutAll, AUTH_CHANGED_EVENT, type User } from '../lib/api';
  import { path } from '../lib/config';
  import type { Snippet } from 'svelte';

  let { activePage = 'dashboard', children }: { activePage?: string; children: Snippet<[User]> } = $props();

  let user = $state<User | null>(null);
  let loading = $state(true);
  let error = $state('');
  let signingOutAll = $state(false);

  // #115 — full admin nav for superadmin. Sovereign-admin only sees the
  // Billing item (where they issue/manage vouchers); other items would 403
  // from the backend's requireAdmin guard. Filtered at render time below
  // based on the loaded `user.role`.
  const fullNav = [
    { id: 'dashboard', label: 'Revenue', superadminOnly: true, href: path(''), icon: 'M3 13.125C3 12.504 3.504 12 4.125 12h2.25c.621 0 1.125.504 1.125 1.125v6.75C7.5 20.496 6.996 21 6.375 21h-2.25A1.125 1.125 0 013 19.875v-6.75zM9.75 8.625c0-.621.504-1.125 1.125-1.125h2.25c.621 0 1.125.504 1.125 1.125v11.25c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V8.625zM16.5 4.125c0-.621.504-1.125 1.125-1.125h2.25C20.496 3 21 3.504 21 4.125v15.75c0 .621-.504 1.125-1.125 1.125h-2.25a1.125 1.125 0 01-1.125-1.125V4.125z' },
    { id: 'catalog', label: 'Catalog', superadminOnly: true, href: path('catalog'), icon: 'M20.25 7.5l-.625 10.632a2.25 2.25 0 01-2.247 2.118H6.622a2.25 2.25 0 01-2.247-2.118L3.75 7.5M10 11.25h4M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125z' },
    { id: 'tenants', label: 'Tenants', superadminOnly: true, href: path('tenants'), icon: 'M2.25 21h19.5M3.75 3v18m0-18h16.5m-16.5 0L12 3m8.25 0v18m0-18L12 3m0 0v18' },
    { id: 'orders', label: 'Orders', superadminOnly: true, href: path('orders'), icon: 'M9 12h3.75M9 15h3.75M9 18h3.75m3 .75H18a2.25 2.25 0 002.25-2.25V6.108c0-1.135-.845-2.098-1.976-2.192a48.424 48.424 0 00-1.123-.08m-5.801 0c-.065.21-.1.433-.1.664 0 .414.336.75.75.75h4.5a.75.75 0 00.75-.75 2.25 2.25 0 00-.1-.664m-5.8 0A2.251 2.251 0 0113.5 2.25H15c1.012 0 1.867.668 2.15 1.586m-5.8 0c-.376.023-.75.05-1.124.08C9.095 4.01 8.25 4.973 8.25 6.108V8.25m0 0H4.875c-.621 0-1.125.504-1.125 1.125v11.25c0 .621.504 1.125 1.125 1.125h9.75c.621 0 1.125-.504 1.125-1.125V9.375c0-.621-.504-1.125-1.125-1.125H8.25z' },
    { id: 'billing', label: 'Billing', superadminOnly: false, href: path('billing'), icon: 'M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 002.25-2.25V6.75A2.25 2.25 0 0019.5 4.5h-15a2.25 2.25 0 00-2.25 2.25v10.5A2.25 2.25 0 004.5 19.5z' },
  ];

  // Derived nav: superadmin sees everything; sovereign-admin only sees
  // items where superadminOnly === false.
  const nav = $derived(
    user?.role === 'sovereign-admin'
      ? fullNav.filter(n => !n.superadminOnly)
      : fullNav,
  );

  // Hydrate user on mount, and re-hydrate whenever another island dispatches
  // `sme-admin-auth-changed` (login, logout, token refresh, logout-all in
  // another tab via `storage`). Without this the header avatar / email go
  // stale until the next full reload — matches the marketplace pattern
  // from #51 (#83).
  $effect(() => {
    const sync = () => {
      const token = localStorage.getItem('sme-admin-token');
      if (!token) {
        user = null;
        loading = false;
        window.location.href = path('login');
        return;
      }
      getMe()
        .then(u => {
          // #115 — sovereign-admin can sign in to issue vouchers on a
          // franchised Sovereign. Other admin sections (Stripe settings,
          // revenue rollups, etc.) remain superadmin-only and the backend
          // enforces that via requireAdmin; the UI hides those nav items
          // below for sovereign-admin so they don't see dead links.
          if (u.role !== 'superadmin' && u.role !== 'sovereign-admin') {
            error = 'Access denied. Superadmin or sovereign-admin role required.';
            user = null;
            loading = false;
            return;
          }
          user = u;
          error = '';
          loading = false;
        })
        .catch(e => { error = e.message; loading = false; });
    };
    sync();
    window.addEventListener(AUTH_CHANGED_EVENT, sync);
    const onStorage = (e: StorageEvent) => {
      if (e.key === 'sme-admin-token' || e.key === null) sync();
    };
    window.addEventListener('storage', onStorage);
    return () => {
      window.removeEventListener(AUTH_CHANGED_EVENT, sync);
      window.removeEventListener('storage', onStorage);
    };
  });

  async function handleLogoutAll() {
    if (signingOutAll) return;
    if (!confirm('Sign out of every session on every device?')) return;
    signingOutAll = true;
    try {
      await logoutAll();
    } catch {
      // Non-fatal — we still want to log out locally even if the server
      // call fails, so falling through to logout() below is correct.
    }
    logout();
  }
</script>

{#if loading}
  <div class="flex h-screen items-center justify-center">
    <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
  </div>
{:else if error}
  <div class="flex h-screen flex-col items-center justify-center gap-4">
    <p class="text-[var(--color-danger)]">{error}</p>
  </div>
{:else if user}
  <div class="flex min-h-screen">
    <!-- Sidebar -->
    <aside class="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]">
      <div class="flex h-14 items-center gap-2 border-b border-[var(--color-border)] px-4">
        <!-- Canonical OpenOva mark — see /brand/logo-mark.svg -->
        <svg viewBox="0 0 700 400" width="36" height="20" class="flex-shrink-0" fill="none" aria-hidden="true">
          <defs>
            <linearGradient id="admin-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
              <stop offset="0%" stop-color="#3B82F6"/>
              <stop offset="100%" stop-color="#818CF8"/>
            </linearGradient>
          </defs>
          <path d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
                fill="none" stroke="url(#admin-logo-grad)" stroke-width="100" stroke-linecap="butt"/>
        </svg>
        <span class="text-sm font-semibold text-[var(--color-text-strong)]">OpenOva <span class="text-[var(--color-text-dim)] font-normal">Admin</span></span>
      </div>
      <nav class="flex-1 overflow-y-auto py-3">
        {#each nav as item}
          {@const isActive = activePage === item.id}
          <a
            href={item.href}
            class="flex items-center gap-3 mx-2 rounded-lg px-3 py-2 text-sm no-underline transition-colors
              {isActive ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]' : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'}"
          >
            <svg class="h-4.5 w-4.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
              <path stroke-linecap="round" stroke-linejoin="round" d={item.icon}/>
            </svg>
            {item.label}
          </a>
        {/each}
      </nav>
      <div class="border-t border-[var(--color-border)] p-3 flex flex-col gap-2">
        <p class="truncate text-xs text-[var(--color-text-dim)]">{user.email}</p>
        <div class="flex items-center gap-2">
          <button
            type="button"
            onclick={logout}
            class="flex-1 rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]"
            title="Sign out"
            aria-label="Sign out"
          >Sign out</button>
          <button
            type="button"
            onclick={handleLogoutAll}
            disabled={signingOutAll}
            class="flex-1 rounded-md border border-[var(--color-danger)]/40 px-2 py-1 text-xs text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10 disabled:opacity-50"
            title="Sign out of every session on every device"
            aria-label="Sign out everywhere"
          >{signingOutAll ? '…' : 'Sign out all'}</button>
        </div>
      </div>
    </aside>

    <main class="ml-56 flex-1 p-8">
      {@render children(user)}
    </main>
  </div>
{/if}
