// Gate check FND-G22 (FND-164): the plumbing under single sign-on — the sso-bridge reconciler, the network
// policies its traffic crosses, and the client credential the access gate of PowerDNS-Admin holds. Read
// from the clusters (regionKubectl), the reconciler's own log and the identity provider (kcadm). One test
// per ledger row; the title is the row id and its clause. Nothing here writes to the Sovereign.
import { test, expect } from '../fixtures.ts'
import { arr, asOwner, at, expectNoLoginForm, getObj, host, kcadm, kube, list, ssoBridgeLog, str, ticksOf, until, type Obj } from './lib.ts'

/** The ports a policy rule names, as numbers (a Cilium rule: toPorts[].ports[]; a NetworkPolicy rule: ports[]). */
const portsOf = (rule: unknown) => [...arr(rule, 'toPorts').flatMap((p) => arr(p, 'ports')), ...arr(rule, 'ports')].map((p) => Number(at(p, 'port')))

/** The egress rules of a CiliumNetworkPolicy towards the Pods of a namespace. */
const egressTo = (cnp: Obj, namespace: string) =>
  arr(cnp, 'spec.egress').filter((e) => arr(e, 'toEndpoints').some((t) => at(t, 'matchLabels', 'k8s:io.kubernetes.pod.namespace') === namespace))

/** The reconciler's lines of the last minutes that say a call to OpenBao or the identity provider failed. */
async function reconcilerFailures(since: string): Promise<string[]> {
  return (await ssoBridgeLog(since)).map((l) => l.text).filter((t) => /failed to login to OpenBao|OpenBao PUT .* failed|keycloak.*(failed|refused|timed out)/i.test(t))
}

/** A tick of the reconciler in the last minutes that ran without a failed call: its traffic is not blocked. */
async function expectCleanTick(): Promise<void> {
  expect((await ssoBridgeLog('3m')).some((l) => l.text.startsWith('reconcile bp-')), 'a reconciler tick in the last 3 minutes').toBe(true)
  expect(await reconcilerFailures('3m'), 'no failed call of the reconciler').toEqual([])
}

test('R3 bp-plane-isolation openbao default-deny ADMITS sso-bridge ingress → sso-bridge reaches openbao:8200.', async () => {
  const deny = await getObj('a', 'networkpolicy', 'openbao', 'openbao-default-deny')
  expect(deny, 'the default-deny of namespace openbao (bp-plane-isolation)').not.toBeNull()
  expect(Object.keys((at(deny, 'spec', 'podSelector') ?? { none: true }) as object), 'it selects every Pod of openbao').toEqual([])
  expect(arr(deny, 'spec.policyTypes'), 'for ingress').toContain('Ingress')
  const fromSso = arr(deny, 'spec.ingress').filter((r) => arr(r, 'from').some((f) => at(f, 'namespaceSelector', 'matchLabels', 'kubernetes.io/metadata.name') === 'sso-bridge'))
  expect(fromSso.length, 'an ingress rule from namespace sso-bridge').toBeGreaterThan(0)
  const ports = fromSso.flatMap(portsOf)
  expect(ports.length === 0 || ports.includes(8200), `the rule admits :8200 (ports: ${ports.join(', ') || 'all'})`).toBe(true)
  await expectCleanTick()
})

test('R4 sso-bridge egress CNP permits openbao + keycloak — the reconciler\'s own egress is not blocked.', async () => {
  const cnp = await getObj('a', 'ciliumnetworkpolicies.cilium.io', 'sso-bridge', 'sso-bridge-reconciler-apiserver')
  expect(cnp, 'the CiliumNetworkPolicy sso-bridge/sso-bridge-reconciler-apiserver').not.toBeNull()
  const c = cnp as Obj
  expect(at(c, 'spec', 'endpointSelector', 'matchLabels', 'app.kubernetes.io/name'), 'it selects the reconciler').toBe('sso-bridge-reconciler')
  expect(egressTo(c, 'openbao').flatMap(portsOf), 'egress to openbao').toContain(8200)
  expect(egressTo(c, 'keycloak').flatMap(portsOf), 'egress to keycloak (the Pod port Cilium enforces)').toContain(8080)
  const api = arr(c, 'spec.egress').filter((e) => arr(e, 'toEntities').includes('kube-apiserver')).flatMap(portsOf)
  expect(api, 'egress to the kube-apiserver').toEqual(expect.arrayContaining([6443, 443]))
  await expectCleanTick()
})

