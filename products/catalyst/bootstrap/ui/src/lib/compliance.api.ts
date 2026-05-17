/**
 * lib/compliance.api.ts — Wave-2 Family-E endpoint-helper shim.
 *
 * Re-exports the runtime + supply-chain compliance helpers from the
 * canonical `pages/admin/compliance/compliance.api.ts` so consumers
 * that live OUTSIDE the admin/compliance route tree (the per-Pod
 * SBOMTab on the cloud-list ResourceDetailPage, the per-App SBOM tile
 * on AppDetail, the future widget surfaces) can import without
 * reaching across the page boundary.
 *
 * The canonical surface still lives next to the dashboard page so the
 * dashboard's own imports stay local; this re-export keeps the
 * dependency direction flat (lib/* → pages/*) for everything else.
 */

export {
  getFalcoEvents,
  getSBOMForPod,
  getSBOMSummary,
  getPolicyByName,
  COMPLIANCE_FRAMEWORKS,
} from '@/pages/admin/compliance/compliance.api'
export type {
  ComplianceFramework,
  ComplianceFrameworkId,
  FalcoEvent,
  FalcoEventsResponse,
  VulnerabilitySeverityCounts,
  SBOMComponent,
  SBOMContainerEntry,
  SBOMPodResponse,
  SBOMSummaryResponse,
} from '@/pages/admin/compliance/compliance.api'
