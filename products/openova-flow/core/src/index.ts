/**
 * @openova/flow-core — barrel.
 *
 * Public surface: types (FlowInstance, FlowNode, Relationship,
 * FlowMessage, FlowAdapter, StatusTone, FamilyDescriptor,
 * RegionDescriptor, NodeAction) + the pure `layout` function and the
 * `defaultFoldedAtDepth` helper.
 *
 * Nothing else is exported — callers reach into core/src/* directly
 * only in tests.
 */

export type {
  FlowInstance,
  FlowNode,
  Relationship,
  RelationshipType,
  FlowMessage,
  FlowAdapter,
  StatusTone,
  FamilyDescriptor,
  RegionDescriptor,
  NodeAction,
} from './types'
export { isBlockingRelationship } from './types'

export type {
  LayoutInput,
  LayoutOutput,
  LayoutHints,
  FlowLayoutHint,
  PositionedNode,
  PositionedEdge,
  ComponentInfo,
  LaneDescriptor,
} from './layout'
export { layout, defaultFoldedAtDepth, FALLBACK_REGION_ID, MAX_VISIBLE_DEPTH } from './layout'
