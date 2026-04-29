/**
 * ProvisionPage — thin re-export of the Sovereign Admin landing surface.
 *
 * The original DAG view (~1300 lines of SVG bubbles + edges + supernode
 * mapping + hcloud sub-progress) has been gutted in favour of the
 * application card grid the operator chose: every Application installed
 * on this Sovereign renders as a card from first paint, click any card
 * for the per-Application page with Logs / Dependencies / Status /
 * Overview tabs. See `src/pages/sovereign/AdminPage.tsx` for the new
 * implementation.
 *
 * The route `/sovereign/provision/$deploymentId` continues to mount this
 * file (StepReview's redirect target is unchanged) so the URL contract
 * with the wizard is preserved. This module exists ONLY to keep that
 * import path stable; all behaviour is provided by AdminPage.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target-state shape on
 * first commit), the new view ships at full quality — per-Application
 * status pills, dependency tabs, log replay from /events — without any
 * "for now" intermediate bubble layout.
 */
export { AdminPage as ProvisionPage } from '@/pages/sovereign/AdminPage'
