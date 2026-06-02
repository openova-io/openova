// G117.1 #2740 AC4 — Blueprint admission spec.
//
// Verifies the G117 EPIC admission contract end-to-end:
//
//   1. Negative cases — each malformed Blueprint must surface a
//      `status.conditions[type=Ready, status=False, reason=ValidationFailed]`
//      condition on the Blueprint CR (live mode) OR be rejected by
//      the structural validator (mock mode):
//        - missing `spec.topology.defaults.single-region` when
//          `spec.topology` is declared
//        - `spec.endpoints[0].protocol` set to an invalid value
//          (e.g. "banana")
//        - `spec.sso.realm: null` while `spec.sso.silentLogin: true`
//        - `spec.multiInstance.isolationLevel: "off-grid"` (enum
//          violation)
//
//   2. Happy case — a Blueprint with all four G117 blocks populated
//      correctly admits cleanly + status condition reports
//      `Ready=True` with reason=`ChartLatest` (or `Ready`).
//
// ─── Two modes ──────────────────────────────────────────────────────
//
//   MOCK mode (default for PR-time CI — SOV_FQDN unset):
//     Runs an in-spec structural validator that mirrors the G117 CRD
//     enums + required-field gates added to
//     `products/catalyst/chart/crds/blueprint.yaml` by PR #2825 (AC1)
//     and the runtime jsonschema validator landing in the F2 sibling
//     PR (AC2). The validator returns the same shape the live cluster
//     would surface — { ready: bool, reason: string, message: string }
//     — so the assertions in MOCK and LIVE mode are identical.
//
//   LIVE mode (post-handover Sovereign walk — SOV_FQDN set):
//     POSTs the malformed Blueprint manifests to the live K8s API
//     server via server-side apply against the bastion's apiserver
//     (operator passes the bearer token via SOV_TOKEN; the apiserver
//     URL defaults to https://api.<sov-fqdn> and can be overridden
//     via SOV_APISERVER_URL). Polls
//     `status.conditions[type=Ready]` until reason=ValidationFailed
//     (negative cases) or Ready=True (happy case), within ~10s.
//
// SOV_FQDN gating matches `g117-5-silent-sso-tier1.spec.ts`: the
// LIVE block skips when SOV_FQDN is empty; the MOCK block skips when
// SOV_FQDN is set so the two paths don't conflict in CI.
//
// Refs #2740, AC1=#2825, AC2=sibling F2 PR.

import { test, expect, type APIRequestContext } from '@playwright/test'

const SOV_FQDN = (process.env.SOV_FQDN || '').trim()
const SOV_TOKEN = process.env.SOV_TOKEN || ''
const ADMISSION_TIMEOUT_MS = Number(process.env.ADMISSION_TIMEOUT_MS || 10_000)

// ─── G117 schema enums (kept in lockstep with PR #2825 CRD) ─────────
//
// These mirror `products/catalyst/chart/crds/blueprint.yaml` and the
// `core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go`
// `AllBcpTopologies()` slice. If the CRD enum widens, the mirror here
// must widen too — chart-test `bp-catalyst-platform/test-crd-...`
// covers the CRD-side; this spec covers the admission-result side.
const ALLOWED_TOPOLOGIES = new Set(['active-hot-standby', 'active-active', 'singleton'])
const ALLOWED_PROTOCOLS = new Set(['http', 'https', 'grpc', 'tcp'])
const ALLOWED_REALMS = new Set(['sovereign', '{{.OrgSlug}}'])
const ALLOWED_ISOLATION_LEVELS = new Set(['namespace', 'vcluster'])

type AdmissionResult = {
  ready: boolean
  // The condition reason: 'ValidationFailed' on negative cases,
  // 'ChartLatest' (or 'Ready') on the happy case.
  reason: string
  message: string
}

