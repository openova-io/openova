<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import { getMembers, inviteMember, type User, type Org, type Member } from '../lib/api';

  let members = $state<Member[]>([]);
  let inviteEmail = $state('');
  let inviteRole = $state('member');
  let inviting = $state(false);
  let orgId = $state('');

  async function loadMembers(org: Org | null) {
    if (!org) return;
    orgId = org.id;
    try {
      members = await getMembers(org.id);
    } catch { /* empty */ }
  }

  async function handleInvite() {
    if (!inviteEmail || !orgId) return;
    inviting = true;
    try {
      const member = await inviteMember(orgId, inviteEmail, inviteRole);
      members = [...members, member];
      inviteEmail = '';
    } catch { /* empty */ }
    inviting = false;
  }
</script>

<PortalShell activePage="team">
  {#snippet children(user: User, org: Org | null)}
    <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Team</h1>
    <p class="mt-1 text-sm text-[var(--color-text-dim)]">Manage tenant members</p>

    <!-- Invite Form -->
    <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
      <h2 class="mb-4 text-base font-semibold text-[var(--color-text-strong)]">Invite a team member</h2>
      <form onsubmit={(e) => { e.preventDefault(); handleInvite(); }} class="flex gap-3">
        <input
          type="email"
          bind:value={inviteEmail}
          placeholder="colleague@company.com"
          required
          class="flex-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-2.5 text-sm text-[var(--color-text)] placeholder-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none"
        />
        <select
          bind:value={inviteRole}
          class="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2.5 text-sm text-[var(--color-text)]"
        >
          <option value="admin">Admin</option>
          <option value="member">Member</option>
          <option value="viewer">Viewer</option>
        </select>
        <button
          type="submit"
          disabled={inviting}
          class="rounded-lg bg-[var(--color-accent)] px-5 py-2.5 text-sm font-semibold text-white hover:bg-[var(--color-accent-hover)] disabled:opacity-50"
        >
          {inviting ? 'Sending...' : 'Invite'}
        </button>
      </form>
    </div>

    <!-- Members List -->
    <div class="mt-6">
      <h2 class="mb-3 text-base font-semibold text-[var(--color-text-strong)]">Members</h2>
      {#if members.length > 0}
        <div class="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] divide-y divide-[var(--color-border)]">
          {#each members as member}
            <div class="flex items-center justify-between px-5 py-3">
              <div class="flex items-center gap-3">
                <div class="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-accent)]/10 text-xs font-bold text-[var(--color-accent)]">
                  {member.email[0].toUpperCase()}
                </div>
                <span class="text-sm text-[var(--color-text)]">{member.email}</span>
              </div>
              <span class="rounded-full bg-[var(--color-bg)] px-3 py-1 text-xs text-[var(--color-text-dim)]">{member.role}</span>
            </div>
          {/each}
        </div>
      {:else}
        <p class="text-sm text-[var(--color-text-dim)]">No members yet. Invite someone above.</p>
      {/if}
    </div>

    {#if org}
      <!-- Load members when org becomes available -->
      {@const _ = loadMembers(org)}
    {/if}
  {/snippet}
</PortalShell>
