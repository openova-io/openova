import { motion } from 'framer-motion'
import { Plus, Server, Activity, Clock, AlertCircle } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/shared/ui/button'
import { Badge } from '@/shared/ui/badge'
import { Card, CardContent } from '@/shared/ui/card'

// Mock data — will be replaced by TanStack Query + API
const MOCK_DEPLOYMENTS = [
  {
    id: 'd-001',
    name: 'hz-fsn-rtz-prod',
    org: 'acme-corp',
    provider: 'Hetzner',
    region: 'Falkenstein',
    status: 'healthy' as const,
    nodes: 2,
    components: 14,
    createdAt: '2026-03-18T10:30:00Z',
  },
  {
    id: 'd-002',
    name: 'hz-hel-rtz-prod',
    org: 'acme-corp',
    provider: 'Hetzner',
    region: 'Helsinki',
    status: 'healthy' as const,
    nodes: 2,
    components: 14,
    createdAt: '2026-03-18T10:45:00Z',
  },
  {
    id: 'd-003',
    name: 'hz-fsn-rtz-dev',
    org: 'acme-corp',
    provider: 'Hetzner',
    region: 'Falkenstein',
    status: 'provisioning' as const,
    nodes: 1,
    components: 6,
    createdAt: '2026-03-19T09:00:00Z',
  },
]

const STATUS_CONFIG = {
  healthy: { label: 'Healthy', variant: 'success' as const, icon: Activity },
  provisioning: { label: 'Provisioning', variant: 'info' as const, icon: Clock },
  degraded: { label: 'Degraded', variant: 'warning' as const, icon: AlertCircle },
  failed: { label: 'Failed', variant: 'error' as const, icon: AlertCircle },
  pending: { label: 'Pending', variant: 'default' as const, icon: Clock },
  destroying: { label: 'Destroying', variant: 'warning' as const, icon: Clock },
}

function StatCard({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <Card>
      <CardContent className="pt-5">
        <p className="text-xs text-[oklch(45%_0.01_250)] uppercase tracking-wider font-medium">{label}</p>
        <p className="mt-1 text-2xl font-bold text-[oklch(92%_0.01_250)] tabular-nums">{value}</p>
        {sub && <p className="mt-0.5 text-xs text-[oklch(40%_0.01_250)]">{sub}</p>}
      </CardContent>
    </Card>
  )
}

export function DashboardPage() {
  const healthy = MOCK_DEPLOYMENTS.filter((d) => d.status === 'healthy').length
  const totalNodes = MOCK_DEPLOYMENTS.reduce((s, d) => s + d.nodes, 0)

  return (
    <div className="flex flex-col gap-8 p-8 max-w-5xl mx-auto">
      {/* Header */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3 }}
        className="flex items-start justify-between gap-4"
      >
        <div>
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Deployments</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
            Manage your OpenOva clusters across providers and regions.
          </p>
        </div>
        <Link to="/wizard">
          <Button size="md">
            <Plus className="h-4 w-4" />
            New deployment
          </Button>
        </Link>
      </motion.div>

      {/* Stats */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.05 }}
        className="grid grid-cols-2 sm:grid-cols-4 gap-4"
      >
        <StatCard label="Total" value={MOCK_DEPLOYMENTS.length} sub="deployments" />
        <StatCard label="Healthy" value={healthy} sub={`of ${MOCK_DEPLOYMENTS.length}`} />
        <StatCard label="Total nodes" value={totalNodes} sub="across all clusters" />
        <StatCard label="Provider" value="Hetzner" sub="1 active provider" />
      </motion.div>

      {/* Deployment list */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.1 }}
        className="flex flex-col gap-3"
      >
        <h2 className="text-sm font-semibold text-[oklch(60%_0.01_250)] uppercase tracking-wider">
          Clusters
        </h2>

        {MOCK_DEPLOYMENTS.map((d, i) => {
          const config = STATUS_CONFIG[d.status]
          const StatusIcon = config.icon
          return (
            <motion.div
              key={d.id}
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.25, delay: 0.12 + i * 0.05 }}
            >
              <Card className="hover:border-[oklch(28%_0.02_250)] transition-colors duration-150 cursor-pointer">
                <CardContent className="flex items-center gap-4 py-4">
                  {/* Status dot */}
                  <div className="shrink-0">
                    <div
                      className={`flex h-9 w-9 items-center justify-center rounded-[--radius-md] ${
                        d.status === 'healthy' ? 'bg-[--color-success]/10' :
                        d.status === 'provisioning' ? 'bg-[--color-info]/10' :
                        'bg-[--color-surface-2]'
                      }`}
                    >
                      <Server className={`h-4 w-4 ${
                        d.status === 'healthy' ? 'text-[--color-success]' :
                        d.status === 'provisioning' ? 'text-[--color-info]' :
                        'text-[oklch(50%_0.01_250)]'
                      }`} />
                    </div>
                  </div>

                  {/* Info */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <p className="text-sm font-semibold text-[oklch(92%_0.01_250)] font-mono">{d.name}</p>
                      <Badge variant={config.variant}>
                        <StatusIcon className="h-3 w-3" />
                        {config.label}
                      </Badge>
                    </div>
                    <p className="mt-0.5 text-xs text-[oklch(45%_0.01_250)]">
                      {d.provider} · {d.region} · {d.nodes} node{d.nodes !== 1 ? 's' : ''} · {d.components} components
                    </p>
                  </div>

                  {/* Actions */}
                  <div className="shrink-0">
                    <Button variant="secondary" size="sm">Manage</Button>
                  </div>
                </CardContent>
              </Card>
            </motion.div>
          )
        })}

        {MOCK_DEPLOYMENTS.length === 0 && (
          <div className="flex flex-col items-center justify-center gap-4 py-20 text-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-full bg-[--color-surface-2]">
              <Server className="h-6 w-6 text-[oklch(40%_0.01_250)]" />
            </div>
            <div>
              <p className="font-medium text-[oklch(70%_0.01_250)]">No deployments yet</p>
              <p className="mt-1 text-sm text-[oklch(45%_0.01_250)]">Provision your first cluster to get started.</p>
            </div>
            <Link to="/wizard"><Button>New deployment</Button></Link>
          </div>
        )}
      </motion.div>
    </div>
  )
}
