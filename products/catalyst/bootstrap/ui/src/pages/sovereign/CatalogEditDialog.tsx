/**
 * CatalogEditDialog — #3603 (EPIC #3597): modal wrapper around the reusable
 * CatalogEditForm (#3648, founder item #1).
 *
 * The edit FORM now lives in CatalogEditForm so the catalog DETAIL page can
 * render it inline (edit-in-place — the founder's "the page must be editable"
 * directive). This dialog is the modal presentation, retained for any caller
 * that still wants a popup; the catalog flow opens the inline editor on the
 * detail page instead. Testids + behaviour are preserved so existing tests
 * keep passing.
 */

import { CatalogEditForm, type CatalogEditFormInitial } from './CatalogEditForm'
import { type CatalogEntryEdit } from '@/lib/commerce.api'

export interface CatalogEditDialogProps {
  /** Catalog entry id (may be `bp-`-prefixed) — keyed to the store slug. */
  blueprintId: string
  /** Initial form values from the current catalog card. */
  initial: CatalogEditFormInitial
  onClose: () => void
  /** Called with the edited fields after a successful save. */
  onSaved: (edit: CatalogEntryEdit & { slug: string }) => void
}

export function CatalogEditDialog({ blueprintId, initial, onClose, onSaved }: CatalogEditDialogProps) {
  return (
    <div
      className="catalog-edit-backdrop"
      data-testid="catalog-edit-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <style>{CATALOG_EDIT_DIALOG_CSS}</style>
      <div
        className="catalog-edit-dialog"
        role="dialog"
        aria-modal="true"
        aria-label={`Edit catalog entry ${initial.name}`}
        data-testid="catalog-edit-dialog"
      >
        <div className="ce-header">
          <h2 className="ce-title">Edit catalog entry</h2>
          <button type="button" className="ce-close" aria-label="Close" onClick={onClose}>
            ×
          </button>
        </div>
        <CatalogEditForm
          blueprintId={blueprintId}
          initial={initial}
          onSaved={onSaved}
          onCancel={onClose}
        />
      </div>
    </div>
  )
}

const CATALOG_EDIT_DIALOG_CSS = `
.catalog-edit-backdrop {
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(0,0,0,0.55);
  display: flex; align-items: center; justify-content: center;
  padding: 1rem;
}
.catalog-edit-dialog {
  width: min(540px, 100%);
  max-height: 90vh; overflow-y: auto;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 14px;
  padding: 1.25rem;
  display: flex; flex-direction: column; gap: 0.85rem;
  box-shadow: 0 12px 48px rgba(0,0,0,0.35);
}
.ce-header { display: flex; align-items: center; justify-content: space-between; }
.ce-title { margin: 0; font-size: 1.05rem; font-weight: 700; color: var(--color-text-strong); }
.ce-close {
  background: transparent; border: none; cursor: pointer;
  font-size: 1.4rem; line-height: 1; color: var(--color-text-dim);
}
.ce-close:hover { color: var(--color-text); }
`
