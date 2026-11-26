import { create } from 'zustand'
import { persist, devtools } from 'zustand/middleware'
import {
  type WizardState,
  INITIAL_WIZARD_STATE,
  type Region,
  type CloudProvider,
  type NodeSize,
  type TopologyTemplate,
  type ProvisionResult,
  type MarketplaceBrand,
  RETIRED_ORG_DEFAULTS_5401,
  WIZARD_PERSIST_VERSION,
} from './model'
import { parseDeploymentID } from '@/shared/types/deployment'
import { KIT_COMPONENT_IDS } from '@/shared/lib/kitComponents'

/** The selection: every kit release. The wizard installs every ring and has no selection. */
function computeInitialComponentSelection(): string[] {
  return [...KIT_COMPONENT_IDS]
}

interface WizardActions {
  setStep: (step: number) => void
  markStepComplete: (step: number) => void
  /**
   * Set / clear the wizard's current deployment id. Accepts a raw
   * `string` for ergonomic call-sites (e.g. the StepReview launch
   * handler reading `data.id` from the catalyst-api response), but
   * routes EVERY non-null value through `parseDeploymentID()` so the
   * persisted value is guaranteed to be a real 16-char hex id. A
   * malformed id throws synchronously — the wizard surfaces this as
   * a launch failure rather than persisting an invalid state.
   * Closes #749 + #754.
   */
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
  setSovereignTimeZone: (zone: string) => void
  setSovereignByoDomain: (domain: string) => void

  // Step 1 — BYO-api registrar credentials (#169 BYO Flow B)
  setRegistrarType: (registrar: import('./model').RegistrarType | null) => void
  setRegistrarToken: (token: string) => void
  setRegistrarTokenValidated: (validated: boolean) => void
  clearRegistrarCredentials: () => void

  // Step 2 — Topology (resets the per-region choices when topology changes)
  setTopology: (topology: TopologyTemplate) => void

  // Step 3 — the cloud (a provider pack and an installation document of
  // GET /api/v1/providers), and per region a region and a zone of its profile
  setProvider: (provider: CloudProvider, installation: string) => void
  setRegionCloudRegion: (regionIndex: number, cloudRegion: string) => void
  setRegionZone: (regionIndex: number, zone: string) => void

  // Step 3 — Per-region flavors (inside the provider step, since the
  // flavors are those of the cloud profile).
  setRegionControlPlaneSize: (regionIndex: number, sizeId: string) => void
  setRegionWorkerSize: (regionIndex: number, sizeId: string) => void
  setRegionWorkerCount: (regionIndex: number, count: number) => void

  // Step 7 — the credential map of the cloud profile's credential fields
  setCredential: (key: string, value: string) => void
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
  setComponentsAppliedForProfile: (hash: string | null) => void
  /** Toggle a Blueprint in the unified marketplace card grid (StepComponents). */
  toggleBlueprint: (blueprintId: string) => void
  /** Replace the entire selectedBlueprints list (e.g. when a preset is picked). */
  setSelectedBlueprints: (ids: string[]) => void

  // Step 5 — the kit releases (read-only in the wizard)
  /** Reset the component selection back to every kit release. */
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
  // Step — Default storage class (issue #4057). Simple mirror of the
  // setMarketplaceEnabled pattern; empty string = accept the backend's
  // per-provider default.
  setStorageClass: (sc: string) => void

  // Step — Marketplace (issue #710 wave 3a). Both setters are simple
  // mirrors of the setHaEnabled pattern; the brand setter takes a
  // partial so callers can edit one field at a time.
  setMarketplaceEnabled: (enabled: boolean) => void
  setMarketplaceBrand: (brand: Partial<MarketplaceBrand>) => void
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
        setDeploymentId: (deploymentId) =>
          set(
            {
              // Branded-type guard — a raw string from anywhere
              // (catalyst-api response, Storybook, test fixture) is
              // re-validated here so the persisted state can never hold
              // a malformed id. Issues #749 + #754.
              deploymentId:
                deploymentId === null ? null : parseDeploymentID(deploymentId),
            },
            false,
            'wizard/setDeploymentId',
          ),
        reset: () => set(INITIAL_WIZARD_STATE, false, 'wizard/reset'),

