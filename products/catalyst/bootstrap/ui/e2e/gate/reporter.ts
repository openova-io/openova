/**
 * e2e/gate/reporter.ts — one result file per check (FND-161).
 *
 * For every project of the run (one project per gate check) it writes
 * <outputDir>/<check id>.json in the form scripts/acceptance-gate.py
 * merge-check reads (the form of FND-036):
 *
 *   {"id": "<check>", "status": "PASS" | "FAIL", "steps": [<one step per test>]}
 *
 * A step has the fields of a step of the gate record: journey (the check),
 * id (the first word of the test's title: the ledger row id), name (the
 * title), status, started_at, finished_at, duration_ms, evidence (the spec
 * file and line, the files of the failure) and error. A check passes only
 * when every one of its tests ran and passed: a skipped test, a test marked
 * to fail, a project without a test and an error of the run each make it FAIL.
 */

import { mkdirSync, writeFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import type { FullConfig, FullResult, Reporter, TestCase, TestError, TestResult } from '@playwright/test/reporter'

interface Step {
  journey: string
  id: string
  name: string
  status: 'PASS' | 'FAIL' | 'SKIP'
  started_at: string
  finished_at: string
  duration_ms: number
  evidence?: Record<string, unknown>
  error?: string
}

const ANSI = new RegExp(String.fromCharCode(27) + '\\[[0-9;]*m', 'g')

function firstLine(text: string | undefined): string {
  return (text ?? '').replace(ANSI, '').split('\n').map((l) => l.trim()).find((l) => l !== '') ?? ''
}

export default class GateResults implements Reporter {
  private readonly outputDir: string
  private readonly repoRoot: string
  private projects: string[] = []
  private readonly outputDirs = new Map<string, string>()
  private readonly last = new Map<string, { project: string; test: TestCase; result: TestResult }>()
  private readonly errors: string[] = []
  private started = new Date()

  constructor(options: { outputDir: string; repoRoot: string }) {
    this.outputDir = options.outputDir
    this.repoRoot = options.repoRoot
  }

  onBegin(config: FullConfig): void {
    this.projects = config.projects.map((p) => p.name)
    for (const p of config.projects) this.outputDirs.set(p.name, p.outputDir)
    this.started = new Date()
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    // A retry replaces the attempt before it: the last attempt is the test's result.
    this.last.set(test.id, { project: test.parent.project()?.name ?? '', test, result })
  }

  onError(error: TestError): void {
    this.errors.push(firstLine(error.message ?? error.value) || 'an error outside a test')
  }

  onEnd(result: FullResult): void {
    mkdirSync(this.outputDir, { recursive: true })
    const now = new Date().toISOString()
    for (const project of this.projects) {
      const steps: Step[] = []
      for (const { project: p, test, result: r } of this.last.values()) {
        if (p === project) steps.push(this.step(project, test, r))
      }
      const fail = (name: string, error: string): Step => ({
        journey: project, id: '-', name, status: 'FAIL', started_at: this.started.toISOString(),
        finished_at: now, duration_ms: Math.max(0, Date.now() - this.started.getTime()), error,
      })
      if (steps.length === 0) steps.push(fail('the check ran its tests', 'no test of the check ran'))
      for (const e of this.errors) steps.push(fail('the harness ran without an error', e))
      if (result.status === 'interrupted' || result.status === 'timedout') {
        steps.push(fail('the harness run ended', `the run ended ${result.status}`))
      }
      const status = steps.every((s) => s.status === 'PASS') ? 'PASS' : 'FAIL'
      writeFileSync(join(this.outputDir, `${project}.json`), JSON.stringify({ id: project, status, steps }, null, 2) + '\n')
    }
  }

  private step(project: string, test: TestCase, r: TestResult): Step {
    const start = r.startTime
    const passed = r.status === 'passed' && test.expectedStatus === 'passed'
    const step: Step = {
      journey: project,
      id: test.title.split(/\s+/)[0] || '-',
      name: test.titlePath().slice(3).join(' › ') || test.title,
      status: passed ? 'PASS' : r.status === 'skipped' ? 'SKIP' : 'FAIL',
      started_at: start.toISOString(),
      finished_at: new Date(start.getTime() + r.duration).toISOString(),
      duration_ms: Math.max(0, Math.round(r.duration)),
      evidence: {
        file: relative(this.repoRoot, test.location.file),
        line: test.location.line,
        retry: r.retry,
        // The files of a failure, from the folder of the test results (GATE_OUTPUT), which the workflow uploads.
        files: r.attachments.filter((a) => a.path).map((a) => relative(this.outputDirs.get(project) ?? this.repoRoot, a.path as string)),
      },
    }
    if (!passed) {
      step.error = firstLine(r.error?.message ?? r.error?.value) ||
        (r.status === 'skipped' ? 'the test was skipped' : test.expectedStatus !== 'passed' ? `the test is marked ${test.expectedStatus}` : `the test ended ${r.status}`)
    }
    return step
  }
}