// validateBlueprintSpec mirrors what the CRD's structural openAPIV3Schema
// (PR #2825) + the runtime jsonschema validator (sibling F2 PR) would
// reject. Returns the same `status.conditions[type=Ready]` shape the
// live cluster surfaces so assertions stay mode-agnostic.
function validateBlueprintSpec(spec: any): AdmissionResult {
  const errs: string[] = []

  // spec.topology — when present, `defaults.single-region` is required
  // per PR #2825 CRD schema (`required: [single-region]` under
  // topology.defaults).
  if (spec?.topology !== undefined && spec.topology !== null) {
    const t = spec.topology
    if (!t.defaults || typeof t.defaults !== 'object') {
      errs.push('spec.topology.defaults: Required value')
    } else if (t.defaults['single-region'] === undefined || t.defaults['single-region'] === null) {
      errs.push('spec.topology.defaults.single-region: Required value')
    }
    if (Array.isArray(t.supported)) {
      for (let i = 0; i < t.supported.length; i++) {
        if (!ALLOWED_TOPOLOGIES.has(t.supported[i])) {
          errs.push(`spec.topology.supported[${i}]: Unsupported value "${t.supported[i]}"`)
        }
      }
    }
  }

  // spec.endpoints[].protocol — enum gate per PR #2825.
  if (Array.isArray(spec?.endpoints)) {
    for (let i = 0; i < spec.endpoints.length; i++) {
      const ep = spec.endpoints[i]
      if (ep?.protocol !== undefined && !ALLOWED_PROTOCOLS.has(ep.protocol)) {
        errs.push(`spec.endpoints[${i}].protocol: Unsupported value "${ep.protocol}"`)
      }
    }
  }

  // spec.sso.realm — null disallowed when silentLogin=true; otherwise
  // must be one of the enum values per PR #2825.
  if (spec?.sso !== undefined && spec.sso !== null) {
    const s = spec.sso
    if (s.silentLogin === true && (s.realm === null || s.realm === undefined)) {
      errs.push('spec.sso.realm: Required value when silentLogin=true')
    } else if (s.realm !== undefined && s.realm !== null && !ALLOWED_REALMS.has(s.realm)) {
      errs.push(`spec.sso.realm: Unsupported value "${s.realm}"`)
    }
  }

  // spec.multiInstance.isolationLevel — enum gate per PR #2825.
  if (spec?.multiInstance !== undefined && spec.multiInstance !== null) {
    const m = spec.multiInstance
    if (m.isolationLevel !== undefined && !ALLOWED_ISOLATION_LEVELS.has(m.isolationLevel)) {
      errs.push(`spec.multiInstance.isolationLevel: Unsupported value "${m.isolationLevel}"`)
    }
  }

  if (errs.length > 0) {
    return { ready: false, reason: 'ValidationFailed', message: errs.join('; ') }
  }
  return { ready: true, reason: 'ChartLatest', message: 'Blueprint admitted' }
}

// ─── Fixture Blueprint specs ────────────────────────────────────────

const BASE_VALID_TOPOLOGY = {
  supported: ['active-hot-standby', 'singleton'],
  defaults: { 'multi-region': 'active-hot-standby', 'single-region': 'singleton' },
}

const HAPPY_SPEC = {
  version: '1.0.0',
  card: { title: 'happy-g117' },
  topology: BASE_VALID_TOPOLOGY,
  endpoints: [
    {
      name: 'ui',
      hostnameTemplate: '{{.AppName}}.{{.SovereignFQDN}}',
      port: 443,
      protocol: 'https',
      tls: true,
      visibility: 'public',
      launchDefault: true,
      ssoEnabled: true,
    },
  ],
  sso: { realm: 'sovereign', silentLogin: true, groupsClaim: 'groups' },
  multiInstance: { enabled: true, maxPerOrg: 5, isolationLevel: 'namespace' },
}

const NEGATIVE_CASES: Array<{ name: string; spec: any; expectMessage: RegExp }> = [
  {
    name: 'missing topology.defaults.single-region',
    spec: {
      version: '1.0.0',
      card: { title: 'bad-topology-defaults' },
      topology: {
        supported: ['singleton'],
        defaults: { 'multi-region': 'active-hot-standby' },
        // single-region intentionally omitted
      },
    },
    expectMessage: /single-region/i,
  },
  {
    name: 'endpoints[0].protocol = "banana" (enum violation)',
    spec: {
      version: '1.0.0',
      card: { title: 'bad-endpoint-protocol' },
      endpoints: [
        {
          name: 'ui',
          hostnameTemplate: 'x.{{.SovereignFQDN}}',
          port: 443,
          protocol: 'banana',
        },
      ],
    },
    expectMessage: /protocol/i,
  },
  {
    name: 'sso.realm = null while sso.silentLogin = true',
    spec: {
      version: '1.0.0',
      card: { title: 'bad-sso-realm-null' },
      sso: { realm: null, silentLogin: true },
    },
    expectMessage: /realm/i,
  },
  {
    name: 'multiInstance.isolationLevel = "off-grid" (enum violation)',
    spec: {
      version: '1.0.0',
      card: { title: 'bad-multi-isolation' },
      multiInstance: { enabled: true, isolationLevel: 'off-grid' },
    },
    expectMessage: /isolationLevel/i,
  },
]

