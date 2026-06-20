/**
 * useCloudLens.tsx — the SHARED lens / active-chip-set state for the
 * unified Cloud surface (#3970).
 *
 * THE MODEL: a lens IS a named chip-set. The active chip-set is the single
 * source of what renders in BOTH the graph and the list. Lifting it into a
 * context (provided once at the CloudPage level) means toggling between
 * `view=graph` and `view=list` preserves the lens + chips, and `+ Add` in
 * either rendering grows the same set.
 *
 *   • selectLens(id)  → active chip-set := exactly that lens's chips
 *                       (clean reset; prior +Add additions are dropped).
 *   • addChip(t)      → active := active ∪ {t}
 *   • removeChip(t)   → active := active − {t}
 *   • isCustom        → active chip-set has diverged from the pure lens.
 *
 * Default lens = Cloud (#3970).
 */
/* eslint-disable react-refresh/only-export-components */
// This module intentionally co-locates the lens-state hooks
// (useCloudLensState / useCloudLensOptional) with the CloudLensProvider —
// they are one cohesive unit. Fast-refresh of the (trivial) provider is a
// non-concern; the lens state is shared, not hot-swapped.

import {
  createContext,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import {
  LENSES,
  DEFAULT_LENS_ID,
  lensChips,
  setsEqual,
  type LensId,
} from './presets'
import type { ArchNodeType } from './types'

export interface CloudLensState {
  lensId: LensId
  activeTypes: Set<ArchNodeType>
  isCustom: boolean
  selectLens: (id: LensId) => void
  addChip: (t: ArchNodeType) => void
  removeChip: (t: ArchNodeType) => void
}

/**
 * useCloudLensState — the state implementation. Used directly by the
 * provider; exposed so the graph widget can also fall back to a private
 * instance when it's rendered standalone (no provider).
 */
export function useCloudLensState(): CloudLensState {
  const [lensId, setLensId] = useState<LensId>(DEFAULT_LENS_ID)
  const [activeTypes, setActiveTypes] = useState<Set<ArchNodeType>>(
    () => lensChips(LENSES[DEFAULT_LENS_ID]),
  )

  const api = useMemo<CloudLensState>(() => {
    const selectLens = (id: LensId) => {
      setLensId(id)
      setActiveTypes(lensChips(LENSES[id]))
    }
    const addChip = (t: ArchNodeType) =>
      setActiveTypes((prev) => {
        const n = new Set(prev)
        n.add(t)
        return n
      })
    const removeChip = (t: ArchNodeType) =>
      setActiveTypes((prev) => {
        const n = new Set(prev)
        n.delete(t)
        return n
      })
    return {
      lensId,
      activeTypes,
      isCustom: !setsEqual(activeTypes, lensChips(LENSES[lensId])),
      selectLens,
      addChip,
      removeChip,
    }
  }, [lensId, activeTypes])

  return api
}

const CloudLensContext = createContext<CloudLensState | null>(null)

export function CloudLensProvider({ children }: { children: ReactNode }) {
  const value = useCloudLensState()
  return <CloudLensContext.Provider value={value}>{children}</CloudLensContext.Provider>
}

/**
 * useCloudLens — read the shared lens state. When no provider is mounted
 * (standalone graph in tests/storybook) the caller should fall back to a
 * private `useCloudLensState()` instance instead; this hook returns null
 * in that case so the caller can branch.
 */
export function useCloudLensOptional(): CloudLensState | null {
  return useContext(CloudLensContext)
}
