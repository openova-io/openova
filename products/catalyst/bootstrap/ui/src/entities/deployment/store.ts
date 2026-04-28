import { create } from 'zustand'
import { persist, devtools } from 'zustand/middleware'
import {
  type WizardState,
  INITIAL_WIZARD_STATE,
  type Region,
  type SelectedComponent,
  type CloudProvider,
  type NodeSize,
  type TopologyTemplate,
  type ProvisionResult,
} from './model'

interface WizardActions {
  setStep: (step: number) => void
  markStepComplete: (step: number) => void
  setDeploymentId: (id: string | null) => void
  reset: () => void

  // Step 1 — Org
  setOrgName: (name: string) => void
  setOrgDomain: (domain: string) => void
  setOrgEmail: (email: string) => void
  setOrgIndustry: (industry: string) => void
  setOrgSize: (size: string) => void
  setOrgHeadquarters: (hq: string) => void
  setOrgCompliance: (tags: string[]) => void

  // Step 1 — Sovereign domain (pool or BYO)
  setSovereignDomainMode: (mode: import('./model').DomainMode) => void
  setSovereignPoolDomain: (id: string) => void
  setSovereignSubdomain: (subdomain: string) => void
  setSovereignByoDomain: (domain: string) => void

  // Step 2 — Topology (resets per-region providers when topology changes)
  setTopology: (topology: TopologyTemplate) => void

  // Step 3 — Per-region provider
  setRegionProvider: (regionIndex: number, provider: CloudProvider) => void
  setRegionCloudRegion: (regionIndex: number, cloudRegion: string) => void
  applyProviderToAll: (provider: CloudProvider, regionCount: number) => void

  // Step 4 — Per-provider credentials
  setProviderToken: (provider: CloudProvider, token: string) => void
  setProviderValidated: (provider: CloudProvider, validated: boolean) => void

  // Compat setters
  setProvider: (provider: CloudProvider) => void
  setHetznerToken: (token: string) => void
  setHetznerProjectId: (projectId: string) => void
  setCredentialValidated: (validated: boolean) => void

  // AIR-GAP add-on
  setAirgap: (airgap: boolean) => void

  // Step 5 — Components
  setGroupComponents: (groupId: string, componentIds: string[]) => void
  toggleGroupComponent: (groupId: string, componentId: string, allIds: string[]) => void
  setComponentsAppliedForProfile: (hash: string | null) => void
  /** Toggle a Blueprint in the unified marketplace card grid (StepComponents). */
  toggleBlueprint: (blueprintId: string) => void
  /** Replace the entire selectedBlueprints list (e.g. when a preset is picked). */
  setSelectedBlueprints: (ids: string[]) => void

  // Step 7 — Provisioning result (captured by SSE done event)
  setLastProvisionResult: (result: ProvisionResult | null) => void

  // Legacy
  addRegion: (region: Region) => void
  removeRegion: (id: string) => void
  setControlPlaneSize: (size: NodeSize) => void
  setWorkerSize: (size: NodeSize) => void
  setWorkerCount: (count: number) => void
  setHaEnabled: (enabled: boolean) => void
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
        setDeploymentId: (deploymentId) => set({ deploymentId }, false, 'wizard/setDeploymentId'),
        reset: () => set(INITIAL_WIZARD_STATE, false, 'wizard/reset'),

        setOrgName: (orgName) => set({ orgName }, false, 'wizard/setOrgName'),
        setOrgDomain: (orgDomain) => set({ orgDomain }, false, 'wizard/setOrgDomain'),
        setOrgEmail: (orgEmail) => set({ orgEmail }, false, 'wizard/setOrgEmail'),
        setOrgIndustry: (orgIndustry) => set({ orgIndustry }, false, 'wizard/setOrgIndustry'),
        setOrgSize: (orgSize) => set({ orgSize }, false, 'wizard/setOrgSize'),
        setOrgHeadquarters: (orgHeadquarters) => set({ orgHeadquarters }, false, 'wizard/setOrgHeadquarters'),
        setOrgCompliance: (orgCompliance) => set({ orgCompliance }, false, 'wizard/setOrgCompliance'),

        // Sovereign-domain setters
        setSovereignDomainMode: (sovereignDomainMode) =>
          set({ sovereignDomainMode }, false, 'wizard/setSovereignDomainMode'),
        setSovereignPoolDomain: (sovereignPoolDomain) =>
          set({ sovereignPoolDomain }, false, 'wizard/setSovereignPoolDomain'),
        setSovereignSubdomain: (sovereignSubdomain) =>
          // normalize: lowercase, strip whitespace; keep validation in the form layer
          set({ sovereignSubdomain: sovereignSubdomain.toLowerCase().replace(/\s+/g, '') },
              false, 'wizard/setSovereignSubdomain'),
        setSovereignByoDomain: (sovereignByoDomain) =>
          set({ sovereignByoDomain: sovereignByoDomain.toLowerCase().trim() },
              false, 'wizard/setSovereignByoDomain'),

        // Reset regionProviders and regionCloudRegions when topology changes
        setTopology: (topology) =>
          set({ topology, regionProviders: {}, regionCloudRegions: {}, providerValidated: {}, providerTokens: {} }, false, 'wizard/setTopology'),