        setOrgName: (orgName) => set({ orgName }, false, 'wizard/setOrgName'),
        setOrgDomain: (orgDomain) => set({ orgDomain }, false, 'wizard/setOrgDomain'),
        setOrgEmail: (orgEmail) => set({ orgEmail }, false, 'wizard/setOrgEmail'),
        setOrgIndustry: (orgIndustry) => set({ orgIndustry }, false, 'wizard/setOrgIndustry'),
        setOrgSize: (orgSize) => set({ orgSize }, false, 'wizard/setOrgSize'),
        setOrgHeadquarters: (orgHeadquarters) => set({ orgHeadquarters }, false, 'wizard/setOrgHeadquarters'),
        setOrgCompliance: (orgCompliance) => set({ orgCompliance }, false, 'wizard/setOrgCompliance'),
        setSovereignTimeZone: (sovereignTimeZone) => set({ sovereignTimeZone }, false, 'wizard/setSovereignTimeZone'),

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

        // Reset the per-region regions, zones AND sizing arrays when
        // topology changes — the new topology may have
        // a different region count, and a stale flavor at index 1 carried
        // over from two regions into one would silently feed the cost
        // rollup an unreachable region. The legacy single workerCount
        // field is also reset (StepProvider seeds per-region counts now).
        setTopology: (topology) =>
          set(
            {
              topology,
              regionCloudRegions: {},
              regionZones: {},
              regionControlPlaneSizes: [],
              regionWorkerSizes: [],
              regionWorkerCounts: [],
              workerCount: 0,
            },
            false,
            'wizard/setTopology',
          ),

        // Another cloud has other regions, zones, flavors and credential
        // fields: every per-region choice and the credential map are wiped.
        // StepProvider pre-selects the new profile's defaults right after.
        setProvider: (provider, installation) =>
          set(
            {
              provider,
              installation,
              regionCloudRegions: {},
              regionZones: {},
              regionControlPlaneSizes: [],
              regionWorkerSizes: [],
              regionWorkerCounts: [],
              credentials: {},
              credentialValidated: false,
            },
            false,
            'wizard/setProvider',
          ),
        setRegionCloudRegion: (regionIndex, cloudRegion) =>
          set(
            (s) => ({ regionCloudRegions: { ...s.regionCloudRegions, [regionIndex]: cloudRegion } }),
            false,
            'wizard/setRegionCloudRegion'
          ),
        setRegionZone: (regionIndex, zone) =>
          set((s) => ({ regionZones: { ...s.regionZones, [regionIndex]: zone } }), false, 'wizard/setRegionZone'),

        // Per-region SKU mutators. Each writes ONLY into the index it was
        // given — adjacent indices are preserved untouched. The setters
        // pad the array with empty strings (for sizes) or zeros (for
        // counts) up to the requested index so a sparse selection from
        // an earlier topology choice doesn't leak NaN/undefined holes
        // into the cost rollup. The legacy single-valued field mirrors
        // index 0.
        setRegionControlPlaneSize: (regionIndex, sizeId) =>
          set(
            (s) => {
              const next = [...s.regionControlPlaneSizes]
              while (next.length <= regionIndex) next.push('')
              next[regionIndex] = sizeId
              const patch: Partial<WizardState> = { regionControlPlaneSizes: next }
              if (regionIndex === 0) patch.controlPlaneSize = sizeId
              return patch
            },
            false,
            'wizard/setRegionControlPlaneSize',
          ),
        setRegionWorkerSize: (regionIndex, sizeId) =>
          set(
            (s) => {
              const next = [...s.regionWorkerSizes]
              while (next.length <= regionIndex) next.push('')
              next[regionIndex] = sizeId
              const patch: Partial<WizardState> = { regionWorkerSizes: next }
              if (regionIndex === 0) patch.workerSize = sizeId
              return patch
            },
            false,
            'wizard/setRegionWorkerSize',
          ),
        setRegionWorkerCount: (regionIndex, count) =>
          set(
            (s) => {
              const safe = Math.max(0, Math.min(50, Math.floor(count)))
              const next = [...s.regionWorkerCounts]
              while (next.length <= regionIndex) next.push(0)
              next[regionIndex] = safe
              const patch: Partial<WizardState> = { regionWorkerCounts: next }
              if (regionIndex === 0) patch.workerCount = safe
              return patch
            },
            false,
            'wizard/setRegionWorkerCount',
          ),

        // A changed credential value must be validated again.
        setCredential: (key, value) =>
          set(
            (s) => ({ credentials: { ...s.credentials, [key]: value }, credentialValidated: false }),
            false,
            'wizard/setCredential',
          ),
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

        setComponentsAppliedForProfile: (componentsAppliedForProfile) =>
          set({ componentsAppliedForProfile }, false, 'wizard/setComponentsAppliedForProfile'),
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

