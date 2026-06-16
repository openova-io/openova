/**
 * IconPicker — #3668 (founder item §5B, the visual icon-picker half).
 *
 * The founder's verbatim complaint about the prior catalog edit: "why I am not
 * able to see the already uploaded logos? i need to have profile icon picker
 * like approach." The pre-#3668-followup edit form exposed the icon as a bare
 * filename text input + an opaque "Upload" button — the operator could neither
 * SEE the logos already shipped with the console nor pick one visually.
 *
 * This is the profile-icon-picker-style grid: it renders the vendored
 * `public/component-logos/*` assets (via `AVAILABLE_ICONS`) as a grid of real,
 * clickable thumbnails, shows a live preview of the CURRENT selection, lets the
 * operator clear it, paste a custom URL, or upload a new image (→ data: URI).
 * The selected thumbnail is highlighted so the current value is obvious.
 *
 * Theme-aware: the preview + grid tiles render on the console surface for the
 * active theme, so a dark-theme icon is previewed against the dark surface it
 * will live on (the founder's "theme icons" requirement). One picker component
 * is reused for both the light-theme and dark-theme icon fields — `which` only
 * drives the testids + labels so the two never diverge.
 */

import { useState } from 'react'
import { AVAILABLE_ICONS, findGalleryIcon } from '@/shared/lib/catalogIconGallery'

export interface IconPickerProps {
  /** Which theme icon this picker edits — drives labels + testids only. */
  which: 'light' | 'dark'
  /** The current icon src (a vendored url, a custom URL, or a data: URI). */
  value: string
  /** Called with the new src whenever the operator picks / types / uploads. */
  onChange: (src: string) => void
}

/** Read a chosen file as a data: URI so an uploaded icon is self-contained.
 *  Module-private — the picker owns its own upload→data-URI conversion. */
function fileToDataURI(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.readAsDataURL(file)
  })
}

export function IconPicker({ which, value, onChange }: IconPickerProps) {
  const [uploadError, setUploadError] = useState<string | null>(null)
  const current = (value ?? '').trim()
  const selected = findGalleryIcon(current)
  // A value that is set but is NOT one of the gallery thumbnails is a custom
  // URL or an uploaded data: URI — preview it as the active selection too.
  const isCustom = current !== '' && !selected

  const onUpload = async (file: File | undefined) => {
    if (!file) return
    setUploadError(null)
    try {
      const uri = await fileToDataURI(file)
      onChange(uri)
    } catch (e) {
      setUploadError(`upload failed: ${e instanceof Error ? e.message : String(e)}`)
    }
  }

  return (
    <div className="iconpicker" data-testid={`iconpicker-${which}`}>
      <style>{ICON_PICKER_CSS}</style>

      {/* Current selection preview */}
      <div className="ip-preview" data-testid={`iconpicker-${which}-preview`}>
        {current !== '' ? (
          <img
            src={current}
            alt={`${which} icon preview`}
            className="ip-preview-img"
            data-testid={`iconpicker-${which}-preview-img`}
            loading="lazy"
          />
        ) : (
          <span className="ip-preview-empty" data-testid={`iconpicker-${which}-preview-empty`}>
            no icon
          </span>
        )}
        <div className="ip-preview-meta">
          <span className="ip-preview-label">
            {selected ? selected.label : isCustom ? 'custom image' : 'letter-mark fallback'}
          </span>
          {current !== '' ? (
            <button
              type="button"
              className="ip-clear"
              onClick={() => onChange('')}
              data-testid={`iconpicker-${which}-clear`}
            >
              Clear
            </button>
          ) : null}
        </div>
      </div>

      {/* Visual gallery of the vendored logos */}
      <div className="ip-grid" role="listbox" aria-label={`${which} icon gallery`} data-testid={`iconpicker-${which}-grid`}>
        {AVAILABLE_ICONS.map((g) => {
          const isSel = selected?.url === g.url
          return (
            <button
              type="button"
              key={g.id}
              role="option"
              aria-selected={isSel}
              title={g.label}
              className={`ip-tile${isSel ? ' ip-tile-selected' : ''}`}
              onClick={() => onChange(g.url)}
              data-testid={`iconpicker-${which}-tile-${g.id}`}
            >
              <img src={g.url} alt={g.label} className="ip-tile-img" loading="lazy" />
            </button>
          )
        })}
      </div>

      {/* Custom URL + upload escape hatches */}
      <div className="ip-custom">
        <input
          type="text"
          className="ip-url"
          value={current}
          onChange={(e) => onChange(e.target.value)}
          placeholder="https://… (or pick / upload above)"
          aria-label={`${which} icon URL`}
          data-testid={`iconpicker-${which}-url`}
        />
        <label className="ip-upload" data-testid={`iconpicker-${which}-upload-label`}>
          Upload new
          <input
            type="file"
            accept="image/*"
            className="ip-file"
            data-testid={`iconpicker-${which}-upload`}
            onChange={(e) => onUpload(e.target.files?.[0])}
          />
        </label>
      </div>

      {uploadError ? (
        <p className="ip-error" role="alert" data-testid={`iconpicker-${which}-error`}>
          {uploadError}
        </p>
      ) : null}
    </div>
  )
}

