/**
 * HandoverRedirectBanner.css.ts — banner stylesheet for
 * `./HandoverRedirectBanner.tsx`, extracted into its own module so the
 * .tsx file only exports React components (react-refresh/only-export-
 * components — non-component exports break Vite's HMR boundary).
 *
 * Style tokens mirror AppsPage.handover-ready-banner so the JobsPage
 * + AppsPage handover banners feel identical across the two surfaces.
 * The Cancel button is a ghost variant (transparent bg + dim border)
 * so the CTA stays the dominant call to action.
 */
export const HANDOVER_REDIRECT_BANNER_CSS = `
.handover-redirect-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.85rem 1rem;
  margin: 0.75rem 0 0;
  border: 1.5px solid color-mix(in srgb, var(--color-success) 55%, var(--color-border));
  border-radius: 12px;
  background: color-mix(in srgb, var(--color-success) 12%, var(--color-surface));
}
.handover-redirect-body {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
  flex: 1 1 auto;
  min-width: 0;
}
.handover-redirect-title {
  color: var(--color-success);
  font-size: 0.95rem;
  font-weight: 700;
  letter-spacing: 0.01em;
}
.handover-redirect-sub {
  color: var(--color-text);
  font-size: 0.82rem;
  line-height: 1.45;
}
.handover-redirect-count {
  display: inline-block;
  min-width: 1.2em;
  text-align: center;
  font-weight: 700;
  color: var(--color-success);
  font-variant-numeric: tabular-nums;
}
.handover-redirect-actions {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  flex: 0 0 auto;
}
.handover-redirect-cta {
  background: var(--color-success);
  color: #fff;
  padding: 0.55rem 1.1rem;
  border-radius: 8px;
  font-size: 0.88rem;
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
  border: 1px solid transparent;
  transition: filter 0.15s, box-shadow 0.15s;
}
.handover-redirect-cta:hover {
  filter: brightness(0.92);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.18);
}
.handover-redirect-cta:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.handover-redirect-cancel {
  background: transparent;
  color: var(--color-text-dim);
  padding: 0.5rem 0.9rem;
  border-radius: 8px;
  font-size: 0.85rem;
  font-weight: 500;
  white-space: nowrap;
  border: 1px solid var(--color-border);
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
}
.handover-redirect-cancel:hover {
  color: var(--color-text);
  border-color: color-mix(in srgb, var(--color-text) 30%, var(--color-border));
}
.handover-redirect-cancel:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
`
