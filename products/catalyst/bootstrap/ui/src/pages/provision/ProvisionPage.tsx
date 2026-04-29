/**
 * ProvisionPage — thin re-export of the Sovereign provisioning surface.
 *
 * The original DAG view (~1300 lines of SVG bubbles + edges + supernode
 * mapping + hcloud sub-progress) has been gutted in favour of the
 * pixel-ported core/console AppsPage: a Deployments / Catalog tabs +
 * auto-fit card grid that mirrors core/console exactly, with each card
 * navigating to a per-Application AppDetail page (sections — NOT tabs).
 *
 * The route `/sovereign/provision/$deploymentId` continues to mount the
 * AppsPage component (StepReview's redirect target is unchanged) so the
 * URL contract with the wizard is preserved. This module exists ONLY to
 * keep older import paths stable.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target-state shape on
 * first commit), the new view ships at full quality — per-Application
 * status pills, expand-in-place jobs, log replay from /events — without
 * any "for now" intermediate bubble layout.
 */
export { AppsPage as ProvisionPage } from '@/pages/sovereign/AppsPage'
