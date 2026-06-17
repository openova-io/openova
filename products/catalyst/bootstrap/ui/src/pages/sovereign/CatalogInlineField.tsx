/**
 * CatalogInlineField — #3668 (founder item §5A, the per-field-inline half).
 *
 * The founder's verbatim complaint about the prior catalog edit: "why there is
 * a global edit button instead of setting each catalog items fields to be
 * edited independently and save globally, why this ugly no standard ux
 * approach". The pre-#3668-followup edit was ONE global "Edit" button that
 * swapped the whole hero for a monolithic 7-field form (CatalogEditForm) — the
 * exact non-standard UX the founder called out.
 *
 * This component is the standard inline-edit affordance, one per field: the
 * field renders its current value with a subtle "Edit" pencil on hover; clicking
 * it (or the value) expands an inline editor in place; Save commits THAT field
 * independently (merging onto the current values via the SAME saveCatalogEdit
 * Gitea/store seam #3702/#3710 wired, so no sibling field is clobbered); Cancel
 * reverts. Escape cancels, ⌘/Ctrl+Enter saves — standard inline-edit ergonomics.
 *
 * Field kinds (the `render`/`editor` are injected by the caller so this stays
 * generic — one mechanism for text, textarea, topology-multiselect, and the
 * visual icon picker):
 *   • the DISPLAY is whatever the caller renders for the current value;
 *   • the EDITOR is a render-prop given the working draft + a setter;
 *   • SAVE serializes the draft into a CatalogEntryEdit patch via `toPatch`.
 */

import { useEffect, useRef, useState, type ReactNode } from 'react'
import { saveCatalogEdit, type CatalogEntryEdit } from '@/lib/commerce.api'

export interface CatalogInlineFieldProps<T> {
  /** Catalog entry id (may be `bp-`-prefixed) — keyed to the store slug. */
  blueprintId: string
  /** Stable field key — drives the testids (e.g. `name`, `summary`, `icon-light`). */
  fieldKey: string
  /** Human label for the field (shown above the editor + as the a11y name). */
  label: string
  /** The FULL current catalog values — the merge base so a per-field save
   *  preserves every sibling field (saveCatalogEdit does a full upsert). */
  current: CatalogEntryEdit
  /** This field's working draft initial value (derived from `current`). */
  initialDraft: T
  /** Render the read-only display of the current value (the non-editing view). */
  renderDisplay: () => ReactNode
  /** Render the inline editor for the working draft. */
  renderEditor: (draft: T, setDraft: (next: T) => void) => ReactNode
  /** Fold the working draft into a CatalogEntryEdit patch (only this field's keys). */
  toPatch: (draft: T) => Partial<CatalogEntryEdit>
  /** Called after a successful per-field save so the caller can refetch. */
  onSaved: (saved: CatalogEntryEdit) => void
  /** Admin gate — when false the field renders display-only (no edit affordance). */
  editable: boolean
  /** Optional: render the editor inline-beside the label (text) vs block (icons). */
  block?: boolean
}

