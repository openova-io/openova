/**
 * FlowDeploymentTree.test.tsx — locks the multi-region rendering
 * contract for the left sidebar of the Flow page.
 *
 * Forcing functions covered:
 *   • Multi-region fixture renders ≥ 2 [data-testid=flow-tree-region-*]
 *     wrappers (one per region, NOT one per family).
 *   • Each wrapper carries a stable data-region-id attribute.
 *   • Region rows render in the SAME order as the input fixture.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { FlowDeploymentTree } from './FlowDeploymentTree'
import {
  buildFlowGroupRows,
  DEMO_TWO_REGION_FIXTURE,
} from './flowDeploymentTreeData'
import { DEFAULT_FAMILIES } from '@/lib/flowLayoutV4'

afterEach(() => cleanup())

describe('FlowDeploymentTree — multi-region grouping', () => {
  function renderWithFixture() {
    const groups = buildFlowGroupRows({
      jobs: DEMO_TWO_REGION_FIXTURE.jobs.map((j) => ({
        id: j.id,
        jobName: j.jobName,
        status: j.status,
        durationMs: j.durationMs,
      })),
      hintByJob: DEMO_TWO_REGION_FIXTURE.hints,
      regions: DEMO_TWO_REGION_FIXTURE.regions,
      families: DEFAULT_FAMILIES,
      familyDescriptions: {
        catalyst: 'Bootstrap & K8s',
        spine: 'Networking & Mesh',
        pilot: 'GitOps & IaC',
        guardian: 'Security & Identity',
        fabric: 'Data & Integration',
        insights: 'AIOps & Observability',
      },
    })
    return render(
      <FlowDeploymentTree
        groups={groups}
        selectedJobId={null}
        onSelectJob={() => {}}
        totals={{ finished: 2, total: groups.reduce((n, g) => n + g.rows.length, 0) }}
      />,
    )
  }

  it('FORCING FUNCTION: renders ≥ 2 [data-testid=flow-tree-region-*] wrappers when given a 2-region fixture', () => {
    renderWithFixture()
    const wrappers = document.querySelectorAll('[data-testid^="flow-tree-region-"]')
    // Filter to the WRAPPER ids (not the region-name child label).
    const regionWrappers = Array.from(wrappers).filter((el) => {
      const tid = el.getAttribute('data-testid') ?? ''
      // The wrappers are flow-tree-region-<id>; the name label is
      // flow-tree-region-name-<id> — exclude the latter.
      return !tid.startsWith('flow-tree-region-name-')
    })
    expect(regionWrappers.length).toBeGreaterThanOrEqual(2)
    const ids = regionWrappers.map((el) => el.getAttribute('data-region-id'))
    expect(ids).toContain('fsn1')
    expect(ids).toContain('nbg1')
  })

  it('renders fsn1 wrapper BEFORE nbg1 wrapper (caller-supplied order)', () => {
    renderWithFixture()
    const wrappers = Array.from(
      document.querySelectorAll('[data-region-id]'),
    ) as HTMLElement[]
    const ids = wrappers.map((el) => el.getAttribute('data-region-id'))
    const fsnIdx = ids.indexOf('fsn1')
    const nbgIdx = ids.indexOf('nbg1')
    expect(fsnIdx).toBeGreaterThanOrEqual(0)
    expect(nbgIdx).toBeGreaterThan(fsnIdx)
  })

  it('exposes the region label inside its wrapper', () => {
    renderWithFixture()
    const fsnWrapper = document.querySelector('[data-testid="flow-tree-region-fsn1"]')
    expect(fsnWrapper).toBeTruthy()
    expect(fsnWrapper!.textContent).toContain('FSN1')
    const nbgWrapper = document.querySelector('[data-testid="flow-tree-region-nbg1"]')
    expect(nbgWrapper).toBeTruthy()
    expect(nbgWrapper!.textContent).toContain('NBG1')
  })

  it('renders one job button per fixture job, with stable testids', () => {
    renderWithFixture()
    for (const j of DEMO_TWO_REGION_FIXTURE.jobs) {
      expect(screen.getByTestId(`flow-tree-job-${j.id}`)).toBeTruthy()
    }
  })

  it('empty groups → empty placeholder, no wrappers', () => {
    render(
      <FlowDeploymentTree
        groups={[]}
        selectedJobId={null}
        onSelectJob={() => {}}
        totals={{ finished: 0, total: 0 }}
      />,
    )
    expect(document.querySelectorAll('[data-region-id]').length).toBe(0)
  })
})
