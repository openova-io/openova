import { minorUnits, type PackageAddonPick } from './packages';

export type { PackageAddonPick } from './packages';

export interface CartState {
  plan: string | null;
  planName: string;
  apps: string[];
  addons: string[];
  // #6971 — the BSS package chosen on the comparison table (`plan.m`) and the
  // optional features ticked on it, snapshotted with name + price so /review
  // and /checkout render them without a second fetch. `plan` above stays the
  // CATALOG plan id billing resolves (packages.ts::catalogPlanIdForPackage);
  // the sku travels beside it as `package_sku`, and the add-on SKUs are
  // merged into the `addons` the funnel already POSTs (orderAddonIds).
  // Null / empty when the legacy deck (no BSS) made the choice.
  packageSku: string | null;
  packageAddons: PackageAddonPick[];
  orgName: string;
  subdomain: string;
  // Parent domain (TLD) chosen on /addons. Persisted across wizard steps
  // so /review and /checkout don't fall back to the default. Convergence
  // bug: previously local to AddonsStep, so a user picking .omani.homes
  // would see .omani.rest on Review + Checkout.
  tld: string;
  email: string;
  // Sandbox-product agent picks (Wave 4 — products/sandbox/README.md).
  // When the cart contains the `sandbox` app, the customer pre-selects
  // a subset of the 6 supported agents on the Sandbox detail page. The
  // checkout/create-org payload forwards this to the tenant-service,
  // which emits a `tenant.sandbox_requested` event the sandbox-
  // controller consumes to materialize a Sandbox CR with the matching
  // spec.agentCatalogue. Empty when Sandbox isn't in the cart.
  agents: string[];
  // TBD-V18-D follow-up to PR #2038 — per-app config values keyed by
  // the marketplace app SLUG (NOT id, so the persisted cart survives a
  // catalog id reshuffle). Shape per slug is the dict of
  // `ConfigField.key` → user-chosen value, matching the ConfigField
  // schema declared by the catalog. Threaded into the install POST
  // body (createTenant → /tenant/orgs) under the `app_configs`
  // sibling field. Empty record when no app exposes a configSchema
  // (e.g. cart is Sandbox-only, or all picks are Ghost/Nextcloud which
  // ship empty schemas today).
  //
  // Independent of TBD-V26 (#2040): this wires the SHAPE end-to-end;
  // the backend HelmRelease consumption is gated on Path A/B of
  // TBD-V26 and lives in its own track. The shape is correct today so
  // that flipping the Path A/B switch lights up the form values
  // without a second frontend round-trip.
  appConfigs: Record<string, Record<string, number | string | boolean>>;
}

const STORAGE_KEY = 'org-cart';

// Default parent domain — kept in sync with the picker in AddonsStep.svelte
// (`tlds` array). Single source of truth so a future change to the default
// is a one-liner.
export const DEFAULT_TLD = 'omani.rest';

const defaultCart: CartState = {
  plan: null,
  planName: '',
  apps: [],
  addons: [],
  orgName: '',
  subdomain: '',
  tld: DEFAULT_TLD,
  email: '',
  agents: [],
  appConfigs: {},
  packageSku: null,
  packageAddons: [],
};

// The 6 agents the Sandbox CRD (sandbox.openova.io/v1) accepts in
// `spec.agentCatalogue` — kept in sync with
// products/catalyst/chart/crds/sandbox.yaml. Single source of truth for
// the marketplace detail-page picker.
export const SANDBOX_AGENTS: { slug: string; name: string; tagline: string }[] = [
  { slug: 'claude-code',  name: 'Claude Code',  tagline: 'Anthropic — the native CLI' },
  { slug: 'cursor-agent', name: 'Cursor Agent', tagline: 'Cursor CLI in headless mode' },
  { slug: 'qwen-code',    name: 'Qwen Code',    tagline: 'Alibaba Qwen — local-first' },
  { slug: 'aider',        name: 'Aider',        tagline: 'AI pair programming in the terminal' },
  { slug: 'opencode',     name: 'Opencode',     tagline: 'OSS multi-provider coding agent' },
  { slug: 'little-coder', name: 'Little Coder', tagline: 'Lightweight scripted agent' },
];

export function readCart(): CartState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...defaultCart };
    return { ...defaultCart, ...JSON.parse(raw) };
  } catch {
    return { ...defaultCart };
  }
}

export function writeCart(cart: CartState): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(cart));
    window.dispatchEvent(new CustomEvent('cart-updated', { detail: cart }));
  } catch {}
}

