<script lang="ts">
  import AdminShell from './AdminShell.svelte';
  import {
    getBillingSettings,
    updateBillingSettings,
    listPromoCodes,
    upsertPromoCode,
    deletePromoCode,
    type User,
    type BillingSettings,
    type PromoCode,
  } from '../lib/api';

  let settings = $state<BillingSettings | null>(null);
  let promos = $state<PromoCode[]>([]);
  let loading = $state(true);
  let error = $state('');
  let message = $state('');

  let secretInput = $state('');
  let webhookInput = $state('');
  let publicInput = $state('');
  let saving = $state(false);

  let newPromo = $state<Partial<PromoCode>>({
    code: '',
    credit_omr: 100,
    description: '',
    active: true,
    max_redemptions: 0,
  });
  let savingPromo = $state(false);

  async function load() {
    loading = true;
    try {
      const [s, p] = await Promise.all([getBillingSettings(), listPromoCodes()]);
      settings = s;
      promos = p;
      publicInput = s.stripe_public_key || '';
    } catch (e: any) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  async function saveSettings(e: Event) {
    e.preventDefault();
    saving = true;
    error = '';
    message = '';
    try {
      // Send only provided fields — empty secret inputs are treated as "no change".
      const body: Record<string, string> = { stripe_public_key: publicInput };
      if (secretInput) body.stripe_secret_key = secretInput;
      if (webhookInput) body.stripe_webhook_secret = webhookInput;
      await updateBillingSettings(body as any);
      secretInput = '';
      webhookInput = '';
      message = 'Settings saved.';
      await load();
    } catch (e: any) {
      error = e.message;
    } finally {
      saving = false;
    }
  }

  async function savePromo(e: Event) {
    e.preventDefault();
    if (!newPromo.code || !newPromo.credit_omr) return;
    savingPromo = true;
    try {
      await upsertPromoCode({
        ...newPromo,
        code: newPromo.code!.toUpperCase().trim(),
      });
      newPromo = { code: '', credit_omr: 100, description: '', active: true, max_redemptions: 0 };
      await load();
    } catch (e: any) {
      error = e.message;
    } finally {
      savingPromo = false;
    }
  }

  async function removePromo(code: string) {
    if (!confirm(`Delete promo code ${code}?`)) return;
    try {
      await deletePromoCode(code);
      await load();
    } catch (e: any) {
      error = e.message;
    }
  }

  async function togglePromo(p: PromoCode) {
    try {
      await upsertPromoCode({ ...p, active: !p.active });
      await load();
    } catch (e: any) {
      error = e.message;
    }
  }
</script>

<AdminShell activePage="billing">
  {#snippet children(user: User)}
    <div>
      <div>
        <h1 class="text-2xl font-bold text-[var(--color-text-strong)]">Billing</h1>
        <p class="mt-1 text-sm text-[var(--color-text-dim)]">Stripe keys and promo codes.</p>
      </div>

      {#if error}
        <div class="mt-4 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-3 text-sm text-[var(--color-danger)]">{error}</div>
      {/if}
      {#if message}
        <div class="mt-4 rounded-lg border border-[var(--color-success)]/30 bg-[var(--color-success)]/10 p-3 text-sm text-[var(--color-success)]">{message}</div>
      {/if}

      {#if loading}
        <div class="mt-12 flex justify-center">
          <div class="h-8 w-8 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
        </div>
      {:else if settings}
        <section class="mt-8 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6">
          <h2 class="text-base font-semibold text-[var(--color-text-strong)]">Stripe Configuration</h2>
          <p class="mt-1 text-xs text-[var(--color-text-dim)]">
            Paste the Stripe secret and webhook signing secret from your Stripe dashboard. Leave blank to keep
            the existing value. Once keys are present, real checkout sessions will be created; otherwise the
            checkout falls back to credit-only payment.
          </p>

          <form onsubmit={saveSettings} class="mt-5 space-y-4">
            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Stripe Secret Key</label>
              <div class="mt-1 flex items-center gap-3">
                <input
                  type="password"
                  bind:value={secretInput}
                  placeholder={settings.stripe_secret_key_configured ? `sk_****${settings.stripe_secret_key_last4}` : 'sk_test_... or sk_live_...'}
                  class="w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
                />
                <span class="shrink-0 text-xs {settings.stripe_secret_key_configured ? 'text-[var(--color-success)]' : 'text-[var(--color-warn)]'}">
                  {settings.stripe_secret_key_configured ? 'configured' : 'not configured'}
                </span>
              </div>
            </div>

            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Stripe Webhook Signing Secret</label>
              <div class="mt-1 flex items-center gap-3">
                <input
                  type="password"
                  bind:value={webhookInput}
                  placeholder={settings.stripe_webhook_secret_configured ? `whsec_****${settings.stripe_webhook_secret_last4}` : 'whsec_...'}
                  class="w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
                />
                <span class="shrink-0 text-xs {settings.stripe_webhook_secret_configured ? 'text-[var(--color-success)]' : 'text-[var(--color-warn)]'}">
                  {settings.stripe_webhook_secret_configured ? 'configured' : 'not configured'}
                </span>
              </div>
            </div>

            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Stripe Publishable Key</label>
              <input
                type="text"
                bind:value={publicInput}
                placeholder="pk_test_... or pk_live_..."
                class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono"
              />
            </div>

            <div class="flex items-center justify-between pt-2">
              <p class="text-xs text-[var(--color-text-dim)]">
                Last updated: {settings.updated_at ? new Date(settings.updated_at).toLocaleString() : '—'}
              </p>
              <button
                type="submit"
                disabled={saving}
                class="rounded-lg bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              >
                {saving ? 'Saving…' : 'Save Settings'}
              </button>
            </div>
          </form>
        </section>

        <section class="mt-8 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6">
          <h2 class="text-base font-semibold text-[var(--color-text-strong)]">Promo Codes</h2>
          <p class="mt-1 text-xs text-[var(--color-text-dim)]">
            Promo codes grant OMR credit. If a customer's credit covers the full order, checkout completes
            without Stripe.
          </p>

          <form onsubmit={savePromo} class="mt-5 grid grid-cols-[1fr_120px_1fr_auto] gap-3 items-end">
            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Code</label>
              <input
                type="text"
                required
                bind:value={newPromo.code}
                placeholder="OPENOVA-DEV-2026"
                class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm font-mono uppercase"
              />
            </div>
            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Credit (OMR)</label>
              <input
                type="number"
                required
                min="1"
                bind:value={newPromo.credit_omr}
                class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm"
              />
            </div>
            <div>
              <label class="block text-xs font-medium text-[var(--color-text-dim)]">Description</label>
              <input
                type="text"
                bind:value={newPromo.description}
                placeholder="Launch credit"
                class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm"
              />
            </div>
            <button
              type="submit"
              disabled={savingPromo}
              class="h-10 rounded-lg bg-[var(--color-accent)] px-4 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
            >
              {savingPromo ? '…' : 'Add / Update'}
            </button>
          </form>

          <div class="mt-6 overflow-x-auto rounded-xl border border-[var(--color-border)]">
            <table class="w-full text-sm">
              <thead>
                <tr class="border-b border-[var(--color-border)] bg-[var(--color-surface)]">
                  <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Code</th>
                  <th class="px-4 py-3 text-right font-medium text-[var(--color-text-dim)]">Credit</th>
                  <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Description</th>
                  <th class="px-4 py-3 text-right font-medium text-[var(--color-text-dim)]">Used</th>
                  <th class="px-4 py-3 text-left font-medium text-[var(--color-text-dim)]">Active</th>
                  <th class="px-4 py-3"></th>
                </tr>
              </thead>
              <tbody>
                {#if promos.length === 0}
                  <tr>
                    <td colspan="6" class="px-4 py-8 text-center text-[var(--color-text-dim)]">
                      No promo codes yet.
                    </td>
                  </tr>
                {:else}
                  {#each promos as p (p.code)}
                    <tr class="border-b border-[var(--color-border)] last:border-0">
                      <td class="px-4 py-3 font-mono text-[var(--color-text-strong)]">{p.code}</td>
                      <td class="px-4 py-3 text-right text-[var(--color-text)]">{p.credit_omr} OMR</td>
                      <td class="px-4 py-3 text-[var(--color-text-dim)]">{p.description || '—'}</td>
                      <td class="px-4 py-3 text-right text-[var(--color-text-dim)]">
                        {p.times_redeemed}{p.max_redemptions > 0 ? ` / ${p.max_redemptions}` : ''}
                      </td>
                      <td class="px-4 py-3">
                        <button
                          onclick={() => togglePromo(p)}
                          class="rounded-full px-2 py-0.5 text-xs font-medium"
                          style="background: color-mix(in srgb, {p.active ? 'var(--color-success)' : 'var(--color-text-dim)'} 15%, transparent); color: {p.active ? 'var(--color-success)' : 'var(--color-text-dim)'};"
                        >
                          {p.active ? 'active' : 'inactive'}
                        </button>
                      </td>
                      <td class="px-4 py-3 text-right">
                        <button
                          onclick={() => removePromo(p.code)}
                          class="text-xs text-[var(--color-danger)] hover:underline"
                        >
                          Delete
                        </button>
                      </td>
                    </tr>
                  {/each}
                {/if}
              </tbody>
            </table>
          </div>
        </section>
      {/if}
    </div>
  {/snippet}
</AdminShell>