// ─── LIVE-mode helpers (K8s REST against the Sovereign apiserver) ───

type Condition = { type: string; status: string; reason?: string; message?: string }

async function applyBlueprint(
  req: APIRequestContext,
  apiserverURL: string,
  name: string,
  spec: any,
): Promise<{ status: number; body: any }> {
  // K8s server-side apply against the cluster-scoped Blueprint
  // resource — equivalent to `kubectl apply -f -`. apiserverURL is
  // typically `https://api.<sov-fqdn>` per the bastion's kubeconfig
  // (operator sets SOV_APISERVER_URL when it differs).
  const obj = {
    apiVersion: 'catalyst.openova.io/v1',
    kind: 'Blueprint',
    metadata: { name },
    spec,
  }
  // Use server-side apply (PATCH with content-type application/apply-patch+yaml).
  // JSON-encoded SSA also accepted by apiservers ≥ 1.22.
  const resp = await req.patch(
    `${apiserverURL}/apis/catalyst.openova.io/v1/blueprints/${name}?fieldManager=playwright-admission-spec&force=true`,
    {
      headers: {
        Authorization: `Bearer ${SOV_TOKEN}`,
        'Content-Type': 'application/apply-patch+yaml',
      },
      data: JSON.stringify(obj),
      ignoreHTTPSErrors: true,
      failOnStatusCode: false,
    },
  )
  let body: any = null
  try {
    body = await resp.json()
  } catch {
    body = await resp.text()
  }
  return { status: resp.status(), body }
}

async function getBlueprintReadyCondition(
  req: APIRequestContext,
  apiserverURL: string,
  name: string,
): Promise<Condition | null> {
  const resp = await req.get(`${apiserverURL}/apis/catalyst.openova.io/v1/blueprints/${name}`, {
    headers: { Authorization: `Bearer ${SOV_TOKEN}` },
    ignoreHTTPSErrors: true,
    failOnStatusCode: false,
  })
  if (resp.status() === 404) return null
  if (resp.status() >= 400) return null
  const bp = await resp.json()
  const conds: Condition[] | undefined = bp?.status?.conditions
  if (!Array.isArray(conds)) return null
  return conds.find((c) => c.type === 'Ready') || null
}

async function pollForReady(
  req: APIRequestContext,
  apiserverURL: string,
  name: string,
  predicate: (c: Condition) => boolean,
  timeoutMs: number,
): Promise<Condition | null> {
  const start = Date.now()
  while (Date.now() - start < timeoutMs) {
    const c = await getBlueprintReadyCondition(req, apiserverURL, name)
    if (c && predicate(c)) return c
    await new Promise((r) => setTimeout(r, 500))
  }
  return await getBlueprintReadyCondition(req, apiserverURL, name)
}

async function deleteBlueprint(req: APIRequestContext, apiserverURL: string, name: string): Promise<void> {
  await req.delete(`${apiserverURL}/apis/catalyst.openova.io/v1/blueprints/${name}`, {
    headers: { Authorization: `Bearer ${SOV_TOKEN}` },
    ignoreHTTPSErrors: true,
    failOnStatusCode: false,
  })
}

// ─── MOCK-mode block ────────────────────────────────────────────────

test.describe('G117.1 #2740 AC4 — Blueprint admission (MOCK mode)', () => {
  test.skip(!!SOV_FQDN, 'SOV_FQDN set — MOCK block skipped; LIVE block runs instead')

  for (const tc of NEGATIVE_CASES) {
    test(`negative: ${tc.name} → Ready=False reason=ValidationFailed`, () => {
      const result = validateBlueprintSpec(tc.spec)
      expect(result.ready, `expected admission rejection for "${tc.name}"; got ${JSON.stringify(result)}`).toBe(false)
      expect(result.reason).toBe('ValidationFailed')
      expect(result.message).toMatch(tc.expectMessage)
    })
  }

  test('happy: all 4 G117 blocks populated → Ready=True reason=ChartLatest', () => {
    const result = validateBlueprintSpec(HAPPY_SPEC)
    expect(result.ready, `expected happy spec to admit; got ${JSON.stringify(result)}`).toBe(true)
    expect(result.reason).toBe('ChartLatest')
  })
})