        // Default storage class — issue #4057. Empty string is the common
        // case: the catalyst-api Request struct fills the default class of
        // the cloud profile when it arrives
        // blank. A non-empty value overrides that default.
        setStorageClass: (storageClass) =>
          set({ storageClass }, false, 'wizard/setStorageClass'),

        // Marketplace mode — issue #710 wave 3a. Toggle persists via the
        // existing deploy-request → tofu var → cloud-init → Flux substitute
        // → chart render path; no ConfigMap shortcut.
        setMarketplaceEnabled: (marketplaceEnabled) =>
          set({ marketplaceEnabled }, false, 'wizard/setMarketplaceEnabled'),
        setMarketplaceBrand: (brand) =>
          set(
            (s) => ({ marketplaceBrand: { ...s.marketplaceBrand, ...brand } }),
            false,
            'wizard/setMarketplaceBrand',
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
        version: WIZARD_PERSIST_VERSION,
        /**
         * #5401 / UAT rows W1 + W2 — retire the fabricated ORG_DEFAULTS from
         * payloads written by the pre-fix bundle.
         *
         * Emptying ORG_DEFAULTS in model.ts only changes INITIAL_WIZARD_STATE.
         * `merge()` below returns `{ ...current, ...persisted }`, so a payload
         * from the old bundle overrides that fix and re-seeds
         * `orgName: 'Acme Financial'` (W1) and
         * `orgHeadquarters: 'Frankfurt, Germany'` (W2) on every returning
         * operator's browser. Without this migration the fix is only visible on
         * a browser profile that has never run the wizard.
         *
         * VALUE-MATCHED, not a blanket wipe: a field is dropped only when it is
         * still verbatim-identical to the retired default, so an operator who
         * had already typed their own identity keeps it.
         *
         * Deleting the key (rather than writing '') is deliberate — `merge()`
         * spreads the persisted payload OVER `current`, so an absent key lets
         * INITIAL_WIZARD_STATE's empty value win, while an explicit '' would
         * work only by coincidence of the two being equal today.
         */
        migrate: (persisted, fromVersion) => {
          const p = { ...(persisted as Partial<WizardState>) } as Record<string, unknown>
          // `fromVersion` is `undefined` for payloads written before this
          // option existed — zustand persists no `version` key at all then.
          if (fromVersion == null || fromVersion < 1) {
            let hqWasFabricated = false
            for (const [key, retired] of Object.entries(RETIRED_ORG_DEFAULTS_5401)) {
              if (p[key] === retired) {
                if (key === 'orgHeadquarters') hqWasFabricated = true
                delete p[key]
              }
            }
            // The DERIVATION is as much a fabricated value as its input: a
            // provider and regions (regionProviders is the key the bundle of
            // that time wrote) derived from 'Frankfurt, Germany' are dropped,
            // so the provider step picks them again.
            if (hqWasFabricated) {
              delete p.provider
              delete p.regionProviders
              delete p.regionCloudRegions
            }
          }
          return p as unknown as WizardState
        },
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
          //   • credentials       — the cloud credential map
          // All are in-memory only. registrarTokenValidated and
          // credentialValidated drop too — a fresh tab must re-prove them
          // even if the password manager re-fills the fields.
          const {
            sshPrivateKeyOnce: _omitPriv,
            sshKeyGeneratedThisSession: _omitGenFlag,
            registrarToken: _omitTok,
            registrarTokenValidated: _omitTokFlag,
            credentials: _omitCreds,
            credentialValidated: _omitCredsFlag,
            ...rest
          } = state
          void _omitPriv; void _omitGenFlag; void _omitTok; void _omitTokFlag; void _omitCreds; void _omitCredsFlag
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
          // A stored topology that is not a current choice (the retired
          // templates included) becomes the default choice, two regions.
          const validTopologies: TopologyTemplate[] = ['dual', 'solo']
          if (p.topology && !validTopologies.includes(p.topology)) {
            p.topology = 'dual'
            p.regionCloudRegions = {}
            p.regionZones = {}
            p.regionControlPlaneSizes = []
            p.regionWorkerSizes = []
            p.regionWorkerCounts = []
          }
          // Per-region sizing migration. A persist payload from before the
          // per-region rework only has the singular controlPlaneSize /
          // workerSize / workerCount fields. Hydrate the arrays from those
          // for index 0 so a returning user sees their previous selection
          // applied to Region 1 — the rest of the regions stay empty until
          // the user revisits the provider step. Any cx22 default (the
          // legacy initial value) is treated as "no selection yet" so it is
          // not carried into a region of another cloud.
          {
            const legacyCp = (p as { controlPlaneSize?: string }).controlPlaneSize
            const legacyWk = (p as { workerSize?: string }).workerSize
            const legacyWc = (p as { workerCount?: number }).workerCount
            const validLegacy = (id: string | undefined) =>
              typeof id === 'string' && id !== '' && id !== 'cx22'
            if (!Array.isArray(p.regionControlPlaneSizes)) {
              p.regionControlPlaneSizes = validLegacy(legacyCp) ? [legacyCp as string] : []
            }
            if (!Array.isArray(p.regionWorkerSizes)) {
              p.regionWorkerSizes = validLegacy(legacyWk) ? [legacyWk as string] : []
            }
            if (!Array.isArray(p.regionWorkerCounts)) {
              p.regionWorkerCounts = typeof legacyWc === 'number' ? [legacyWc] : []
            }
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
          // Marketplace fields added in #710 wave 3a — coerce missing
          // values on a legacy persisted payload. D27 zero-touch ruling
          // 2026-05-16: default true so a fresh wizard hydration provisions
          // a Sovereign that's ready to host Organizations (D29).
          if (typeof p.marketplaceEnabled !== 'boolean') {
            p.marketplaceEnabled = true
          }
          if (
            !p.marketplaceBrand ||
            typeof p.marketplaceBrand !== 'object'
          ) {
            p.marketplaceBrand = { name: '', tagline: '', primaryColor: '' }
          } else {
            p.marketplaceBrand = {
              name: typeof p.marketplaceBrand.name === 'string' ? p.marketplaceBrand.name : '',
              tagline: typeof p.marketplaceBrand.tagline === 'string' ? p.marketplaceBrand.tagline : '',
              primaryColor: typeof p.marketplaceBrand.primaryColor === 'string' ? p.marketplaceBrand.primaryColor : '',
            }
          }
          // SSH-key fields added after first install (#160) — coerce missing
          // values so the StepCredentials SSH section renders cleanly on a
          // legacy persisted payload.
          if (typeof p.sshPublicKey !== 'string') p.sshPublicKey = ''
          if (typeof p.sshFingerprint !== 'string') p.sshFingerprint = ''
          // The cloud fields of CLD-122 — coerce missing values on a legacy
          // persisted payload. The credential map is never restored.
          if (typeof p.installation !== 'string') p.installation = ''
          if (!p.regionZones || typeof p.regionZones !== 'object') p.regionZones = {}
          p.credentials = {}
          p.credentialValidated = false
          // A key the store no longer holds is dropped. A payload already at
          // version 1 skips migrate, so without this the cloud fields CLD-122
          // retired (the token and project of one named cloud, the
          // per-provider tokens and validated flags, the per-region provider
          // and the Object Storage fields) would be read back here and
          // written again by partialize on every save.
          for (const key of Object.keys(p)) {
            if (!(key in current)) delete (p as Record<string, unknown>)[key]
          }
          // Always start a session with no private blob and no "generated
          // this session" flag — partialize() omits them on save, this
          // double-protects an older persist payload that may have
          // accidentally retained them.
          p.sshPrivateKeyOnce = ''
          p.sshKeyGeneratedThisSession = false
          // The selection is every kit release, whatever a persisted draft
          // named: the wizard installs every ring and has no selection.
          p.selectedComponents = computeInitialComponentSelection()
          // Re-validate the persisted deploymentId against the branded
          // shape (issues #749 + #754). A legacy localStorage payload
          // could carry a malformed value (truncated id, uppercased,
          // legacy uuid shape) — drop it rather than re-pollute the
          // active session. Falls back to null so the wizard's
          // first-run state is the same as a fresh install.
          {
            const persistedId = (p as { deploymentId?: unknown }).deploymentId
            if (persistedId == null) {
              p.deploymentId = null
            } else {
              try {
                p.deploymentId = parseDeploymentID(persistedId)
              } catch {
                // Bad payload — wipe it. Better to lose a stale id
                // than to render the misleading 15-char error this
                // PR exists to prevent.
                p.deploymentId = null
              }
            }
          }
          // The product families are gone: the family map (componentGroups) of a
          // persisted draft is dropped, not carried into the state.
          delete (p as Record<string, unknown>).componentGroups
          return { ...current, ...p }
        },
      }
    ),
    { name: 'CatalystWizard' }
  )
)
