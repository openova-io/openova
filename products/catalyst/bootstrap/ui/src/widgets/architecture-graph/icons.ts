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
  IconBucketDroplet,
  IconCloud,
  IconCpu,
  IconDatabase,
  IconDisc,
  IconMapPin,
  IconNetwork,
  IconRouteAltLeft,
  IconStack2,
  IconStack3,
  IconWorld,
  type Icon,
} from '@tabler/icons-react'
import type { ArchNodeType } from './types'

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
}
