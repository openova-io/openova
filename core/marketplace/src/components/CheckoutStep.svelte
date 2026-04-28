<script lang="ts">
  import { sendMagicLink, verifyMagicLink, getMe, getGoogleAuthUrl, googleCallback, createTenant, getMyOrgs, createCheckout, startProvisioning, getProvisionByTenant, checkSlug, getPlans, getAddons, getCreditBalance, setAuthTokens, setActiveOrg, type User, type Provision, type Plan, type AddOn } from '../lib/api';
  import { readCart, clearCart } from '../lib/cart';
  import { formatOMR } from '../lib/currency';
  import { consoleHref } from '../lib/config';

  let cart = $state(readCart());
  let plans = $state<Plan[]>([]);
  let addons = $state<AddOn[]>([]);
  const selectedPlan = $derived(plans.find(p => p.id === cart.plan));
  const selectedAddons = $derived(addons.filter(a => cart.addons.includes(a.id)));
  const planCost = $derived(selectedPlan?.monthly_price ?? 0);
  const addonCost = $derived(selectedAddons.reduce((sum, a) => sum + a.monthly_price, 0));
  const totalCost = $derived(planCost + addonCost);

  $effect(() => {
    getPlans().then(p => { plans = p; }).catch(() => {});
    getAddons().then(a => { addons = a; }).catch(() => {});
  });

  // #85 — checkout renders all OMR values via the shared helper so they
  // match the plan cards, review sidebar, and console billing page exactly.
  let user = $state<User | null>(null);
  let authMode = $state<'login' | 'verify'>('login');
  let email = $state(cart.email || '');
  let code = $state('');
  let authError = $state('');
  let authLoading = $state(false);
  let checkoutLoading = $state(false);
  let provision = $state<Provision | null>(null);
  let provisionError = $state('');
  let tenantId = $state('');
  let orgName = $state(cart.orgName || '');
  // Subdomain is validated in AddonsStep and persisted to cart there. Trust
  // cart.subdomain as the source of truth when present; the inline editor only
  // shows up as a fallback when the user skipped add-ons.
  let subdomain = $state(cart.subdomain || '');
  let userEditedSubdomain = $state(!!cart.subdomain);
  let promoCode = $state('');
  type PayMethod = 'applepay' | 'mastercard' | 'visa';
  let payMethod = $state<PayMethod | null>(null);

  function togglePayMethod(m: PayMethod) {
    payMethod = payMethod === m ? null : m;
  }
  let slugStatus = $state<'idle' | 'checking' | 'available' | 'taken' | 'invalid'>('idle');
  let slugChecked = $state('');
  let slugTimer: ReturnType<typeof setTimeout> | null = null;

  function normalizeSlug(s: string): string {
    return s.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
  }

  function derivedSlug(): string {
    // Subdomain wins only if the user has actually edited it. Otherwise we
    // derive purely from orgName, which keeps the URL in sync with what
    // appears on screen and prevents stale cart state from winning.
    const src = userEditedSubdomain ? (subdomain || orgName) : orgName;
    return normalizeSlug(src) || '';
  }

  // Auto-mirror orgName → subdomain while the user hasn't taken manual control.
  $effect(() => {
    if (!userEditedSubdomain) {
      subdomain = normalizeSlug(orgName);
    }
  });

  // Debounced subdomain availability check. Tracks both fields so that users
  // who only type a workspace name (leaving subdomain blank) still see whether
  // the derived slug is available.
  $effect(() => {
    // Track both so Svelte re-runs this effect when either changes.
    void subdomain; void orgName;
    if (slugTimer) { clearTimeout(slugTimer); slugTimer = null; }
    const s = derivedSlug();
    if (!s) { slugStatus = 'idle'; slugChecked = ''; return; }
    if (s.length < 3) { slugStatus = 'invalid'; slugChecked = s; return; }
    slugStatus = 'checking';
    slugChecked = s;
    slugTimer = setTimeout(async () => {
      try {
        const { available } = await checkSlug(s);
        if (slugChecked !== s) return;
        slugStatus = available ? 'available' : 'taken';
      } catch {
        if (slugChecked !== s) return;
        slugStatus = 'idle';
      }
    }, 400);
  });

  // Check if already authenticated
  $effect(() => {
    const token = localStorage.getItem('sme-token');
    if (token) {
      getMe()
        .then((u) => { user = u; })
        .catch(() => { localStorage.removeItem('sme-token'); });
    }
  });

  // Credit balance (shown only when signed in). The billing API emits the
  // balance as baisa (`credit_baisa`); `getCreditBalance` in lib/api.ts
  // normalises legacy `credit_omr` fallbacks. Every comparison against
  // `totalCost` (also baisa) stays in the same unit — no more omr→baisa
  // conversion inline.
  let creditBaisa = $state<number>(0);
  $effect(() => {
    if (!user) return;
    getCreditBalance().then(b => { creditBaisa = b.credit_baisa || 0; }).catch(() => {});
  });

  const creditCovers = $derived(creditBaisa >= totalCost && totalCost > 0);
  const creditPartial = $derived(creditBaisa > 0 && creditBaisa < totalCost);

  // Handle return from Stripe checkout — redirect straight to console so the
  // user watches real-time provisioning on the Jobs page.
  $effect(() => {
    const params = new URLSearchParams(window.location.search);
    const orderId = params.get('order_id');
    if (orderId) {
      const savedTenantId = localStorage.getItem('sme-checkout-tenant');
      if (savedTenantId) {
        setActiveOrg(savedTenantId);
        localStorage.removeItem('sme-checkout-tenant');
        clearCart();
        redirectToConsole();
      }
    }
  });

  function redirectToConsole() {
    const tok = encodeURIComponent(localStorage.getItem('sme-token') || '');
    const refresh = encodeURIComponent(localStorage.getItem('sme-refresh-token') || '');
    window.location.href = consoleHref('/jobs', { token: decodeURIComponent(tok), refresh_token: decodeURIComponent(refresh) });
  }

  async function handleSendCode() {
    authLoading = true;
    authError = '';
    try {
      await sendMagicLink(email);
      authMode = 'verify';
    } catch (e: any) {
      authError = e.message || 'Failed to send code';
    }
    authLoading = false;
  }

  async function handleVerify() {
    authLoading = true;
    authError = '';
    try {
      const res = await verifyMagicLink(email, code);
      setAuthTokens(res.token, res.refresh_token);
      user = res.user;
    } catch (e: any) {
      authError = e.message || 'Invalid code';
    }
    authLoading = false;
  }

  async function createTenantWithRetry(baseSlug: string, name: string): Promise<{ id: string; slug: string }> {
    // Try the requested slug first, then -2, -3, ... up to -6 on 409.
    let lastErr: any;
    for (let i = 0; i < 6; i++) {
      const s = i === 0 ? baseSlug : `${baseSlug}-${i + 1}`;
      try {
        const t = await createTenant({
          slug: s,
          name,
          plan_id: cart.plan || '',
          apps: cart.apps,
          addons: cart.addons,
        });
        return { id: t.id, slug: t.slug || s };
      } catch (e: any) {
        lastErr = e;
        const msg = String(e?.message || '');
        if (msg.startsWith('409') && msg.includes('slug is already taken')) continue;
        throw e;
      }
    }
    throw lastErr || new Error('tenant name is taken — please choose a different subdomain');
  }

  async function handleCheckout() {
    if (!user) return;
    checkoutLoading = true;
    provisionError = '';
    try {
      // Step 1: Create the tenant (workspace). Re-use a previously created
      // tenant for this cart — but only after verifying it still exists on the
      // backend. Stale localStorage from a wiped DB otherwise silently ships a
      // phantom tenant_id to billing and produces an orphan provision.
      const cartKey = `sme-tenant:${user.id}:${cart.plan}:${derivedSlug()}`;
      const cachedTenantId = localStorage.getItem(cartKey);
      let tenant: { id: string; slug: string } | null = null;
      if (cachedTenantId) {
        try {
          const orgs = await getMyOrgs();
          const match = orgs.find(o => o.id === cachedTenantId && o.status !== 'deleted');
          if (match) {
            tenant = { id: match.id, slug: match.slug || derivedSlug() };
          } else {
            localStorage.removeItem(cartKey);
          }
        } catch {
          // Membership lookup failed — don't trust the cache, fall through to create.
          localStorage.removeItem(cartKey);
        }
      }
      if (!tenant) {
        const baseSlug = derivedSlug();
        tenant = await createTenantWithRetry(
          baseSlug,
          orgName || user.email.split('@')[0] + "'s tenant",
        );
        localStorage.setItem(cartKey, tenant.id);
      }
      tenantId = tenant.id;

      // Step 2: Billing checkout — promo code is additive (credit first, card
      // covers the remainder). Always send if the user entered one.
      const billing = await createCheckout({
        plan_id: cart.plan || '',
        apps: cart.apps,
        addons: cart.addons,
        tenant_id: tenant.id,
        promo_code: promoCode ? promoCode.trim() : undefined,
      });

      if (billing.session_url) {
        // Stripe is configured + credit did not cover total — redirect to Stripe.
        localStorage.setItem('sme-checkout-tenant', tenant.id);
        window.location.href = billing.session_url;
        return;
      }

      if (!billing.paid_by_credit) {
        throw new Error('Checkout did not complete. Please try again.');
      }

      // Credit-covered: trigger provisioning directly (broker event is best-effort).
      await startProvisioning({
        tenant_id: tenant.id,
        order_id: billing.order_id || ('direct-' + tenant.id),
        plan_id: cart.plan || '',
        apps: cart.apps,
        subdomain: tenant.slug,
      });

      // Step 3: Redirect to console — user watches progress there on the Jobs page.
      setActiveOrg(tenant.id);
      clearCart();
      redirectToConsole();
    } catch (e: any) {
      provisionError = e.message || 'Failed to create tenant';
      checkoutLoading = false;
    }
  }

  function pollProvisioning(tid: string) {
    const poll = setInterval(async () => {
      try {
        const p = await getProvisionByTenant(tid);
        provision = p;
        if (p.status === 'completed' || p.status === 'failed') {
          clearInterval(poll);
          checkoutLoading = false;
          localStorage.removeItem('sme-checkout-tenant');
        }
      } catch {
        // Provisioning not started yet — show waiting state
        if (!provision) {
          provision = {
            id: '',
            tenant_id: tid,
            status: 'pending',
            steps: [{ name: 'Waiting for provisioning to start...', status: 'running' }],
          };
        }
      }
    }, 2000);

    // Timeout after 2 minutes
    setTimeout(() => {
      clearInterval(poll);
      if (provision?.status !== 'completed') {
        checkoutLoading = false;
        if (provision) provision.status = 'completed';
      }
    }, 120000);
  }

  async function handleGoogleAuth() {
    try {
      const callbackUrl = window.location.origin + '/auth/callback';
      const data = await getGoogleAuthUrl(callbackUrl);
      window.location.href = data.url;
    } catch (e: any) {
      authError = e.message || 'Failed to start Google login';
    }
  }

  // Check for Google OAuth callback params on mount
  $effect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const googleAuth = params.get('google_auth');
    if (code && googleAuth === '1') {
      handleGoogleCallback(code);
      window.history.replaceState({}, '', window.location.pathname);
    }
  });

  async function handleGoogleCallback(code: string) {
    authLoading = true;
    authError = '';
    try {
      const callbackUrl = window.location.origin + '/auth/callback';
      const res = await googleCallback(code, callbackUrl);
      setAuthTokens(res.token, res.refresh_token);
      user = res.user;
    } catch (e: any) {
      authError = e.message || 'Google login failed';
    }
    authLoading = false;
  }
