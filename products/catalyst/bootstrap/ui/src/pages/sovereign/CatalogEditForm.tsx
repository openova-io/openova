/**
 * CatalogEditForm — #3648 (founder item #1, 2026-06-16): the reusable
 * catalog-entry edit FORM, extracted from CatalogEditDialog so the edit
 * happens INLINE on the catalog detail page (/catalog/bp-<name>) rather than
 * in a separate chip + modal.
 *
 * "Editing the catalog item on a separate edit chip and popup is a very bad
 *  idea. We already have the individual page and the page must be editable."
 *
 * This component is the form body ONLY — no backdrop, no dialog chrome — so
 * it drops straight into the detail page in an edit-in-place panel. The
 * modal wrapper (CatalogEditDialog) now just renders <CatalogEditForm> inside
 * its backdrop, so the two surfaces never diverge. Fields:
 *
 *   • display name      (text)
 *   • summary           (text)
 *   • supported topologies (multiselect — reuses TopologyEditor's ALL_MODES
 *     + describeMode so the vocabulary never diverges from the post-create
 *     Topology editor)
 *   • light icon        (URL or file upload → data: URI)
 *   • dark icon         (URL or file upload → data: URI)
 *
 * Save persists via saveCatalogEdit (PUT/POST /api/v1/sme/commerce/apps); the
 * caller's onSaved refreshes the page so the new name/icon/topologies render
 * live.
 */

import { useState } from 'react'
import { ALL_MODES, describeMode } from '@/widgets/topology/TopologyEditor'
import { saveCatalogEdit, type CatalogEntryEdit } from '@/lib/commerce.api'

export interface CatalogEditFormInitial {
  name: string
  summary: string
  supportedTopologies: string[]
  iconLight: string
  iconDark: string
}

export interface CatalogEditFormProps {
  /** Catalog entry id (may be `bp-`-prefixed) — keyed to the store slug. */
  blueprintId: string
  /** Initial form values from the current catalog entry. */
  initial: CatalogEditFormInitial
  /** Called with the edited fields after a successful save. */
  onSaved: (edit: CatalogEntryEdit & { slug: string }) => void
  /** Called when the operator cancels (closes the inline editor / modal). */
  onCancel: () => void
}

/** Read a chosen file as a data: URI so an uploaded icon is self-contained. */
export function fileToDataURI(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.readAsDataURL(file)
  })
}

