import { useEffect, useRef, useState } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { CheckCircle2, Circle, Loader2, XCircle, Terminal } from 'lucide-react'
import { Progress } from '@/shared/ui/progress'
import { Badge } from '@/shared/ui/badge'
import { cn } from '@/shared/lib/utils'
import { useWizardStore } from '@/entities/deployment/store'

interface LogLine {
  id: number
  ts: string
  level: 'info' | 'warn' | 'error' | 'debug'
  text: string
}

type PhaseStatus = 'pending' | 'running' | 'done' | 'error'

interface Phase {
  id: string
  label: string
  status: PhaseStatus
}

// Mock phases — will be driven by SSE events from the backend
const INITIAL_PHASES: Phase[] = [
  { id: 'network', label: 'Create private network', status: 'pending' },
  { id: 'servers', label: 'Provision control-plane nodes', status: 'pending' },
  { id: 'k3s', label: 'Bootstrap K3s cluster', status: 'pending' },
  { id: 'csi', label: 'Install hcloud CSI driver', status: 'pending' },
  { id: 'flux', label: 'Bootstrap Flux GitOps', status: 'pending' },
  { id: 'components', label: 'Deploy platform components', status: 'pending' },
  { id: 'verify', label: 'Verify cluster health', status: 'pending' },
]

// Mock SSE log stream
const MOCK_LOGS: Array<Omit<LogLine, 'id'>> = [
  { ts: '10:31:00', level: 'info', text: 'Initialising OpenTofu workspace...' },
  { ts: '10:31:01', level: 'info', text: 'Provider: hcloud v1.47.0' },
  { ts: '10:31:02', level: 'info', text: 'Planning infrastructure changes...' },
  { ts: '10:31:03', level: 'info', text: 'Plan: 14 to add, 0 to change, 0 to destroy' },
  { ts: '10:31:04', level: 'info', text: 'hcloud_network.rtz-prod: Creating...' },
  { ts: '10:31:06', level: 'info', text: 'hcloud_network.rtz-prod: Creation complete [id=1234567]' },
  { ts: '10:31:07', level: 'info', text: 'hcloud_network_subnet.workers: Creating...' },
  { ts: '10:31:08', level: 'info', text: 'hcloud_network_subnet.workers: Creation complete' },
  { ts: '10:31:10', level: 'info', text: 'hcloud_server.hzfsnr-k8s-cp-1p: Creating...' },
  { ts: '10:31:25', level: 'info', text: 'hcloud_server.hzfsnr-k8s-cp-1p: Creation complete [id=9876543]' },
  { ts: '10:31:26', level: 'info', text: 'Waiting for cloud-init to complete on cp node...' },
  { ts: '10:31:55', level: 'info', text: 'K3s control-plane is ready' },
  { ts: '10:31:56', level: 'info', text: 'Retrieving kubeconfig...' },
  { ts: '10:31:57', level: 'info', text: 'hcloud_volume.data: Creating...' },
  { ts: '10:31:58', level: 'info', text: 'Installing hcloud-csi-driver v2.6.0...' },
  { ts: '10:32:05', level: 'info', text: 'StorageClass hcloud-volumes created' },
  { ts: '10:32:06', level: 'info', text: 'Bootstrapping Flux v2.4.0...' },
  { ts: '10:32:12', level: 'info', text: 'GitRepository openova-platform reconciled' },
  { ts: '10:32:13', level: 'info', text: 'Kustomization infrastructure: Applied' },
  { ts: '10:32:14', level: 'info', text: 'Deploying: cert-manager, external-secrets, kyverno, cilium...' },
  { ts: '10:32:45', level: 'info', text: 'All mandatory components healthy' },
  { ts: '10:32:46', level: 'info', text: 'Cluster health check: PASSED' },
  { ts: '10:32:47', level: 'info', text: '✓ Provisioning complete — hz-fsn-rtz-prod is ready' },
]

const LEVEL_COLORS: Record<LogLine['level'], string> = {
  info: 'text-[oklch(70%_0.01_250)]',
  warn: 'text-[--color-warning]',
  error: 'text-[--color-error]',
  debug: 'text-[oklch(40%_0.01_250)]',
}

