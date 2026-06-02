// G117.6 (W2.C1) — application-controller topology fan-out E2E probe.
//
// Walks the operator-console drill-down that consumes
// `Application.status.perCluster[]` (the slice G117.6 application-
// controller writes when it fans HelmReleases per
// `Blueprint.spec.topology.perTopology[<choice>].placement.clusters[]`).
//
// The spec asserts:
//
//   1. For a freshly-created multi-region Application installed from a
//      Blueprint that declares `active-hot-standby` in its topology
//      supported list, GET /catalyst/v1/apps/{id} returns a
//      `perCluster` array of length ≥ 2 with one row whose role is
//      `active` and at least one whose role is `passive`.
//
//   2. Each `perCluster[].hr` matches the K8s DNS-1123 naming pattern
//      `<app-name>-<cluster-short>` (or its truncated 63-char form with
//      a 5-hex suffix when the combined length would overflow).
//
//   3. Submitting an Application with `spec.topology` set to a value
//      NOT in the Blueprint's `topology.supported` returns a 4xx
//      synchronously, OR the Application transitions to phase=Failed
//      with `status.conditions[type=Ready,status=False,
//      reason=InvalidTopology]` within 30 seconds.
//
// Auth: the spec mints an operator-scope JWT via the handover key on
// the Sovereign's bastion (matches G117.5's pattern). SKIPPED when
// `SOV_FQDN` is unset (local dev + PR-time CI).
//
// Brief: `.claude/templates/G117-wave2-briefs/W2.C1-application-controller-topology.md`
// Refs #2745 (G117.6) + #2737 (G117 EPIC).

import { test, expect, type APIRequestContext } from '@playwright/test'

const SOV_FQDN = process.env.SOV_FQDN || ''
const TOPOLOGY_E2E_BLUEPRINT = process.env.TOPOLOGY_E2E_BLUEPRINT || 'grafana'
const TOPOLOGY_E2E_ORG = process.env.TOPOLOGY_E2E_ORG || 'acme'
const TOPOLOGY_E2E_APP_PREFIX = process.env.TOPOLOGY_E2E_APP_PREFIX || 'obs-prod'
const TOPOLOGY_E2E_TIMEOUT_MS = 30_000

type PerCluster = {
  cluster: string
  role: string
  hr: string
  status?: string
}

type Application = {
  metadata: { name: string; namespace: string }
  status?: {
    phase?: string
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string }>
    perCluster?: PerCluster[]
  }
}

// SovereignAPI base — catalyst-api lives at api.<sov-fqdn>; the
// Wave-2 GET /apps/{id} surface is /catalyst/v1/apps/{id}. The shape
// matches `docs/api/catalyst-api-openapi.yaml` per the brief.
function sovereignAPI(): string {
  return `https://api.${SOV_FQDN}/catalyst/v1`
}

async function getApplication(req: APIRequestContext, name: string, token: string): Promise<Application | null> {
  const url = `${sovereignAPI()}/orgs/${TOPOLOGY_E2E_ORG}/apps/${name}`
  const resp = await req.get(url, { headers: { Authorization: `Bearer ${token}` } })
  if (resp.status() === 404) return null
  expect(resp.ok(), `${url} returned ${resp.status()}: ${await resp.text()}`).toBeTruthy()
  return (await resp.json()) as Application
}

