/**
 * InstallForm — auto-form generator for Blueprint installs.
 *
 * EPIC-2 Slice I (#1097, I2): given a CatalogItem with a populated
 * `raw.spec.configSchema`, render a JSON-Schema-driven form via
 * `@rjsf/core` + `@rjsf/validator-ajv8`. The submit button posts the
 * collected parameters; a separate Preview button asks the catalyst-api
 * to render the would-be manifests without committing.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 we use the canonical JSON-Schema-
 * to-React-Form library (RJSF). NO custom form generator. This means a
 * Blueprint author who follows BLUEPRINT-AUTHORING.md §3-§5 gets a
 * usable install form with zero UI work.
 *
 * Per BLUEPRINT-AUTHORING.md §4 each schema field can carry a
 * `x-catalyst-ui-hint` extension that selects a custom widget:
 *
 *   x-catalyst-ui-hint: password         → masked input
 *   x-catalyst-ui-hint: domain-picker    → calls catalyst-api domains list
 *   x-catalyst-ui-hint: application-ref  → existing-Applications dropdown
 *   x-catalyst-ui-hint: secret-ref       → K8s Secret reference
 *
 * The `password` hint is wired in slice I; the other three are stubbed
 * to fall back to the default string input — they ship as separate
 * widget files in a follow-up slice (I-followup) so the install form's
 * minimum-viable surface lands in I.
 */

import { useMemo, useState } from 'react'
import RJSFForm from '@rjsf/core'
import validator from '@rjsf/validator-ajv8'
import type {
  RJSFSchema,
  RegistryWidgetsType,
  WidgetProps,
} from '@rjsf/utils'

import type { CatalogItem } from '@/lib/catalog.api'
import { buildUiSchema, extractConfigSchema } from './installFormSchema'

/* ── Custom widgets ──────────────────────────────────────────────── */

/**
 * PasswordWidget — masked input. Re-renders the default string widget
 * with `type="password"` so the value never appears on screen. Per
 * docs/INVIOLABLE-PRINCIPLES.md #10 (output hygiene) we never log the
 * value either; the only consumer is the install POST body.
 */
function PasswordWidget(props: WidgetProps) {
  const { id, value, required, disabled, readonly, onChange, placeholder } = props
  return (
    <input
      type="password"
      id={id}
      autoComplete="new-password"
      disabled={disabled}
      readOnly={readonly}
      required={required}
      placeholder={placeholder ?? ''}
      value={value ?? ''}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
      data-testid="install-form-password-input"
    />
  )
}

/**
 * DomainPickerWidget — placeholder for the domain-picker custom widget.
 * Today renders a plain text input with a hint that the operator may
 * supply any reachable domain; a follow-up slice wires the
 * `/api/v1/sovereigns/{id}/domains` REST call (catalyst-api emits the
 * Sovereign's owned-domains list including the per-Org subdomain
 * pool).
 */
function DomainPickerWidget(props: WidgetProps) {
  const { id, value, required, disabled, readonly, onChange, placeholder } = props
  return (
    <input
      type="text"
      id={id}
      disabled={disabled}
      readOnly={readonly}
      required={required}
      placeholder={placeholder ?? 'shop.acme.com'}
      value={value ?? ''}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
      data-testid="install-form-domain-input"
    />
  )
}

/**
 * ApplicationRefWidget — placeholder for the existing-Applications
 * dropdown. Renders the default string input today; a follow-up slice
 * wires `GET /api/v1/sovereigns/{id}/applications` and renders a
 * dropdown of the names the operator can target.
 */
function ApplicationRefWidget(props: WidgetProps) {
  const { id, value, required, disabled, readonly, onChange, placeholder } = props
  return (
    <input
      type="text"
      id={id}
      disabled={disabled}
      readOnly={readonly}
      required={required}
      placeholder={placeholder ?? 'wp-prod'}
      value={value ?? ''}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
      data-testid="install-form-app-ref-input"
    />
  )
}

/**
 * SecretRefWidget — placeholder for the K8s Secret reference. Renders
 * the default string input today; a follow-up slice wires the
 * `/api/v1/sovereigns/{id}/secrets` REST call.
 */
