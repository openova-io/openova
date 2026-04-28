<script lang="ts">
  import AdminShell from './AdminShell.svelte';
  import { getAdminOrders, type User, type Order } from '../lib/api';
  import { formatOMR } from '../lib/currency';

  let orders = $state<Order[]>([]);
  let loading = $state(true);
  let error = $state('');
  let filter = $state('all');

  let filteredOrders = $derived(
    filter === 'all' ? orders : orders.filter(o => o.status === filter)
  );

  $effect(() => {
    getAdminOrders()
      .then(o => { orders = o ?? []; loading = false; })
      .catch(e => { error = e?.message || 'Failed to load orders'; loading = false; });
  });

  // #85 — every currency cell in the admin uses the shared `formatOMR`
  // helper against canonical baisa. The client normalises `amount_omr`
  // legacy fields to baisa at the API boundary.

  function formatDate(iso: string) {
    return new Date(iso).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' });
  }

  function statusColor(status: string) {
    if (status === 'paid' || status === 'completed') return 'var(--color-success)';
    if (status === 'pending') return 'var(--color-warn)';
    if (status === 'failed' || status === 'cancelled') return 'var(--color-danger)';
    return 'var(--color-text-dim)';
  }

  let totalBaisa = $derived(filteredOrders.reduce((sum, o) => sum + (typeof o.amount_baisa === 'number' ? o.amount_baisa : 0), 0));

  const filters = ['all', 'paid', 'pending', 'failed', 'cancelled'];
</script>

<AdminShell activePage="orders">
  {#snippet children(user: User)}
<div>
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Orders</h1>
      <p class="mt-1 text-sm text-[var(--color-text-dim)]">{filteredOrders.length} order{filteredOrders.length !== 1 ? 's' : ''} &middot; Total: {formatOMR(totalBaisa)}</p>
    </div>
  </div>

  {#if error}
    <div class="mt-4 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-sm text-[var(--color-danger)]">
      {error}
    </div>
  {/if}

  <!-- Filters -->
  <div class="mt-6 flex gap-2">
    {#each filters as f}
      <button
        onclick={() => filter = f}
        class="rounded-full px-3 py-1 text-xs font-medium capitalize transition-colors
          {filter === f ? 'bg-[var(--color-accent)] text-white' : 'border border-[var(--color-border)] text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)]'}"
      >
        {f}
      </button>
    {/each}
  </div>

  {#if loading}
    <div class="mt-12 flex justify-center">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else if filteredOrders.length === 0}
    <div class="mt-12 text-center">
      <p class="text-[var(--color-text-dim)]">No orders {filter !== 'all' ? `with status "${filter}"` : ''}.</p>
    </div>
  {:else}
    <div class="mt-4 overflow-x-auto rounded-xl border border-[var(--color-border)]">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-[var(--color-border)] bg-[var(--color-surface)]">
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Order ID</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Tenant</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Plan</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Promo</th>
            <th class="px-4 py-3 text-right font-medium text-[var(--color-text-dim)]">Amount</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Status</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Date</th>
          </tr>
        </thead>
        <tbody>
          {#each filteredOrders as order}
            <tr class="border-b border-[var(--color-border)] last:border-0 hover:bg-[var(--color-surface-hover)]">
              <td class="px-4 py-3 font-mono text-xs text-[var(--color-text-dim)]">{order.id.slice(0, 8)}</td>
              <td class="px-4 py-3 text-[var(--color-text)]">{order.tenant_id.slice(0, 8)}</td>
              <td class="px-4 py-3 text-[var(--color-text)]">{order.plan_id}</td>
              <td class="px-4 py-3 text-[var(--color-text)]">
                {#if order.promo_code}
                  <span class="font-mono text-xs">{order.promo_code}</span>
                  {#if order.promo_deleted}
                    <!-- #91 — tombstone pill so retired codes stay visible. -->
                    <span class="ml-1 inline-flex rounded-full bg-[var(--color-danger)]/15 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-[var(--color-danger)]">deleted</span>
                  {/if}
                {:else}
                  <span class="text-[var(--color-text-dimmer)]">&mdash;</span>
                {/if}
              </td>
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
  {/snippet}
</AdminShell>