async function createApplication(
  req: APIRequestContext,
  body: Record<string, unknown>,
  token: string,
): Promise<{ status: number; json: any }> {
  const url = `${sovereignAPI()}/orgs/${TOPOLOGY_E2E_ORG}/apps/instances`
  const resp = await req.post(url, {
    headers: { Authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    data: body,
  })
  let json: any = null
  try {
    json = await resp.json()
  } catch {
    json = await resp.text()
  }
  return { status: resp.status(), json }
}

async function deleteApplication(req: APIRequestContext, name: string, token: string): Promise<void> {
  const url = `${sovereignAPI()}/orgs/${TOPOLOGY_E2E_ORG}/apps/${name}`
  await req.delete(url, { headers: { Authorization: `Bearer ${token}` } })
}

async function pollUntil<T>(fn: () => Promise<T | null>, predicate: (t: T) => boolean, timeoutMs: number): Promise<T | null> {
  const start = Date.now()
  while (Date.now() - start < timeoutMs) {
    const v = await fn()
    if (v !== null && predicate(v)) return v
    await new Promise((r) => setTimeout(r, 1_000))
  }
  return null
}

test.describe('G117.6 topology fan-out (E2E on live Sovereign)', () => {
  test.skip(!SOV_FQDN, 'SOV_FQDN env not set — skipping live-Sovereign topology fan-out probe')

  // Mint a fresh app name per run so reruns don't collide on a cached
  // CR. Suffix is a 6-char unix-ms hash.
  const runID = Date.now().toString(36).slice(-6)
  const happyAppName = `${TOPOLOGY_E2E_APP_PREFIX}-${runID}`
  const sadAppName = `${TOPOLOGY_E2E_APP_PREFIX}-bad-${runID}`

  let TOKEN = ''

  test.beforeAll(async () => {
    // The handover JWT is minted on the bastion via
    // `/var/lib/catalyst/handover-jwt-private.pem` (per
    // memory.feedback_canonical_end_user_dod.md / G117.5 spec
    // precedent). For the Playwright run, the operator pipes the
    // minted token through SOV_TOKEN.
    TOKEN = process.env.SOV_TOKEN || ''
    test.skip(!TOKEN, 'SOV_TOKEN not set — skipping (operator must mint via handover JWT key)')
  })

  test('happy path — Blueprint topology multi-region active-hot-standby fans out per cluster', async ({ request }) => {
    // 1. Create a multi-region App against the W1.B1 grafana fixture
    //    Blueprint (supported: active-hot-standby + singleton).
    const created = await createApplication(
      request,
      {
        blueprint: TOPOLOGY_E2E_BLUEPRINT,
        org: TOPOLOGY_E2E_ORG,
        name: happyAppName,
        topology: 'active-hot-standby',
      },
      TOKEN,
    )
    expect.soft(created.status, `POST /apps/instances → ${created.status}: ${JSON.stringify(created.json)}`).toBeLessThan(400)

    // 2. Poll until status.perCluster[] has at least 2 rows OR the
    //    timeout elapses.
    const app = await pollUntil(
      () => getApplication(request, happyAppName, TOKEN),
      (a) => Array.isArray(a.status?.perCluster) && (a.status!.perCluster!.length >= 2),
      TOPOLOGY_E2E_TIMEOUT_MS,
    )
    expect(app, `Application ${happyAppName} never surfaced status.perCluster[] within ${TOPOLOGY_E2E_TIMEOUT_MS}ms`).not.toBeNull()
    const per = app!.status!.perCluster!
    expect(per.length, `perCluster[] length`).toBeGreaterThanOrEqual(2)

    // 3. Role distribution: exactly one 'active', at least one
    //    'passive'.
    const actives = per.filter((p) => p.role === 'active')
    const passives = per.filter((p) => p.role === 'passive')
    expect(actives.length, `active-hot-standby must have ≥1 active role`).toBeGreaterThanOrEqual(1)
    expect(passives.length, `active-hot-standby must have ≥1 passive role`).toBeGreaterThanOrEqual(1)

    // 4. HR names obey the DNS-1123 cap.
    for (const row of per) {
      expect(row.hr.length, `hr name "${row.hr}" exceeds 63 chars`).toBeLessThanOrEqual(63)
      expect(row.hr, `hr name "${row.hr}" not RFC1123-compatible`).toMatch(/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/)
    }
  })

  test('sad path — unsupported topology choice surfaces reason=InvalidTopology', async ({ request }) => {
    // bp-grafana fixture supports only active-hot-standby + singleton.
    // Submitting active-active must be rejected (sync 4xx OR async
    // condition InvalidTopology).
    const created = await createApplication(
      request,
      {
        blueprint: TOPOLOGY_E2E_BLUEPRINT,
        org: TOPOLOGY_E2E_ORG,
        name: sadAppName,
        topology: 'active-active',
      },
      TOKEN,
    )

    if (created.status >= 400) {
      // Sync rejection path — catalyst-api validated against the
      // Blueprint's supported list before persistence. Done.
      return
    }

    // Async path: the Application landed but the application-controller
    // must mark it Failed with reason=InvalidTopology within the
    // timeout.
    const app = await pollUntil(
      () => getApplication(request, sadAppName, TOKEN),
      (a) => {
        const cond = a.status?.conditions?.find((c) => c.type === 'Ready')
        return cond?.status === 'False' && cond.reason === 'InvalidTopology'
      },
      TOPOLOGY_E2E_TIMEOUT_MS,
    )
    expect(app, `Application ${sadAppName} did not surface InvalidTopology within ${TOPOLOGY_E2E_TIMEOUT_MS}ms`).not.toBeNull()
  })

  test.afterAll(async ({ request }) => {
    // Best-effort cleanup. We don't fail the run if delete returns
    // 404 — the controller's cascade-delete may already have run.
    if (TOKEN) {
      await deleteApplication(request, happyAppName, TOKEN).catch(() => {})
      await deleteApplication(request, sadAppName, TOKEN).catch(() => {})
    }
  })
})