function SecretRefWidget(props: WidgetProps) {
  const { id, value, required, disabled, readonly, onChange, placeholder } = props
  return (
    <input
      type="text"
      id={id}
      disabled={disabled}
      readOnly={readonly}
      required={required}
      placeholder={placeholder ?? 'my-secret'}
      value={value ?? ''}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
      data-testid="install-form-secret-ref-input"
    />
  )
}

/**
 * Widget registry — RJSF expects a `widgets` map keyed by widget name.
 * The UiSchema below sets `ui:widget` to one of these names per field
 * that carries a known x-catalyst-ui-hint.
 */
const installFormWidgets: RegistryWidgetsType = {
  PasswordWidget,
  DomainPickerWidget,
  ApplicationRefWidget,
  SecretRefWidget,
}

/* ── Public component ────────────────────────────────────────────── */

export interface InstallFormProps {
  blueprint: CatalogItem
  /** Submit-time callback. Receives the collected parameters. */
  onSubmit: (parameters: Record<string, unknown>) => void
  /** Preview-button callback. Same parameters as onSubmit. */
  onPreview?: (parameters: Record<string, unknown>) => void
  /** Optional initial values (e.g. when editing a draft). */
  initialFormData?: Record<string, unknown>
  /** Disable the submit button (e.g. while a parent install is in flight). */
  disabled?: boolean
  /**
   * Submit-button label. Defaults to `Install` (the wizard install
   * surface). The AppDetail SettingsTab passes `Save` so the same form
   * doubles as a Day-2 parameter editor without re-implementing the
   * RJSF + configSchema plumbing.
   */
  submitLabel?: string
}

/**
 * InstallForm — renders a Blueprint's configSchema as a form. Submits
 * the collected parameters via `onSubmit`; offers a Preview button
 * that surfaces the same parameters via `onPreview` so the parent can
 * open a modal showing the would-be manifests.
 */
export function InstallForm({
  blueprint,
  onSubmit,
  onPreview,
  initialFormData,
  disabled,
  submitLabel = 'Install',
}: InstallFormProps) {
  const schema = useMemo(() => extractConfigSchema(blueprint), [blueprint])
  const uiSchema = useMemo(() => buildUiSchema(schema), [schema])
  const [formData, setFormData] = useState<Record<string, unknown> | undefined>(initialFormData)

  if (!schema) {
    return (
      <div
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-elev)] px-4 py-6 text-sm text-[var(--color-text-dim)]"
        data-testid="install-form-no-schema"
      >
        Blueprint <strong>{blueprint.name}</strong>@{blueprint.version} declares no
        configSchema — installing with default parameters.
        <div className="mt-3 flex gap-2">
          {onPreview ? (
            <button
              type="button"
              className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
              data-testid="install-form-preview-btn"
              disabled={disabled}
              onClick={() => onPreview({})}
            >
              Preview
            </button>
          ) : null}
          <button
            type="button"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90"
            data-testid="install-form-submit-btn"
            disabled={disabled}
            onClick={() => onSubmit({})}
          >
            {submitLabel}
          </button>
        </div>
      </div>
    )
  }

  return (
    <RJSFForm
      schema={schema as RJSFSchema}
      uiSchema={uiSchema}
      validator={validator}
      widgets={installFormWidgets}
      formData={formData}
      onChange={(e) => setFormData(e.formData as Record<string, unknown>)}
      onSubmit={(e) => onSubmit((e.formData ?? {}) as Record<string, unknown>)}
      disabled={disabled}
    >
      <div className="mt-4 flex gap-2" data-testid="install-form-actions">
        {onPreview ? (
          <button
            type="button"
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
            data-testid="install-form-preview-btn"
            disabled={disabled}
            onClick={() => onPreview((formData ?? {}) as Record<string, unknown>)}
          >
            Preview
          </button>
        ) : null}
        <button
          type="submit"
          className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90"
          data-testid="install-form-submit-btn"
          disabled={disabled}
        >
          {submitLabel}
        </button>
      </div>
    </RJSFForm>
  )
}

