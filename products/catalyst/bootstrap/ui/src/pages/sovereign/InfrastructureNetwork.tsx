/**
 * InfrastructureNetwork — Network tab. Flat table [LB · Peering ·
 * Firewall · DNS zone], reads off the shared infrastructure tree.
 *
 * Per founder spec (issue #228): "Network — flat table [LB · Peering
 * · Firewall · DNS zone]. Bulk: add rule, attach."
 */

import { useMemo, useState } from 'react'
import { useCloud } from './CloudPage'
import {
  AddLBModal,
  AddPeeringModal,
  EditFirewallRulesModal,
  EditDNSRecordsModal,
} from '@/components/CrudModals'
import type {
  FirewallSpec,
  LoadBalancerSpec,
  NetworkSpec,
  PeeringSpec,
  RegionSpec,
} from '@/lib/infrastructure.types'

interface LBRow {
  lb: LoadBalancerSpec
  region: RegionSpec
  clusterId: string
}

interface PeeringRow {
  peering: PeeringSpec
  region: RegionSpec
}

interface FirewallRow {
  firewall: FirewallSpec
  region: RegionSpec
}

export function InfrastructureNetwork() {
  const { deploymentId, data, isLoading } = useCloud()

  const { lbs, peerings, firewalls, networks } = useMemo(() => {
    const lbs: LBRow[] = []
    const peerings: PeeringRow[] = []
    const firewalls: FirewallRow[] = []
    const networks: NetworkSpec[] = []
    if (!data) return { lbs, peerings, firewalls, networks }
    for (const region of data.topology.regions ?? []) {
      for (const cluster of region.clusters ?? []) {
        for (const lb of cluster.loadBalancers ?? []) {
          lbs.push({
            lb: { ...lb, listeners: lb.listeners ?? [], targets: lb.targets ?? [] },
            region,
            clusterId: cluster.id,
          })
        }
      }
      for (const net of region.networks ?? []) {
        networks.push(net)
        for (const p of net.peerings ?? []) peerings.push({ peering: p, region })
        for (const f of net.firewalls ?? []) firewalls.push({ firewall: f, region })
      }
    }
    return { lbs, peerings, firewalls, networks }
  }, [data])

  const [addLBFor, setAddLBFor] = useState<RegionSpec | null>(null)
  const [addPeeringOpen, setAddPeeringOpen] = useState(false)
  const [editFirewall, setEditFirewall] = useState<FirewallSpec | null>(null)
  const [editDNS, setEditDNS] = useState<string | null>(null)

  const isEmpty =
    !isLoading && lbs.length === 0 && peerings.length === 0 && firewalls.length === 0

  return (
    <div data-testid="infrastructure-network">
      {isLoading && (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]" data-testid="infrastructure-network-loading">
          Loading network resources…
        </div>
      )}

      {isEmpty && (
        <div className="infra-empty" data-testid="infrastructure-network-empty">
          <p className="title">No network resources yet.</p>
          <p className="sub">Load balancers, peerings, firewalls and DNS zones will appear here.</p>
        </div>
      )}

      {!isEmpty && data && (
        <>
          <div className="infra-bulk-actions" data-testid="infrastructure-network-bulk">
            <span className="label">Bulk actions</span>
            <button
              type="button"
              className="primary"
              data-testid="infrastructure-network-add-peering"
              onClick={() => setAddPeeringOpen(true)}
            >
              + Add peering
            </button>
            <button
              type="button"
              data-testid="infrastructure-network-edit-dns"
              onClick={() => setEditDNS(`zone-${data.topology.regions[0]?.id ?? 'default'}`)}
            >
              Edit DNS zone
            </button>
          </div>

          <section className="infra-section" data-testid="infrastructure-lbs-section">
            <h2>
              Load Balancers <span className="count" data-testid="infrastructure-lbs-count">{lbs.length}</span>
            </h2>
            <FlatTable testId="infrastructure-lbs-table" headers={['Name', 'Public IP', 'Listeners', 'Targets', 'Region', 'Status', '']}>
              {lbs.map(({ lb, region }) => (
                <tr key={lb.id} data-testid={`infrastructure-lb-row-${lb.id}`}>
                  <td>{lb.name}</td>
                  <td style={{ fontFamily: 'monospace' }}>{lb.publicIP}</td>
                  <td>{lb.listeners.map((l) => `${l.protocol}:${l.port}`).join(', ')}</td>
                  <td>{`${lb.targets.filter((t) => t.status === 'healthy').length}/${lb.targets.length}`}</td>
                  <td>{region.providerRegion}</td>
                  <td>
                    <StatusBadge status={lb.status} />
                  </td>
                  <td />
                </tr>
              ))}
            </FlatTable>
            <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
              {data.topology.regions.map((r) => (
                <button
                  key={r.id}
                  type="button"
                  style={{ ...rowBtn, borderColor: 'var(--color-accent)', color: 'var(--color-accent)' }}
                  onClick={() => setAddLBFor(r)}
                  data-testid={`infrastructure-network-add-lb-${r.id}`}
                >
                  + Add LB to {r.name}
                </button>
              ))}
            </div>
          </section>

          <section className="infra-section" data-testid="infrastructure-peerings-section">
            <h2>
              Peerings <span className="count" data-testid="infrastructure-peerings-count">{peerings.length}</span>
            </h2>
            <FlatTable testId="infrastructure-peerings-table" headers={['Name', 'VPCs', 'Subnets', 'Region', 'Status']}>
              {peerings.map(({ peering, region }) => (
                <tr key={peering.id} data-testid={`infrastructure-peering-row-${peering.id}`}>
                  <td>{peering.name}</td>
                  <td>{peering.vpcPair}</td>
                  <td style={{ fontFamily: 'monospace' }}>{peering.subnets}</td>
                  <td>{region.providerRegion}</td>
                  <td>
                    <StatusBadge status={peering.status} />
                  </td>
                </tr>
              ))}
              {peerings.length === 0 && (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', color: 'var(--color-text-dim)', padding: 12 }}>
                    No peerings yet.
                  </td>
                </tr>
              )}
            </FlatTable>
          </section>

          <section className="infra-section" data-testid="infrastructure-firewalls-section">
            <h2>
              Firewalls <span className="count" data-testid="infrastructure-firewalls-count">{firewalls.length}</span>
            </h2>
            <FlatTable testId="infrastructure-firewalls-table" headers={['Name', 'Rules', 'Region', 'Status', '']}>
              {firewalls.map(({ firewall, region }) => (
                <tr key={firewall.id} data-testid={`infrastructure-firewall-row-${firewall.id}`}>
                  <td>{firewall.name}</td>
                  <td>{firewall.rules.length}</td>
                  <td>{region.providerRegion}</td>
                  <td>
                    <StatusBadge status={firewall.status} />
                  </td>
                  <td>
                    <button
                      type="button"
                      style={rowBtn}
                      onClick={() => setEditFirewall(firewall)}
                      data-testid={`infrastructure-firewall-row-${firewall.id}-edit`}
                    >
                      Edit rules
                    </button>
                  </td>
                </tr>
              ))}
              {firewalls.length === 0 && (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', color: 'var(--color-text-dim)', padding: 12 }}>
                    No firewalls yet.
                  </td>
                </tr>
              )}
            </FlatTable>
          </section>
        </>
      )}

      {addLBFor && (
        <AddLBModal
          open
          deploymentId={deploymentId}
          regionId={addLBFor.id}
          onClose={() => setAddLBFor(null)}
        />
      )}
      {addPeeringOpen && (
        <AddPeeringModal
          open
          deploymentId={deploymentId}
          networks={networks}
          onClose={() => setAddPeeringOpen(false)}
        />
      )}
      {editFirewall && (
        <EditFirewallRulesModal
          open
          deploymentId={deploymentId}
          firewall={editFirewall}
          onClose={() => setEditFirewall(null)}
        />
      )}
      {editDNS && (
        <EditDNSRecordsModal
          open
          deploymentId={deploymentId}
          zoneId={editDNS}
          existingRecords={[]}
          onClose={() => setEditDNS(null)}
        />
      )}
    </div>
  )
}

