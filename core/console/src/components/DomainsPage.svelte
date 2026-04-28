<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import { getDomains, type User, type Org, type Domain } from '../lib/api';

  let domains = $state<Domain[]>([]);

  async function loadDomains(org: Org | null) {
    if (!org) return;
    try { domains = await getDomains(org.id); } catch { /* empty */ }
  }
</script>

<PortalShell activePage="domains">
  {#snippet children(user: User, org: Org | null)}
    <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Domains</h1>
    <p class="mt-1 text-sm text-[var(--color-text-dim)]">Manage your tenant domains</p>

    {#if org}
      <!-- Default domain -->
      <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
        <div class="flex items-center justify-between">
          <div>
            <p class="font-mono text-sm text-[var(--color-accent)]">{org.slug}.omani.rest</p>
            <p class="text-xs text-[var(--color-text-dim)]">Default subdomain</p>
          </div>
          <span class="flex items-center gap-1 text-xs text-[var(--color-success)]">
            <span class="h-1.5 w-1.5 rounded-full bg-[var(--color-success)]"></span>
            Active
          </span>
        </div>
      </div>

      {#if domains.length > 0}
        <div class="mt-4 flex flex-col gap-3">
          {#each domains as domain}
            <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
              <div class="flex items-center justify-between">
                <div>
                  <p class="font-mono text-sm text-[var(--color-text-strong)]">{domain.domain}</p>
                  <p class="text-xs text-[var(--color-text-dim)]">Custom domain</p>
                </div>
                <span class="rounded-full px-2.5 py-0.5 text-xs
                  {domain.dns_status === 'verified' ? 'bg-[var(--color-success)]/15 text-[var(--color-success)]' : 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]'}">
                  {domain.dns_status}
                </span>
              </div>
            </div>
          {/each}
        </div>
      {/if}

      <button class="mt-4 rounded-xl border border-dashed border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-4 text-sm text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)] w-full transition-colors">
        + Connect Custom Domain
      </button>

      {@const _ = loadDomains(org)}
    {/if}
  {/snippet}
</PortalShell>
