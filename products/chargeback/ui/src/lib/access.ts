import type { Me, Permission } from '../api/types'

/**
 * The console's view of the access model (DESIGN.md §10). Everything the UI
 * hides or shows is decided by `/me.permissions` — never by the legacy `role`
 * key — so a new role on the server needs no console change to be honoured.
 *
 * A permission under the 'sovereign' key covers every customer; one under
 * 'customer:<id>' covers that customer only.
 */

export const SOVEREIGN = 'sovereign'

export function customerScope(customerId: string): string {
  return `customer:${customerId}`
}

/** Whether the principal holds any Sovereign-scoped binding. */
export function isSovereign(me: Me | null | undefined): boolean {
  if (!me) return false
  if (me.permissions && SOVEREIGN in me.permissions) return true
  if (me.roles?.some((b) => b.scope_kind === SOVEREIGN)) return true
  // A /me document from before bindings existed: the legacy role decides.
  return !me.permissions && !me.roles && me.role === 'operator'
}

/**
 * `can(me, perm)` asks at the Sovereign; `can(me, perm, customerId)` asks on
 * that customer, which a Sovereign-scoped permission also satisfies.
 */
export function can(me: Me | null | undefined, perm: Permission, customerId?: string | null): boolean {
  if (!me) return false
  const perms = me.permissions
  if (perms) {
    if (perms[SOVEREIGN]?.includes(perm)) return true
    if (customerId && perms[customerScope(customerId)]?.includes(perm)) return true
    return false
  }
  return legacyCan(me, perm, customerId)
}

/** The pre-binding document (role + customer_id only), mapped onto the matrix. */
function legacyCan(me: Me, perm: Permission, customerId?: string | null): boolean {
  switch (me.role) {
    case 'operator':
      return true
    case 'customer-admin':
      return Boolean(customerId && me.customer_id === customerId) && (perm === 'metering.read' || perm === 'account.topup' || perm === 'customer.self.manage')
    case 'customer-viewer':
      return Boolean(customerId && me.customer_id === customerId) && perm === 'metering.read'
  }
  return false
}

/** The customer ids the principal holds a binding on, in scope order. */
export function customerIds(me: Me | null | undefined): string[] {
  if (!me) return []
  const out: string[] = []
  for (const key of me.scopes ?? Object.keys(me.permissions ?? {})) {
    if (key.startsWith('customer:')) out.push(key.slice('customer:'.length))
  }
  if (out.length === 0 && me.customer_id) out.push(me.customer_id)
  return out
}

/** The role name shown in the sidebar: the highest-power binding, new vocabulary. */
export function displayRole(me: Me | null | undefined): string {
  if (!me) return ''
  const first = me.roles?.[0]
  if (first) {
    // roles[] is in resolution order (config, bindings, groups); pick the most powerful.
    const order = ['sovereign-admin', 'billing-operator', 'finance-viewer', 'customer-owner', 'customer-billing', 'customer-viewer']
    const best = [...(me.roles ?? [])].sort((a, b) => order.indexOf(String(a.role)) - order.indexOf(String(b.role)))[0]
    return String(best?.role ?? first.role)
  }
  return me.role
}

/** Home route per lens. */
export function homeFor(me: Me | null | undefined): string {
  if (!me) return '/signin'
  return isSovereign(me) ? '/overview' : '/my/overview'
}

export const ROLE_LABEL: Record<string, string> = {
  'sovereign-admin': 'Sovereign admin',
  'billing-operator': 'Billing operator',
  'finance-viewer': 'Finance viewer',
  'customer-owner': 'Owner',
  'customer-billing': 'Billing',
  'customer-viewer': 'Viewer',
  operator: 'Sovereign admin',
  'customer-admin': 'Owner',
  admin: 'Owner',
  viewer: 'Viewer',
}

export function roleLabel(role: string | null | undefined): string {
  if (!role) return ''
  return ROLE_LABEL[role] ?? role
}

/** The three customer roles an owner may grant, with the copy the picker shows. */
export const CUSTOMER_ROLES: ReadonlyArray<{ role: 'customer-owner' | 'customer-billing' | 'customer-viewer'; label: string; help: string }> = [
  { role: 'customer-viewer', label: 'Viewer', help: 'reads costs, resources and invoices' },
  { role: 'customer-billing', label: 'Billing', help: 'reads, and tops up the account' },
  { role: 'customer-owner', label: 'Owner', help: 'reads, tops up, manages users, PO reference and tax registration' },
]

/** The three Sovereign roles and the three customer roles, for the Access page picker. */
export const SOVEREIGN_ROLES = ['sovereign-admin', 'billing-operator', 'finance-viewer'] as const
export const SCOPED_CUSTOMER_ROLES = ['customer-owner', 'customer-billing', 'customer-viewer'] as const

export function scopeKindOf(role: string): 'sovereign' | 'customer' | '' {
  if ((SOVEREIGN_ROLES as readonly string[]).includes(role)) return 'sovereign'
  if ((SCOPED_CUSTOMER_ROLES as readonly string[]).includes(role)) return 'customer'
  return ''
}
