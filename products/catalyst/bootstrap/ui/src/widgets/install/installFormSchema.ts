/**
 * installFormSchema.ts — pure helpers used by InstallForm. Lifted into
 * their own module so the React component file exports only the
 * component (per react-refresh/only-export-components).
 *
 * Per BLUEPRINT-AUTHORING.md §4 each schema field can carry a
 * `x-catalyst-ui-hint` extension that selects a custom widget:
 *
 *   x-catalyst-ui-hint: password         → masked input
 *   x-catalyst-ui-hint: domain-picker    → custom widget
 *   x-catalyst-ui-hint: application-ref  → existing-Applications dropdown
 *   x-catalyst-ui-hint: secret-ref       → K8s Secret reference
 *
 * Unknown hint values are tolerated (the field renders with the
 * default widget). The widget name strings (PasswordWidget,
 * DomainPickerWidget, ...) match the keys of the registry the
 * InstallForm component supplies to RJSF.
 */

import type { UiSchema } from '@rjsf/utils'

import type { CatalogItem } from '@/lib/catalog.api'

/** UIHint values consumed by the install form. */
export type UIHint = 'password' | 'domain-picker' | 'application-ref' | 'secret-ref'

const KNOWN_HINTS: ReadonlySet<UIHint> = new Set([
  'password',
  'domain-picker',
  'application-ref',
  'secret-ref',
])

/**
 * widgetForHint maps a x-catalyst-ui-hint value to the registered
 * widget name. Returns undefined for unknown hints so the caller can
 * skip the UiSchema entry and let RJSF render the default widget.
 */
export function widgetForHint(hint: UIHint): string | undefined {
  switch (hint) {
    case 'password':
      return 'PasswordWidget'
    case 'domain-picker':
      return 'DomainPickerWidget'
    case 'application-ref':
      return 'ApplicationRefWidget'
    case 'secret-ref':
      return 'SecretRefWidget'
    default:
      return undefined
  }
}

/**
 * buildUiSchema — walk the configSchema recursively and project every
 * `x-catalyst-ui-hint: <hint>` field onto an RJSF UiSchema entry that
 * picks the corresponding widget name.
 */
export function buildUiSchema(schema: unknown): UiSchema {
  const out: UiSchema = {}
  walk(schema, out)
  return out
}

function walk(schema: unknown, ui: UiSchema): void {
  if (!schema || typeof schema !== 'object') return
  const obj = schema as Record<string, unknown>
  const props = obj.properties as Record<string, unknown> | undefined
  if (!props) return
  for (const [key, raw] of Object.entries(props)) {
    if (!raw || typeof raw !== 'object') continue
    const fieldSchema = raw as Record<string, unknown>
    const hintRaw = fieldSchema['x-catalyst-ui-hint']
    if (typeof hintRaw === 'string' && KNOWN_HINTS.has(hintRaw as UIHint)) {
      const widget = widgetForHint(hintRaw as UIHint)
      if (widget) ui[key] = { ...(ui[key] ?? {}), 'ui:widget': widget }
    }
    if ((fieldSchema.type as string | undefined) === 'object' && fieldSchema.properties) {
      const nested: UiSchema = ui[key] && typeof ui[key] === 'object' ? (ui[key] as UiSchema) : {}
      walk(fieldSchema, nested)
      if (Object.keys(nested).length > 0) ui[key] = nested
    }
  }
}

/**
 * extractConfigSchema — pull `spec.configSchema` from a CatalogItem's
 * `raw` map. Returns null when the Blueprint has no configSchema.
 */
export function extractConfigSchema(bp: CatalogItem): unknown {
  if (!bp.raw || typeof bp.raw !== 'object') return null
  const spec = (bp.raw as Record<string, unknown>).spec as Record<string, unknown> | undefined
  if (!spec) return null
  const cs = spec.configSchema
  if (!cs || typeof cs !== 'object') return null
  return cs
}
