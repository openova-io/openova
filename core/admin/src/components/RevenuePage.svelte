<script lang="ts">
  import AdminShell from './AdminShell.svelte';
  import { getRevenue, getAdminOrders, type User, type Revenue, type Order } from '../lib/api';
  import { formatOMR } from '../lib/currency';

  let revenue = $state<Revenue | null>(null);
  let recentOrders = $state<Order[]>([]);
  let loading = $state(true);
  let error = $state('');

  $effect(() => {
    Promise.all([getRevenue(), getAdminOrders()])
      .then(([rev, orders]) => {
        revenue = rev;
        recentOrders = orders.slice(0, 10);
        loading = false;
      })
      .catch(e => { error = e.message; loading = false; });
  });

  // #85 — single shared `formatOMR(baisa)` helper across the admin app.

  function formatDate(iso: string) {
    return new Date(iso).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' });
  }

  function statusColor(status: string) {
    if (status === 'paid' || status === 'completed') return 'var(--color-success)';
    if (status === 'pending') return 'var(--color-warn)';
    if (status === 'failed' || status === 'cancelled') return 'var(--color-danger)';
    return 'var(--color-text-dim)';
  }
</script>

<AdminShell activePage="dashboard">
  {#snippet children(user: User)}
<div>
  <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Revenue Dashboard</h1>
  <p class="mt-1 text-sm text-[var(--color-text-dim)]">Platform billing overview</p>

  {#if loading}
    <div class="mt-12 flex justify-center">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else if error}
    <div class="mt-6 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-4 text-sm text-[var(--color-danger)]">
      {error}
    </div>
  {:else if revenue}
    <!-- Stats Grid -->
    <div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-3">
      <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
        <p class="text-xs font-medium uppercase tracking-wider text-[var(--color-text-dim)]">Monthly Recurring Revenue</p>
        <p class="mt-2 text-3xl font-bold text-[var(--color-success)]">{formatOMR(revenue.total_mrr_baisa ?? 0)}</p>
      </div>
      <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
        <p class="text-xs font-medium uppercase tracking-wider text-[var(--color-text-dim)]">Total Customers</p>
        <p class="mt-2 text-3xl font-bold text-[var(--color-text-strong)]">{revenue.total_customers ?? 0}</p>
      </div>
      <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
        <p class="text-xs font-medium uppercase tracking-wider text-[var(--color-text-dim)]">Active Subscriptions</p>
        <p class="mt-2 text-3xl font-bold text-[var(--color-accent)]">{revenue.active_subscriptions ?? 0}</p>
      </div>
    </div>

    <!-- Recent Orders -->
    <div class="mt-8">
      <h2 class="text-lg font-semibold text-[var(--color-text-strong)]">Recent Orders</h2>
      {#if recentOrders.length === 0}
        <p class="mt-4 text-sm text-[var(--color-text-dim)]">No orders yet.</p>
      {:else}
        <div class="mt-3 overflow-x-auto rounded-xl border border-[var(--color-border)]">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--color-border)] bg-[var(--color-surface)]">
                <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Order ID</th>
                <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Tenant</th>
                <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Plan</th>
                <th class="px-4 py-3 text-right font-medium text-[var(--color-text-dim)]">Amount</th>
                <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Status</th>
                <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Date</th>
              </tr>
            </thead>
            <tbody>
              {#each recentOrders as order}
                <tr class="border-b border-[var(--color-border)] last:border-0">
                  <td class="px-4 py-3 font-mono text-xs text-[var(--color-text-dim)]">{order.id.slice(0, 8)}</td>
                  <td class="px-4 py-3 text-[var(--color-text)]">{order.tenant_id.slice(0, 8)}</td>
                  <td class="px-4 py-3 text-[var(--color-text)]">{order.plan_id}</td>
                  <td class="px-4 py-3 text-right font-medium text-[var(--color-text-strong)]">{formatOMR(order.amount_baisa)}</td>
                  <td class="px-4 py-3">
                    <span
                      class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
                      style="background: color-mix(in srgb, {statusColor(order.status)} 15%, transparent); color: {statusColor(order.status)};"
                    >
                      {order.status}
                    </span>
                  </td>
                  <td class="px-4 py-3 text-[var(--color-text-dim)]">{formatDate(order.created_at)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {/if}
</div>
  {/snippet}
</AdminShell>