export function CatalogInlineField<T>({
  blueprintId,
  fieldKey,
  label,
  current,
  initialDraft,
  renderDisplay,
  renderEditor,
  toPatch,
  onSaved,
  editable,
  block,
}: CatalogInlineFieldProps<T>) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<T>(initialDraft)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const editorRef = useRef<HTMLDivElement>(null)

  // Re-seed the draft from the latest current value each time the editor opens,
  // so re-opening after an external refetch starts from the live value.
  const open = () => {
    setDraft(initialDraft)
    setError(null)
    setEditing(true)
  }
  const cancel = () => {
    setEditing(false)
    setError(null)
  }

  // Autofocus the first focusable control when the editor opens.
  useEffect(() => {
    if (!editing) return
    const node = editorRef.current?.querySelector<HTMLElement>(
      'input:not([type=file]), textarea, select, button[role=option], [tabindex]',
    )
    node?.focus()
  }, [editing])

  const save = async () => {
    setSaving(true)
    setError(null)
    // Merge ONLY this field's keys onto the full current values so the save is
    // surgical — every sibling field round-trips unchanged.
    const edit: CatalogEntryEdit = { ...current, ...toPatch(draft) }
    try {
      const slug = await saveCatalogEdit(blueprintId, edit)
      void slug
      setSaving(false)
      setEditing(false)
      onSaved(edit)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setSaving(false)
    }
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      cancel()
    } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void save()
    }
  }

  if (!editable) {
    // Display-only (non-admin): just the value, no affordance.
    return (
      <div className="cif" data-testid={`cif-${fieldKey}`}>
        <style>{CATALOG_INLINE_FIELD_CSS}</style>
        <div className="cif-display cif-display-readonly">{renderDisplay()}</div>
      </div>
    )
  }

  if (!editing) {
    return (
      <div className="cif" data-testid={`cif-${fieldKey}`}>
        <style>{CATALOG_INLINE_FIELD_CSS}</style>
        <button
          type="button"
          className="cif-display cif-display-editable"
          onClick={open}
          title={`Edit ${label}`}
          data-testid={`cif-${fieldKey}-edit`}
        >
          <span className="cif-value">{renderDisplay()}</span>
          <span className="cif-pencil" aria-hidden="true">
            {/* pencil glyph */}
            <svg viewBox="0 0 24 24" width={13} height={13} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 20h9" />
              <path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z" />
            </svg>
          </span>
        </button>
      </div>
    )
  }

  return (
    <div
      className={`cif cif-editing${block ? ' cif-block' : ''}`}
      data-testid={`cif-${fieldKey}`}
      ref={editorRef}
      onKeyDown={onKeyDown}
    >
      <style>{CATALOG_INLINE_FIELD_CSS}</style>
      <span className="cif-edit-label">{label}</span>
      <div className="cif-editor" data-testid={`cif-${fieldKey}-editor`}>
        {renderEditor(draft, setDraft)}
      </div>
      {error ? (
        <p className="cif-error" role="alert" data-testid={`cif-${fieldKey}-error`}>
          {error}
        </p>
      ) : null}
      <div className="cif-actions">
        <button
          type="button"
          className="cif-btn cif-btn-ghost"
          onClick={cancel}
          disabled={saving}
          data-testid={`cif-${fieldKey}-cancel`}
        >
          Cancel
        </button>
        <button
          type="button"
          className="cif-btn cif-btn-primary"
          onClick={() => void save()}
          disabled={saving}
          data-testid={`cif-${fieldKey}-save`}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

const CATALOG_INLINE_FIELD_CSS = `
.cif { display: block; }
.cif-display-readonly { color: var(--color-text); }

.cif-display-editable {
  display: inline-flex; align-items: center; gap: 0.4rem;
  border: 1px solid transparent; border-radius: 8px;
  background: transparent; color: inherit; font: inherit;
  padding: 0.1rem 0.35rem; margin: -0.1rem -0.35rem;
  cursor: pointer; text-align: left; max-width: 100%;
}
.cif-display-editable:hover { border-color: var(--color-border); background: color-mix(in srgb, var(--color-border) 22%, transparent); }
.cif-display-editable .cif-value { min-width: 0; }
.cif-pencil { color: var(--color-text-dim); opacity: 0; flex: 0 0 auto; display: inline-flex; transition: opacity 0.12s ease; }
.cif-display-editable:hover .cif-pencil, .cif-display-editable:focus-visible .cif-pencil { opacity: 1; }

.cif-editing {
  display: flex; flex-direction: column; gap: 0.45rem;
  border: 1px solid var(--color-accent); border-radius: 10px;
  padding: 0.7rem; background: var(--color-surface);
}
.cif-edit-label { font-size: 0.72rem; font-weight: 600; color: var(--color-text-strong); text-transform: uppercase; letter-spacing: 0.03em; }
.cif-editor { display: flex; flex-direction: column; gap: 0.4rem; }

.cif-input {
  width: 100%; padding: 0.45rem 0.65rem;
  background: var(--color-bg, var(--color-surface));
  border: 1px solid var(--color-border); border-radius: 8px;
  color: var(--color-text); font: inherit; font-size: 0.9rem; font-weight: 400;
}
.cif-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
.cif-textarea { resize: vertical; font-size: 0.85rem; }
.cif-topos { display: flex; flex-wrap: wrap; gap: 0.6rem; }
.cif-topo { display: inline-flex; align-items: center; gap: 0.35rem; font-size: 0.8rem; cursor: pointer; color: var(--color-text); font-weight: 400; }
.cif-topo-label { text-transform: none; }
.cif-topo-summary { font-size: 0.85rem; color: var(--color-text); font-weight: 400; }

.cif-error { margin: 0; color: var(--color-danger); font-size: 0.78rem; }
.cif-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.1rem; }
.cif-btn {
  padding: 0.4rem 0.95rem; border-radius: 8px; border: 1px solid transparent;
  font: inherit; font-size: 0.82rem; font-weight: 600; cursor: pointer;
}
.cif-btn:disabled { opacity: 0.6; cursor: wait; }
.cif-btn-ghost { background: transparent; border-color: var(--color-border); color: var(--color-text); }
.cif-btn-ghost:hover:not(:disabled) { border-color: var(--color-accent); }
.cif-btn-primary { background: var(--color-accent); color: #fff; }
.cif-btn-primary:hover:not(:disabled) { filter: brightness(0.92); }
`
