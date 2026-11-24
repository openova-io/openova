// Gate check FND-G20 (FND-162): the per-Organization Agenity workspace (bp-agenity), its agent and its
// tools. One test per ledger row; the title is the row id and its clause. The agent is reached where it
// runs, in the workspace Pod (regionKubectl exec), and the console is read signed in.
import { test, expect } from '../fixtures.ts'
import { RUN_TOKEN, applications, arr, field, kube, list, ready, str, until, type Obj } from './lib.ts'

const AGENT_BLUEPRINT = 'bp-postgres'

interface Workspace {
  pod: Obj
  ns: string
}

/** A Running workspace Pod of bp-agenity and its namespace. */
async function workspace(): Promise<Workspace> {
  const out = JSON.parse(await kube('a', ['get', 'pods', '-A', '-l', 'app.kubernetes.io/name=agenity', '-o', 'json'])) as { items?: Obj[] }
  const mine = (out.items ?? []).filter((p) => str(p, 'status.phase') === 'Running')
  expect(mine.length, 'a Running Agenity workspace Pod').toBeGreaterThan(0)
  const pod = mine[0]
  return { pod, ns: pod.metadata.namespace ?? '' }
}

/** The bp-agenity Application of a workspace's namespace; a missing one fails the test. */
async function workspaceApp(w: Workspace): Promise<Obj> {
  const app = (await applications('a')).find((a) => a.metadata.namespace === w.ns && /agenity/.test(str(a, 'spec.blueprintRef.name') + str(a, 'spec.blueprint') + a.metadata.name))
  expect(app, `the bp-agenity Application in ${w.ns}`).toBeTruthy()
  return app as Obj
}

/** The Organization of a workspace: spec.organizationRef of its Application; an empty reference fails the test. */
async function orgOf(w: Workspace): Promise<string> {
  const app = await workspaceApp(w)
  const org = str(app, 'spec.organizationRef')
  expect(org, `spec.organizationRef of ${w.ns}/${app.metadata.name}`).not.toBe('')
  return org
}

/** Runs the workspace's agent (Claude Code) once with a prompt; the JSON result it prints. */
async function agent(w: Workspace, prompt: string, tools = false): Promise<{ result: string; models: string[] }> {
  const args = ['exec', '-n', w.ns, w.pod.metadata.name, '-c', 'chepherd', '--', 'claude', '-p', '--output-format', 'json']
  if (tools) args.push('--permission-mode', 'bypassPermissions')
  const out = await kube('a', [...args, prompt], 600_000)
  const body = JSON.parse(out.slice(out.indexOf('{'))) as { result?: string; is_error?: boolean; modelUsage?: Record<string, unknown> }
  expect(body.is_error ?? false, `the agent answered without an error: ${body.result ?? ''}`).toBe(false)
  return { result: body.result ?? '', models: Object.keys(body.modelUsage ?? {}) }
}

/** Calls the workspace's tool server (the openova MCP of ~/.claude.json) over stdio, one tools/call per entry. */
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

interface RpcAnswer {
  error?: { code?: number; message?: string }
  result?: { isError?: boolean; content?: Array<{ text?: string }> }
}

async function mcp(w: Workspace, calls: Array<[string, Record<string, string>]>): Promise<RpcAnswer[]> {
  const out = await kube('a', ['exec', '-n', w.ns, w.pod.metadata.name, '-c', 'chepherd', '--', 'node', '-e', MCP_CLIENT, JSON.stringify(calls)], 180_000)
  const byId = JSON.parse(out.trim().split('\n').pop() ?? '{}') as Record<string, RpcAnswer>
  return calls.map((c, i) => {
    const answer = byId[String(i + 1)]
    expect(answer, `the tool server answered ${c[0]}`).toBeTruthy()
    return answer
  })
}

const textOf = (a: RpcAnswer) => (a.result?.content ?? []).map((c) => c.text ?? '').join('\n')

test('R19 The per-Org Agenity workspace StatefulSet reaches Running with its Anthropic credential seeded: the init container validates the token and exits 0', async () => {
  const w = await workspace()
  const inits = arr(w.pod, 'status.initContainerStatuses')
  expect(inits.length, 'init containers').toBeGreaterThan(0)
  for (const c of inits) expect(field(c, 'state.terminated.exitCode'), `init container ${str(c, 'name')} exited`).toBe(0)
  const validator = inits.find((c) => /credential|token|anthropic|claude/i.test(str(c, 'name')))
  expect(validator, 'an init container that validates the Anthropic credential').toBeTruthy()
  const cred = await kube('a', ['exec', '-n', w.ns, w.pod.metadata.name, '-c', 'chepherd', '--', 'sh', '-c', 'test -s "$HOME/.claude/.credentials.json" && echo seeded || echo absent'])
  expect(cred.trim(), 'the credential in the workspace home').toBe('seeded')
})

