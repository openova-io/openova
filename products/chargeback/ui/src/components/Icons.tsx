import { useRef, useState, type ChangeEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { IconRef, StoredIcon } from '../api/types'
import { iconFileProblem, iconSrc, isColour, normalizeColour, resolveIconSrc } from '../lib/icons'

/**
 * One icon as the packages document names it (DESIGN.md §22.10): the image
 * on its tile colour when the item carries one. An item with no icon renders
 * NOTHING — no empty box, no broken-image frame — so a row without an icon
 * reads exactly as it did before icons existed. The image is decorative: the
 * name beside it is the label, so it carries an empty alt and is hidden from
 * assistive technology (the document's `alt` is the name, already read).
 */
export function PackageIcon({ icon, size = 18, documentUrl, className }: { icon?: IconRef | null; size?: number; documentUrl?: string; className?: string }) {
  if (!icon?.src) return null
  const src = documentUrl ? resolveIconSrc(icon.src, documentUrl) : resolveIconSrc(icon.src)
  return (
    <span className={`pkg-icon${icon.bg ? ' tiled' : ''}${className ? ` ${className}` : ''}`} style={icon.bg ? { background: icon.bg } : undefined} data-testid="pkg-icon" aria-hidden="true">
      <img src={src} alt="" width={size} height={size} loading="lazy" decoding="async" />
    </span>
  )
}

/**
 * The icon control of a modal: a preview, Upload (a file input; the file is
 * POSTed raw to /icons, which is content-addressed, so uploading the same
 * file twice gives the same id), Choose existing (a grid of what BSS already
 * holds, from GET /icons, inside the same modal) and Remove. `value` is the
 * icon id, "" for none.
 */
export function IconField({ label = 'Icon', value, onChange, bg, help }: { label?: string; value: string; onChange: (id: string) => void; bg?: string; help?: string }) {
  const file = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [choosing, setChoosing] = useState(false)
  const [stored, setStored] = useState<StoredIcon[] | null>(null)

  const upload = async (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    e.target.value = ''
    if (!f) return
    const problem = iconFileProblem(f)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    setError('')
    try {
      const out = await api.postRaw<{ id: string }>('/icons', f, f.type)
      onChange(out.id)
      setChoosing(false)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  const openChooser = async () => {
    setChoosing((c) => !c)
    if (stored) return
    try {
      setStored(asList<StoredIcon>(await api.get<unknown>('/icons'), 'icons'))
    } catch (err) {
      setError(errorText(err))
      setStored([])
    }
  }
  const tile = normalizeColour(bg ?? '')
  return (
    <div className="field" data-testid="icon-field">
      <label>{label}</label>
      <div className="row icon-field">
        <span className={`icon-preview${tile && isColour(tile) ? ' tiled' : ''}`} style={tile && isColour(tile) ? { background: tile } : undefined} data-testid="icon-preview">
          {value ? <img src={resolveIconSrc(iconSrc(value))} alt="The chosen icon" width={28} height={28} /> : <span className="tiny muted">none</span>}
        </span>
        <span className="btn-row">
          <button type="button" className="small" disabled={busy} onClick={() => file.current?.click()}>
            {busy ? 'Uploading…' : 'Upload'}
          </button>
          <button type="button" className="small" disabled={busy} onClick={() => void openChooser()} aria-expanded={choosing}>
            Choose existing
          </button>
          {value ? (
            <button type="button" className="small link danger" disabled={busy} onClick={() => onChange('')}>
              Remove
            </button>
          ) : null}
        </span>
        <input ref={file} type="file" accept="image/svg+xml,image/png,image/webp" hidden onChange={(e) => void upload(e)} aria-label={`Upload ${label.toLowerCase()}`} data-testid="icon-file" />
      </div>
      {help ? <div className="help">{help}</div> : null}
      {error ? <div className="err small">{error}</div> : null}
      {choosing ? (
        <div className="icon-grid" role="listbox" aria-label="Stored icons" data-testid="icon-grid">
          {stored === null ? <span className="tiny muted">Loading…</span> : null}
          {stored && stored.length === 0 ? <span className="tiny muted">No icon is stored yet — upload one.</span> : null}
          {(stored ?? []).map((ic) => (
            <button
              key={ic.id}
              type="button"
              role="option"
              aria-selected={ic.id === value}
              className={`icon-choice${ic.id === value ? ' chosen' : ''}`}
              title={`${ic.content_type} · ${ic.size} bytes · shown by ${ic.references}`}
              aria-label={`Icon ${ic.id.slice(0, 8)}`}
              onClick={() => {
                onChange(ic.id)
                setChoosing(false)
              }}
            >
              <img src={resolveIconSrc(ic.src)} alt="" width={24} height={24} loading="lazy" />
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}

/** A "#RRGGBB" colour: a swatch picker, the text, and Clear. "" = none. */
export function ColourField({ label, value, onChange, help }: { label: string; value: string; onChange: (v: string) => void; help?: string }) {
  const ok = isColour(value)
  return (
    <div className="field">
      <label>{label}</label>
      <div className="row colour-field">
        <input type="color" value={ok && value ? value : '#FFFFFF'} onChange={(e) => onChange(normalizeColour(e.target.value))} aria-label={`${label} swatch`} />
        <input className="mono" value={value} onChange={(e) => onChange(e.target.value)} placeholder="#RRGGBB" aria-label={label} size={9} />
        {value ? (
          <button type="button" className="small link" onClick={() => onChange('')}>
            Clear
          </button>
        ) : null}
      </div>
      {help ? <div className="help">{help}</div> : null}
      {!ok ? <div className="err small">a colour is written #RRGGBB (six hex digits)</div> : null}
    </div>
  )
}