export function CatalogEditForm({ blueprintId, initial, onSaved, onCancel }: CatalogEditFormProps) {
  const [name, setName] = useState(initial.name)
  const [summary, setSummary] = useState(initial.summary)
  const [topologies, setTopologies] = useState<string[]>(initial.supportedTopologies ?? [])
  const [iconLight, setIconLight] = useState(initial.iconLight)
  const [iconDark, setIconDark] = useState(initial.iconDark)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const toggleTopology = (mode: string) => {
    setTopologies((prev) =>
      prev.includes(mode) ? prev.filter((m) => m !== mode) : [...prev, mode],
    )
  }

  const onPickIcon = async (which: 'light' | 'dark', file: File | undefined) => {
    if (!file) return
    try {
      const uri = await fileToDataURI(file)
      if (which === 'light') setIconLight(uri)
      else setIconDark(uri)
    } catch (e) {
      setError(`icon upload failed: ${e instanceof Error ? e.message : String(e)}`)
    }
  }

  const onSave = async () => {
    setSaving(true)
    setError(null)
    const edit: CatalogEntryEdit = {
      name: name.trim(),
      tagline: summary.trim(),
      supported_topologies: topologies,
      icon_light: iconLight.trim(),
      icon_dark: iconDark.trim(),
    }
    try {
      const slug = await saveCatalogEdit(blueprintId, edit)
      onSaved({ ...edit, slug })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setSaving(false)
    }
  }

  return (
    <div className="ce-form" data-testid="catalog-edit-form">
      <style>{CATALOG_EDIT_FORM_CSS}</style>

      <label className="ce-field">
        <span className="ce-label">Display name</span>
        <input
          type="text"
          className="ce-input"
          value={name}
          onChange={(e) => setName(e.target.value)}
          data-testid="catalog-edit-name"
          placeholder="WordPress"
        />
      </label>

      <label className="ce-field">
        <span className="ce-label">Summary</span>
        <textarea
          className="ce-input ce-textarea"
          value={summary}
          onChange={(e) => setSummary(e.target.value)}
          data-testid="catalog-edit-summary"
          rows={2}
          placeholder="One-line description shown on the card"
        />
      </label>

      <div className="ce-field">
        <span className="ce-label">Supported topologies</span>
        <div className="ce-topos" data-testid="catalog-edit-topologies">
          {ALL_MODES.map((mode) => (
            <label key={mode} className="ce-topo" title={describeMode(mode)}>
              <input
                type="checkbox"
                checked={topologies.includes(mode)}
                onChange={() => toggleTopology(mode)}
                data-testid={`catalog-edit-topo-${mode}`}
              />
              <span className="ce-topo-label">{mode}</span>
            </label>
          ))}
        </div>
      </div>

      <div className="ce-field">
        <span className="ce-label">Light-theme icon</span>
        <div className="ce-icon-row">
          <input
            type="text"
            className="ce-input"
            value={iconLight}
            onChange={(e) => setIconLight(e.target.value)}
            data-testid="catalog-edit-icon-light"
            placeholder="https://… or upload"
          />
          <label className="ce-upload" data-testid="catalog-edit-icon-light-upload-label">
            Upload
            <input
              type="file"
              accept="image/*"
              className="ce-file"
              data-testid="catalog-edit-icon-light-upload"
              onChange={(e) => onPickIcon('light', e.target.files?.[0])}
            />
          </label>
        </div>
      </div>

      <div className="ce-field">
        <span className="ce-label">Dark-theme icon</span>
        <div className="ce-icon-row">
          <input
            type="text"
            className="ce-input"
            value={iconDark}
            onChange={(e) => setIconDark(e.target.value)}
            data-testid="catalog-edit-icon-dark"
            placeholder="https://… or upload"
          />
          <label className="ce-upload" data-testid="catalog-edit-icon-dark-upload-label">
            Upload
            <input
              type="file"
              accept="image/*"
              className="ce-file"
              data-testid="catalog-edit-icon-dark-upload"
              onChange={(e) => onPickIcon('dark', e.target.files?.[0])}
            />
          </label>
        </div>
      </div>

      {error ? (
        <p className="ce-error" data-testid="catalog-edit-error" role="alert">
          {error}
        </p>
      ) : null}

      <div className="ce-actions">
        <button type="button" className="ce-btn ce-btn-ghost" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
        <button
          type="button"
          className="ce-btn ce-btn-primary"
          onClick={onSave}
          disabled={saving}
          data-testid="catalog-edit-save"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

const CATALOG_EDIT_FORM_CSS = `
.ce-form { display: flex; flex-direction: column; gap: 0.85rem; }
.ce-field { display: flex; flex-direction: column; gap: 0.3rem; }
.ce-label { font-size: 0.78rem; font-weight: 600; color: var(--color-text-strong); }
.ce-input {
  width: 100%; padding: 0.5rem 0.7rem;
  background: var(--color-bg, var(--color-surface));
  border: 1px solid var(--color-border); border-radius: 8px;
  color: var(--color-text); font: inherit; font-size: 0.85rem;
}
.ce-input:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
.ce-textarea { resize: vertical; }
.ce-topos { display: flex; flex-wrap: wrap; gap: 0.6rem; }
.ce-topo { display: inline-flex; align-items: center; gap: 0.35rem; font-size: 0.8rem; cursor: pointer; color: var(--color-text); }
.ce-topo-label { text-transform: none; }
.ce-icon-row { display: flex; gap: 0.5rem; align-items: center; }
.ce-icon-row .ce-input { flex: 1; }
.ce-upload {
  position: relative; flex: 0 0 auto;
  padding: 0.5rem 0.8rem; border-radius: 8px;
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text); font-size: 0.8rem; cursor: pointer; white-space: nowrap;
}
.ce-upload:hover { border-color: var(--color-accent); }
.ce-file { position: absolute; inset: 0; opacity: 0; cursor: pointer; }
.ce-error { margin: 0; color: var(--color-danger); font-size: 0.8rem; }
.ce-actions { display: flex; justify-content: flex-end; gap: 0.6rem; margin-top: 0.4rem; }
.ce-btn {
  padding: 0.5rem 1.1rem; border-radius: 8px; border: 1px solid transparent;
  font: inherit; font-size: 0.85rem; font-weight: 600; cursor: pointer;
}
.ce-btn:disabled { opacity: 0.6; cursor: wait; }
.ce-btn-ghost { background: transparent; border-color: var(--color-border); color: var(--color-text); }
.ce-btn-ghost:hover:not(:disabled) { border-color: var(--color-accent); }
.ce-btn-primary { background: var(--color-accent); color: #fff; }
.ce-btn-primary:hover:not(:disabled) { filter: brightness(0.92); }
`