test('R5 sso-bridge re-mints the grafana OIDC client secret each tick (KC_ADDR cache refreshed) — no stale-secret 401.', async ({ browser }) => {
  test.setTimeout(420_000)
  const ticks = await until('two ticks that re-registered grafana', async () => { const t = await ticksOf('grafana', '5m'); return t.length >= 2 ? t : null }, 240_000, 30_000)
  expect(ticks.length, 'consecutive ticks re-register grafana').toBeGreaterThanOrEqual(2)
  for (const l of (await ssoBridgeLog('5m')).filter((x) => x.text.startsWith('reconcile bp-grafana '))) expect(l.text, 'grafana is re-registered, not skipped').toMatch(/skipUpsert=false/)
  const pods = (await list('a', 'pods', 'grafana')).filter((p) => p.metadata.labels?.['app.kubernetes.io/name'] === 'grafana' && str(p, 'status.phase') === 'Running')
  expect(pods.length, 'a running Grafana').toBeGreaterThan(0)
  const log = await kube('a', ['-n', 'grafana', 'logs', pods[0].metadata.name, '-c', 'grafana', '--tail=300'])
  expect(log.split('\n').filter((l) => /invalid_client|oauth.*401|status=401.*oauth/i.test(l)), 'no stale-secret 401 in Grafana').toEqual([])
  await asOwner(browser, async (page, owner) => {
    await page.goto(host('grafana', '/login/generic_oauth'))
    await expect(page).not.toHaveURL(/\/login/, { timeout: 120_000 })
    const u = await page.request.get(host('grafana', '/api/user'))
    expect(u.status(), 'signed in through the identity provider').toBe(200)
    expect(((await u.json()) as { email?: string }).email).toBe(owner)
  })
})

test('R9 oidc-gate client_secret is seeded for powerdns-admin — full OIDC round-trip completes.', async ({ browser }) => {
  const deploys = (await list('a', 'deployments', 'oidc-gate')).filter((d) => d.metadata.name.includes('powerdns-admin'))
  expect(deploys.map((d) => d.metadata.name), 'the access gate of PowerDNS-Admin').toHaveLength(1)
  const gate = deploys[0]
  const env = arr(gate, 'spec.template.spec.containers').flatMap((c) => arr(c, 'env'))
  const clientId = str(env.find((e) => str(e, 'name') === 'OAUTH2_PROXY_CLIENT_ID'), 'value')
  expect(clientId, 'its client id').not.toBe('')
  const secret = await getObj('a', 'secret', 'oidc-gate', `${gate.metadata.name}-oidc`)
  expect(secret, `the Secret oidc-gate/${gate.metadata.name}-oidc`).not.toBeNull()
  const seeded = Buffer.from(String(at(secret, 'data', 'client-secret') ?? ''), 'base64').toString('utf8')
  expect(seeded.length, 'a seeded client-secret').toBeGreaterThan(0)
  const clients = await kcadm<Array<{ id: string; clientId: string }>>(['get', 'clients', '-r', 'sovereign', '-q', `clientId=${clientId}`])
  expect(clients.map((x) => x.clientId), `the client ${clientId} of the realm sovereign`).toEqual([clientId])
  const kc = await kcadm<{ value?: string }>(['get', `clients/${clients[0].id}/client-secret`, '-r', 'sovereign'])
  // Compared here, never printed.
  expect(seeded === (kc.value ?? ''), 'the gate holds the secret the identity provider holds').toBe(true)
  await asOwner(browser, async (page) => {
    await page.goto(host('pdns-admin', '/'))
    await expect(page, 'the round trip lands on PowerDNS-Admin').toHaveURL(/pdns-admin\.[^/]+\/dashboard/, { timeout: 120_000 })
    await expectNoLoginForm(page, 'PowerDNS-Admin')
  })
})
