<script lang="ts">
  import { logout, type User, type Org } from '../lib/api';
  import { path } from '../lib/config';

  let {
    user,
    org,
    orgs = [],
    activePage = 'dashboard',
    onSwitchOrg,
  }: {
    user: User;
    org: Org | null;
    orgs?: Org[];
    activePage?: string;
    onSwitchOrg?: (id: string) => void;
  } = $props();

  let switcherOpen = $state(false);

  const nav = [
    { id: 'dashboard', label: 'Dashboard', href: path(''), icon: 'M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-6 0a1 1 0 001-1v-4a1 1 0 011-1h2a1 1 0 011 1v4a1 1 0 001 1m-6 0h6' },
    { id: 'apps', label: 'Apps', href: path('apps'), icon: 'M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zm10 0a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z' },
    { id: 'jobs', label: 'Jobs', href: path('jobs'), icon: 'M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4' },
    { id: 'domains', label: 'Domains', href: path('domains'), icon: 'M21 12a9 9 0 01-9 9m9-9a9 9 0 00-9-9m9 9H3m9 9a9 9 0 01-9-9m9 9c1.657 0 3-4.03 3-9s-1.343-9-3-9m0 18c-1.657 0-3-4.03-3-9s1.343-9 3-9m-9 9a9 9 0 019-9' },
    { id: 'billing', label: 'Billing', href: path('billing'), icon: 'M3 10h18M7 15h1m4 0h1m-7 4h12a3 3 0 003-3V8a3 3 0 00-3-3H6a3 3 0 00-3 3v8a3 3 0 003 3z' },
    { id: 'team', label: 'Team', href: path('team'), icon: 'M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0zm6 3a2 2 0 11-4 0 2 2 0 014 0zM7 10a2 2 0 11-4 0 2 2 0 014 0z' },
    { id: 'settings', label: 'Settings', href: path('settings'), icon: 'M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z' },
  ];
</script>

<aside class="fixed left-0 top-0 flex h-screen w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-bg-2)]">
  <!-- Logo + Tenant switcher -->
  <div class="border-b border-[var(--color-border)]">
    <div class="flex h-14 items-center gap-2 px-4">
      <!-- Canonical OpenOva mark — see /brand/logo-mark.svg -->
      <svg viewBox="0 0 700 400" width="36" height="20" class="flex-shrink-0" fill="none" aria-hidden="true">
        <defs>
          <linearGradient id="sidebar-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
            <stop offset="0%" stop-color="#3B82F6"/>
            <stop offset="100%" stop-color="#818CF8"/>
          </linearGradient>
        </defs>
        <path d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
              fill="none" stroke="url(#sidebar-logo-grad)" stroke-width="100" stroke-linecap="butt"/>
      </svg>
      <span class="text-sm font-semibold text-[var(--color-text-strong)]">OpenOva <span class="text-[var(--color-text-dim)] font-normal">Console</span></span>
    </div>
    {#if org}
      <div class="relative px-3 pb-3">
        <button
          onclick={() => (switcherOpen = !switcherOpen)}
          class="flex w-full items-center justify-between gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-left text-xs"
        >
          <span class="min-w-0 flex-1 truncate text-[var(--color-text-strong)]">{org.name}</span>
          <svg class="h-3.5 w-3.5 shrink-0 text-[var(--color-text-dim)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
            <path stroke-linecap="round" stroke-linejoin="round" d="M8.25 15L12 18.75 15.75 15m-7.5-6L12 5.25 15.75 9" />
          </svg>
        </button>
        {#if switcherOpen}
          <div class="absolute left-3 right-3 z-20 mt-1 overflow-hidden rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] shadow-lg">
            {#each orgs as o (o.id)}
              <button
                onclick={() => { switcherOpen = false; if (o.id !== org?.id) onSwitchOrg?.(o.id); }}
                class="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-xs hover:bg-[var(--color-surface-hover)] {o.id === org?.id ? 'text-[var(--color-accent)]' : 'text-[var(--color-text)]'}"
              >
                <span class="min-w-0 flex-1 truncate">{o.name}</span>
                {#if o.id === org?.id}
                  <svg class="h-3.5 w-3.5 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5"/>
                  </svg>
                {/if}
              </button>
            {/each}
            {#if orgs.length === 0}
              <p class="px-3 py-2 text-xs text-[var(--color-text-dim)]">No tenants.</p>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Navigation -->
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

  <!-- User -->
  <div class="border-t border-[var(--color-border)] p-3">
    <div class="flex items-center gap-2">
      <div class="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-xs font-bold text-[var(--color-accent)]">
        {user.name?.[0]?.toUpperCase() || user.email[0].toUpperCase()}
      </div>
      <div class="min-w-0 flex-1">
        <p class="truncate text-xs font-medium text-[var(--color-text)]">{user.name || user.email}</p>
        <button onclick={logout} class="text-[10px] text-[var(--color-text-dimmer)] hover:text-[var(--color-danger)]">Sign out</button>
      </div>
    </div>
  </div>
</aside>