function FlatTable({ testId, headers, children }: { testId: string; headers: string[]; children: React.ReactNode }) {
  return (
    <table data-testid={testId} style={{ width: '100%', borderCollapse: 'separate', borderSpacing: 0, fontSize: '0.82rem' }}>
      <thead>
        <tr>
          {headers.map((h, i) => (
            <th key={i} style={{ textAlign: 'left', fontWeight: 600, fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--color-text-dim)', padding: '6px 8px', borderBottom: '1px solid var(--color-border)' }}>
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody style={{ verticalAlign: 'middle' }}>{children}</tbody>
      <style>{`
        tbody tr td { padding: 8px; border-bottom: 1px solid var(--color-border); color: var(--color-text); }
        tbody tr:hover { background: var(--color-bg-2); }
      `}</style>
    </table>
  )
}

function StatusBadge({ status }: { status: 'healthy' | 'degraded' | 'failed' | 'unknown' }) {
  return (
    <span data-status={status} style={{ display: 'inline-block', fontSize: '0.65rem', textTransform: 'uppercase', letterSpacing: '0.06em', fontWeight: 700, padding: '0.1rem 0.45rem', borderRadius: 999, background: status === 'healthy' ? 'color-mix(in srgb, var(--color-success) 18%, transparent)' : status === 'degraded' ? 'color-mix(in srgb, var(--color-warn) 18%, transparent)' : status === 'failed' ? 'color-mix(in srgb, var(--color-danger) 18%, transparent)' : 'color-mix(in srgb, var(--color-text-dim) 18%, transparent)', color: status === 'healthy' ? 'var(--color-success)' : status === 'degraded' ? 'var(--color-warn)' : status === 'failed' ? 'var(--color-danger)' : 'var(--color-text-dim)' }}>
      {status}
    </span>
  )
}

const rowBtn: React.CSSProperties = {
  border: '1px solid var(--color-border)',
  background: 'transparent',
  color: 'var(--color-text)',
  padding: '3px 8px',
  borderRadius: 5,
  fontSize: '0.72rem',
  cursor: 'pointer',
}
