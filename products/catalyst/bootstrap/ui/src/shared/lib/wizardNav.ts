import { create } from 'zustand'
import type { ReactNode } from 'react'

/**
 * Wizard nav state — published by the currently rendered step,
 * consumed by the persistent footer in WizardLayout. This split
 * lets the footer DOM stay mounted across step transitions, killing
 * the mount/unmount flicker.
 */
export interface WizardNavState {
  onNext?: () => void
  onBack?: () => void
  nextDisabled?: boolean
  nextLoading?: boolean
  nextLabel?: ReactNode
  stepTitle?: string
}

interface NavStore {
  nav: WizardNavState
  setNav: (nav: WizardNavState) => void
}

export const useWizardNav = create<NavStore>((set) => ({
  nav: {},
  setNav: (nav) => set({ nav }),
}))
