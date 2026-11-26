import { useEffect, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS } from '@/entities/deployment/model'
import { useSession } from '@/shared/lib/useSession'
import { PinSignInModal } from '@/widgets/auth/PinSignInModal'
import {
  useProviders, useProfile, flavorsFor, defaultFlavor, findFlavor, describeFlavor, totalPrice, ProvidersApiError,
  type CloudProfile, type CredentialSource, type FlavorRole,
} from '@/lib/providers.api'
import { StepShell, useStepNav } from './_shared'

/* ── Custom dropdown ─────────────────────────────────────────────── */
interface SelectOption { value: string; label: string; sublabel?: string; logo?: React.ReactNode }

function CustomSelect({ value, onChange, options, placeholder = 'Select…' }: {
  value: string
  onChange: (v: string) => void
  options: SelectOption[]
  placeholder?: string
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function onDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const selected = options.find(o => o.value === value)

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <div
        onClick={() => setOpen(v => !v)}
        style={{
          display: 'flex', alignItems: 'center', gap: 8,
          padding: '8px 10px', borderRadius: 8, cursor: 'pointer',
          border: selected ? '1.5px solid rgba(56,189,248,0.35)' : '1.5px solid var(--wiz-border)',
          background: selected ? 'rgba(56,189,248,0.06)' : 'var(--wiz-bg-sub)',
          transition: 'all 0.12s',
        }}
      >
        {selected?.logo}
        <span style={{ flex: 1, fontSize: 12, fontWeight: selected ? 500 : 400, color: selected ? 'var(--wiz-text-hi)' : 'var(--wiz-text-sub)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {selected ? selected.label : placeholder}
        </span>
        {selected?.sublabel && (
          <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', flexShrink: 0 }}>{selected.sublabel}</span>
        )}
        <ChevronDown size={13} style={{ color: 'var(--wiz-text-sub)', flexShrink: 0, transform: open ? 'rotate(180deg)' : 'none', transition: 'transform 0.15s' }} />
      </div>

      {open && (
        <div style={{
          position: 'absolute', top: 'calc(100% + 4px)', left: 0, right: 0, zIndex: 200,
          borderRadius: 9, border: '1px solid var(--wiz-border)',
          background: 'var(--wiz-panel-bg)', boxShadow: '0 4px 20px rgba(0,0,0,0.15)',
          overflow: 'hidden', maxHeight: 320, overflowY: 'auto',
        }}>
          {options.map(o => {
            const active = o.value === value
            return (
              <div
                key={o.value}
                onClick={() => { onChange(o.value); setOpen(false) }}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  padding: '9px 12px', cursor: 'pointer',
                  background: active ? 'rgba(56,189,248,0.08)' : 'transparent',
                  borderBottom: '1px solid var(--wiz-bg-card)',
                  transition: 'background 0.1s',
                }}
              >
                {o.logo}
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 12, fontWeight: active ? 600 : 400, color: active ? 'var(--wiz-text-hi)' : 'var(--wiz-text-md)' }}>{o.label}</div>
                  {o.sublabel && <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 1 }}>{o.sublabel}</div>}
                </div>
                {active && <Check size={13} strokeWidth={2.5} style={{ color: '#38BDF8', flexShrink: 0 }} />}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

type WizardStore = ReturnType<typeof useWizardStore.getState>

const SOURCE_TEXT: Record<CredentialSource, string> = {
  user: 'Your credentials',
  operator: "The operator's credentials",
  either: "Your credentials or the operator's",
}

/** The flavors of the role offered in the region, as dropdown options. */
function flavorOptions(profile: CloudProfile, role: FlavorRole, region: string): SelectOption[] {
  return flavorsFor(profile, role, region).map((f) => ({ value: f.name, label: f.name, sublabel: describeFlavor(f) }))
}

/**
 * Region i takes the region of the profile with the given code. Its zone
 * becomes that region's default zone, and a flavor the region does not
 * offer becomes the default flavor of its role there (issue #916: a flavor
 * the cloud refuses in that region fails minutes into `tofu apply`).
 */
function placeRegion(s: WizardStore, profile: CloudProfile, i: number, code: string) {
  const keep = (role: FlavorRole, name: string | undefined) =>
    name && flavorsFor(profile, role, code).some((f) => f.name === name) ? name : defaultFlavor(profile, role, code)
  s.setRegionCloudRegion(i, code)
  s.setRegionZone(i, profile.regions.find((r) => r.code === code)?.defaultZone ?? '')
  s.setRegionControlPlaneSize(i, keep('control-plane', s.regionControlPlaneSizes[i]))
  s.setRegionWorkerSize(i, keep('worker', s.regionWorkerSizes[i]))
}

/** Region i has a region and a zone of the profile, a control-plane flavor, and a worker flavor when it has workers. */
function regionReady(s: WizardStore, profile: CloudProfile, i: number): boolean {
  const region = profile.regions.find((r) => r.code === s.regionCloudRegions[i])
  if (!region || !region.zones.includes(s.regionZones[i] ?? '') || !s.regionControlPlaneSizes[i]) return false
  return (s.regionWorkerCounts[i] ?? 0) === 0 || !!s.regionWorkerSizes[i]
}

function regionMachines(s: WizardStore, profile: CloudProfile, i: number) {
  return [
    { flavor: findFlavor(profile, s.regionControlPlaneSizes[i] ?? ''), count: 1 },
    { flavor: findFlavor(profile, s.regionWorkerSizes[i] ?? ''), count: s.regionWorkerCounts[i] ?? 0 },
  ]
}

const pickerTitle: React.CSSProperties = { fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--wiz-text-sub)', marginBottom: 5 }
const notice: React.CSSProperties = { borderRadius: 8, padding: '9px 12px', fontSize: 11, color: 'var(--wiz-text-sub)', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.12)' }
const warning: React.CSSProperties = { ...notice, color: '#F59E0B', background: 'rgba(245,158,11,0.06)', border: '1px solid rgba(245,158,11,0.3)' }

/* ── RegionCard ──────────────────────────────────────────────────── */
function RegionCard({ index, label, profile, isAirgap }: { index: number; label: string; profile: CloudProfile; isAirgap?: boolean }) {
  const s = useWizardStore()
  const code = s.regionCloudRegions[index] ?? ''
  const region = profile.regions.find((r) => r.code === code)
  const workerCount = s.regionWorkerCounts[index] ?? 0
  const isConfigured = regionReady(s, profile, index)
  const price = totalPrice(regionMachines(s, profile, index))

  const accentColor  = isAirgap ? 'rgba(245,158,11,0.5)'  : 'rgba(56,189,248,0.22)'
  const headerColor  = isAirgap ? 'rgba(245,158,11,0.08)' : undefined

  return (
    <div data-testid={`step-provider-region-${index}`} style={{
      borderRadius: 10,
      border: isConfigured ? `1.5px solid ${accentColor}` : '1.5px solid var(--wiz-border-sub)',
      background: 'var(--wiz-bg-xs)',
    }}>
      {/* Header */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        padding: '10px 14px', borderBottom: '1px solid var(--wiz-border-sub)',
        background: isAirgap ? headerColor : undefined,
      }}>
        <div style={{
          width: 20, height: 20, borderRadius: '50%', flexShrink: 0,
          background: isConfigured
            ? isAirgap ? 'linear-gradient(135deg,#F59E0B,#F97316)' : 'linear-gradient(135deg,#38BDF8,#818CF8)'
            : 'var(--wiz-border-sub)',
          border: isConfigured ? 'none' : '1px solid var(--wiz-border)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 9, fontWeight: 700, color: isConfigured ? '#fff' : 'var(--wiz-text-sub)',
        }}>
          {isConfigured ? <Check size={10} strokeWidth={2.5}/> : index + 1}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <div style={{ fontSize: 11, fontWeight: 600, color: isAirgap ? '#F59E0B' : 'var(--wiz-text-md)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {label}
            </div>
            {isAirgap && (
              <span style={{ fontSize: 8, fontWeight: 700, letterSpacing: '0.06em', textTransform: 'uppercase', color: '#F59E0B', background: 'rgba(245,158,11,0.12)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 3, padding: '1px 5px', flexShrink: 0 }}>isolated</span>
            )}
          </div>
          {isConfigured && region && (
            <div style={{ fontSize: 10, color: isAirgap ? '#F59E0B' : 'var(--wiz-accent)', marginTop: 1 }}>
              {profile.displayName} · {region.location}
            </div>
          )}
        </div>
        {price && (
          <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', whiteSpace: 'nowrap', flexShrink: 0 }}>
            {price.amount.toFixed(2)} {price.currency}/{price.per}
          </div>
        )}
      </div>

      {/* Pickers */}
      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div>
          <div style={pickerTitle}>Region</div>
          <CustomSelect
            value={code}
            onChange={v => placeRegion(useWizardStore.getState(), profile, index, v)}
            options={profile.regions.map(r => ({ value: r.code, label: r.label, sublabel: r.location }))}
            placeholder="Select region…"
          />
        </div>

        {region && (
          <div>
            <div style={pickerTitle}>Zone</div>
            <CustomSelect
              value={s.regionZones[index] ?? ''}
              onChange={v => s.setRegionZone(index, v)}
              options={region.zones.map(z => ({ value: z, label: z }))}
              placeholder="Select zone…"
            />
          </div>
        )}

        <div>
          <div style={pickerTitle}>Control-plane size</div>
          <CustomSelect
            value={s.regionControlPlaneSizes[index] ?? ''}
            onChange={v => s.setRegionControlPlaneSize(index, v)}
            options={flavorOptions(profile, 'control-plane', code)}
            placeholder="Select size…"
          />
        </div>

        <div>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 5 }}>
            <div style={{ ...pickerTitle, marginBottom: 0 }}>Worker nodes</div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <button
                type="button"
                onClick={() => s.setRegionWorkerCount(index, Math.max(0, workerCount - 1))}
                style={{ width: 22, height: 22, borderRadius: 5, border: '1px solid var(--wiz-border)', background: 'transparent', color: 'var(--wiz-text-md)', cursor: 'pointer', fontSize: 14, lineHeight: 1 }}
                aria-label="Decrease worker count"
              >−</button>
              <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-hi)', minWidth: 18, textAlign: 'center' }}>{workerCount}</span>
              <button
                type="button"
                onClick={() => s.setRegionWorkerCount(index, Math.min(50, workerCount + 1))}
                style={{ width: 22, height: 22, borderRadius: 5, border: '1px solid var(--wiz-border)', background: 'transparent', color: 'var(--wiz-text-md)', cursor: 'pointer', fontSize: 14, lineHeight: 1 }}
                aria-label="Increase worker count"
              >+</button>
            </div>
          </div>
          {workerCount > 0 ? (
            <CustomSelect
              value={s.regionWorkerSizes[index] ?? ''}
              onChange={v => s.setRegionWorkerSize(index, v)}
              options={flavorOptions(profile, 'worker', code)}
              placeholder="Select worker size…"
            />
          ) : (
            <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', padding: '6px 0' }}>
              No worker nodes — control plane runs all workloads (solo mode).
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

/* ── StepProvider ────────────────────────────────────────────────── */
/** The notice for separation none on two or more regions: word for word the
 *  message the writer of the Sovereign object gives the condition
 *  RegionSeparation in that case. */
const SAME_FAILURE_DOMAIN = 'Regions share one failure domain. Not a disaster-recovery configuration.'

/**
 * The region separation of a request, by the rule the writer of the
 * Sovereign object applies to the regions of status.network
 * (core/cmd/sovereign-ops/internal/sovereignobject/separation.go): site when
 * every region has its own cloud region; zone when cloud regions repeat but
 * every region has its own pair of cloud region and zone; none otherwise,
 * and always for fewer than two regions. Zone is not stated before a cloud
 * keeps the zone of the request (the open point of spike NET-300), so the
 * step gives site or none.
 */
function requestSeparation(cloudRegions: string[]): 'site' | 'none' {
  if (cloudRegions.length < 2) return 'none'
  return new Set(cloudRegions).size === cloudRegions.length ? 'site' : 'none'
}

/**
 * The provider step: the clouds of GET /api/v1/providers, and per region a
 * region, a zone and flavors of the chosen cloud's profile
 * (GET /api/v1/providers/{provider}). Every region of a Sovereign is on the
 * one chosen cloud. The step keeps no list of clouds, regions or flavors.
 */
export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const session = useSession()
  const [attempt, setAttempt] = useState(0)
  const [signIn, setSignIn] = useState(false)

  const list = useProviders(attempt)
  const entries = list.state === 'ready' ? list.value : []
  const chosen = entries.find(e => e.provider === store.provider && e.installation === store.installation)
  const loaded = useProfile(chosen ? chosen.provider : null, chosen?.installation ?? '', attempt)
  const profile = loaded?.state === 'ready' ? loaded.value : null

  const topology     = store.topology
  const regionCount  = topology ? (TOPOLOGY_REGION_COUNT[topology]  ?? 1) : 1
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? ['Region 1']) : ['Region 1']
  const hasAirgap    = store.airgap
  const totalCards   = regionCount + (hasAirgap ? 1 : 0)
  const cards        = Array.from({ length: totalCards }, (_, i) => i)

  /* A first visit, or a stored cloud the list no longer holds: the first
     listed cloud is chosen. */
  useEffect(() => {
    const first = entries[0]
    if (list.state === 'ready' && !chosen && first) useWizardStore.getState().setProvider(first.provider, first.installation)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list.state, chosen?.id])

  /* Each region without a region of the profile takes one — region i the
     i-th region of the profile, round the list — with its default zone and
     flavors, and 2 workers (issue #733: a first Sovereign is multi-node). */
  useEffect(() => {
    if (!profile) return
    const s = useWizardStore.getState()
    for (let i = 0; i < totalCards; i++) {
      if (profile.regions.some(r => r.code === s.regionCloudRegions[i])) continue
      const region = profile.regions[i % profile.regions.length]
      if (!region) return
      placeRegion(useWizardStore.getState(), profile, i, region.code)
      if (s.regionWorkerCounts[i] === undefined) s.setRegionWorkerCount(i, 2)
    }
  }, [profile, totalCards])

  const oneRegionOnly = !!profile && !profile.capabilities.multiRegion && totalCards > 1
  const allConfigured = !!profile && !oneRegionOnly && cards.every(i => regionReady(store, profile, i))
  const total = profile ? totalPrice(cards.flatMap(i => regionMachines(store, profile, i))) : null

  /* The region separation of the request: its regions are every card, the
     AIR-GAP region included. Stated once every region of a request of two or
     more regions has its cloud region; with none, the notice says the
     regions are not a disaster-recovery configuration. */
  const cloudRegions = cards.map(i => store.regionCloudRegions[i] ?? '')
  const separationShown = cloudRegions.length >= 2 && cloudRegions.every(Boolean)
  const separation = requestSeparation(cloudRegions)

  /* Max 3 cards per row */
  const gridCols = `repeat(${Math.min(totalCards, 3)}, 1fr)`
  const listError = list.state === 'error' ? list.error : null
  const unauthorized = listError instanceof ProvidersApiError && listError.status === 401

  return (
    <StepShell
      title="Cloud provider per region"
      description="Pick a provider, region, and instance sizes for each topology slot. The clouds, their regions, zones and instance sizes are those of the cloud profiles the provisioning API serves."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      {list.state === 'loading' && <div style={notice}>Loading the clouds…</div>}
      {unauthorized && (
        <div data-testid="step-provider-sign-in" style={notice}>
          The clouds are listed for a signed-in sovereign-admin.{' '}
          <button type="button" onClick={() => setSignIn(true)} style={{ color: 'var(--wiz-accent)', background: 'none', border: 'none', cursor: 'pointer', padding: 0, textDecoration: 'underline' }}>
            Sign in to see them
          </button>
        </div>
      )}
      {listError && !unauthorized && (
        <div data-testid="step-provider-list-error" style={warning}>
          The clouds cannot be read: {listError.message}.{' '}
          <button type="button" onClick={() => setAttempt(a => a + 1)}>Retry</button>
        </div>
      )}
      {list.state === 'ready' && entries.length === 0 && (
        <div data-testid="step-provider-no-cloud" style={warning}>
          No cloud is offered: the provisioning API lists no proven provider pack or installation.
        </div>
      )}

      {entries.length > 0 && (
        <div role="radiogroup" aria-label="Cloud" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 8 }}>
          {entries.map(e => {
            const active = e.id === chosen?.id
            return (
              <button
                key={e.id}
                type="button"
                role="radio"
                aria-checked={active}
                data-testid={`provider-card-${e.id}`}
                onClick={() => { if (!active) store.setProvider(e.provider, e.installation) }}
                style={{
                  textAlign: 'left', padding: '9px 12px', borderRadius: 9, cursor: 'pointer',
                  border: active ? '1.5px solid rgba(56,189,248,0.5)' : '1.5px solid var(--wiz-border-sub)',
                  background: active ? 'rgba(56,189,248,0.06)' : 'var(--wiz-bg-xs)',
                  display: 'flex', flexDirection: 'column', gap: 2,
                }}
              >
                <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>{e.displayName}</span>
                <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', fontFamily: 'JetBrains Mono, monospace' }}>{e.id}</span>
                <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)' }}>{SOURCE_TEXT[e.credentialSource]}</span>
              </button>
            )
          })}
        </div>
      )}

      {loaded?.state === 'loading' && <div style={notice}>Loading the profile of {chosen?.displayName}…</div>}
      {loaded?.state === 'error' && (
        <div data-testid="step-provider-profile-error" style={warning}>
          The profile of {chosen?.displayName} cannot be read: {loaded.error.message}.{' '}
          <button type="button" onClick={() => setAttempt(a => a + 1)}>Retry</button>
        </div>
      )}
      {oneRegionOnly && profile && (
        <div data-testid="step-provider-one-region" style={warning}>
          {profile.displayName} allows one region. Go back and choose the one-region topology.
        </div>
      )}

      {profile && (
        <div style={{ display: 'grid', gridTemplateColumns: gridCols, gap: 12, alignItems: 'start' }}>
          {/* Standard topology regions */}
          {regionLabels.map((label, i) => (
            <RegionCard key={i} index={i} label={label} profile={profile} />
          ))}

          {/* AIR-GAP region card — appears when air-gap add-on is enabled */}
          {hasAirgap && (
            <RegionCard key="airgap" index={regionCount} label="AIR-GAP Region" profile={profile} isAirgap />
          )}
        </div>
      )}

      {separationShown && (
        <div
          data-testid="step-provider-region-separation"
          style={{
            borderRadius: 8, padding: '9px 12px',
            background: separation === 'none' ? 'rgba(245,158,11,0.06)' : 'rgba(56,189,248,0.04)',
            border: separation === 'none' ? '1px solid rgba(245,158,11,0.3)' : '1px solid rgba(56,189,248,0.12)',
            display: 'flex', flexDirection: 'column', gap: 3, fontSize: 11, color: 'var(--wiz-text-sub)',
          }}
        >
          <span>
            Region separation:{' '}
            <strong data-testid="step-provider-region-separation-value" style={{ color: 'var(--wiz-text-hi)' }}>{separation}</strong>
          </span>
          {separation === 'none' && (
            <span data-testid="step-provider-region-separation-notice" style={{ color: '#F59E0B' }}>{SAME_FAILURE_DOMAIN}</span>
          )}
        </div>
      )}

      {/* Total cost — the display prices of the profile, shown only when
          every chosen flavor has one in one currency and period. */}
      {total && (
        <div data-testid="step-provider-total" style={{ ...notice, marginTop: 6, display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
          <span>
            Estimated infrastructure cost across {totalCards} region{totalCards > 1 ? 's' : ''}
          </span>
          <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--wiz-accent)' }}>
            {total.amount.toFixed(2)} {total.currency}/{total.per}
          </span>
        </div>
      )}

      {/* Default storage class (issue #4057) — a single OPTIONAL override.
          Empty is the common case: the catalyst-api Request struct fills the
          default class of the cloud profile when this arrives blank; the
          placeholder shows it. `local-path` is not permitted — PVCs must land
          on a durable cloud-volume class. */}
      <label
        style={{
          marginTop: 6,
          display: 'flex',
          flexDirection: 'column',
          gap: 4,
          fontSize: '0.78rem',
          fontWeight: 600,
          color: 'var(--wiz-text-md)',
          letterSpacing: '0.02em',
        }}
      >
        Default storage class
        <input
          type="text"
          data-testid="step-provider-storage-class"
          placeholder={profile?.storage.defaultClass ?? ''}
          value={store.storageClass}
          onChange={e => store.setStorageClass(e.target.value)}
          style={{
            width: '100%',
            padding: '0.55rem 0.7rem',
            background: 'var(--wiz-bg-input)',
            border: '1px solid var(--wiz-border-sub)',
            borderRadius: 7,
            color: 'var(--wiz-text-hi)',
            font: 'inherit',
            fontSize: '0.88rem',
          }}
        />
        <span style={{ fontSize: '0.74rem', fontWeight: 400, color: 'var(--wiz-text-sub)', lineHeight: 1.45 }}>
          Optional — defaults to the storage class of the cloud profile
          {profile && <>{' '}(<code style={{ fontFamily: 'JetBrains Mono, monospace' }}>{profile.storage.defaultClass}</code>)</>}.
          {' '}local-path is not permitted.
        </span>
      </label>

      <PinSignInModal
        open={signIn}
        onClose={() => setSignIn(false)}
        title="Sign in to see the clouds"
        onAuthenticated={() => {
          setSignIn(false)
          session.refetch()
          setAttempt(a => a + 1)
        }}
      />
    </StepShell>
  )
}
