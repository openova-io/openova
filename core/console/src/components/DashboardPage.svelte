<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import { getPlans, type User, type Org, type Plan } from '../lib/api';
  import { path, MARKETPLACE_URL } from '../lib/config';

  let plans = $state<Plan[]>([]);
  $effect(() => { getPlans().then(p => { plans = p; }).catch(() => {}); });

  function planName(planId: string | undefined): string {
    if (!planId) return '—';
    const p = plans.find(pl => pl.id === planId);
    return p?.name ?? planId.substring(0, 8).toUpperCase();
  }
</script>

<PortalShell activePage="dashboard">
  {#snippet children(user: User, org: Org | null)}
    <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Dashboard</h1>
    <p class="mt-1 text-sm text-[var(--color-text-dim)]">Welcome back, {user.name || user.email}</p>

    {#if org}
      <!-- Stats Grid -->
      <div class="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
          <p class="text-sm text-[var(--color-text-dim)]">Apps Installed</p>
          <p class="mt-1 text-2xl font-bold text-[var(--color-text-strong)]">{org.apps?.length ?? 0}</p>
        </div>
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
          <p class="text-sm text-[var(--color-text-dim)]">Plan</p>
          <p class="mt-1 text-2xl font-bold text-[var(--color-accent)]">{planName(org.plan_id)}</p>
        </div>
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
          <p class="text-sm text-[var(--color-text-dim)]">Status</p>
          <p class="mt-1 flex items-center gap-2 text-lg font-semibold text-[var(--color-success)]">
            <span class="h-2 w-2 rounded-full bg-[var(--color-success)]"></span>
            Active
          </p>
        </div>
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
          <p class="text-sm text-[var(--color-text-dim)]">Tenant</p>
          <p class="mt-1 truncate text-sm font-mono text-[var(--color-accent)]">{org.slug}.omani.rest</p>
        </div>
      </div>

      <!-- Quick Actions -->
      <div class="mt-8">
        <h2 class="mb-4 text-lg font-semibold text-[var(--color-text-strong)]">Quick Actions</h2>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <a href={path('apps')} class="flex items-center gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 transition-colors hover:border-[var(--color-border-strong)] no-underline">
            <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-accent)]/10">
              <svg class="h-5 w-5 text-[var(--color-accent)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M12 4.5v15m7.5-7.5h-15"/>
              </svg>
            </div>
            <div>
              <p class="text-sm font-medium text-[var(--color-text-strong)]">Add More Apps</p>
              <p class="text-xs text-[var(--color-text-dim)]">Browse the catalog</p>
            </div>
          </a>
          <a href={path('domains')} class="flex items-center gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 transition-colors hover:border-[var(--color-border-strong)] no-underline">
            <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-success)]/10">
              <svg class="h-5 w-5 text-[var(--color-success)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M12 21a9.004 9.004 0 008.716-6.747M12 21a9.004 9.004 0 01-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 017.843 4.582M12 3a8.997 8.997 0 00-7.843 4.582m15.686 0A11.953 11.953 0 0112 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0121 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0112 16.5c-3.162 0-6.133-.815-8.716-2.247m0 0A9.015 9.015 0 013 12c0-1.605.42-3.113 1.157-4.418"/>
              </svg>
            </div>
            <div>
              <p class="text-sm font-medium text-[var(--color-text-strong)]">Manage Domains</p>
              <p class="text-xs text-[var(--color-text-dim)]">Connect a custom domain</p>
            </div>
          </a>
          <a href={path('team')} class="flex items-center gap-3 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 transition-colors hover:border-[var(--color-border-strong)] no-underline">
            <div class="flex h-10 w-10 items-center justify-center rounded-lg bg-[var(--color-warn)]/10">
              <svg class="h-5 w-5 text-[var(--color-warn)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M19 7.5v3m0 0v3m0-3h3m-3 0h-3m-2.25-4.125a3.375 3.375 0 11-6.75 0 3.375 3.375 0 016.75 0zM4 19.235v-.11a6.375 6.375 0 0112.75 0v.109A12.318 12.318 0 0110.374 21c-2.331 0-4.512-.645-6.374-1.766z"/>
              </svg>
            </div>
            <div>
              <p class="text-sm font-medium text-[var(--color-text-strong)]">Invite Team</p>
              <p class="text-xs text-[var(--color-text-dim)]">Add team members</p>
            </div>
          </a>
        </div>
      </div>
    {:else}
      <div class="mt-12 text-center">
        <p class="text-[var(--color-text-dim)]">No tenant found.</p>
        <a href={MARKETPLACE_URL} class="mt-4 inline-block rounded-xl bg-[var(--color-accent)] px-6 py-3 text-sm font-semibold text-white no-underline">
          Create Tenant
        </a>
      </div>
    {/if}
  {/snippet}
</PortalShell>