test('M4 agenity image-pull half: the fresh-install ghcr-pull path', async () => {
  const w = await workspace()
  const statuses = [...arr(w.pod, 'status.initContainerStatuses'), ...arr(w.pod, 'status.containerStatuses')]
  for (const c of statuses) {
    expect(str(c, 'image'), `${str(c, 'name')}: pulled from ghcr.io/openova-io`).toMatch(/^ghcr\.io\/openova-io\//)
    expect(str(c, 'imageID'), `${str(c, 'name')}: an image was pulled`).not.toBe('')
    expect(str(c, 'state.waiting.reason'), `${str(c, 'name')}: not waiting on a pull`).not.toMatch(/ErrImagePull|ImagePullBackOff/)
  }
})

test('G8 The Anthropic credential is seeded into the agentic runtime: chat works end-to-end', async () => {
  test.setTimeout(600_000)
  const w = await workspace()
  const a = await agent(w, 'Reply with the single word PONG and nothing else.')
  expect(a.result).toMatch(/\bPONG\b/)
})

test("220 chepherd's solo agent is pre-configured with Claude Opus 4.7 + a working token (the agent is live and chat works)", async () => {
  test.setTimeout(600_000)
  const w = await workspace()
  const a = await agent(w, 'Reply with the single word PONG and nothing else.')
  expect(a.result).toMatch(/\bPONG\b/)
  expect(a.models.some((m) => /opus-4-7/.test(m)), `the model of the answer: ${a.models.join(', ')}`).toBe(true)
})

test('219 The user provisions chepherd as an Application: it converges (Helm deploys, pods Ready, the chepherd console reachable in his Org)', async ({ request }) => {
  const w = await workspace()
  const app = await workspaceApp(w)
  expect(ready(app), 'the Application is Ready').toBe('True')
  const hrs = (await list('a', 'helmreleases.helm.toolkit.fluxcd.io', w.ns)).filter((h) => /agenity/.test(h.metadata.name))
  expect(hrs.map(ready), 'its HelmRelease deployed').toEqual(hrs.map(() => 'True'))
  expect(hrs.length).toBeGreaterThan(0)
  const conds = arr(w.pod, 'status.conditions')
  expect(conds.some((c) => str(c, 'type') === 'Ready' && str(c, 'status') === 'True'), 'the workspace Pod is Ready').toBe(true)
  const hosts = (await list('a', 'httproutes.gateway.networking.k8s.io', w.ns)).flatMap((r) => arr(r, 'spec.hostnames').map(String)).filter((h) => h.startsWith('agenity.'))
  expect(hosts.length, 'the workspace host').toBeGreaterThan(0)
  const r = await request.get(`https://${hosts[0]}/`, { maxRedirects: 0 })
  expect(r.status(), `https://${hosts[0]}/ answers`).toBeLessThan(400)
})

test.describe.serial('the agent creates an Application', () => {
  const name = `gate-agent-${RUN_TOKEN}`.toLowerCase().slice(0, 40)
  let w: Workspace | null = null

  test.afterAll(async () => {
    for (const a of (await applications('a')).filter((x) => x.metadata.name === name)) {
      await kube('a', ['delete', 'applications.apps.openova.io', '-n', a.metadata.namespace ?? '', name, '--wait=false'])
    }
  })

  test('G9 agentic-run half: the agenity solo agent chats and drives create_application', async () => {
    test.setTimeout(900_000)
    w = await workspace()
    const a = await agent(w, `Use the openova tool create_application to create an application named ${name} from the Blueprint ${AGENT_BLUEPRINT} in my Organization. Then reply with the name of the application you created.`, true)
    expect(a.result, 'the agent reports the new application').toContain(name)
    await until(`the Application ${name} in ${w.ns}`, async () => ((await applications('a')).some((x) => x.metadata.name === name && x.metadata.namespace === w?.ns) ? true : null), 180_000)
  })

  test("221 The user chats with the solo agent, it calls create_application with the user's Org-scoped token: a new Application CR is created in the user's Org and appears provisioning in /apps, with NO operator-console action", async ({ page }) => {
    expect(w, 'the agent created the application (G9)').not.toBeNull()
    const org = await orgOf(w as Workspace)
    const cr = (await applications('a')).find((x) => x.metadata.name === name)
    expect(cr?.metadata.namespace, 'in the Org of the workspace').toBe(w?.ns)
    expect(str(cr, 'spec.organizationRef'), 'attributed to the Org').toBe(org)
    await page.goto('/apps')
    await expect(page.getByTestId(`sov-app-card-${name}`)).toBeVisible({ timeout: 60_000 })
    // Provisioning: an instance card reads installing until its Application is installed (AppsPage.tsx).
    await expect(page.getByTestId(`sov-app-card-${name}`), 'the card shows the Application provisioning').toHaveAttribute('data-status', 'installing')
  })

  test('222 The agent-created application converges and appears in the user Org (chat-driven app creation works end-to-end)', async ({ page }) => {
    test.setTimeout(1_500_000)
    expect(w, 'the agent created the application (G9)').not.toBeNull()
    const org = await orgOf(w as Workspace)
    await until(`the Application ${name} Ready`, async () => {
      const cr = (await applications('a')).find((x) => x.metadata.name === name)
      return cr && ready(cr) === 'True' ? true : null
    }, 1_200_000, 20_000)
    await page.goto('/apps')
    await expect(page.getByTestId(`sov-app-org-${name}`)).toHaveAttribute('data-org', org)
  })
})

test("223 The agent's actions are RBAC-scoped to the user Org: list_applications returns ONLY this Org's apps, a cross-Org get_application is REFUSED as not found (-32000, naming only the caller's own Org), a cross-Org create_application is -32003 forbidden / 403 and creates nothing", async () => {
  test.setTimeout(300_000)
  const w = await workspace()
  const org = await orgOf(w)
  const all = await applications('a')
  const other = all.find((a) => str(a, 'spec.organizationRef') !== '' && str(a, 'spec.organizationRef') !== org)
  expect(other, 'an Application of a second Organization').toBeTruthy()
  const otherOrg = str(other, 'spec.organizationRef')
  const probe = `gate-xorg-${RUN_TOKEN}`.toLowerCase().slice(0, 40)
  const [listed, got, created] = await mcp(w, [
    ['list_applications', {}],
    ['get_application', { name: other?.metadata.name ?? '' }],
    ['create_application', { blueprint: AGENT_BLUEPRINT, name: probe, organization: otherOrg }],
  ])
  const mine = new Set(all.filter((a) => str(a, 'spec.organizationRef') === org).map((a) => a.metadata.name))
  const theirs = all.filter((a) => str(a, 'spec.organizationRef') !== org).map((a) => a.metadata.name).filter((n) => !mine.has(n))
  // The Org's own Applications in the workspace's namespace (the bp-agenity one at least), none being deleted.
  const own = all.filter((a) => a.metadata.namespace === w.ns && str(a, 'spec.organizationRef') === org && field(a, 'metadata.deletionTimestamp') === undefined).map((a) => a.metadata.name)
  const listText = textOf(listed)
  expect(listed.error, 'list_applications answers').toBeUndefined()
  expect(listed.result?.isError ?? false, `list_applications is not a tool error: ${listText}`).toBe(false)
  expect(own.length, `Applications of ${org} in ${w.ns}`).toBeGreaterThan(0)
  for (const n of own) expect(listText.includes(`"${n}"`), `list_applications holds ${n} of the caller's own Org`).toBe(true)
  for (const n of theirs) expect(listText.includes(`"${n}"`), `list_applications leaves out ${n} of another Org`).toBe(false)
  const getText = `${got.error?.message ?? ''} ${textOf(got)}`
  expect(got.error?.code ?? (/-32000/.test(getText) ? -32000 : 0), 'a cross-Org get is not found (-32000)').toBe(-32000)
  expect(getText, 'it names the caller own Organization').toContain(org)
  expect(getText, 'it does not name the other Organization').not.toContain(otherOrg)
  const createText = `${created.error?.message ?? ''} ${textOf(created)}`
  expect(created.error?.code === -32003 || /-32003|\b403\b|forbidden/i.test(createText), `a cross-Org create is forbidden: ${createText}`).toBe(true)
  expect((await applications('a')).some((a) => a.metadata.name === probe), 'nothing was created').toBe(false)
})
