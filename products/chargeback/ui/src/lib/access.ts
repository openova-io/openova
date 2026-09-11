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

/** The scope key a partner-scoped permission is listed under (DESIGN.md §11.5). */
export function partnerScope(partnerId: string): string {
  return `partner:${partnerId}`
}

/** Whether the principal holds any Sovereign-scoped binding. */
export function isSovereign(me: Me | null | undefined): boolean {
  if (!me) return false
  if (me.permissions && SOVEREIGN in me.permissions) return true
  if (me.roles?.some((b) => b.scope_kind === SOVEREIGN)) return true
  // A /me document from before bindings existed: the legacy role decides.
  return !me.permissions && !me.roles && me.role === 'operator'
}

/** The partners the principal is bound to, in binding order. */
export function partnerIds(me: Me | null | undefined): string[] {
  if (!me) return []
  const out: string[] = []
  for (const b of me.roles ?? []) {
    if (b.scope_kind === 'partner' && b.partner_id && !out.includes(b.partner_id)) out.push(b.partner_id)
  }
  if (out.length === 0) {
    for (const key of me.scopes ?? Object.keys(me.permissions ?? {})) {
      if (key.startsWith('partner:')) out.push(key.slice('partner:'.length))
    }
  }
  return out
}

/**
 * The PARTNER lens (DESIGN.md §11.5): a principal bound to a partner and not
 * to the Sovereign. It reads its own customers and its own partner, and
 * never the Sovereign's pages.
 */
export function isPartner(me: Me | null | undefined): boolean {
  return !isSovereign(me) && partnerIds(me).length > 0
}

/** The one partner a partner principal acts as, or null. */
export function primaryPartnerId(me: Me | null | undefined): string | null {
  return partnerIds(me)[0] ?? null
}

/** The name of that partner, when /me carried it. */
export function partnerName(me: Me | null | undefined, partnerId: string | null): string {
  if (!partnerId) return ''
  return me?.roles?.find((b) => b.partner_id === partnerId)?.partner_name ?? ''
}

/**
 * `can(me, perm)` asks at the Sovereign; `can(me, perm, customerId)` asks on
 * that customer, which a Sovereign-scoped permission also satisfies — and so
 * does a partner-scoped one on a customer of that partner.
 */
export function can(me: Me | null | undefined, perm: Permission, customerId?: string | null): boolean {
  if (!me) return false
  const perms = me.permissions
  if (perms) {
    if (perms[SOVEREIGN]?.includes(perm)) return true
    if (customerId && perms[customerScope(customerId)]?.includes(perm)) return true
    if (customerId) {
      for (const b of me.roles ?? []) {
        if (b.scope_kind !== 'partner' || !b.partner_id) continue
        if (!b.customer_ids?.includes(customerId)) continue
        if (perms[partnerScope(b.partner_id)]?.includes(perm)) return true
      }
    }
    return false
  }
  return legacyCan(me, perm, customerId)
}

/** `canPartner(me, perm, partnerId)` asks at ONE partner's scope. */
export function canPartner(me: Me | null | undefined, perm: Permission, partnerId: string | null | undefined): boolean {
  if (!me) return false
  const perms = me.permissions
  if (!perms) return legacyCan(me, perm, null)
  if (perms[SOVEREIGN]?.includes(perm)) return true
  return Boolean(partnerId && perms[partnerScope(partnerId)]?.includes(perm))
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
  // A partner binding covers the customers it expanded to.
  for (const b of me.roles ?? []) {
    for (const id of b.customer_ids ?? []) if (!out.includes(id)) out.push(id)
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
    const order = ['sovereign-admin', 'billing-operator', 'finance-viewer', 'partner-owner', 'partner-viewer', 'customer-owner', 'customer-billing', 'customer-viewer']
    const best = [...(me.roles ?? [])].sort((a, b) => order.indexOf(String(a.role)) - order.indexOf(String(b.role)))[0]
    return String(best?.role ?? first.role)
  }
  return me.role
}

/** Home route per lens. */
export function homeFor(me: Me | null | undefined): string {
  if (!me) return '/signin'
  if (isSovereign(me)) return '/overview'
  return isPartner(me) ? '/partner/overview' : '/my/overview'
}

export const ROLE_LABEL: Record<string, string> = {
  'sovereign-admin': 'Sovereign admin',
  'billing-operator': 'Billing operator',
  'finance-viewer': 'Finance viewer',
  'partner-owner': 'Partner owner',
  'partner-viewer': 'Partner viewer',
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

/** The two partner roles an owner may grant on its own partner. */
export const PARTNER_ROLES: ReadonlyArray<{ role: 'partner-owner' | 'partner-viewer'; label: string; help: string }> = [
  { role: 'partner-viewer', label: 'Viewer', help: 'reads its customers, statements, account and margin' },
  { role: 'partner-owner', label: 'Owner', help: 'reads all of that, edits the retail rule, manages users and tops up the account' },
]

/** The Sovereign roles, the partner roles and the customer roles, for the Access page picker. */
export const SOVEREIGN_ROLES = ['sovereign-admin', 'billing-operator', 'finance-viewer'] as const
export const SCOPED_PARTNER_ROLES = ['partner-owner', 'partner-viewer'] as const
export const SCOPED_CUSTOMER_ROLES = ['customer-owner', 'customer-billing', 'customer-viewer'] as const

export function scopeKindOf(role: string): 'sovereign' | 'partner' | 'customer' | '' {
  if ((SOVEREIGN_ROLES as readonly string[]).includes(role)) return 'sovereign'
  if ((SCOPED_PARTNER_ROLES as readonly string[]).includes(role)) return 'partner'
  if ((SCOPED_CUSTOMER_ROLES as readonly string[]).includes(role)) return 'customer'
  return ''
}
