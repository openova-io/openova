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
export {
  edgeNodeId,
  type ArchEdgeType,
  type ArchNodeType,
  type ArchStatus,
  type GraphEdge,
  type GraphNode,
} from './types'
