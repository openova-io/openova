/**
 * useClassFilter — SVC-180: the class filter of Resources lives in the
 * address as `?class=<class>` (validated by both cloud routes), so a reload
 * or a shared link opens the same view, and a class view of the left menu is
 * nothing but that address. The lists whose rows are installed Blueprints
 * (HelmReleases, Applications) read it here and pass it to K8sListPage.
 */

import { useCallback } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { isBlueprintClass, type BlueprintClass } from '@/shared/constants/classes'

export interface ClassFilter {
  /** The class in the address, or null when it holds none (or no valid one). */
  value: BlueprintClass | null
  /** Writes a new class to the address; null removes the parameter. */
  set: (next: BlueprintClass | null) => void
}

export function useClassFilter(): ClassFilter {
  const search = useSearch({ strict: false }) as { class?: unknown }
  const navigate = useNavigate()
  const value = isBlueprintClass(search.class) ? search.class : null
  const set = useCallback(
    (next: BlueprintClass | null) => {
      void navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({ ...prev, class: next ?? undefined }),
        replace: false,
      } as never)
    },
    [navigate],
  )
  return { value, set }
}