</script>

<div class="py-4">
  <div class="mb-8 text-center">
    <h1 class="text-3xl font-bold text-[var(--color-text-strong)]">Checkout</h1>
    <p class="mt-2 text-[var(--color-text-dim)]">
      {#if !user}
        Sign in to complete your order
      {:else if provision}
        Setting up your tenant...
      {:else}
        Review and launch your tenant
      {/if}
    </p>
  </div>

  <div class="mx-auto max-w-lg">
    <!-- Provisioning Progress -->
    {#if provision}
      <div class="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8">
        <div class="mb-6 text-center">
          {#if provision.status === 'completed'}
            <div class="mx-auto mb-3 flex h-16 w-16 items-center justify-center rounded-full bg-[var(--color-success)]/20">
              <svg class="h-8 w-8 text-[var(--color-success)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
              </svg>
            </div>
            <h2 class="text-xl font-bold text-[var(--color-text-strong)]">Your tenant is ready!</h2>
            <p class="mt-1 text-sm text-[var(--color-text-dim)]">You'll receive a welcome email shortly.</p>
          {:else if provision.status === 'failed'}
            <div class="mx-auto mb-3 flex h-16 w-16 items-center justify-center rounded-full bg-[var(--color-danger)]/20">
              <svg class="h-8 w-8 text-[var(--color-danger)]" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12"/>
              </svg>
            </div>
            <h2 class="text-lg font-semibold text-[var(--color-danger)]">Provisioning failed</h2>
            <p class="mt-1 text-sm text-[var(--color-text-dim)]">Please contact support.</p>
          {:else}
            <div class="mx-auto mb-3 h-12 w-12 animate-spin rounded-full border-3 border-[var(--color-accent)] border-t-transparent"></div>
            <h2 class="text-lg font-semibold text-[var(--color-text-strong)]">Setting up your tenant</h2>
          {/if}
        </div>
        <div class="flex flex-col gap-3">
          {#each provision.steps as step}
            <div class="flex items-center gap-3">
              {#if step.status === 'completed'}
                <div class="flex h-6 w-6 items-center justify-center rounded-full bg-[var(--color-success)]">
                  <svg class="h-3.5 w-3.5 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="3">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
                  </svg>
                </div>
              {:else if step.status === 'running'}
                <div class="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-accent)] border-t-transparent"></div>
              {:else}
                <div class="h-6 w-6 rounded-full border border-[var(--color-border)]"></div>
              {/if}
              <span class="text-sm {step.status === 'completed' ? 'text-[var(--color-text)]' : step.status === 'running' ? 'text-[var(--color-accent)] font-medium' : 'text-[var(--color-text-dimmer)]'}">{step.name}</span>
            </div>
          {/each}
        </div>
        {#if provision.status === 'completed'}
          <a
            href={consoleHref('/jobs', { token: localStorage.getItem('sme-token') || '', refresh_token: localStorage.getItem('sme-refresh-token') || '' })}
            class="mt-6 flex w-full items-center justify-center gap-2 rounded-xl bg-[var(--color-success)] px-6 py-3 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-success)]/90 no-underline"
          >
            Go to Console
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 6H5.25A2.25 2.25 0 003 8.25v10.5A2.25 2.25 0 005.25 21h10.5A2.25 2.25 0 0018 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"/>
            </svg>
          </a>
        {/if}
      </div>

    <!-- Auth Section -->
    {:else if !user}
      <div class="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8">
        <h2 class="mb-6 text-center text-lg font-semibold text-[var(--color-text-strong)]">Sign in to continue</h2>

        <!-- Google SSO -->
        <button
          onclick={handleGoogleAuth}
          class="flex w-full items-center justify-center gap-3 rounded-xl border border-[var(--color-border)] bg-white px-4 py-3 text-sm font-medium text-gray-800 transition-colors hover:bg-gray-50"
        >
          <svg class="h-5 w-5" viewBox="0 0 24 24">
            <path d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92a5.06 5.06 0 01-2.2 3.32v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.1z" fill="#4285F4"/>
            <path d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z" fill="#34A853"/>
            <path d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z" fill="#FBBC05"/>
            <path d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z" fill="#EA4335"/>
          </svg>
          Continue with Google
        </button>

        <div class="my-6 flex items-center gap-3">
          <div class="flex-1 border-t border-[var(--color-border)]"></div>
          <span class="text-xs text-[var(--color-text-dimmer)]">or use email</span>
          <div class="flex-1 border-t border-[var(--color-border)]"></div>
        </div>

        <!-- Magic Link -->
        {#if authMode === 'login'}
          <form onsubmit={(e) => { e.preventDefault(); handleSendCode(); }}>
            <input
              type="email"
              bind:value={email}
              placeholder="you@company.com"
              required
              class="mb-3 w-full rounded-xl border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-3 text-sm text-[var(--color-text)] placeholder-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none"
            />
            <button
              type="submit"
              disabled={authLoading}
              class="flex w-full items-center justify-center rounded-xl bg-[var(--color-accent)] px-4 py-3 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent-hover)] disabled:opacity-50"
            >
              {authLoading ? 'Sending...' : 'Send sign-in code'}
            </button>
          </form>
        {:else}
          <form onsubmit={(e) => { e.preventDefault(); handleVerify(); }}>
            <p class="mb-3 text-sm text-[var(--color-text-dim)]">
              We sent a 6-digit code to <span class="font-medium text-[var(--color-text)]">{email}</span>
            </p>
            <input
              type="text"
              bind:value={code}
              placeholder="Enter 6-digit code"
              maxlength={6}
              required
              class="mb-3 w-full rounded-xl border border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-3 text-center font-mono text-lg tracking-[0.5em] text-[var(--color-text)] placeholder-[var(--color-text-dimmer)] focus:border-[var(--color-accent)] focus:outline-none"
            />
            <button
              type="submit"
              disabled={authLoading}
              class="flex w-full items-center justify-center rounded-xl bg-[var(--color-accent)] px-4 py-3 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent-hover)] disabled:opacity-50"
            >
              {authLoading ? 'Verifying...' : 'Verify & Continue'}
            </button>
            <button
              type="button"
              onclick={() => { authMode = 'login'; code = ''; authError = ''; }}
              class="mt-2 w-full text-center text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
            >
              Use a different email
            </button>
          </form>
        {/if}

        {#if authError}
          <p class="mt-3 text-center text-sm text-[var(--color-danger)]">{authError}</p>
        {/if}
      </div>

    <!-- Launch Section -->
    {:else}
      <div class="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8">
        <div class="mb-6 flex items-center gap-3 rounded-xl bg-[var(--color-bg)] p-4">
          <div class="flex h-10 w-10 items-center justify-center rounded-full bg-[var(--color-accent)]/20 text-sm font-bold text-[var(--color-accent)]">
            {user.name?.[0]?.toUpperCase() || user.email[0].toUpperCase()}
          </div>
          <div>
            <p class="text-sm font-medium text-[var(--color-text-strong)]">{user.name || user.email}</p>
            <p class="text-xs text-[var(--color-text-dim)]">{user.email}</p>
          </div>
        </div>

        <!-- Workspace details — single source of truth for subdomain is AddonsStep.
             We show a compact read-only summary here with a link back to edit, so
             the user never re-enters what they already chose. Availability was
             already validated in AddonsStep. -->
        <div class="mb-4 space-y-3">
          <div>
            <label class="text-xs font-medium text-[var(--color-text-dim)]">Tenant name</label>
            <input
              bind:value={orgName}
              placeholder="My Company"
              class="mt-1 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
            />
          </div>
          {#if cart.subdomain}
            <div>
              <div class="flex items-baseline justify-between">
                <label class="text-xs font-medium text-[var(--color-text-dim)]">Subdomain</label>
                <a href="/addons" class="text-xs text-[var(--color-accent)] no-underline hover:underline">Change</a>
              </div>
              <div class="mt-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 font-mono text-sm text-[var(--color-text)]">
                {cart.subdomain}.omani.rest
              </div>
              <p class="mt-1 text-[11px] text-[var(--color-text-dimmer)]">Already validated — you can change this during add-ons.</p>
            </div>
          {:else}
            <!-- Fallback: user landed on checkout without going through add-ons. -->
            <div>
              <label class="text-xs font-medium text-[var(--color-text-dim)]">Subdomain</label>
              <div class="mt-1 flex items-center gap-0">
                <input
                  bind:value={subdomain}
                  oninput={() => { userEditedSubdomain = true; }}
                  placeholder="my-company"
                  class="w-full rounded-l-lg border border-r-0 border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
                />
                <span class="rounded-r-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm text-[var(--color-text-dim)]">.omani.rest</span>
              </div>
              <div class="mt-1 text-xs h-4">
                {#if slugStatus === 'checking'}
                  <span class="text-[var(--color-text-dim)]">Checking availability…</span>
                {:else if slugStatus === 'available'}
                  <span class="text-[var(--color-success)]">✓ {slugChecked}.omani.rest is available</span>
                {:else if slugStatus === 'taken'}
                  <span class="text-[var(--color-danger)]">✗ {slugChecked}.omani.rest is already taken</span>
                {:else if slugStatus === 'invalid'}
                  <span class="text-[var(--color-danger)]">Subdomain must be at least 3 characters</span>
                {:else}
                  <span class="text-[var(--color-text-dimmer)]">Auto-synced from tenant name — edit to override</span>
                {/if}
              </div>
            </div>
          {/if}
        </div>

        <!-- Order summary -->
        <div class="mb-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg)] p-4">
          <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">Order summary</div>
          <div class="flex flex-col gap-2 text-sm">
            <div class="flex justify-between">
              <span class="text-[var(--color-text-dim)]">Plan · {selectedPlan?.name || cart.planName || '—'}</span>
              <span class="text-[var(--color-text)]">{formatOMR(planCost)}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-[var(--color-text-dim)]">Apps ({cart.apps.length})</span>
              <span class="text-[var(--color-text-dim)]">Free</span>
            </div>
            {#each selectedAddons as a}
              <div class="flex justify-between">
                <span class="text-[var(--color-text-dim)]">+ {a.name}</span>
                <span class="text-[var(--color-text)]">{formatOMR(a.monthly_price)}</span>
              </div>
            {/each}
            <div class="mt-1 flex justify-between border-t border-dashed border-[var(--color-border)] pt-2 font-semibold">
              <span class="text-[var(--color-text-strong)]">Total (monthly)</span>
              <span class="text-[var(--color-text-strong)]">{formatOMR(totalCost)}</span>
            </div>
            {#if creditBaisa > 0}
              <div class="flex justify-between text-[var(--color-success)]">
                <span>Credit available</span>
                <span>{formatOMR(-creditBaisa)}</span>
              </div>
              <div class="flex justify-between border-t border-dashed border-[var(--color-border)] pt-2 text-sm font-semibold">
                <span class="text-[var(--color-text-strong)]">Due now</span>
                <span class="text-[var(--color-text-strong)]">
                  {formatOMR(Math.max(0, totalCost - creditBaisa))}
                </span>
              </div>
              {#if creditCovers}
                <p class="text-xs text-[var(--color-success)]">✓ Credit covers this order — no card charge needed.</p>
              {:else if creditPartial}
                <p class="text-xs text-[var(--color-text-dim)]">Credit is applied first; the remainder is charged to your card.</p>
              {/if}
            {/if}
          </div>
        </div>

        <!-- Payment method picker -->
        {#if totalCost > 0}
          <div class="mb-6">
            <div class="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">Payment method</div>

            <!-- Three brand tiles in one row — click a tile to select (radio-group behaviour, no separate dot) -->
            <div class="grid grid-cols-3 gap-3">
              <!-- Apple Pay -->
              <button
                type="button"
                onclick={() => togglePayMethod('applepay')}
                aria-pressed={payMethod === 'applepay'}
                class="flex h-16 items-center justify-center rounded-xl border-2 transition-all
                  {payMethod === 'applepay' ? 'border-[var(--color-accent)] bg-[var(--color-accent)]/10 ring-2 ring-[var(--color-accent)]/40' : 'border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)] opacity-60 hover:opacity-100'}"
              >
                <svg class="h-8" viewBox="0 0 165.52 105.97" aria-label="Apple Pay" xmlns="http://www.w3.org/2000/svg">
                  <path fill="currentColor" class="text-[var(--color-text)]" d="M150.7 0H14.82C14.26 0 13.7 0 13.13.01c-.47 0-.94.01-1.42.02-1.03.03-2.06.09-3.09.27a10.42 10.42 0 0 0-2.94.97A9.93 9.93 0 0 0 1.27 5.67c-.46.9-.77 1.86-.97 2.94-.18 1.02-.24 2.06-.27 3.08-.01.47-.02.95-.02 1.42C0 13.67 0 14.24 0 14.8v76.36c0 .57 0 1.13.01 1.7 0 .47.01.94.02 1.42.03 1.02.09 2.06.27 3.08.18 1.08.5 2.03.97 2.94a9.67 9.67 0 0 0 1.83 2.5c.76.76 1.6 1.38 2.5 1.83.92.47 1.86.78 2.95.98 1.03.18 2.08.23 3.08.26.48.01.95.02 1.42.02.57.01 1.13.01 1.7.01h135.88c.56 0 1.13 0 1.69-.01.47 0 .95 0 1.42-.02 1.04-.03 2.05-.08 3.08-.26 1.1-.2 2.03-.5 2.94-.98a9.97 9.97 0 0 0 4.33-4.33c.47-.9.78-1.86.98-2.94.18-1.02.23-2.06.26-3.08.01-.48.02-.95.02-1.42.01-.56.01-1.13.01-1.7V14.81c0-.56 0-1.13-.01-1.69 0-.47-.01-.95-.02-1.42-.03-1.02-.08-2.06-.26-3.08-.2-1.08-.5-2.04-.98-2.94a9.96 9.96 0 0 0-4.33-4.4 10.42 10.42 0 0 0-2.94-.97c-1.03-.18-2.04-.24-3.08-.27-.47 0-.95-.01-1.42-.02-.56-.01-1.13-.01-1.69-.01z"/>
                  <path fill="currentColor" class="text-[var(--color-bg)]" d="M150.69 3.53l1.68.01c.45 0 .9 0 1.35.02.79.02 1.71.06 2.57.22.75.13 1.38.34 1.99.65a6.36 6.36 0 0 1 2.81 2.81c.31.61.52 1.23.65 1.99.15.85.2 1.77.22 2.57.01.45.02.9.02 1.36v78.05c0 .46-.01.91-.02 1.37a15.1 15.1 0 0 1-.22 2.56c-.13.76-.34 1.38-.65 1.99a6.38 6.38 0 0 1-2.81 2.81c-.61.31-1.24.52-1.98.65-.89.15-1.82.2-2.56.22-.46.01-.91.02-1.38.02-.56.01-1.12.01-1.68.01H14.83c-.56 0-1.12 0-1.69-.01-.46 0-.9-.01-1.36-.02-.77-.03-1.7-.07-2.55-.22-.76-.13-1.39-.34-2-.65a6.38 6.38 0 0 1-2.81-2.81c-.31-.6-.52-1.23-.65-1.99a14.86 14.86 0 0 1-.22-2.56c-.01-.45-.02-.9-.02-1.35V13c0-.46.01-.91.02-1.36a15.1 15.1 0 0 1 .22-2.56c.13-.76.34-1.39.65-1.99a6.38 6.38 0 0 1 2.81-2.81c.6-.31 1.23-.52 1.99-.65.85-.15 1.78-.2 2.56-.22.45-.01.91-.02 1.36-.02h135.86"/>
                  <path fill="currentColor" class="text-[var(--color-text)]" d="M45.2 35.63c1.41-1.77 2.37-4.14 2.12-6.57-2.07.1-4.59 1.37-6.05 3.14-1.31 1.52-2.47 4-2.17 6.32 2.32.2 4.64-1.17 6.1-2.89m2.09 3.35c-3.37-.2-6.23 1.91-7.84 1.91-1.61 0-4.07-1.81-6.74-1.76a9.94 9.94 0 0 0-8.44 5.13c-3.62 6.24-.95 15.48 2.57 20.56 1.71 2.51 3.77 5.28 6.48 5.18 2.57-.1 3.57-1.66 6.69-1.66s4.02 1.66 6.74 1.61c2.82-.05 4.58-2.51 6.29-5.03 1.96-2.86 2.76-5.63 2.81-5.78-.05-.05-5.43-2.11-5.48-8.3-.05-5.18 4.22-7.64 4.42-7.79-2.41-3.57-6.19-3.97-7.49-4.07m29.12-7.71c7.11 0 12.06 4.9 12.06 12.03 0 7.16-5.05 12.08-12.24 12.08h-7.88v12.51h-5.69V31.27h13.75zm-8.06 19.33h6.53c4.95 0 7.77-2.66 7.77-7.28 0-4.62-2.82-7.26-7.75-7.26h-6.55v14.54zm21.61 9.13c0-4.67 3.58-7.54 9.93-7.89l7.31-.43v-2.06c0-2.97-2.01-4.75-5.36-4.75-3.18 0-5.16 1.52-5.64 3.91h-5.18c.3-4.83 4.42-8.39 11.02-8.39 6.47 0 10.61 3.43 10.61 8.78v18.4h-5.26v-4.39h-.13c-1.55 2.97-4.93 4.85-8.43 4.85-5.24 0-8.87-3.25-8.87-8.04m17.24-2.42v-2.11l-6.58.41c-3.28.23-5.13 1.68-5.13 3.96 0 2.34 1.93 3.86 4.88 3.86 3.83 0 6.83-2.64 6.83-6.13m10.31 17.58v-4.44c.4.1 1.32.1 1.78.1 2.54 0 3.91-1.07 4.75-3.81 0-.05.48-1.63.48-1.66l-9.65-26.75h5.94l6.75 21.72h.1l6.75-21.72h5.79l-10 28.1c-2.29 6.48-4.93 8.56-10.46 8.56-.45 0-1.83-.05-2.24-.1"/>
                </svg>
              </button>
              <!-- Mastercard -->
              <button
                type="button"
                onclick={() => togglePayMethod('mastercard')}
                aria-pressed={payMethod === 'mastercard'}
                class="flex h-16 items-center justify-center rounded-xl border-2 transition-all
                  {payMethod === 'mastercard' ? 'border-[var(--color-accent)] bg-[var(--color-accent)]/10 ring-2 ring-[var(--color-accent)]/40' : 'border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)] opacity-60 hover:opacity-100'}"
              >
                <svg class="h-9" viewBox="0 0 131.39 86.9" aria-label="Mastercard" xmlns="http://www.w3.org/2000/svg">
                  <path fill="#FF5F00" d="M48.37 15.14h34.66v53.34H48.37z"/>
                  <path fill="#EB001B" d="M51.94 41.81a33.83 33.83 0 0 1 12.93-26.67 33.89 33.89 0 1 0 0 53.34 33.83 33.83 0 0 1-12.93-26.67z"/>
                  <path fill="#F79E1B" d="M117.19 62.83v-1.09h.47v-.23h-1.13v.23h.45v1.09h.21zm2.2 0v-1.32h-.34l-.39.94-.4-.94h-.34v1.32h.25v-1l.37.86h.25l.37-.86v1zm.41-20.98A33.89 33.89 0 0 1 64.87 68.5a33.89 33.89 0 0 0 0-53.34 33.89 33.89 0 0 1 54.94 26.67z"/>
                </svg>
              </button>
              <!-- Visa -->
              <button
                type="button"
                onclick={() => togglePayMethod('visa')}
                aria-pressed={payMethod === 'visa'}
                class="flex h-16 items-center justify-center rounded-xl border-2 transition-all visa-tile
                  {payMethod === 'visa' ? 'border-[var(--color-accent)] bg-[var(--color-accent)]/10 ring-2 ring-[var(--color-accent)]/40' : 'border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)] opacity-60 hover:opacity-100'}"
              >
                <svg class="h-6" viewBox="0 0 100 32" aria-label="Visa" xmlns="http://www.w3.org/2000/svg">
                  <text x="50" y="24" font-family="Arial Black,Helvetica,Arial,sans-serif" font-size="22" font-weight="900" font-style="italic" fill="currentColor" text-anchor="middle" letter-spacing="1">VISA</text>
                </svg>
              </button>
            </div>

            <p class="mt-3 text-xs text-[var(--color-text-dim)]">
              On <strong class="text-[var(--color-text)]">Purchase</strong>, you'll be redirected to Stripe's PCI-compliant checkout to complete payment securely.
            </p>

            <!-- Additive voucher — not a payment method, it applies credit against the total -->
            <div class="mt-4 rounded-xl border border-dashed border-[var(--color-border)] bg-[var(--color-bg)] p-3">
              <label class="flex items-center gap-2 text-xs font-medium text-[var(--color-text-dim)]">
                <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M9 12h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/>
                </svg>
                Voucher or promo code <span class="opacity-60">(optional)</span>
              </label>
              <div class="mt-2 flex gap-2">
                <input
                  bind:value={promoCode}
                  placeholder="e.g. OPENOVA-DEV-2026"
                  class="flex-1 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
                />
              </div>
              <p class="mt-1 text-[11px] text-[var(--color-text-dimmer)]">Credit is applied first. If the voucher doesn't cover the full amount, the remainder is charged to your card at Stripe.</p>
            </div>
          </div>
        {/if}

        {#if provisionError}
          <p class="mb-4 text-center text-sm text-[var(--color-danger)]">{provisionError}</p>
        {/if}

        <button
          onclick={handleCheckout}
          disabled={checkoutLoading || !cart.plan || cart.apps.length === 0}
          class="flex w-full items-center justify-center gap-2 rounded-xl bg-[var(--color-accent)] px-6 py-3.5 text-sm font-semibold text-white transition-colors hover:bg-[var(--color-accent-hover)] disabled:cursor-not-allowed disabled:opacity-50"
        >
          {#if checkoutLoading}
            <div class="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent"></div>
            Processing…
          {:else if totalCost === 0}
            Launch my tenant
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13 7l5 5m0 0l-5 5m5-5H6"/>
            </svg>
          {:else}
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7"/>
            </svg>
            Purchase · {formatOMR(totalCost)}
          {/if}
        </button>
        <p class="mt-3 text-center text-[11px] text-[var(--color-text-dimmer)]">
          After payment is confirmed, your tenant will be created automatically.
        </p>
      </div>
    {/if}
  </div>

  <div class="mt-8">
    <a href="/review" class="text-sm text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline">
      &larr; Back to Review
    </a>
  </div>
</div>