function PhaseRow({ phase }: { phase: Phase }) {
  return (
    <div className="flex items-center gap-3">
      <div className="shrink-0">
        {phase.status === 'done' && <CheckCircle2 className="h-4 w-4 text-[--color-success]" />}
        {phase.status === 'running' && <Loader2 className="h-4 w-4 text-[--color-brand-400] animate-spin" />}
        {phase.status === 'pending' && <Circle className="h-4 w-4 text-[oklch(30%_0.01_250)]" />}
        {phase.status === 'error' && <XCircle className="h-4 w-4 text-[--color-error]" />}
      </div>
      <span className={cn(
        'text-sm',
        phase.status === 'done' && 'text-[oklch(60%_0.01_250)]',
        phase.status === 'running' && 'text-[oklch(90%_0.01_250)] font-medium',
        phase.status === 'pending' && 'text-[oklch(35%_0.01_250)]',
        phase.status === 'error' && 'text-[--color-error]',
      )}>
        {phase.label}
      </span>
    </div>
  )
}

// Phase detection — maps log message substrings to phase transitions
const PHASE_RULES: Array<{ keyword: string; phaseIdx: number; action: 'start' | 'done' }> = [
  { keyword: 'Initialising OpenTofu', phaseIdx: 0, action: 'start' },
  { keyword: 'hcloud_network_subnet.workers: Creation complete', phaseIdx: 0, action: 'done' },
  { keyword: 'hcloud_server', phaseIdx: 1, action: 'start' },
  { keyword: 'K3s control-plane is ready', phaseIdx: 1, action: 'done' },
  { keyword: 'Retrieving kubeconfig', phaseIdx: 2, action: 'done' },
  { keyword: 'hcloud_volume.data: Creating', phaseIdx: 3, action: 'start' },
  { keyword: 'StorageClass hcloud-volumes created', phaseIdx: 3, action: 'done' },
  { keyword: 'Bootstrapping Flux', phaseIdx: 4, action: 'start' },
  { keyword: 'GitRepository', phaseIdx: 4, action: 'done' },
  { keyword: 'Deploying:', phaseIdx: 5, action: 'start' },
  { keyword: 'All mandatory components healthy', phaseIdx: 5, action: 'done' },
  { keyword: 'Cluster health check: PASSED', phaseIdx: 6, action: 'start' },
  { keyword: '✓ Provisioning complete', phaseIdx: 6, action: 'done' },
]

