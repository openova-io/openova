export interface CartState {
  plan: string | null;
  planName: string;
  apps: string[];
  addons: string[];
  orgName: string;
  subdomain: string;
  email: string;
}

const STORAGE_KEY = 'sme-cart';

const defaultCart: CartState = {
  plan: null,
  planName: '',
  apps: [],
  addons: [],
  orgName: '',
  subdomain: '',
  email: '',
};

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

export function clearCart(): void {
  localStorage.removeItem(STORAGE_KEY);
  window.dispatchEvent(new CustomEvent('cart-updated', { detail: defaultCart }));
}

export function cartItemCount(cart: CartState): number {
  return cart.apps.length;
}
