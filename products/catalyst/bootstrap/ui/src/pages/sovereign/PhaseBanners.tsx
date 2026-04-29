/**
 * PhaseBanners — "Hetzner infra" + "Cluster bootstrap" status banners
 * rendered ABOVE the application card grid on the Sovereign Admin page.
 *
 * These two phases are NOT Applications — they're the Phase 0 (cloud
 * provisioning via OpenTofu) and the cloud-init handoff that bootstraps
 * Flux + Crossplane in the freshly-minted cluster. The operator wanted
 * them visible because they're prerequisites for any Application card
 * flipping out of `pending`, but distinct in shape from per-component
 * install events. Compact banners, click to expand inline log details,
 * never confused with an Application tile.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the phase
 * states + log buckets come from `eventReducer.ts` — the same reducer
 * the per-Application cards consume.
 */

import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import {
  CLUSTER_BOOTSTRAP_BUCKET,
  HETZNER_INFRA_BUCKET,
  type DeploymentEvent,
  type PhaseStatus,
  type ReducerState,
} from './eventReducer'

interface PhaseBannersProps {
  state: ReducerState
}

export function PhaseBanners({ state }: PhaseBannersProps) {
  return (
    <div className="sov-phase-row" data-testid="sov-phase-row">
      <PhaseBanner
        id="hetzner-infra"
        name="Hetzner infra"
        sub="OpenTofu Phase 0 — network · firewall · servers · load balancer"
        status={state.hetznerInfra.status}
        message={state.hetznerInfra.message}
        events={state.eventsByTarget[HETZNER_INFRA_BUCKET()] ?? []}
      />
      <PhaseBanner
        id="cluster-bootstrap"
        name="Cluster bootstrap"
        sub="cloud-init → Flux + Crossplane in-cluster"
        status={state.clusterBootstrap.status}
        message={state.clusterBootstrap.message}
        events={state.eventsByTarget[CLUSTER_BOOTSTRAP_BUCKET()] ?? []}
      />
    </div>
  )
}

interface PhaseBannerProps {
  id: string
  name: string
  sub: string
  status: PhaseStatus
  message: string | null
  events: readonly DeploymentEvent[]
}

const PHASE_STATE_LABEL: Record<PhaseStatus, string> = {
  pending: 'Pending',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
}

const PHASE_TONE: Record<PhaseStatus, { bg: string; fg: string; border: string }> = {
  pending: { bg: 'rgba(148,163,184,0.10)', fg: 'var(--wiz-text-md)', border: 'rgba(148,163,184,0.30)' },
  running: { bg: 'rgba(56,189,248,0.10)',  fg: '#38BDF8',            border: 'rgba(56,189,248,0.35)' },
  done:    { bg: 'rgba(74,222,128,0.10)',  fg: '#4ADE80',            border: 'rgba(74,222,128,0.35)' },
  failed:  { bg: 'rgba(248,113,113,0.10)', fg: '#F87171',            border: 'rgba(248,113,113,0.35)' },
}

function PhaseBanner({ id, name, sub, status, message, events }: PhaseBannerProps) {
  const [expanded, setExpanded] = useState(false)
  const tone = PHASE_TONE[status]
  return (
    <section
      className="sov-phase"
      data-status={status}
      data-testid={`sov-phase-${id}`}
    >
      <div className="sov-phase-head">
        <span className="sov-phase-name">{name}</span>
        <span className="sov-phase-sub">{sub}</span>
        <span
          data-testid={`sov-phase-${id}-status`}
          style={{
            marginLeft: 'auto',
            padding: '0.15rem 0.55rem',
            borderRadius: 999,
            fontSize: '0.62rem',
            fontWeight: 700,
            letterSpacing: '0.06em',
            textTransform: 'uppercase',
            background: tone.bg,
            color: tone.fg,
            border: `1px solid ${tone.border}`,
          }}
        >
          {PHASE_STATE_LABEL[status]}
        </span>
      </div>
      {message && (
        <pre className="sov-phase-msg" data-testid={`sov-phase-${id}-msg`}>
          {message}
        </pre>
      )}
      <button
        type="button"
        className="sov-phase-toggle"
        onClick={() => setExpanded((v) => !v)}
        data-testid={`sov-phase-${id}-toggle`}
        aria-expanded={expanded}
      >
        {expanded ? <ChevronDown size={12} aria-hidden /> : <ChevronRight size={12} aria-hidden />}
        {events.length} events
      </button>
      {expanded && (
        <div className="sov-phase-log" data-testid={`sov-phase-${id}-log`}>
          {events.length === 0 ? (
            <div className="sov-log-empty">No events yet.</div>
          ) : (
            events.map((ev, i) => (
              <div key={i} className="sov-log-line" data-level={ev.level ?? 'info'}>
                <span className="sov-log-ts">{(ev.time ?? '').slice(11, 19) || '—'}</span>
                <span className="sov-log-phase">{ev.phase}</span>
                <span className="sov-log-msg">{ev.message ?? ''}</span>
              </div>
            ))
          )}
        </div>
      )}
    </section>
  )
}
