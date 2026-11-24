// Gate check FND-G22 (FND-164): the OpenOva tool server (bp-openova-mcp) and what each caller may see. The
// Sovereign's instance (https://mcp.<fqdn>/mcp) is called over HTTPS with the owner's console session as
// the bearer; an Organization-scoped caller is the tool server of a per-Organization Agenity workspace,
// reached where it runs (regionKubectl exec, as FND-162 reaches it). One test per ledger row; the title is
// the row id and its clause. The one write a test asks for is refused, and the test proves nothing was
// created.
import { test, expect } from '../fixtures.ts'
import { request as http } from '@playwright/test'
import { field, host, kube, list, mintSession, ownerEmail, str, type Obj } from './lib.ts'

interface RpcAnswer {
  error?: { code?: number; message?: string }
  result?: { tools?: Array<{ name: string }>; isError?: boolean; content?: Array<{ text?: string }> }
}

const textOf = (a: RpcAnswer) => (a.result?.content ?? []).map((c) => c.text ?? '').join('\n')

/** The JSON-RPC answer of a response body: plain JSON or the data line of an event stream. */
function answerOf(body: string): RpcAnswer {
  const data = body.split('\n').filter((l) => l.startsWith('data:')).map((l) => l.slice(5).trim()).pop()
  return JSON.parse(data ?? body) as RpcAnswer
}

/** One JSON-RPC call to the Sovereign's tool server after initialize, from a request context with no cookie, with a bearer or without one. */
async function sovereignMcp(bearer: string | null, method: string, params: object): Promise<RpcAnswer> {
  const request = await http.newContext()
  try {
    return await call(request, bearer, method, params)
  } finally {
    await request.dispose()
  }
}

async function call(request: Awaited<ReturnType<typeof http.newContext>>, bearer: string | null, method: string, params: object): Promise<RpcAnswer> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' }
  if (bearer) headers.Authorization = `Bearer ${bearer}`
  const init = await request.post(host('mcp', '/mcp'), { headers, data: { jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'gate', version: '1' } } } })
  expect(init.status(), 'initialize').toBeLessThan(300)
  const session = init.headers()['mcp-session-id']
  if (session) headers['Mcp-Session-Id'] = session
  await request.post(host('mcp', '/mcp'), { headers, data: { jsonrpc: '2.0', method: 'notifications/initialized' } })
  const r = await request.post(host('mcp', '/mcp'), { headers, data: { jsonrpc: '2.0', id: 1, method, params } })
  expect(r.status(), `${method} over HTTPS`).toBe(200)
  return answerOf(await r.text())
}

/** The Applications of region a that are not being deleted. */
async function applications(): Promise<Obj[]> {
  return (await list('a', 'applications.apps.openova.io')).filter((a) => field(a, 'metadata.deletionTimestamp') === undefined)
}

/** A Running per-Organization Agenity workspace Pod, its namespace and its Organization. */
async function workspace(): Promise<{ pod: string; ns: string; org: string }> {
  const out = JSON.parse(await kube('a', ['get', 'pods', '-A', '-l', 'app.kubernetes.io/name=agenity', '-o', 'json'])) as { items?: Obj[] }
  const pods = (out.items ?? []).filter((p) => str(p, 'status.phase') === 'Running')
  expect(pods.length, 'a Running Agenity workspace Pod (the Organization-scoped caller)').toBeGreaterThan(0)
  const ns = pods[0].metadata.namespace ?? ''
  const app = (await applications()).find((a) => a.metadata.namespace === ns && /agenity/.test(str(a, 'spec.blueprintRef.name') + str(a, 'spec.blueprint') + a.metadata.name))
  const org = str(app, 'spec.organizationRef')
  expect(org, `the Organization of the workspace in ${ns}`).not.toBe('')
  return { pod: pods[0].metadata.name, ns, org }
}

/** Calls the workspace's tool server (the openova server of ~/.claude.json) over stdio, one tools/call per entry. */
const MCP_CLIENT = `
const { spawn } = require('child_process'); const fs = require('fs');
const cfg = JSON.parse(fs.readFileSync(process.env.HOME + '/.claude.json', 'utf8')).mcpServers.openova;
const calls = JSON.parse(process.argv[1]); const out = {}; let buf = '';
const p = spawn(cfg.command, cfg.args || [], { env: Object.assign({}, process.env, cfg.env || {}), stdio: ['pipe', 'pipe', 'inherit'] });
const done = () => { console.log(JSON.stringify(out)); p.kill(); process.exit(0); };
p.stdout.on('data', (d) => { buf += d; let i; while ((i = buf.indexOf('\\n')) >= 0) { const l = buf.slice(0, i); buf = buf.slice(i + 1);
  try { const m = JSON.parse(l); if (m.id !== undefined) out[m.id] = m; } catch (e) {} }
  if (Object.keys(out).length > calls.length) done(); });
const send = (m) => p.stdin.write(JSON.stringify(m) + '\\n');
send({ jsonrpc: '2.0', id: 0, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'gate', version: '1' } } });
send({ jsonrpc: '2.0', method: 'notifications/initialized' });
calls.forEach((c, i) => send({ jsonrpc: '2.0', id: i + 1, method: 'tools/call', params: { name: c[0], arguments: c[1] } }));
setTimeout(done, 90000);
`

