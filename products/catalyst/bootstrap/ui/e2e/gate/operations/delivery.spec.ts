// Gate check FND-G21 (FND-163): the delivery path of the candidate — its own Git service and what Flux
// pulls from it, the reflected pull credential of the Organizations, the sovereignty of the sources after
// cut-over, and the per-line pin bumps of the release bot in the history of the commit under test. One
// test per ledger row; the title is the row id and its clause. Row R11 restarts the Git service's Pod.
import { test, expect } from '../fixtures.ts'
import { arr, execIn, fqdnOf, getObj, git, kube, list, orgNamespaces, ready, str, until, type Obj } from './lib.ts'

/** The Running Pod of the in-cluster Git service of region a. */
async function giteaPod(): Promise<Obj> {
  const pods = (await list('a', 'pods', 'gitea')).filter((p) => /^gitea-/.test(p.metadata.name) && str(p, 'status.phase') === 'Running' && !/postgres|redis|valkey|memcache/.test(p.metadata.name))
  expect(pods.length, 'a Running gitea Pod in namespace gitea').toBeGreaterThan(0)
  return pods[0]
}

/** The HEAD commit of every bare repository of the Git service, as "<owner>/<repo> <sha>" lines. */
async function heads(pod: Obj): Promise<Map<string, string>> {
  const script = [
    'root=$(awk -F" *= *" \'/^\\[repository\\]/{f=1;next} /^\\[/{f=0} f&&$1=="ROOT"{print $2; exit}\' /data/gitea/conf/app.ini 2>/dev/null)',
    '[ -n "$root" ] || root=/data/git/gitea-repositories',
    'for d in "$root"/*/*.git; do [ -d "$d" ] || continue; h=$(git --git-dir="$d" rev-parse --verify -q HEAD 2>/dev/null) || continue; echo "${d#$root/} $h"; done',
  ].join('\n')
  const out = await execIn('a', 'gitea', pod.metadata.name, 'gitea', script)
  const m = new Map<string, string>()
  for (const line of out.split('\n')) {
    const [repo, sha] = line.trim().split(' ')
    if (repo && /^[0-9a-f]{40,64}$/.test(sha ?? '')) m.set(repo, sha)
  }
  return m
}

test('R8 gitea-flux-auth secrets are seeded — Flux pulls from gitea with valid credentials.', async () => {
  const repos = (await list('a', 'gitrepositories.source.toolkit.fluxcd.io')).filter((g) => /gitea/.test(str(g, 'spec.url')))
  expect(repos.length, 'GitRepositories that pull from the candidate gitea').toBeGreaterThan(0)
  for (const g of repos) {
    const where = `${g.metadata.namespace}/${g.metadata.name}`
    const secret = str(g, 'spec.secretRef.name')
    expect(secret, `${where}: names its credential`).not.toBe('')
    expect(await getObj('a', 'secrets', g.metadata.namespace ?? '', secret), `${where}: the credential ${secret} is seeded`).not.toBeNull()
    expect(ready(g), `${where}: Ready`).toBe('True')
    expect(str(g, 'status.artifact.revision'), `${where}: an artifact was stored (an authenticated pull)`).not.toBe('')
  }
})

test('R11 gitea git-data survives a POD RESTART — the PVC rebinds and every bare repo retains its HEAD commit; no empty-PVC data loss on reschedule.', async () => {
  test.setTimeout(600_000)
  const before = await giteaPod()
  const headsBefore = await heads(before)
  expect(headsBefore.size, 'bare repositories with a HEAD before the restart').toBeGreaterThan(0)
  const claims = arr(before, 'spec.volumes').map((v) => str(v, 'persistentVolumeClaim.claimName')).filter((c) => c !== '')
  expect(claims.length, 'the gitea Pod mounts a PersistentVolumeClaim').toBeGreaterThan(0)
  await kube('a', ['delete', 'pod', '-n', 'gitea', before.metadata.name, '--wait=true'], 180_000)
  const after = await until('a new gitea Pod Ready', async () => {
    const p = (await list('a', 'pods', 'gitea')).find((x) => /^gitea-/.test(x.metadata.name) && x.metadata.uid !== before.metadata.uid && !/postgres|redis|valkey|memcache/.test(x.metadata.name))
    const isReady = p && arr(p, 'status.conditions').some((c) => str(c, 'type') === 'Ready' && str(c, 'status') === 'True')
    return isReady ? (p as Obj) : null
  }, 420_000)
  for (const c of claims) {
    const pvc = await getObj('a', 'persistentvolumeclaims', 'gitea', c)
    expect(str(pvc, 'status.phase'), `the claim ${c} is bound again`).toBe('Bound')
  }
  const headsAfter = await heads(after)
  const lost = [...headsBefore].filter(([repo, sha]) => headsAfter.get(repo) !== sha).map(([repo]) => repo)
  expect(lost, 'bare repositories whose HEAD changed or vanished across the restart').toEqual([])
})

