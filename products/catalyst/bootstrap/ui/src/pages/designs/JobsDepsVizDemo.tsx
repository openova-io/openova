/**
 * JobsDepsVizDemo — visual lock-in surface for the dependency graph
 * widget + Gantt timeline. Reachable at `/sovereign/designs/jobs-deps-viz`,
 * intended for Playwright-MCP cosmetic screenshots and reviewers eye-
 * checking the widget without running a full deployment.
 *
 * NOT a production surface. Lives under `/designs/` so it's clearly a
 * design-system showcase, same neighbourhood as `/designs/wizard`.
 */

import { useState } from 'react'
import { JobDependenciesGraph } from '@/widgets/job-deps-graph/JobDependenciesGraph'
import {
  FIVE_JOB_GRAPH,
  THREE_NODE_CHAIN,
} from '@/test/fixtures/deps-graph.fixture'

export function JobsDepsVizDemo() {
  const [selected, setSelected] = useState<string | null>(null)

  return (
    <div className="min-h-screen bg-[var(--color-bg)] p-8 text-[var(--color-text)]">
      <h1 className="text-2xl font-bold text-[var(--color-text-strong)]">
        Jobs dependencies viz — design showcase
      </h1>
      <p className="mt-1 text-sm text-[var(--color-text-dim)]">
        Sub-ticket openova-io/openova#206 — primary SVG DAG + (stretch) Gantt timeline.
      </p>

      <h2 className="mt-8 text-lg font-semibold text-[var(--color-text-strong)]">
        Five-job graph (Phase 0 → bootstrap → installs)
      </h2>
      <div className="mt-3" data-testid="demo-five-job-graph">
        <JobDependenciesGraph
          jobs={FIVE_JOB_GRAPH}
          height={380}
          onNodeClick={(id) => setSelected(id)}
        />
      </div>

      <h2 className="mt-8 text-lg font-semibold text-[var(--color-text-strong)]">
        Three-node chain (smaller graph for the Job-detail Dependencies tab)
      </h2>
      <div className="mt-3" data-testid="demo-three-node-chain">
        <JobDependenciesGraph
          jobs={THREE_NODE_CHAIN}
          height={350}
          onNodeClick={(id) => setSelected(id)}
        />
      </div>

      <p className="mt-6 text-xs text-[var(--color-text-dim)]">
        Last clicked node:{' '}
        <span data-testid="demo-clicked-node" className="font-mono">
          {selected ?? '(none)'}
        </span>
      </p>
    </div>
  )
}
