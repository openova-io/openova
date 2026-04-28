<script lang="ts">
  import AdminShell from './AdminShell.svelte';
  import BackingServices from './BackingServices.svelte';
  import { getAdminTenants, adminDeleteTenant, type User, type Tenant } from '../lib/api';

  let tenants = $state<Tenant[]>([]);
  let total = $state(0);
  let page = $state(1);
  let loading = $state(true);
  let error = $state('');
  let deleteTarget = $state<Tenant | null>(null);
  let deleting = $state(false);
  // Expandable "Backing services" row per tenant. A plain Set of tenant IDs
  // keeps the toggle state independent of pagination reloads.
  let expanded = $state<Set<string>>(new Set());
  function toggleExpanded(id: string) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expanded = next;
  }

  async function confirmDelete() {
    if (!deleteTarget) return;
    deleting = true;
    try {
      await adminDeleteTenant(deleteTarget.id);
      deleteTarget = null;
      await loadTenants(page);
    } catch (e: any) {
      error = e.message;
    }
    deleting = false;
  }

  const perPage = 20;
  let totalPages = $derived(Math.ceil(total / perPage));

  async function loadTenants(p: number) {
    loading = true;
    try {
      const data = await getAdminTenants(p);
      tenants = data.tenants;
      total = data.total;
      page = p;
    } catch (e: any) {
      error = e.message;
    }
    loading = false;
  }

  $effect(() => { loadTenants(1); });

  function formatDate(iso: string) {
    return new Date(iso).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' });
  }

  function statusColor(status: string) {
    if (status === 'active') return 'var(--color-success)';
    if (status === 'provisioning') return 'var(--color-warn)';
    if (status === 'suspended') return 'var(--color-danger)';
    return 'var(--color-text-dim)';
  }
</script>

<AdminShell activePage="tenants">
  {#snippet children(user: User)}
<div>
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Tenants</h1>
      <p class="mt-1 text-sm text-[var(--color-text-dim)]">{total} total tenant{total !== 1 ? 's' : ''}</p>
    </div>
  </div>

  {#if error}
    <div class="mt-4 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-sm text-[var(--color-danger)]">
      {error}
    </div>
  {/if}

  {#if loading}
    <div class="mt-12 flex justify-center">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
    </div>
  {:else if tenants.length === 0}
    <div class="mt-12 text-center">
      <p class="text-[var(--color-text-dim)]">No tenants yet.</p>
    </div>
  {:else}
    <div class="mt-4 overflow-x-auto rounded-xl border border-[var(--color-border)]">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-[var(--color-border)] bg-[var(--color-surface)]">
            <th class="w-8 px-2 py-3"></th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Name</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Slug</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Plan</th>
            <th class="px-4 py-3 text-center font-medium text-[var(--color-text-dim)]">Members</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Status</th>
            <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Created</th>
            <th class="px-4 py-3 text-right font-medium text-[var(--color-text-dim)]">Actions</th>
          </tr>
        </thead>
        <tbody>
          {#each tenants as tenant}
            {@const isOpen = expanded.has(tenant.id)}
            <tr class="border-b border-[var(--color-border)] hover:bg-[var(--color-surface-hover)]">
              <td class="px-2 py-3 text-center">
                <button
                  onclick={() => toggleExpanded(tenant.id)}
                  class="caret-btn"
                  aria-expanded={isOpen}
                  aria-label={isOpen ? 'Hide backing services' : 'Show backing services'}
                  title="Show backing services"
                >
                  <span class="caret" class:open={isOpen}>▸</span>
                </button>
              </td>
              <td class="px-4 py-3 font-medium text-[var(--color-text-strong)]">{tenant.name}</td>
              <td class="px-4 py-3 font-mono text-xs text-[var(--color-text-dim)]">{tenant.slug}</td>
              <td class="px-4 py-3 text-[var(--color-text)]">{tenant.plan_id}</td>
              <td class="px-4 py-3 text-center text-[var(--color-text)]">{tenant.member_count}</td>
              <td class="px-4 py-3">
                <span
                  class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium"
                  style="background: color-mix(in srgb, {statusColor(tenant.status)} 15%, transparent); color: {statusColor(tenant.status)};"
                >
                  {tenant.status}
                </span>
              </td>
              <td class="px-4 py-3 text-[var(--color-text-dim)]">{formatDate(tenant.created_at)}</td>
              <td class="px-4 py-3 text-right">
                {#if tenant.status !== 'deleted'}
                  <button
                    onclick={() => (deleteTarget = tenant)}
                    class="rounded-md border border-[var(--color-danger)]/40 px-2 py-1 text-xs text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10"
                  >Delete</button>
                {:else}
                  <span class="text-xs text-[var(--color-text-dimmer)]">—</span>
                {/if}
              </td>
            </tr>
            {#if isOpen}
              <tr class="border-b border-[var(--color-border)] last:border-0">
                <td></td>
                <td colspan="7" class="px-4 py-3 bg-[var(--color-surface)]/50">
                  <div class="section-title">Backing services</div>
                  <BackingServices tenantId={tenant.id} />
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    {#if totalPages > 1}
      <div class="mt-4 flex items-center justify-between">
        <p class="text-xs text-[var(--color-text-dim)]">
          Page {page} of {totalPages}
        </p>
        <div class="flex gap-1">
          <button
            onclick={() => loadTenants(page - 1)}
            disabled={page <= 1}
            class="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] disabled:opacity-30"
          >
            Previous
          </button>
          <button
            onclick={() => loadTenants(page + 1)}
            disabled={page >= totalPages}
            class="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] disabled:opacity-30"
          >
            Next
          </button>
        </div>
      </div>
    {/if}
  {/if}

  {#if deleteTarget}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <div class="w-full max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
        <h3 class="text-lg font-semibold text-[var(--color-text-strong)]">Delete tenant?</h3>
        <p class="mt-2 text-sm text-[var(--color-text-dim)]">
          This will soft-delete <strong class="text-[var(--color-text)]">{deleteTarget.name}</strong>
          ({deleteTarget.slug}) and publish a tenant.deleted event. This cannot be undone from the admin UI.
        </p>
        <div class="mt-5 flex justify-end gap-2">
          <button
            onclick={() => (deleteTarget = null)}
            class="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)]"
          >Cancel</button>
          <button
            onclick={confirmDelete}
            disabled={deleting}
            class="rounded-lg bg-[var(--color-danger)] px-3 py-1.5 text-sm font-medium text-white hover:bg-[var(--color-danger)]/90 disabled:opacity-50"
          >{deleting ? 'Deleting…' : 'Delete'}</button>
        </div>
      </div>
    </div>
  {/if}
</div>
  {/snippet}
</AdminShell>

<style>
  .caret-btn {
    background: transparent;
    border: 0;
    padding: 0.25rem 0.35rem;
    cursor: pointer;
    color: var(--color-text-dim);
    font-size: 0.8rem;
    line-height: 1;
  }
  .caret-btn:hover { color: var(--color-text); }
  .caret { display: inline-block; transition: transform 0.15s ease; }
  .caret.open { transform: rotate(90deg); }
  .section-title {
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--color-text-dim);
    margin-bottom: 0.35rem;
    font-weight: 600;
  }
</style>