export function toggleApp(appId: string): CartState {
  const cart = readCart();
  const idx = cart.apps.indexOf(appId);
  if (idx >= 0) {
    cart.apps.splice(idx, 1);
  } else {
    cart.apps.push(appId);
  }
  writeCart(cart);
  return cart;
}

export function setPlan(planId: string, planName?: string): CartState {
  const cart = readCart();
  if (cart.plan !== planId) {
    // #6971 — a package's add-on SKUs are priced per package, so a plan change
    // made outside the comparison table (legacy deck, /review radios) drops
    // the package choice rather than carrying another package's add-ons.
    cart.packageSku = null;
    cart.packageAddons = [];
  }
  cart.plan = planId;
  if (planName) cart.planName = planName;
  writeCart(cart);
  return cart;
}

/**
 * #6971 — persist a choice made on the package comparison table: the catalog
 * plan id billing needs, the display name, the BSS package sku and the ticked
 * add-ons, atomically.
 */
export function setPackage(sel: {
  planId: string;
  planName: string;
  packageSku: string;
  addons: PackageAddonPick[];
}): CartState {
  const cart = readCart();
  cart.plan = sel.planId;
  cart.planName = sel.planName;
  cart.packageSku = sel.packageSku;
  cart.packageAddons = sel.addons.map(a => ({ ...a }));
  writeCart(cart);
  return cart;
}

/**
 * #6971 — every add-on identifier the order carries: the catalog add-on ids
 * picked on /addons plus the BSS add-on SKUs ticked on the package table, in
 * that order, de-duplicated. This is what both the Organization create POST
 * and `POST /billing/checkout` receive as `addons`.
 */
export function orderAddonIds(cart: CartState): string[] {
  const out: string[] = [];
  for (const id of [...(cart.addons || []), ...(cart.packageAddons || []).map(a => a.sku)]) {
    if (id && !out.includes(id)) out.push(id);
  }
  return out;
}

/** #6971 — the ticked package add-ons' monthly price, in baisa, for display totals. */
export function packageAddonsBaisa(cart: CartState): number {
  return (cart.packageAddons || []).reduce((sum, a) => sum + minorUnits(a.price_month), 0);
}

export function toggleAddon(addonId: string): CartState {
  const cart = readCart();
  const idx = cart.addons.indexOf(addonId);
  if (idx >= 0) {
    cart.addons.splice(idx, 1);
  } else {
    cart.addons.push(addonId);
  }
  writeCart(cart);
  return cart;
}

export function setOrgDetails(orgName: string, subdomain: string, email: string): CartState {
  const cart = readCart();
  cart.orgName = orgName;
  cart.subdomain = subdomain;
  cart.email = email;
  writeCart(cart);
  return cart;
}

// Persist the parent domain (TLD) chosen on /addons. Separate setter from
// setOrgDetails so the TLD picker can save immediately on change (no need
// to wait for the subdomain input to blur).
export function setTLD(tld: string): CartState {
  const cart = readCart();
  cart.tld = tld;
  writeCart(cart);
  return cart;
}

// setAppConfig stores the customer-chosen configSchema field values
// for a single app, keyed by the app's marketplace SLUG. Called by
// AppDetail.svelte whenever the user mutates any field in the rendered
// ConfigField form — Svelte's reactive update fires this so the cart
// always reflects the on-screen state. Empty `values` is a legitimate
// signal that the operator wiped the form; we keep the slot present
// rather than deleting it so the install-POST shape stays stable. See
// TBD-V18-D follow-up to PR #2038.
export function setAppConfig(
  appSlug: string,
  values: Record<string, number | string | boolean>,
): CartState {
  const cart = readCart();
  cart.appConfigs = { ...(cart.appConfigs || {}), [appSlug]: { ...values } };
  writeCart(cart);
  return cart;
}

// toggleAgent flips one agent slug in/out of cart.agents. Used by the
// Sandbox detail page (AppDetail.svelte) when slug === 'sandbox'. The
// list is kept stable-ordered by toggling in-place — order in the cart
// matches the order the user clicked.
export function toggleAgent(agentSlug: string): CartState {
  const cart = readCart();
  const idx = cart.agents.indexOf(agentSlug);
  if (idx >= 0) {
    cart.agents.splice(idx, 1);
  } else {
    cart.agents.push(agentSlug);
  }
  writeCart(cart);
  return cart;
}

export function clearCart(): void {
  localStorage.removeItem(STORAGE_KEY);
  window.dispatchEvent(new CustomEvent('cart-updated', { detail: defaultCart }));
}

export function cartItemCount(cart: CartState): number {
  return cart.apps.length;
}