test('R20 the deploy-bot bumps image pins per-line (not a blanket bump) — stale-render avoided.', async () => {
  const log = await git(['log', '--format=%H %s', '-n', '4000', 'HEAD'])
  const bumps = log.split('\n').filter((l) => / deploy(\(|:)/.test(l)).map((l) => l.split(' ')[0]).slice(0, 30)
  expect(bumps.length, 'release-bot pin commits in the history of the commit under test').toBeGreaterThan(0)
  const pin = /^\s*"?(version|appVersion|tag|newTag|orgTag|image|value|digest|generatedAt)"?\s*[:=]/
  for (const c of bumps) {
    const stat = await git(['show', '--format=', '--numstat', c])
    for (const line of stat.trim().split('\n').filter((l) => l !== '')) {
      const [added, removed, path] = line.split('\t')
      expect(added, `${c.slice(0, 10)} ${path}: lines replaced in place (added = removed)`).toBe(removed)
    }
    const diff = await git(['show', '--format=', '--unified=0', c])
    const changed = diff.split('\n').filter((l) => /^[-+](?![-+]{2})/.test(l)).map((l) => l.slice(1))
    expect(changed.length, `${c.slice(0, 10)}: changes lines`).toBeGreaterThan(0)
    expect(changed.filter((l) => !pin.test(l)), `${c.slice(0, 10)}: changed lines that are not a pin line`).toEqual([])
  }
})

test('227 A POST-cutover Sovereign REFUSES a GitHub-side catalog bump: the Blueprint CR does NOT move when the upstream pin advances, because after cutover the only delivery path is the Sovereign\'s own Gitea/Harbor. The refusal IS the sovereignty proof.', async () => {
  const status = await getObj('a', 'configmaps', 'catalyst', 'self-sovereign-cutover-status')
  expect(status, 'the cut-over status of the candidate').not.toBeNull()
  expect(status?.data?.cutoverComplete, 'the candidate is post-cutover').toBe('true')
  const fqdn = fqdnOf()
  const local = (u: string) => {
    const host = u.replace(/^[a-z+]+:\/\//, '').replace(/^[^@/]*@/, '').split(/[/:]/)[0]
    return host === fqdn || host.endsWith(`.${fqdn}`) || /\.svc(\.cluster\.local)?$/.test(host)
  }
  const sources: Obj[] = []
  for (const k of ['gitrepositories.source.toolkit.fluxcd.io', 'ocirepositories.source.toolkit.fluxcd.io', 'helmrepositories.source.toolkit.fluxcd.io']) sources.push(...(await list('a', k)))
  expect(sources.length, 'the Flux sources of the candidate').toBeGreaterThan(0)
  const upstream = sources.filter((s) => !local(str(s, 'spec.url'))).map((s) => `${s.kind} ${s.metadata.namespace}/${s.metadata.name} ${str(s, 'spec.url')}`)
  expect(upstream, 'Flux sources that still deliver from outside the Sovereign').toEqual([])
})

test('230 ghcr-pull reflects into a bare-slug per-Org namespace — the emberstack reflector copies the ghcr-pull imagePullSecret into a per-Org namespace whose name is a bare slug, so private ghcr.io/openova-io/* app images pull (no ImagePullBackOff 401-anonymous).', async () => {
  const orgs = await orgNamespaces('a')
  expect(orgs.length, 'per-Organization namespaces on the candidate').toBeGreaterThan(0)
  for (const ns of orgs) {
    const n = ns.metadata.name
    expect(n, `${n}: a bare slug`).toMatch(/^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/)
    const s = await getObj('a', 'secrets', n, 'ghcr-pull')
    expect(s, `${n}: the reflected ghcr-pull Secret`).not.toBeNull()
    expect(s?.type, `${n}: a docker config Secret`).toBe('kubernetes.io/dockerconfigjson')
    expect(s?.metadata.annotations?.['reflector.v1.k8s.emberstack.com/reflects'] ?? '', `${n}: reflected from its source`).toMatch(/\/ghcr-pull$/)
  }
  const pods = (await list('a', 'pods')).filter((p) => orgs.some((o) => o.metadata.name === p.metadata.namespace))
  const statuses = pods.flatMap((p) => arr(p, 'status.containerStatuses').map((c) => ({ pod: `${p.metadata.namespace}/${p.metadata.name}`, image: str(c, 'image'), waiting: str(c, 'state.waiting.reason'), running: str(c, 'state.running.startedAt') })))
  expect(statuses.filter((c) => /ErrImagePull|ImagePullBackOff/.test(c.waiting)).map((c) => `${c.pod} ${c.image}`), 'per-Org containers that cannot pull').toEqual([])
  expect(statuses.filter((c) => /ghcr\.io\/openova-io\//.test(c.image) && c.running !== '').length, 'a per-Org container of a private ghcr.io/openova-io image Running').toBeGreaterThan(0)
})