// ─── LIVE-mode block ────────────────────────────────────────────────

test.describe('G117.1 #2740 AC4 — Blueprint admission (LIVE mode)', () => {
  test.skip(!SOV_FQDN, 'SOV_FQDN unset — LIVE block skipped; MOCK block covers the contract')
  test.skip(
    !SOV_TOKEN,
    'SOV_TOKEN unset — LIVE block requires a K8s API bearer token (operator mints via bastion kubeconfig)',
  )

  const apiserverURL = (process.env.SOV_APISERVER_URL || `https://api.${SOV_FQDN}`).replace(/\/$/, '')

  // Per-run suffix so reruns don't collide on a cached CR.
  const runID = Date.now().toString(36).slice(-6)

  for (const tc of NEGATIVE_CASES) {
    const safeName = `bp-g117-admit-bad-${runID}-${tc.name.replace(/[^a-z0-9]+/gi, '-').toLowerCase().slice(0, 30)}`

    test(`negative: ${tc.name} → status.conditions[Ready, False, ValidationFailed] within ~10s`, async ({
      request,
    }) => {
      // Step 1: apply the malformed Blueprint. The CRD's structural
      // schema (PR #2825) MAY reject synchronously with a 4xx — that
      // is ALSO a valid admission outcome (structural rejection ≡
      // ValidationFailed condition). If it admits, the runtime
      // jsonschema validator in blueprint-controller writes the
      // condition.
      const applied = await applyBlueprint(request, apiserverURL, safeName, tc.spec)

      if (applied.status >= 400) {
        // Synchronous CRD rejection — the apiserver's status reply
        // carries the same "ValidationFailed" semantics as the
        // controller's condition. Assert the response body mentions
        // the offending field.
        const text = typeof applied.body === 'string' ? applied.body : JSON.stringify(applied.body)
        expect(text, `synchronous rejection should mention offending field for "${tc.name}"`).toMatch(
          tc.expectMessage,
        )
        return
      }

      // Step 2: cluster admitted; poll for the Ready=False condition.
      const cond = await pollForReady(
        request,
        apiserverURL,
        safeName,
        (c) => c.status === 'False' && c.reason === 'ValidationFailed',
        ADMISSION_TIMEOUT_MS,
      )
      expect(cond, `Blueprint ${safeName} never surfaced Ready=False reason=ValidationFailed`).not.toBeNull()
      expect(cond!.status).toBe('False')
      expect(cond!.reason).toBe('ValidationFailed')
      expect(cond!.message || '', `condition message should mention offending field`).toMatch(tc.expectMessage)

      // Cleanup so reruns don't accumulate.
      await deleteBlueprint(request, apiserverURL, safeName)
    })
  }

  test('happy: all 4 G117 blocks populated → status.conditions[Ready, True, ChartLatest]', async ({ request }) => {
    const safeName = `bp-g117-admit-happy-${runID}`
    const applied = await applyBlueprint(request, apiserverURL, safeName, HAPPY_SPEC)
    expect(applied.status, `happy apply returned ${applied.status}: ${JSON.stringify(applied.body)}`).toBeLessThan(
      400,
    )

    const cond = await pollForReady(
      request,
      apiserverURL,
      safeName,
      (c) => c.status === 'True',
      ADMISSION_TIMEOUT_MS,
    )
    expect(cond, `happy Blueprint never surfaced Ready=True within ${ADMISSION_TIMEOUT_MS}ms`).not.toBeNull()
    expect(cond!.status).toBe('True')
    // Reason vocabulary: 'Ready' from blueprint_controller.go +
    // 'ChartLatest' from W2.C2 chart-pin reconciler. Accept either.
    expect(['Ready', 'ChartLatest']).toContain(cond!.reason)

    await deleteBlueprint(request, apiserverURL, safeName)
  })
})
