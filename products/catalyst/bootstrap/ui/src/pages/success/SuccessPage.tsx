import { motion } from 'framer-motion'
import { CheckCircle2, Copy, Download, ExternalLink, Server, ArrowRight } from 'lucide-react'
import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/shared/ui/button'
import { Badge } from '@/shared/ui/badge'
import { IS_SAAS } from '@/shared/constants/env'

const MOCK_RESULT = {
  clusterContext: 'hz-fsn-rtz-prod',
  region: 'Falkenstein, DE',
  nodes: 1,
  managerUrl: 'https://catalyst.hfmp.acme.io',
  grafanaUrl: 'https://grafana.hfrp.acme.io',
  kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://65.21.xxx.xxx:6443
    certificate-authority-data: <redacted>
  name: hz-fsn-rtz-prod
contexts:
- context:
    cluster: hz-fsn-rtz-prod
    user: default
  name: hz-fsn-rtz-prod
current-context: hz-fsn-rtz-prod
kind: Config
users:
- name: default
  user:
    client-certificate-data: <redacted>
    client-key-data: <redacted>`,
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    await navigator.clipboard.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <button onClick={copy} className="flex items-center gap-1 text-xs text-[oklch(50%_0.01_250)] hover:text-[oklch(75%_0.01_250)] transition-colors">
      <Copy className="h-3.5 w-3.5" />
      {copied ? 'Copied!' : 'Copy'}
    </button>
  )
}

export function SuccessPage() {
  function downloadKubeconfig() {
    const blob = new Blob([MOCK_RESULT.kubeconfig], { type: 'text/yaml' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${MOCK_RESULT.clusterContext}.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="min-h-dvh bg-[--color-surface-0] flex items-center justify-center p-6">
      <div className="w-full max-w-lg">
        <motion.div
          initial={{ opacity: 0, scale: 0.95, y: 20 }}
          animate={{ opacity: 1, scale: 1, y: 0 }}
          transition={{ duration: 0.5, ease: [0.34, 1.56, 0.64, 1] }}
          className="flex flex-col gap-8"
        >
          {/* Success icon */}
          <div className="flex flex-col items-center gap-4 text-center">
            <motion.div
              initial={{ scale: 0 }}
              animate={{ scale: 1 }}
              transition={{ delay: 0.2, type: 'spring', stiffness: 300, damping: 20 }}
              className="flex h-16 w-16 items-center justify-center rounded-full bg-[--color-success]/15 ring-8 ring-[--color-success]/8"
            >
              <CheckCircle2 className="h-8 w-8 text-[--color-success]" />
            </motion.div>
            <div>
              <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Cluster is ready</h1>
              <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
                Your OpenOva platform is running. The Bootstrap wizard has completed its job.
              </p>
            </div>
          </div>

          {/* Cluster info */}
          <div className="rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-1] divide-y divide-[--color-surface-border]">
            <div className="flex items-center gap-3 p-4">
              <Server className="h-4 w-4 text-[--color-success] shrink-0" />
              <div className="flex-1">
                <code className="text-sm font-mono text-[oklch(90%_0.01_250)]">{MOCK_RESULT.clusterContext}</code>
                <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">{MOCK_RESULT.region} · {MOCK_RESULT.nodes} node</p>
              </div>
              <Badge variant="success">Healthy</Badge>
            </div>

            <div className="flex items-center justify-between p-4">
              <div>
                <p className="text-xs font-semibold text-[oklch(60%_0.01_250)]">Lifecycle Manager</p>
                <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5 font-mono">{MOCK_RESULT.managerUrl}</p>
              </div>
              <a href={MOCK_RESULT.managerUrl} target="_blank" rel="noopener noreferrer">
                <Button variant="secondary" size="sm">
                  Open
                  <ExternalLink className="h-3.5 w-3.5" />
                </Button>
              </a>
            </div>

            <div className="flex items-center justify-between p-4">
              <div>
                <p className="text-xs font-semibold text-[oklch(60%_0.01_250)]">Grafana</p>
                <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5 font-mono">{MOCK_RESULT.grafanaUrl}</p>
              </div>
              <a href={MOCK_RESULT.grafanaUrl} target="_blank" rel="noopener noreferrer">
                <Button variant="secondary" size="sm">
                  Open
                  <ExternalLink className="h-3.5 w-3.5" />
                </Button>
              </a>
            </div>
          </div>

          {/* Kubeconfig */}
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <p className="text-sm font-semibold text-[oklch(70%_0.01_250)]">kubeconfig</p>
              <div className="flex items-center gap-3">
                <CopyButton text={MOCK_RESULT.kubeconfig} />
                <button onClick={downloadKubeconfig} className="flex items-center gap-1 text-xs text-[oklch(50%_0.01_250)] hover:text-[oklch(75%_0.01_250)] transition-colors">
                  <Download className="h-3.5 w-3.5" />
                  Download
                </button>
              </div>
            </div>
            <pre className="rounded-[--radius-md] bg-[oklch(8%_0.008_250)] border border-[--color-surface-border] p-4 text-[10px] font-mono text-[oklch(55%_0.01_250)] overflow-x-auto scrollbar-none max-h-36">
              {MOCK_RESULT.kubeconfig}
            </pre>
          </div>

          {/* Next steps */}
          <div className="flex flex-col gap-2">
            <p className="text-xs font-semibold text-[oklch(45%_0.01_250)] uppercase tracking-wider">Next steps</p>
            <div className="flex flex-col gap-1.5 text-sm text-[oklch(55%_0.01_250)]">
              {[
                'Open the Lifecycle Manager to install additional components',
                'Configure your DNS to point your domain to the cluster ingress',
                'Review the Grafana observability stack for baseline metrics',
              ].map((step, i) => (
                <div key={i} className="flex gap-2.5">
                  <span className="text-[oklch(35%_0.01_250)] shrink-0 font-mono">{i + 1}.</span>
                  {step}
                </div>
              ))}
            </div>
          </div>

          {/* CTA */}
          <div className="flex items-center gap-3">
            <a href={MOCK_RESULT.managerUrl} target="_blank" rel="noopener noreferrer" className="flex-1">
              <Button size="lg" className="w-full">
                Open Lifecycle Manager
                <ArrowRight className="h-4 w-4" />
              </Button>
            </a>
            {IS_SAAS && (
              <Link to="/app/dashboard">
                <Button variant="secondary" size="lg">Dashboard</Button>
              </Link>
            )}
          </div>
        </motion.div>
      </div>
    </div>
  )
}
