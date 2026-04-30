/**
 * Shared sort-state types for the Cloud list pages (P3 of #309).
 * Lives in its own module so both the component file and the
 * useCloudListState hook can import it without forming a cycle.
 */

export type SortDir = 'asc' | 'desc'

export interface SortState {
  column: string
  dir: SortDir
}
