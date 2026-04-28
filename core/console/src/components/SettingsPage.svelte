<script lang="ts">
  import PortalShell from './PortalShell.svelte';
  import { updateOrg, deleteOrg, logout, logoutAll, type User, type Org } from '../lib/api';

  let saving = $state(false);
  let saveError = $state('');
  let saveOk = $state(false);
  let nameEdit = $state('');
  let initialName = $state('');

  let confirmOpen = $state(false);
  let confirmText = $state('');
  let deleting = $state(false);
  let deleteError = $state('');

  let signingOutAll = $state(false);
  let logoutAllOk = $state(false);
  let logoutAllError = $state('');

  async function handleSignOutAll() {
    if (signingOutAll) return;
    if (!confirm('Sign out of every session on every device?')) return;
    signingOutAll = true;
    logoutAllError = '';
    logoutAllOk = false;
    try {
      await logoutAll();
      logoutAllOk = true;
      // Revoking all tokens includes the one we just used — bounce this
      // tab back to sign-in so the user doesn't see a cascade of 401s.
      setTimeout(() => logout(), 600);
    } catch (e: any) {
      logoutAllError = e?.message || 'Sign-out failed';
      signingOutAll = false;
    }
  }

  async function handleSave(orgId: string) {
    if (!nameEdit || nameEdit === initialName) return;
    saving = true;
    saveError = '';
    saveOk = false;
    try {
      const updated = await updateOrg(orgId, { name: nameEdit });
      initialName = updated.name;
      nameEdit = updated.name;
      saveOk = true;
      // Sync the SWR session cache so the sidebar, breadcrumb, and any other
      // PortalShell consumers reflect the new name on the next paint. Without
      // this, the sidebar keeps showing the old name until the user navigates
      // away and back (or hard-reloads) — which is why users report "cannot
      // change organization name" even though the PUT succeeds.
      try {
        const raw = sessionStorage.getItem('sme-session-cache-v1');
        if (raw) {
          const cache = JSON.parse(raw) as { user: User; orgs: Org[] };
          if (Array.isArray(cache.orgs)) {
            cache.orgs = cache.orgs.map(o => o.id === orgId ? { ...o, name: updated.name } : o);
            sessionStorage.setItem('sme-session-cache-v1', JSON.stringify(cache));
          }
        }
      } catch { /* non-fatal: cache miss or quota */ }
      // Reload so PortalShell re-hydrates from the updated cache and the whole
      // UI (sidebar, org switcher, danger-zone modal) picks up the new name.
      setTimeout(() => window.location.reload(), 400);
    } catch (e: any) {
      saveError = e.message || 'Save failed';
    }
    saving = false;
  }

  async function handleDelete(orgId: string, slug: string) {
    if (confirmText.trim() !== slug) {
      deleteError = `Type "${slug}" to confirm`;
      return;
    }
    deleting = true;
    deleteError = '';
    try {
      await deleteOrg(orgId);
      // Wipe the active-org pointer so the next login doesn't land on a
      // deleted workspace. logout() also clears cart + token.
      localStorage.removeItem('sme-active-org');
      logout();
    } catch (e: any) {
      deleteError = e.message || 'Delete failed';
      deleting = false;
    }
  }
</script>

