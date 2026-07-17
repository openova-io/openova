/**
 * icons.ts — per-type tabler icon mapping for the architecture graph
 * (#348 item 10). Lives in its own module so GraphCanvas.tsx can be
 * a pure component file (react-refresh/only-export-components).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the mapping
 * is data: any new ArchNodeType added in types.ts MUST get a row
 * here, otherwise the canvas falls back to a plain disc.
 */

import {
  IconArrowsSplit,
  IconBox,
  IconBoxMultiple,
  IconBucketDroplet,
  IconBuildingCommunity,
  IconCertificate,
  IconCircleDot,
  IconCloud,
  IconCopy,
  IconCpu,
  IconDatabase,
  IconDisc,
  IconFileText,
  IconFolderOpen,
  IconKey,
  IconLayersDifference,
  IconLockSquare,
  IconMapPin,
  IconNetwork,
  IconPackage,
  IconRefresh,
  IconRouteAltLeft,
  IconShieldLock,
  IconStack2,
  IconStack3,
  IconUserShield,
  IconWorld,
  type Icon,
} from '@tabler/icons-react'
import type { ArchNodeType } from './types'

// NOTE (#3958): the unified Cloud-graph canvas renders SHAPES, not
// icons. NODE_ICON is retained only for the off-canvas surfaces — the
// detail-panel neighbour list and the add-chip popover — where a tiny
// glyph reads better than a bare dot. The canvas itself never reads it.
export const NODE_ICON: Record<ArchNodeType, Icon> = {
  Cloud: IconCloud,
  Region: IconMapPin,
  Cluster: IconBox,
  vCluster: IconStack3,
  NodePool: IconStack2,
  WorkerNode: IconCpu,
  LoadBalancer: IconArrowsSplit,
  Network: IconNetwork,
  PVC: IconDatabase,
  Bucket: IconBucketDroplet,
  Volume: IconDisc,
  Service: IconWorld,
  Ingress: IconRouteAltLeft,
  // K8s-side
  Namespace: IconFolderOpen,
  Pod: IconCircleDot,
  Deployment: IconBoxMultiple,
  StatefulSet: IconLayersDifference,
  DaemonSet: IconLayersDifference,
  ReplicaSet: IconCopy,
  ConfigMap: IconFileText,
  Secret: IconLockSquare,
  // Reconciler-side
  HelmRelease: IconPackage,
  Kustomization: IconRefresh,
  Certificate: IconCertificate,
  ExternalSecret: IconKey,
  Application: IconBox,
  Environment: IconStack3,
  Organization: IconBuildingCommunity,
  Continuum: IconCloud,
  UserAccess: IconUserShield,
  Gateway: IconRouteAltLeft,
  HTTPRoute: IconRouteAltLeft,
  NetworkPolicy: IconShieldLock,
  CiliumNetworkPolicy: IconShieldLock,
  Database: IconDatabase,
}