export function ProvisionPage() {
  const [phases, setPhases] = useState<Phase[]>(INITIAL_PHASES)
  const [logs, setLogs] = useState<LogLine[]>([])
  const [progress, setProgress] = useState(0)
  const [done, setDone] = useState(false)
  const logRef = useRef<HTMLDivElement>(null)
  const logCounter = useRef(0)
  const deploymentId = useWizardStore((s) => s.deploymentId)

  useEffect(() => {
    const addLog = (line: Omit<LogLine, 'id'>) =>
      setLogs((prev) => [...prev, { ...line, id: logCounter.current++ }])

    const applyPhaseRule = (text: string) => {
      for (const rule of PHASE_RULES) {
        if (text.includes(rule.keyword)) {
          if (rule.action === 'start') {
            setPhases((prev) => prev.map((p, i) => i === rule.phaseIdx ? { ...p, status: 'running' } : p))
          } else {
            setPhases((prev) => prev.map((p, i) => {
              if (i === rule.phaseIdx) return { ...p, status: 'done' }
              if (i === rule.phaseIdx + 1) return { ...p, status: 'running' }
              return p
            }))
            setProgress(Math.round(((rule.phaseIdx + 1) / INITIAL_PHASES.length) * 100))
          }
          break
        }
      }
    }

    if (deploymentId) {
      // Real SSE from the API
      const es = new EventSource(`/api/v1/deployments/${deploymentId}/logs`)
      es.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data) as { time: string; level: string; msg: string }
          addLog({ ts: data.time, level: data.level as LogLine['level'], text: data.msg })
          applyPhaseRule(data.msg)
        } catch { /* ignore malformed events */ }
      }
      es.addEventListener('done', () => {
        es.close()
        setPhases((prev) => prev.map((p) => ({ ...p, status: 'done' })))
        setProgress(100)
        setDone(true)
      })
      es.onerror = () => { es.close(); setDone(true) }
      return () => es.close()
    }

    // Mock simulation (no backend / local dev)
    const phaseTimings = [500, 2000, 3500, 5500, 6500, 8000, 9500]
    const phaseDurations = [1500, 1500, 2000, 1000, 1500, 1500, 1000]
    const timers: ReturnType<typeof setTimeout>[] = []

    phaseTimings.forEach((start, i) => {
      timers.push(setTimeout(() => {
        setPhases((prev) => prev.map((p, j) => j === i ? { ...p, status: 'running' } : p))
        timers.push(setTimeout(() => {
          setPhases((prev) => prev.map((p, j) => j === i ? { ...p, status: 'done' } : p))
          setProgress(Math.round(((i + 1) / INITIAL_PHASES.length) * 100))
          if (i === INITIAL_PHASES.length - 1) setDone(true)
        }, phaseDurations[i]))
      }, start))
    })

    MOCK_LOGS.forEach((line, i) => {
      timers.push(setTimeout(() => addLog(line), 300 + i * 430))
    })

    return () => timers.forEach(clearTimeout)
  }, [deploymentId])

  // Auto-scroll logs
  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [logs])

  return (
    <div className="min-h-dvh bg-[--color-surface-0] flex flex-col">
      {/* Header */}
      <header className="flex h-14 items-center border-b border-[--color-surface-border] px-6 gap-4">
        <div className="flex items-center gap-2">
          {done
            ? <CheckCircle2 className="h-5 w-5 text-[--color-success]" />
            : <Loader2 className="h-5 w-5 text-[--color-brand-400] animate-spin" />}
          <span className="text-sm font-semibold text-[oklch(92%_0.01_250)]">
            {done ? 'Provisioning complete' : 'Provisioning cluster…'}
          </span>
        </div>
        <div className="flex-1 max-w-xs">
          <Progress value={progress} />
        </div>
        <Badge variant={done ? 'success' : 'info'}>{progress}%</Badge>
      </header>

      <div className="flex flex-1 overflow-hidden">
        {/* Phase list */}
        <aside className="w-72 shrink-0 border-r border-[--color-surface-border] bg-[--color-surface-1] p-6 flex flex-col gap-4">
          <p className="text-xs font-semibold text-[oklch(45%_0.01_250)] uppercase tracking-wider">Phases</p>
          <div className="flex flex-col gap-4">
            {phases.map((phase) => <PhaseRow key={phase.id} phase={phase} />)}
          </div>

          {done && (
            <motion.div
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              className="mt-auto"
            >
              <a href="/success">
                <button className="w-full rounded-[--radius-md] bg-[--color-brand-500] text-white text-sm font-medium py-2.5 hover:bg-[--color-brand-400] transition-colors">
                  View cluster →
                </button>
              </a>
            </motion.div>
          )}
        </aside>

        {/* Log stream */}
        <main className="flex-1 flex flex-col overflow-hidden">
          <div className="flex items-center gap-2 border-b border-[--color-surface-border] px-4 py-2.5 bg-[--color-surface-1]">
            <Terminal className="h-4 w-4 text-[oklch(45%_0.01_250)]" />
            <span className="text-xs text-[oklch(45%_0.01_250)] font-mono">opentofu apply — live output</span>
          </div>
          <div
            ref={logRef}
            className="flex-1 overflow-y-auto p-4 font-mono text-xs bg-[oklch(8%_0.008_250)] scrollbar-none"
          >
            <AnimatePresence initial={false}>
              {logs.map((line) => (
                <motion.div
                  key={line.id}
                  initial={{ opacity: 0, x: -4 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ duration: 0.15 }}
                  className="flex gap-3 leading-relaxed"
                >
                  <span className="text-[oklch(30%_0.01_250)] shrink-0 tabular-nums">{line.ts}</span>
                  <span className={LEVEL_COLORS[line.level]}>{line.text}</span>
                </motion.div>
              ))}
            </AnimatePresence>
            <div className="h-4" />
          </div>
        </main>
      </div>
    </div>
  )
}