<PortalShell activePage="settings">
  {#snippet children(user: User, org: Org | null)}
    <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Settings</h1>
    <p class="mt-1 text-sm text-[var(--color-text-dim)]">Tenant configuration</p>

    {#if org}
      {#if initialName === '' && nameEdit === ''}
        {(initialName = org.name, nameEdit = org.name, '')}
      {/if}
      <!-- General -->
      <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
        <h2 class="mb-4 text-base font-semibold text-[var(--color-text-strong)]">General</h2>
        <div class="flex flex-col gap-4">
          <div>
            <label class="mb-1 block text-sm text-[var(--color-text-dim)]">Organization Name</label>
            <input
              type="text"
              bind:value={nameEdit}
              class="w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-2.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            />
          </div>
          <div>
            <label class="mb-1 block text-sm text-[var(--color-text-dim)]">Tenant URL</label>
            <div class="flex items-center gap-2">
              <input
                type="text"
                value={org.slug}
                disabled
                class="flex-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-2.5 text-sm text-[var(--color-text-dim)] opacity-60"
              />
              <span class="text-sm text-[var(--color-text-dim)]">.omani.rest</span>
            </div>
          </div>
        </div>
        <button
          onclick={() => handleSave(org.id)}
          disabled={saving || nameEdit === initialName || !nameEdit.trim()}
          class="mt-4 rounded-lg bg-[var(--color-accent)] px-5 py-2.5 text-sm font-semibold text-white hover:bg-[var(--color-accent-hover)] disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save Changes'}
        </button>
        {#if saveOk}
          <p class="mt-2 text-xs text-[var(--color-success)]">Saved.</p>
        {/if}
        {#if saveError}
          <p class="mt-2 text-xs text-[var(--color-danger)]">{saveError}</p>
        {/if}
      </div>

      <!-- Security -->
      <div class="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
        <h2 class="mb-2 text-base font-semibold text-[var(--color-text-strong)]">Security</h2>
        <p class="mb-4 text-sm text-[var(--color-text-dim)]">
          Revoke every refresh token across all devices — other signed-in browsers will be bounced
          to sign-in on their next request. Useful if you lost a device or suspect a session leak.
        </p>
        <button
          type="button"
          onclick={handleSignOutAll}
          disabled={signingOutAll}
          class="rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-hover)] disabled:opacity-50"
        >
          {signingOutAll ? 'Signing out…' : 'Sign out everywhere'}
        </button>
        {#if logoutAllOk}
          <p class="mt-2 text-xs text-[var(--color-success)]">All sessions revoked. Bouncing you out…</p>
        {/if}
        {#if logoutAllError}
          <p class="mt-2 text-xs text-[var(--color-danger)]">{logoutAllError}</p>
        {/if}
      </div>

      <!-- Danger Zone -->
      <div class="mt-6 rounded-xl border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 p-6">
        <h2 class="mb-2 text-base font-semibold text-[var(--color-danger)]">Danger Zone</h2>
        <p class="mb-4 text-sm text-[var(--color-text-dim)]">
          Deleting your tenant will permanently remove all data, apps, and domains. This action cannot be undone.
        </p>
        <button
          onclick={() => { confirmOpen = true; confirmText = ''; deleteError = ''; }}
          class="rounded-lg border border-[var(--color-danger)] px-5 py-2 text-sm font-semibold text-[var(--color-danger)] hover:bg-[var(--color-danger)] hover:text-white transition-colors"
        >
          Delete Tenant
        </button>
      </div>

      {#if confirmOpen}
        <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
          <div class="w-full max-w-md rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
            <h3 class="text-lg font-semibold text-[var(--color-danger)]">Delete {org.name}?</h3>
            <p class="mt-2 text-sm text-[var(--color-text-dim)]">
              This cannot be undone. All apps, databases, and domains tied to this tenant will be torn down.
              Type <strong class="font-mono text-[var(--color-text)]">{org.slug}</strong> below to confirm.
            </p>
            <input
              type="text"
              bind:value={confirmText}
              placeholder={org.slug}
              class="mt-4 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 font-mono text-sm text-[var(--color-text)] focus:border-[var(--color-danger)] focus:outline-none"
            />
            {#if deleteError}
              <p class="mt-2 text-xs text-[var(--color-danger)]">{deleteError}</p>
            {/if}
            <div class="mt-5 flex justify-end gap-2">
              <button
                onclick={() => { confirmOpen = false; }}
                disabled={deleting}
                class="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)]"
              >Cancel</button>
              <button
                onclick={() => handleDelete(org.id, org.slug)}
                disabled={deleting || confirmText.trim() !== org.slug}
                class="rounded-lg bg-[var(--color-danger)] px-3 py-1.5 text-sm font-medium text-white hover:bg-[var(--color-danger)]/90 disabled:opacity-50"
              >{deleting ? 'Deleting…' : 'Delete Tenant'}</button>
            </div>
          </div>
        </div>
      {/if}
    {/if}
  {/snippet}
</PortalShell>
