/**
 * Public surface of the architecture-graph widget package.
 *
 * Two layers:
 *   • GraphCanvas — reusable, low-level force-directed canvas
 *   • ArchitectureGraphPage — page-level orchestrator (data adapter +
 *     density slider + search + detail panel + context menu + CRUD)
 */

export { GraphCanvas, type GraphCanvasHandle, type GraphCanvasProps } from './GraphCanvas'
export {
  ArchitectureGraphPage,
  type ArchitectureGraphPageProps,
} from './ArchitectureGraphPage'
export { hierarchyToGraph } from './adapter'
export { k8sToGraph, mergeGraphs } from './k8sAdapter'
export {
  GRAPH_K8S_KINDS,
  objectKey,
  useK8sCacheStream,
  type K8sObject,
  type K8sSnapshot,
  type K8sStreamEvent,
} from './useK8sCacheStream'
export { reconcilersToGraph } from './reconcilerAdapter'
export {
  edgeNodeId,
  DEFAULT_INACTIVE_TYPES,
  NODE_CATEGORY,
  NODE_FAMILY,
  STATUS_FILL,
  FAMILY_BORDER,
  FAMILY_LABEL,
  CATEGORY_LABEL,
  ALL_CATEGORIES,
  ALL_FAMILIES,
  familyForApiGroup,
  type ArchEdgeType,
  type ArchNodeType,
  type ArchStatus,
  type NodeCategory,
  type NodeFamily,
  type GraphEdge,
  type GraphNode,
} from './types'
