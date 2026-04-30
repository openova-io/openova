/**
 * InfrastructureNetwork — Network tab of the Infrastructure surface.
 * Three card sections: Load Balancers + DRGs / VPC Gateways + Peerings.
 *
 * Per founder spec: "network (lbs, drgs, peerings etc)".
 */

import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  getNetwork,
  type DRGItem,
  type LoadBalancerItem,
  type NetworkResponse,
  type PeeringItem,
} from '@/lib/infrastructure.types'

const STALE_MS = 30_000

interface InfrastructureNetworkProps {
  initialDataOverride?: NetworkResponse
}

export function InfrastructureNetwork({
  initialDataOverride,
}: InfrastructureNetworkProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/infrastructure/network' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const query = useQuery<NetworkResponse>({
    queryKey: ['infra-network', deploymentId],
    queryFn: () => getNetwork(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
  })

  const data = initialDataOverride ?? query.data
  const isLoading = !initialDataOverride && query.isLoading && !data
  const lbs = data?.loadBalancers ?? []
  const drgs = data?.drgs ?? []
  const peerings = data?.peerings ?? []
  const isEmpty =
    !isLoading && lbs.length === 0 && drgs.length === 0 && peerings.length === 0

  return (
    <div data-testid="infrastructure-network">
      {isLoading && (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="infrastructure-network-loading"
        >
          Loading network resources…
        </div>
      )}

      {isEmpty && !query.isError && (
        <div className="infra-empty" data-testid="infrastructure-network-empty">
          <p className="title">No network resources yet.</p>
          <p className="sub">
            Load balancers, DRGs and peerings will appear here as the
            Sovereign cluster registers them.
          </p>
        </div>
      )}

      {!isEmpty && (
        <>
          <section className="infra-section" data-testid="infrastructure-lbs-section">
            <h2>
              Load Balancers{' '}
              <span className="count" data-testid="infrastructure-lbs-count">{lbs.length}</span>
            </h2>
            {lbs.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No load balancers reported.
              </p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-lbs-grid">
                {lbs.map((l) => <LBCard key={l.id} lb={l} />)}
              </div>
            )}
          </section>

          <section className="infra-section" data-testid="infrastructure-drgs-section">
            <h2>
              DRGs / VPC Gateways{' '}
              <span className="count" data-testid="infrastructure-drgs-count">{drgs.length}</span>
            </h2>
            {drgs.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">No DRGs reported.</p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-drgs-grid">
                {drgs.map((d) => <DRGCard key={d.id} drg={d} />)}
              </div>
            )}
          </section>

          <section className="infra-section" data-testid="infrastructure-peerings-section">
            <h2>
              Peerings{' '}
              <span className="count" data-testid="infrastructure-peerings-count">{peerings.length}</span>
            </h2>
            {peerings.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No peerings reported.
              </p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-peerings-grid">
                {peerings.map((p) => <PeeringCard key={p.id} peering={p} />)}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function LBCard({ lb }: { lb: LoadBalancerItem }) {
  return (
    <div
      className="infra-card"
      data-status={lb.status}
      data-testid={`infrastructure-lb-card-${lb.id}`}
    >
      <span className="infra-card-status" data-status={lb.status}>
        {lb.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{lb.name}</span>
        <span className="infra-card-kind">load balancer</span>
      </div>
      <div className="infra-card-row"><span>Public IP</span><span className="v">{lb.publicIP}</span></div>
      <div className="infra-card-row"><span>Ports</span><span className="v">{lb.ports}</span></div>
      <div className="infra-card-row"><span>Targets</span><span className="v">{lb.targetHealth}</span></div>
      <div className="infra-card-row"><span>Region</span><span className="v">{lb.region}</span></div>
    </div>
  )
}

function DRGCard({ drg }: { drg: DRGItem }) {
  return (
    <div
      className="infra-card"
      data-status={drg.status}
      data-testid={`infrastructure-drg-card-${drg.id}`}
    >
      <span className="infra-card-status" data-status={drg.status}>
        {drg.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{drg.name}</span>
        <span className="infra-card-kind">drg</span>
      </div>
      <div className="infra-card-row"><span>CIDR</span><span className="v">{drg.cidr}</span></div>
      <div className="infra-card-row"><span>Region</span><span className="v">{drg.region}</span></div>
      <div className="infra-card-row"><span>Peers</span><span className="v">{drg.peers || '—'}</span></div>
    </div>
  )
}

function PeeringCard({ peering }: { peering: PeeringItem }) {
  return (
    <div
      className="infra-card"
      data-status={peering.status}
      data-testid={`infrastructure-peering-card-${peering.id}`}
    >
      <span className="infra-card-status" data-status={peering.status}>
        {peering.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{peering.name}</span>
        <span className="infra-card-kind">peering</span>
      </div>
      <div className="infra-card-row"><span>VPCs</span><span className="v">{peering.vpcPair}</span></div>
      <div className="infra-card-row"><span>Subnets</span><span className="v">{peering.subnets}</span></div>
    </div>
  )
}