async function orgMcp(w: { pod: string; ns: string }, calls: Array<[string, Record<string, string>]>): Promise<RpcAnswer[]> {
  const out = await kube('a', ['exec', '-n', w.ns, w.pod, '-c', 'chepherd', '--', 'node', '-e', MCP_CLIENT, JSON.stringify(calls)], 180_000)
  const byId = JSON.parse(out.trim().split('\n').pop() ?? '{}') as Record<string, RpcAnswer>
  return calls.map((c, i) => {
    expect(byId[String(i + 1)], `the tool server answered ${c[0]}`).toBeTruthy()
    return byId[String(i + 1)]
  })
}

test('186 bp-openova-mcp answers a JSON-RPC 2.0 tools/list over HTTPS with a NON-EMPTY tool set for an authenticated caller, and an empty set for an unauthenticated one.', async () => {
  const authed = await sovereignMcp(await mintSession(await ownerEmail()), 'tools/list', {})
  expect(authed.error, 'tools/list answers').toBeUndefined()
  expect((authed.result?.tools ?? []).map((t) => t.name), 'the tools of an authenticated caller').toEqual(expect.arrayContaining(['list_applications', 'get_application', 'create_application']))
  // The unauthenticated half reads the answer as it comes: a JSON-RPC error carries no tool set, so it fails.
  const anon = await sovereignMcp(null, 'tools/list', {})
  expect(anon.error, 'tools/list answers an unauthenticated caller').toBeUndefined()
  expect(anon.result?.tools, 'the tools of an unauthenticated caller').toEqual([])
})

test('211 MCP sovereign-admin token → list_applications returns all apps.', async () => {
  const a = await sovereignMcp(await mintSession(await ownerEmail()), 'tools/call', { name: 'list_applications', arguments: {} })
  expect(a.error, 'list_applications answers').toBeUndefined()
  expect(a.result?.isError ?? false, `not a tool error: ${textOf(a)}`).toBe(false)
  const all = await applications()
  expect(all.length, 'the Applications of the candidate').toBeGreaterThan(0)
  for (const app of all) expect(textOf(a).includes(`"${app.metadata.name}"`), `list_applications holds ${app.metadata.namespace}/${app.metadata.name}`).toBe(true)
})

test('212 MCP Org-scoped token → list_applications returns ONLY that Org\'s apps (RBAC parity).', async () => {
  const w = await workspace()
  const [listed] = await orgMcp(w, [['list_applications', {}]])
  expect(listed.error, 'list_applications answers').toBeUndefined()
  expect(listed.result?.isError ?? false, `not a tool error: ${textOf(listed)}`).toBe(false)
  const all = await applications()
  const own = all.filter((a) => str(a, 'spec.organizationRef') === w.org).map((a) => a.metadata.name)
  const theirs = all.filter((a) => str(a, 'spec.organizationRef') !== w.org).map((a) => a.metadata.name).filter((n) => !own.includes(n))
  expect(own.length, `Applications of ${w.org}`).toBeGreaterThan(0)
  expect(theirs.length, 'Applications of other Organizations or of the Sovereign').toBeGreaterThan(0)
  for (const n of own) expect(textOf(listed).includes(`"${n}"`), `it lists ${n} of ${w.org}`).toBe(true)
  for (const n of theirs) expect(textOf(listed).includes(`"${n}"`), `it leaves out ${n}`).toBe(false)
})

test('213 MCP cross-Org get_application is REFUSED as not found — -32000, and the message names only the caller\'s OWN Organization, never the other one. The cross-Org 403 is asserted on the WRITE path: create_application naming another Organization → -32003 forbidden / status: 403, nothing created. Own-Org get_application succeeds, as the control that the refusal is about the Org boundary. Adjudicated 2026-08-11 (ADR-0013 / #6122): deny-by-LOOKUP answers not-found because a 403 would confirm the Application exists in another Organization; deny-by-ASSERTION answers 403 because the caller named that Organization themselves. The former 403 (UI-parity) wording read a path-level 403 as a resource-level one — an Org console session has no Sovereign-wide by-name read at all.', async () => {
  const w = await workspace()
  const all = await applications()
  const mine = all.find((a) => str(a, 'spec.organizationRef') === w.org)
  const other = all.find((a) => str(a, 'spec.organizationRef') !== '' && str(a, 'spec.organizationRef') !== w.org)
  expect(mine, `an Application of ${w.org}`).toBeTruthy()
  expect(other, 'an Application of a second Organization').toBeTruthy()
  const otherOrg = str(other, 'spec.organizationRef')
  // A fixed name: the create is refused, and nothing of that name may exist afterwards.
  const probe = 'gate-g22-cross-org'
  const [own, got, created] = await orgMcp(w, [
    ['get_application', { name: mine?.metadata.name ?? '' }],
    ['get_application', { name: other?.metadata.name ?? '' }],
    ['create_application', { blueprint: 'bp-postgres', name: probe, organization: otherOrg }],
  ])
  expect(own.error, 'the own-Org get answers').toBeUndefined()
  expect(own.result?.isError ?? false, `the own-Org get succeeds: ${textOf(own)}`).toBe(false)
  const getText = `${got.error?.message ?? ''} ${textOf(got)}`
  expect(got.error?.code ?? (/-32000/.test(getText) ? -32000 : 0), `a cross-Org get is not found (-32000): ${getText}`).toBe(-32000)
  expect(getText, 'it names the caller\'s own Organization').toContain(w.org)
  expect(getText, 'it does not name the other Organization').not.toContain(otherOrg)
  const createText = `${created.error?.message ?? ''} ${textOf(created)}`
  expect(created.error?.code === -32003 || /-32003|\b403\b|forbidden/i.test(createText), `a cross-Org create is forbidden: ${createText}`).toBe(true)
  expect((await applications()).some((a) => a.metadata.name === probe), 'nothing was created').toBe(false)
})
