<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import { getSubscription, getInvoices, getPlans, getCreditBalance, type User, type Org, type Subscription, type Invoice, type Plan, type CreditBalance } from '../lib/api';
  import { formatOMR } from '../lib/currency';

  let subscription = $state<Subscription | null>(null);
  let invoices = $state<Invoice[]>([]);
  let plans = $state<Plan[]>([]);
  let balance = $state<CreditBalance | null>(null);

  $effect(() => {
    getSubscription().then(s => subscription = s).catch(() => {});
    getInvoices().then(i => invoices = i).catch(() => {});
    getPlans().then(p => plans = p).catch(() => {});
    getCreditBalance().then(b => balance = b).catch(() => { balance = { credit_baisa: 0, entries: [] }; });
  });

  // #85 — amounts in state are already baisa (1/1000 OMR), normalized by
  // lib/api.ts. `formatOMR` is the single formatter for every currency
  // display in the console.

  function entryLabel(reason: string): string {
    if (reason.startsWith('promo:')) return `Promo · ${reason.slice(6)}`;
    if (reason === 'order-payment') return 'Order payment';
    return reason;
  }
</script>

<PortalShell activePage="billing">
  {#snippet children(user: User, org: Org | null)}
    <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Billing</h1>
    <p class="mt-1 text-sm text-[var(--color-text-dim)]">Manage your subscription, credits, and invoices</p>

    <!-- Credit balance -->
    <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
      <div class="flex items-center justify-between">
        <div>
          <h2 class="text-base font-semibold text-[var(--color-text-strong)]">Credit balance</h2>
          <p class="mt-1 text-sm text-[var(--color-text-dim)]">
            Credits from promos and refunds — applied automatically at checkout before card charges.
          </p>
        </div>
        <div class="text-right">
          <p class="text-3xl font-bold text-[var(--color-accent)]">{formatOMR(balance?.credit_baisa ?? 0)}</p>
          <p class="text-xs text-[var(--color-text-dim)]">available</p>
        </div>
      </div>

      {#if balance && balance.entries.length > 0}
        <div class="mt-5 border-t border-[var(--color-border)] pt-4">
          <p class="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">Recent activity</p>
          <div class="divide-y divide-[var(--color-border)]">
            {#each balance.entries as entry}
              <div class="flex items-center justify-between py-2">
                <div>
                  <p class="text-sm text-[var(--color-text)]">{entryLabel(entry.reason)}</p>
                  <p class="text-xs text-[var(--color-text-dim)]">{new Date(entry.created_at).toLocaleString()}</p>
                </div>
                <span class="text-sm font-semibold {entry.amount_baisa > 0 ? 'text-[var(--color-success)]' : 'text-[var(--color-text)]'}">
                  {formatOMR(entry.amount_baisa, { signed: true })}
                </span>
              </div>
            {/each}
          </div>
        </div>
      {/if}
    </div>

    <!-- Subscription -->
    <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
      <h2 class="mb-4 text-base font-semibold text-[var(--color-text-strong)]">Current Plan</h2>
      {#if subscription}
        <div class="flex items-center justify-between">
          <div>
            <p class="text-lg font-bold text-[var(--color-accent)]">{plans.find(p => p.id === subscription.plan_id)?.name ?? subscription.plan_id.toUpperCase()}</p>
            <p class="text-sm text-[var(--color-text-dim)]">
              Status: <span class="text-[var(--color-success)]">{subscription.status}</span>
            </p>
          </div>
          <button class="rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-hover)]">
            Change Plan
          </button>
        </div>
      {:else}
        <p class="text-sm text-[var(--color-text-dim)]">No active subscription.</p>
      {/if}
    </div>

    <!-- Invoices -->
    <div class="mt-6">
      <h2 class="mb-3 text-base font-semibold text-[var(--color-text-strong)]">Invoices</h2>
      {#if invoices.length > 0}
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] divide-y divide-[var(--color-border)]">
          {#each invoices as inv}
            <div class="flex items-center justify-between px-5 py-3">
              <div>
                <p class="text-sm text-[var(--color-text)]">{new Date(inv.created_at).toLocaleDateString()}</p>
              </div>
              <div class="flex items-center gap-4">
                <span class="text-sm font-medium text-[var(--color-text-strong)]">{formatOMR(inv.amount_baisa)}</span>
                <span class="rounded-full px-2.5 py-0.5 text-xs {inv.status === 'paid' ? 'bg-[var(--color-success)]/15 text-[var(--color-success)]' : 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]'}">{inv.status}</span>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-[var(--color-text-dim)]">No invoices yet.</p>
      {/if}
    </div>
  {/snippet}
</PortalShell>
