// Gate check FND-G21 (FND-163): the janitor of the candidate's catalyst-api — its cloud sweeps and their
// verdicts, read from its own log (kubectl logs, JSON records "[JANITOR] ...", products/catalyst/
// bootstrap/api/internal/handler/janitor.go), and the provisioning rows of the jobs feed. One test per
// ledger row; the title is the row id and its clause. The janitor passes every five minutes; the window
// read holds several passes.
import { test, expect } from '../fixtures.ts'
import { catalystApiLog, janitorRecords, jobsFeed, selfId, type LogRecord } from './lib.ts'

const WINDOW = '40m'
const PASS = '[JANITOR] pass complete'

/** One verdict of a cloud sweep: "[JANITOR] sweep orphan <kind> <name>…: <verdict>". */
interface Sweep {
  kind: string
  name: string
  verdict: string
}

function sweeps(recs: LogRecord[]): Sweep[] {
  const out: Sweep[] = []
  for (const r of recs) {
    const m = /^\[JANITOR\] sweep orphan (\S+) (\S+)[^:]*: (.*)$/.exec(r.msg)
    if (m) out.push({ kind: m[1], name: m[2], verdict: m[3] })
  }
  return out
}

async function janitor(): Promise<{ recs: LogRecord[]; passes: LogRecord[] }> {
  const recs = janitorRecords(await catalystApiLog(WINDOW))
  const passes = recs.filter((r) => r.msg === PASS)
  expect(passes.length, `janitor passes in the last ${WINDOW} of the catalyst-api log`).toBeGreaterThan(0)
  return { recs, passes }
}

test('R1 catalyst-api orphan-sweep no longer reaps a ready Sovereign — denylist inversion (case "wiped": eligible; default: protect) holds on the live env.', async ({ page }) => {
  const id = await selfId(page)
  const { recs } = await janitor()
  const all = sweeps(recs)
  const kept = all.filter((s) => s.verdict.startsWith('skipped (active deployment)'))
  expect(kept.length, 'resources of the ready deployment that the sweep protects').toBeGreaterThan(0)
  const keptKeys = new Set(kept.map((s) => `${s.kind} ${s.name}`))
  const hit = all.filter((s) => keptKeys.has(`${s.kind} ${s.name}`) && /would-reap|reaped|deleted/i.test(s.verdict))
  expect(hit.map((s) => `${s.kind} ${s.name}: ${s.verdict}`), 'protected resources that a pass also marks for reaping').toEqual([])
  const mine = all.filter((s) => s.name.includes(id.slice(0, 8)) && !s.verdict.startsWith('skipped'))
  expect(mine.map((s) => `${s.kind} ${s.name}: ${s.verdict}`), 'resources of this Sovereign that are not protected').toEqual([])
  expect(recs.filter((r) => r.msg.startsWith('[JANITOR] reaped') && String(r.id ?? '') === id).length, 'this Sovereign reaped as a record').toBe(0)
})

test('M1 janitor hardening — log-only/dry-run for a full live cycle before any destructive sweep.', async () => {
  const { recs, passes } = await janitor()
  for (const p of passes) expect(p.destructive, `a pass at ${String(p.time)}: destructive`).toBe(false)
  for (const s of recs.filter((r) => r.msg === '[JANITOR] starting')) expect(s.cloudSweepDestructive, 'the janitor starts log-only').toBe(false)
  const all = sweeps(recs)
  expect(all.filter((s) => /^(reaped|deleted)/i.test(s.verdict)).map((s) => `${s.kind} ${s.name}: ${s.verdict}`), 'cloud resources deleted by a sweep').toEqual([])
  expect(all.filter((s) => s.verdict.startsWith('would-reap') && !/log-only/.test(s.verdict)).map((s) => `${s.kind} ${s.name}`), 'would-reap verdicts that are not log-only').toEqual([])
})

test('G4 A janitor pass with the destructive gate CLOSED reports its cloud sweeps under a …WouldReap key and NEVER under a …Deleted key — a dry run must not report deletions that did not happen (#5545).', async () => {
  const { passes } = await janitor()
  const local = new Set(['orphanKubeconfigsDeleted', 'orphanTofuWorkdirsDeleted'])
  for (const p of passes) {
    expect(p.destructive, 'the destructive gate is closed').toBe(false)
    expect(Object.keys(p).filter((k) => k.endsWith('Deleted') && !local.has(k)), `a pass at ${String(p.time)}: cloud sweeps under a …Deleted key`).toEqual([])
  }
  const last = passes[passes.length - 1]
  expect(Object.keys(last).filter((k) => /^orphan.+WouldReap$/.test(k)).length, 'the cloud sweeps of the last pass, under …WouldReap keys').toBeGreaterThan(0)
})

test('G5 janitor log-only live proof (the dry-run full-cycle observation).', async () => {
  const { recs } = await janitor()
  const ends = recs.map((r, i) => (r.msg === PASS ? i : -1)).filter((i) => i >= 0)
  expect(ends.length, 'two pass ends, so that one whole cycle lies between them').toBeGreaterThanOrEqual(2)
  const cycle = recs.slice(ends[ends.length - 2] + 1, ends[ends.length - 1] + 1)
  const swept = sweeps(cycle)
  expect(swept.length, 'the cloud sweep verdicts of one whole cycle').toBeGreaterThan(0)
  expect(swept.filter((s) => !(s.verdict.startsWith('skipped') || (s.verdict.startsWith('would-reap') && /log-only/.test(s.verdict)))).map((s) => `${s.kind} ${s.name}: ${s.verdict}`), 'verdicts of the cycle that acted').toEqual([])
  const end = cycle[cycle.length - 1]
  expect(end.destructive, 'the cycle ends log-only').toBe(false)
  expect(end.reaped, 'nothing reaped').toBe(0)
  expect(end.reapErrors, 'no reap errors').toBe(0)
})

test('228 A re-prov AFTER a wipe does NOT false-fail on orphaned catalyst-* VPCs — the orphan-VPC janitor sweep removes the leftover VPC(s) from a prior wiped deployment before the new prov allocates its network.', async ({ page }) => {
  const rows = (await jobsFeed(page, { full: true })).filter((j) => j.kind === 'lifecycle')
  expect(rows.length, 'the provisioning rows of this Sovereign').toBeGreaterThan(0)
  expect(rows.filter((j) => j.status !== 'succeeded').map((j) => `${j.jobName}: ${j.status}`), 'provisioning rows that did not reach green').toEqual([])
  expect(rows.filter((j) => /VPC already exists|quota/i.test(`${j.reason ?? ''} ${j.message ?? ''}`)).map((j) => j.jobName), 'a VPC or quota conflict while provisioning').toEqual([])
  const { passes } = await janitor()
  const last = passes[passes.length - 1]
  const keys = Object.keys(last).filter((k) => /^orphanVPCs(WouldReap|Deleted)$/.test(k))
  expect(keys, 'the last pass swept the networks').not.toEqual([])
  // A destructive pass deletes what it finds and reports orphanVPCsDeleted; a dry pass only counts, as orphanVPCsWouldReap. A missing count fails.
  const left = last.destructive === true ? (typeof last.orphanVPCsDeleted === 'number' ? 0 : last.orphanVPCsDeleted) : last.orphanVPCsWouldReap
  expect(left, 'leftover catalyst-* VPCs of a wiped deployment still in the account').toBe(0)
})
