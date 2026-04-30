/**
 * useCloudListState — shared state hook for the per-resource Cloud
 * list pages (P3 of #309). Owns search / sort / pagination state and
 * derives the visible slice from a comparator + filter callback.
 *
 * Separated from the component-laden cloudListShared.tsx so the
 * react-refresh/only-export-components rule stays clean: each file
 * exports either components OR utilities, not both.
 */

import { useMemo, useState } from 'react'
import type { SortState } from './sortState'

interface UseListStateOpts<T> {
  rows: readonly T[]
  /** Pre-computed search predicate (case-insensitive substring on a name). */
  matchSearch: (row: T, q: string) => boolean
  /** Optional pre-computed extra filter (e.g. status pill). */
  matchExtra?: (row: T) => boolean
  /** Pure sort function consuming the SortState. */
  comparator: (a: T, b: T, sort: SortState) => number
  /** Default sort. */
  defaultSort: SortState
  pageSize?: number
}

export function useCloudListState<T>(opts: UseListStateOpts<T>) {
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<SortState>(opts.defaultSort)
  const [page, setPage] = useState(0)

  const filtered = useMemo(
    () =>
      opts.rows.filter((row) => {
        if (opts.matchExtra && !opts.matchExtra(row)) return false
        if (!opts.matchSearch(row, search)) return false
        return true
      }),
    [opts, search],
  )

  const sorted = useMemo(
    () => [...filtered].sort((a, b) => opts.comparator(a, b, sort)),
    [filtered, opts, sort],
  )

  const pageSize = opts.pageSize ?? 50
  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize))

  // Clamp the current page derivatively (no setState-in-effect) — when
  // filtering shrinks the result set the visible window stays in
  // bounds without an effect cascading another render.
  const effectivePage = Math.min(page, pageCount - 1)

  const visible = useMemo(
    () => sorted.slice(effectivePage * pageSize, effectivePage * pageSize + pageSize),
    [sorted, effectivePage, pageSize],
  )

  return {
    search,
    setSearch,
    sort,
    setSort,
    page: effectivePage,
    setPage,
    pageSize,
    filtered,
    sorted,
    visible,
  }
}