const ICON_PICKER_CSS = `
.iconpicker { display: flex; flex-direction: column; gap: 0.6rem; }

.ip-preview {
  display: flex; align-items: center; gap: 0.7rem;
  padding: 0.55rem 0.7rem;
  border: 1px solid var(--color-border); border-radius: 10px;
  background: var(--color-surface);
}
.ip-preview-img {
  width: 44px; height: 44px; border-radius: 10px; object-fit: contain;
  background: var(--color-bg, var(--color-surface)); padding: 4px;
  border: 1px solid var(--color-border); flex: 0 0 auto;
}
.ip-preview-empty {
  width: 44px; height: 44px; border-radius: 10px;
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 0.62rem; color: var(--color-text-dim);
  border: 1px dashed var(--color-border); flex: 0 0 auto; text-align: center;
}
.ip-preview-meta { display: flex; flex-direction: column; gap: 0.2rem; min-width: 0; }
.ip-preview-label { font-size: 0.78rem; color: var(--color-text); }
.ip-clear {
  align-self: flex-start; padding: 0; border: none; background: transparent;
  color: var(--color-accent); font-size: 0.72rem; font-weight: 600; cursor: pointer;
}
.ip-clear:hover { text-decoration: underline; }

.ip-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(44px, 1fr));
  gap: 0.4rem;
  max-height: 168px; overflow-y: auto;
  padding: 0.5rem;
  border: 1px solid var(--color-border); border-radius: 10px;
  background: var(--color-bg, var(--color-surface));
}
.ip-tile {
  width: 100%; aspect-ratio: 1 / 1;
  display: inline-flex; align-items: center; justify-content: center;
  padding: 5px; border-radius: 8px; cursor: pointer;
  border: 1px solid transparent; background: var(--color-surface);
  transition: border-color 0.12s ease, transform 0.12s ease;
}
.ip-tile:hover { border-color: var(--color-accent); transform: translateY(-1px); }
.ip-tile-selected { border-color: var(--color-accent); box-shadow: 0 0 0 1px var(--color-accent) inset; }
.ip-tile-img { max-width: 100%; max-height: 100%; object-fit: contain; }

.ip-custom { display: flex; gap: 0.5rem; align-items: center; }
.ip-url {
  flex: 1; min-width: 0; padding: 0.45rem 0.65rem;
  background: var(--color-bg, var(--color-surface));
  border: 1px solid var(--color-border); border-radius: 8px;
  color: var(--color-text); font: inherit; font-size: 0.82rem;
}
.ip-url:focus { outline: 2px solid var(--color-accent); border-color: transparent; }
.ip-upload {
  position: relative; flex: 0 0 auto;
  padding: 0.45rem 0.75rem; border-radius: 8px;
  border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text); font-size: 0.78rem; cursor: pointer; white-space: nowrap;
}
.ip-upload:hover { border-color: var(--color-accent); }
.ip-file { position: absolute; inset: 0; opacity: 0; cursor: pointer; }
.ip-error { margin: 0; color: var(--color-danger); font-size: 0.78rem; }
`