        setRegionProvider: (regionIndex, provider) =>
          set(
            (s) => ({ regionProviders: { ...s.regionProviders, [regionIndex]: provider } }),
            false,
            'wizard/setRegionProvider'
          ),
        setRegionCloudRegion: (regionIndex, cloudRegion) =>
          set(
            (s) => ({ regionCloudRegions: { ...s.regionCloudRegions, [regionIndex]: cloudRegion } }),
            false,
            'wizard/setRegionCloudRegion'
          ),
        applyProviderToAll: (provider, regionCount) => {
          const regionProviders: Record<number, CloudProvider> = {}
          for (let i = 0; i < regionCount; i++) regionProviders[i] = provider
          set({ regionProviders }, false, 'wizard/applyProviderToAll')
        },

        setProviderToken: (provider, token) =>
          set(
            (s) => ({ providerTokens: { ...s.providerTokens, [provider]: token } }),
            false,
            'wizard/setProviderToken'
          ),
        setProviderValidated: (provider, validated) =>
          set(
            (s) => ({ providerValidated: { ...s.providerValidated, [provider]: validated } }),
            false,
            'wizard/setProviderValidated'
          ),

        setProvider: (provider) =>
          set({ provider, credentialValidated: false, hetznerToken: '' }, false, 'wizard/setProvider'),
        setHetznerToken: (hetznerToken) => set({ hetznerToken }, false, 'wizard/setHetznerToken'),
        setHetznerProjectId: (hetznerProjectId) =>
          set({ hetznerProjectId: hetznerProjectId.trim() }, false, 'wizard/setHetznerProjectId'),
        setCredentialValidated: (credentialValidated) =>
          set({ credentialValidated }, false, 'wizard/setCredentialValidated'),

        setAirgap: (airgap) => set({ airgap }, false, 'wizard/setAirgap'),

        setGroupComponents: (groupId, componentIds) =>
          set(
            (s) => ({ componentGroups: { ...s.componentGroups, [groupId]: componentIds } }),
            false,
            'wizard/setGroupComponents'
          ),
        setComponentsAppliedForProfile: (componentsAppliedForProfile) =>
          set({ componentsAppliedForProfile }, false, 'wizard/setComponentsAppliedForProfile'),
        toggleGroupComponent: (groupId, componentId, allIds) =>
          set(
            (s) => {
              const current = s.componentGroups[groupId] ?? []
              const next = current.includes(componentId)
                ? current.filter((id) => id !== componentId)
                : [...current, componentId]
              return { componentGroups: { ...s.componentGroups, [groupId]: allIds.filter((id) => next.includes(id)) } }
            },
            false,
            'wizard/toggleGroupComponent'
          ),
        toggleBlueprint: (blueprintId) =>
          set(
            (s) => ({
              selectedBlueprints: s.selectedBlueprints.includes(blueprintId)
                ? s.selectedBlueprints.filter((id) => id !== blueprintId)
                : [...s.selectedBlueprints, blueprintId],
            }),
            false,
            'wizard/toggleBlueprint'
          ),
        setSelectedBlueprints: (selectedBlueprints) =>
          set({ selectedBlueprints }, false, 'wizard/setSelectedBlueprints'),
        setLastProvisionResult: (lastProvisionResult) =>
          set({ lastProvisionResult }, false, 'wizard/setLastProvisionResult'),

        addRegion: (region) =>
          set((s) => ({ regions: [...s.regions, region] }), false, 'wizard/addRegion'),
        removeRegion: (id) =>
          set((s) => ({ regions: s.regions.filter((r) => r.id !== id) }), false, 'wizard/removeRegion'),
        setControlPlaneSize: (controlPlaneSize) => set({ controlPlaneSize }, false, 'wizard/setControlPlaneSize'),
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
        setComponents: (selectedComponents) => set({ selectedComponents }, false, 'wizard/setComponents'),
      }),
      {
        name: 'openova-catalyst-wizard',
        // Merge saved state with initial — handles new fields added after first install
        merge: (persisted, current) => {
          const p = { ...(persisted as Partial<WizardState>) }
          // Sanitize stale topology values
          const validTopologies: TopologyTemplate[] = ['citadel', 'triangle', 'dual', 'zoned', 'compact', 'solo']
          if (p.topology && !validTopologies.includes(p.topology)) {
            p.topology = null
            p.regionProviders = {}
            p.regionCloudRegions = {}
            p.providerValidated = {}
            p.providerTokens = {}
          }
          // Coerce legacy persist payloads that lack new fields. Without
          // these guards, the store returns `undefined` and toggleBlueprint
          // (etc.) crashes on the .includes() call.
          if (!Array.isArray(p.selectedBlueprints)) {
            p.selectedBlueprints = []
          }
          if (p.lastProvisionResult === undefined) {
            p.lastProvisionResult = null
          }
          // Strip old component group IDs — replaced by pilot/spine/surge/silo/guardian/insights/fabric/cortex/relay
          const validGroupIds = ['pilot','spine','surge','silo','guardian','insights','fabric','cortex','relay']
          if (p.componentGroups) {
            const migrated: Record<string, string[]> = {}
            for (const [k, v] of Object.entries(p.componentGroups)) {
              if (validGroupIds.includes(k)) {
                // Migrate minio → seaweedfs
                migrated[k] = (v as string[]).map(id => id === 'minio' ? 'seaweedfs' : id)
              }
            }
            // Ensure vcluster is in pilot defaults
            if (migrated.pilot && !migrated.pilot.includes('vcluster')) {
              migrated.pilot = [...migrated.pilot, 'vcluster']
            }
            p.componentGroups = migrated
          }
          return { ...current, ...p }
        },
      }
    ),
    { name: 'CatalystWizard' }
  )
)
