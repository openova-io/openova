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
import {
  findComponent,
  isMandatory as isMandatoryComponent,
  resolveTransitiveDependencies,
  resolveTransitiveDependents,
  MANDATORY_COMPONENT_IDS,
  computeDefaultSelection,
} from '@/pages/wizard/steps/componentGroups'

/**
 * Normalise any caller-supplied component list (legacy SelectedComponent[]
 * records OR plain string[]) to a sorted, de-duplicated string[]. The
 * marketplace-style wizard stores ids only and treats the catalog as the
 * single source of truth for tier / description / dependency metadata.
 */
function normaliseComponentIds(input: unknown): string[] {
  if (!Array.isArray(input)) return []
  const ids = input
    .map((entry) => {
      if (typeof entry === 'string') return entry
      if (entry && typeof entry === 'object' && 'id' in entry) {
        return String((entry as { id: unknown }).id)
      }
      return null
    })
    .filter((v): v is string => !!v)
  return [...new Set(ids)].sort()
}

/**
 * Initial selection at first wizard run: every mandatory + recommended
 * component plus their transitive deps. Persisted state overrides this on
 * subsequent runs (see merge() below).
 */
function computeInitialComponentSelection(): string[] {
  return [...computeDefaultSelection()].sort()
}

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

  // Step 1 — Sovereign domain (pool, byo-manual, byo-api)
  setSovereignDomainMode: (mode: import('./model').DomainMode) => void
  setSovereignPoolDomain: (id: string) => void
  setSovereignSubdomain: (subdomain: string) => void
  setSovereignByoDomain: (domain: string) => void

  // Step 1 — BYO-api registrar credentials (#169 BYO Flow B)
  setRegistrarType: (registrar: import('./model').RegistrarType | null) => void
  setRegistrarToken: (token: string) => void
  setRegistrarTokenValidated: (validated: boolean) => void
  clearRegistrarCredentials: () => void

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

  // Step 4 — SSH keypair (Mode A: auto-generate / Mode B: paste existing)
  setSshPublicKey: (key: string) => void
  /** Mode A — captures a freshly generated keypair returned by
   *  /api/v1/sshkey/generate. The privateKey is held only until the next
   *  paste (Mode B) replaces it. */
  setSshGenerated: (publicKey: string, privateKey: string, fingerprint: string) => void
  /** Mode B / reset — pasted-key mode clears the private-key blob so the
   *  wizard never accidentally exposes a stale private half. */
  clearSshPrivateKey: () => void

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

  // Step 5 — Corporate platform components (the 60+ catalog)
  /**
   * Add a component to the selection. Cascades: every transitive dep listed
   * in componentGroups.ts is added too. Idempotent — re-adding an
   * already-selected id is a no-op. Returns the new sorted selection.
   */
  addComponent: (id: string) => void
  /**
   * Remove a component from the selection. Cascades: every component that
   * (transitively) depends on it is removed too. Mandatory components
   * cannot be removed — call is a no-op. Idempotent. Returns the new
   * sorted selection.
   */
  removeComponent: (id: string) => void
  /** Replace the entire component selection (used by tests + presets). */
  setSelectedComponents: (ids: string[]) => void
  /** Reset the component selection back to "all mandatory + all recommended + their deps". */
  resetSelectedComponentsToDefault: () => void

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
          set(
            () => {
              // Switching modes wipes anything that doesn't apply, so the
              // wizard never carries a stale registrar token into byo-manual
              // (or vice versa) and never silently keeps the typed BYO
              // domain when the user switches back to pool.
              if (sovereignDomainMode === 'pool') {
                return {
                  sovereignDomainMode,
                  sovereignByoDomain: '',
                  registrarType: null,
                  registrarToken: '',
                  registrarTokenValidated: false,
                }
              }
              if (sovereignDomainMode === 'byo-manual') {
                return {
                  sovereignDomainMode,
                  sovereignSubdomain: '',
                  registrarType: null,
                  registrarToken: '',
                  registrarTokenValidated: false,
                }
              }
              // byo-api — keep typed domain; force a re-validation.
              return {
                sovereignDomainMode,
                sovereignSubdomain: '',
                registrarTokenValidated: false,
              }
            },
            false,
            'wizard/setSovereignDomainMode',
          ),
        setSovereignPoolDomain: (sovereignPoolDomain) =>
          set({ sovereignPoolDomain }, false, 'wizard/setSovereignPoolDomain'),
        setSovereignSubdomain: (sovereignSubdomain) =>
          set({ sovereignSubdomain: sovereignSubdomain.toLowerCase().replace(/\s+/g, '') },
              false, 'wizard/setSovereignSubdomain'),
        setSovereignByoDomain: (sovereignByoDomain) =>
          set({ sovereignByoDomain: sovereignByoDomain.toLowerCase().trim() },
              false, 'wizard/setSovereignByoDomain'),

        // BYO-api registrar credentials (#169) — token never persists to
        // localStorage (partialize() strips it). Every mutation invalidates
        // the validated flag so a typo'd or rotated token must be re-proved.
        setRegistrarType: (registrarType) =>
          set({ registrarType, registrarTokenValidated: false }, false, 'wizard/setRegistrarType'),
        setRegistrarToken: (registrarToken) =>
          set({ registrarToken, registrarTokenValidated: false }, false, 'wizard/setRegistrarToken'),
        setRegistrarTokenValidated: (registrarTokenValidated) =>
          set({ registrarTokenValidated }, false, 'wizard/setRegistrarTokenValidated'),
        clearRegistrarCredentials: () =>
          set({ registrarType: null, registrarToken: '', registrarTokenValidated: false },
              false, 'wizard/clearRegistrarCredentials'),

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

        setSshPublicKey: (sshPublicKey) =>
          set(
            // Pasted key — clear any private blob held over from a Mode-A
            // generation in the same session. Fingerprint goes to '' since
            // we don't ship a JS hashing library to recompute it client-side.
            { sshPublicKey, sshPrivateKeyOnce: '', sshKeyGeneratedThisSession: false, sshFingerprint: '' },
            false,
            'wizard/setSshPublicKey',
          ),
        setSshGenerated: (publicKey, privateKey, fingerprint) =>
          set(
            {
              sshPublicKey: publicKey,
              sshPrivateKeyOnce: privateKey,
              sshFingerprint: fingerprint,
              sshKeyGeneratedThisSession: true,
            },
            false,
            'wizard/setSshGenerated',
          ),
        clearSshPrivateKey: () =>
          set({ sshPrivateKeyOnce: '' }, false, 'wizard/clearSshPrivateKey'),

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
          // Legacy action — kept for back-compat with any old call site that
          // hands a SelectedComponent record. Internally we just toggle the
          // id in the new string[] selectedComponents form.
          set(
            (s) => {
              const has = s.selectedComponents.includes(component.id)
              return {
                selectedComponents: has
                  ? s.selectedComponents.filter((id) => id !== component.id)
                  : [...s.selectedComponents, component.id].sort(),
              }
            },
            false,
            'wizard/toggleComponent'
          ),
        setComponents: (selectedComponents) =>
          // Legacy: accept either string[] or SelectedComponent[] and
          // normalise to string[]. Empty arrays come through unchanged.
          set(
            { selectedComponents: normaliseComponentIds(selectedComponents as unknown) },
            false,
            'wizard/setComponents',
          ),

        addComponent: (id) =>
          set(
            (s) => {
              if (s.selectedComponents.includes(id)) return s
              const comp = findComponent(id)
              if (!comp) return s
              const next = new Set(s.selectedComponents)
              next.add(id)
              for (const dep of resolveTransitiveDependencies(id)) {
                next.add(dep)
              }
              return { selectedComponents: [...next].sort() }
            },
            false,
            'wizard/addComponent',
          ),

        removeComponent: (id) =>
          set(
            (s) => {
              if (isMandatoryComponent(id)) return s
              if (!s.selectedComponents.includes(id)) return s
              const drop = new Set<string>([id, ...resolveTransitiveDependents(id)])
              // Mandatory deps cannot be removed even via cascade — guard.
              for (const d of [...drop]) {
                if (isMandatoryComponent(d) && d !== id) drop.delete(d)
              }
              return {
                selectedComponents: s.selectedComponents.filter((cid) => !drop.has(cid)).sort(),
              }
            },
            false,
            'wizard/removeComponent',
          ),

        setSelectedComponents: (ids) =>
          set(
            { selectedComponents: normaliseComponentIds(ids) },
            false,
            'wizard/setSelectedComponents',
          ),

        resetSelectedComponentsToDefault: () =>
          set(
            { selectedComponents: computeInitialComponentSelection() },
            false,
            'wizard/resetSelectedComponentsToDefault',
          ),
      }),
      {
        name: 'openova-catalyst-wizard',
        // Per credential hygiene (docs/INVIOLABLE-PRINCIPLES.md #10), the
        // private key from /api/v1/sshkey/generate is held in memory ONLY
        // for the duration of the StepCredentials view. We strip it from
        // anything that gets serialized into localStorage so a casual
        // browser-storage inspection (or a stolen device snapshot) cannot
        // recover the operator's break-glass key. The session flag is
        // dropped for the same reason — a fresh tab should always re-prompt
        // the user, not assume "I downloaded the .pem already" from a prior
        // session.
        partialize: (state) => {
          // Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene):
          //   • sshPrivateKeyOnce — Mode-A break-glass private key
          //   • registrarToken    — BYO Flow B registrar API credential
          // Both are in-memory only. registrarTokenValidated drops too —
          // a fresh tab must re-prove the token even if the password
          // manager re-fills the field.
          const {
            sshPrivateKeyOnce: _omitPriv,
            sshKeyGeneratedThisSession: _omitGenFlag,
            registrarToken: _omitTok,
            registrarTokenValidated: _omitTokFlag,
            ...rest
          } = state
          void _omitPriv; void _omitGenFlag; void _omitTok; void _omitTokFlag
          return rest as unknown as WizardState
        },
        // Merge saved state with initial — handles new fields added after first install
        merge: (persisted, current) => {
          const p = { ...(persisted as Partial<WizardState>) }
          // #169 — legacy 'byo' value from earlier wizard runs maps to
          // 'byo-manual'. Anything outside the new vocabulary falls back
          // to 'pool' (safer than carrying a corrupted value forward).
          const validModes: import('./model').DomainMode[] = ['pool', 'byo-manual', 'byo-api']
          const persistedMode = (p as { sovereignDomainMode?: string }).sovereignDomainMode
          if (persistedMode === 'byo') {
            p.sovereignDomainMode = 'byo-manual'
          } else if (persistedMode && !validModes.includes(persistedMode as import('./model').DomainMode)) {
            p.sovereignDomainMode = 'pool'
          }
          // Always start with cleared registrar credentials — partialize()
          // already strips them, this double-protects an older payload that
          // accidentally retained them.
          p.registrarType = (p.registrarType ?? null) as import('./model').RegistrarType | null
          p.registrarToken = ''
          p.registrarTokenValidated = false
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
          // SSH-key fields added after first install (#160) — coerce missing
          // values so the StepCredentials SSH section renders cleanly on a
          // legacy persisted payload.
          if (typeof p.sshPublicKey !== 'string') p.sshPublicKey = ''
          if (typeof p.sshFingerprint !== 'string') p.sshFingerprint = ''
          // Always start a session with no private blob and no "generated
          // this session" flag — partialize() omits them on save, this
          // double-protects an older persist payload that may have
          // accidentally retained them.
          p.sshPrivateKeyOnce = ''
          p.sshKeyGeneratedThisSession = false
          // Coerce selectedComponents from any legacy shape (SelectedComponent[]
          // records, undefined) to a sorted string[]. Always ensure mandatory
          // components are present — they cannot be opted out of.
          {
            const ids = normaliseComponentIds(p.selectedComponents as unknown)
            const present = new Set(ids)
            // First-run fallback: if persisted state is empty, seed with
            // default selection (all mandatory + all recommended + deps).
            if (ids.length === 0) {
              for (const id of computeInitialComponentSelection()) present.add(id)
            } else {
              for (const id of MANDATORY_COMPONENT_IDS) present.add(id)
            }
            // Drop any persisted ids that no longer exist in the catalog.
            for (const id of [...present]) {
              if (!findComponent(id)) present.delete(id)
            }
            p.selectedComponents = [...present].sort()
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
