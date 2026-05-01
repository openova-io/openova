/**
 * cloudListCss — shared CSS for the Cloud per-resource list pages
 * (P3 of #309). Lives in its own module so the components file
 * (cloudListShared.tsx) only exports React components, keeping the
 * react-refresh/only-export-components rule clean.
 */

export const CLOUD_LIST_CSS = `
.cloud-list-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
  margin-bottom: 0.75rem;
}
.cloud-list-search-wrap {
  position: relative;
  flex: 1 1 280px;
  min-width: 240px;
  max-width: 480px;
}
.cloud-list-search-icon {
  position: absolute;
  left: 0.6rem;
  top: 50%;
  transform: translateY(-50%);
  width: 14px;
  height: 14px;
  color: var(--color-text-dim);
}
.cloud-list-search-input {
  width: 100%;
  padding: 0.45rem 0.7rem 0.45rem 1.9rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  color: var(--color-text);
  font-size: 0.85rem;
  outline: none;
  transition: border-color 0.15s ease;
}
.cloud-list-search-input:focus { border-color: var(--color-accent); }

.cloud-list-filters {
  display: flex;
  gap: 0.6rem;
  align-items: center;
  flex-wrap: wrap;
}
.cloud-list-filter-label {
  display: inline-flex;
  flex-direction: column;
  gap: 0.15rem;
}
.cloud-list-filter-caption {
  font-size: 0.62rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.cloud-list-filter-select {
  padding: 0.32rem 0.5rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  cursor: pointer;
}
.cloud-list-result-count {
  font-size: 0.72rem;
  color: var(--color-text-dim);
  align-self: flex-end;
  margin-left: auto;
  padding-bottom: 0.32rem;
  font-variant-numeric: tabular-nums;
}

.cloud-list-table-scroll {
  width: 100%;
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
}
.cloud-list-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
.cloud-list-th {
  padding: 0.55rem 0.8rem;
  text-align: left;
  background: color-mix(in srgb, var(--color-border) 35%, transparent);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-dim);
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-weight: 600;
  white-space: nowrap;
  user-select: none;
}
.cloud-list-th-sortable { cursor: pointer; }
.cloud-list-th-sortable:hover { color: var(--color-text); }
.cloud-list-th-content { display: inline-flex; gap: 4px; align-items: center; }
.cloud-list-th-arrow { color: var(--color-accent); }

.cloud-list-row {
  border-bottom: 1px solid var(--color-border);
  transition: background-color 0.12s ease;
  cursor: pointer;
}
.cloud-list-row:last-of-type { border-bottom: none; }
.cloud-list-row:hover {
  background: color-mix(in srgb, var(--color-accent) 5%, transparent);
}
.cloud-list-cell {
  padding: 0.55rem 0.8rem;
  vertical-align: middle;
  color: var(--color-text);
}
.cloud-list-cell-mono { font-family: var(--font-mono, ui-monospace, monospace); }
.cloud-list-cell-name { font-weight: 500; color: var(--color-text-strong); }
.cloud-list-empty-row {
  padding: 2rem 1rem;
  text-align: center;
  color: var(--color-text-dim);
  font-size: 0.85rem;
}

.cloud-list-status {
  display: inline-block;
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-weight: 700;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  white-space: nowrap;
}
.cloud-list-status[data-status="healthy"]  { background: color-mix(in srgb, var(--color-success) 18%, transparent); color: var(--color-success); }
.cloud-list-status[data-status="degraded"] { background: color-mix(in srgb, var(--color-warn) 18%, transparent);    color: var(--color-warn); }
.cloud-list-status[data-status="failed"]   { background: color-mix(in srgb, var(--color-danger) 18%, transparent);  color: var(--color-danger); }
.cloud-list-status[data-status="unknown"]  { background: color-mix(in srgb, var(--color-text-dim) 18%, transparent); color: var(--color-text-dim); }

.cloud-list-pagination {
  display: flex;
  gap: 0.6rem;
  align-items: center;
  justify-content: flex-end;
  padding: 0.6rem 0.2rem 0;
  font-size: 0.8rem;
  color: var(--color-text-dim);
}
.cloud-list-pagination button {
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text);
  border-radius: 6px;
  padding: 0.25rem 0.6rem;
  font-size: 0.78rem;
  cursor: pointer;
}
.cloud-list-pagination button:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
.cloud-list-pagination-label {
  font-variant-numeric: tabular-nums;
}

.cloud-list-empty {
  margin-top: 2rem;
  text-align: center;
  color: var(--color-text-dim);
  padding: 2rem 1rem;
  border: 1px dashed var(--color-border);
  border-radius: 12px;
  background: var(--color-bg-2);
}
.cloud-list-empty-title {
  font-size: 0.95rem;
  color: var(--color-text-strong);
  font-weight: 600;
  margin: 0 0 0.3rem;
}
.cloud-list-empty-body {
  font-size: 0.82rem;
  margin: 0;
}

.cloud-list-drawer-backdrop {
  position: fixed;
  inset: 0;
  z-index: 60;
  background: color-mix(in srgb, #000 45%, transparent);
  display: flex;
  justify-content: flex-end;
}
.cloud-list-drawer {
  width: min(520px, 92vw);
  height: 100vh;
  background: var(--color-bg-2);
  border-left: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  animation: cloud-list-drawer-slide 0.16s ease-out;
}
@keyframes cloud-list-drawer-slide {
  from { transform: translateX(40px); opacity: 0; }
  to   { transform: translateX(0);    opacity: 1; }
}
.cloud-list-drawer-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.85rem 1rem;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.cloud-list-drawer-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--color-text-strong);
  margin: 0;
}
.cloud-list-drawer-close {
  background: transparent;
  border: none;
  color: var(--color-text-dim);
  font-size: 1.4rem;
  line-height: 1;
  cursor: pointer;
  padding: 0 0.3rem;
}
.cloud-list-drawer-close:hover { color: var(--color-text); }
.cloud-list-drawer-body {
  flex: 1;
  overflow-y: auto;
  padding: 1rem;
}

.cloud-list-detail-row {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 0.6rem;
  padding: 0.45rem 0;
  border-bottom: 1px solid color-mix(in srgb, var(--color-border) 60%, transparent);
}
.cloud-list-detail-row:last-of-type { border-bottom: none; }
.cloud-list-detail-row-label {
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-dim);
}
.cloud-list-detail-row-value {
  font-size: 0.85rem;
  color: var(--color-text);
  word-break: break-word;
}
.cloud-list-detail-row-mono { font-family: var(--font-mono, ui-monospace, monospace); }

.cloud-list-tile-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 0.85rem;
}
.cloud-list-tile {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  padding: 1rem;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
  color: var(--color-text);
  text-decoration: none;
  transition: border-color 0.12s ease, transform 0.12s ease;
}
.cloud-list-tile:hover {
  border-color: var(--color-accent);
  transform: translateY(-1px);
}
.cloud-list-tile-name {
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--color-text-strong);
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.5rem;
}
.cloud-list-tile-count {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 0.08rem 0.5rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
}
.cloud-list-tile-tagline {
  font-size: 0.78rem;
  color: var(--color-text-dim);
  margin: 0;
}
`
