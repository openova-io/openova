import { create } from 'zustand'
import { persist, devtools } from 'zustand/middleware'
import { type WizardState, INITIAL_WIZARD_STATE, type Region, type SelectedComponent, type CloudProvider, type NodeSize } from './model'

interface WizardActions {
  // Navigation
  setStep: (step: number) => void
  markStepComplete: (step: number) => void
  reset: () => void

  // Step 1 — Org
  setOrgName: (name: string) => void
  setOrgDomain: (domain: string) => void
  setOrgEmail: (email: string) => void

  // Step 2 — Provider
  setProvider: (provider: CloudProvider) => void

  // Step 3 — Credentials
  setHetznerToken: (token: string) => void
  setCredentialValidated: (validated: boolean) => void

  // Step 4 — Infra
  addRegion: (region: Region) => void
  removeRegion: (id: string) => void
  setControlPlaneSize: (size: NodeSize) => void
  setWorkerSize: (size: NodeSize) => void
  setWorkerCount: (count: number) => void
  setHaEnabled: (enabled: boolean) => void

  // Step 5 — Components
  toggleComponent: (component: SelectedComponent) => void
  setComponents: (components: SelectedComponent[]) => void
}

type WizardStore = WizardState & WizardActions

export const useWizardStore = create<WizardStore>()(
  devtools(
    persist(
      (set) => ({
        ...INITIAL_WIZARD_STATE,

        setStep: (step) => set({ currentStep: step }, false, 'wizard/setStep'),
        markStepComplete: (step) =>
          set(
            (s) => ({
              completedSteps: s.completedSteps.includes(step)
                ? s.completedSteps
                : [...s.completedSteps, step],
            }),
            false,
            'wizard/markStepComplete'
          ),
        reset: () => set(INITIAL_WIZARD_STATE, false, 'wizard/reset'),

        setOrgName: (orgName) => set({ orgName }, false, 'wizard/setOrgName'),
        setOrgDomain: (orgDomain) => set({ orgDomain }, false, 'wizard/setOrgDomain'),
        setOrgEmail: (orgEmail) => set({ orgEmail }, false, 'wizard/setOrgEmail'),

        setProvider: (provider) =>
          set({ provider, credentialValidated: false, hetznerToken: '' }, false, 'wizard/setProvider'),

        setHetznerToken: (hetznerToken) => set({ hetznerToken }, false, 'wizard/setHetznerToken'),
        setCredentialValidated: (credentialValidated) =>
          set({ credentialValidated }, false, 'wizard/setCredentialValidated'),

        addRegion: (region) =>
          set((s) => ({ regions: [...s.regions, region] }), false, 'wizard/addRegion'),
        removeRegion: (id) =>
          set((s) => ({ regions: s.regions.filter((r) => r.id !== id) }), false, 'wizard/removeRegion'),
        setControlPlaneSize: (controlPlaneSize) =>
          set({ controlPlaneSize }, false, 'wizard/setControlPlaneSize'),
        setWorkerSize: (workerSize) => set({ workerSize }, false, 'wizard/setWorkerSize'),
        setWorkerCount: (workerCount) => set({ workerCount }, false, 'wizard/setWorkerCount'),
        setHaEnabled: (haEnabled) => set({ haEnabled }, false, 'wizard/setHaEnabled'),

        toggleComponent: (component) =>
          set(
            (s) => ({
              selectedComponents: s.selectedComponents.find((c) => c.id === component.id)
                ? s.selectedComponents.filter((c) => c.id !== component.id)
                : [...s.selectedComponents, component],
            }),
            false,
            'wizard/toggleComponent'
          ),
        setComponents: (selectedComponents) =>
          set({ selectedComponents }, false, 'wizard/setComponents'),
      }),
      { name: 'openova-catalyst-wizard' }
    ),
    { name: 'CatalystWizard' }
  )
)
