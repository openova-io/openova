export interface CartState {
  plan: string | null;
  planName: string;
  apps: string[];
  addons: string[];
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
}

const STORAGE_KEY = 'sme-cart';

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
  cart.plan = planId;
  if (planName) cart.planName = planName;
  writeCart(cart);
  return cart;
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
