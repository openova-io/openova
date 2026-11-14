// G117 — In-browser mock backend for dev + Playwright.
//
// Reads the YAML fixture at tests/e2e/fixtures/mock-blueprints.yaml and
// exposes a MockBackend impl on window.__MOCK_API__. The catalystApi client
// shortcuts to this when present.
//
// Loaded by src/lib/api/installMock.ts on MOCK_API=1 dev or by the
// Playwright test runner via page.addInitScript.

import type {
  Application,
  ApplicationSummary,
  CatalogBlueprint,
  CreateInstanceRequest,
  EndpointMutationRequest,
  EndpointPR,
  InstancePlacement,
  LaunchURL,
  ResolvedEndpoint,
} from './types';
import type { MockBackend } from './catalystApi';

export interface MockFixture {
  blueprints: Record<string, CatalogBlueprint>;
  instances: Record<string, ApplicationSummary[]>;
  apps: Record<string, Application>;
  endpoints: Record<string, ResolvedEndpoint[]>;
}

const DEFAULT_FIXTURE: MockFixture = {
  blueprints: {},
  instances: {},
  apps: {},
  endpoints: {},
};

// Deterministic counter so `mock-instance-1`, `-2`, … are stable across
// the Playwright run (avoids relying on a random UUID for static prerender).
let mockSeq = 0;
function mockId(): string {
  mockSeq += 1;
  return `mock-instance-${mockSeq}`;
}

function resolveHostname(template: string, ctx: { app: string; org: string; sov: string }): string {
  return template
    .replace(/\{\{\s*\.AppName\s*\}\}/g, ctx.app)
    .replace(/\{\{\s*\.Org\s*\}\}/g, ctx.org)
    .replace(/\{\{\s*\.SovereignFQDN\s*\}\}/g, ctx.sov);
}

const MOCK_STATE_KEY = 'catalyst-console-mock-state-v1';

function readPersistedFixture(): Partial<MockFixture> | undefined {
  if (typeof sessionStorage === 'undefined') return undefined;
  try {
    const raw = sessionStorage.getItem(MOCK_STATE_KEY);
    return raw ? (JSON.parse(raw) as Partial<MockFixture>) : undefined;
  } catch {
    return undefined;
  }
}

function persistFixture(fixture: MockFixture): void {
  if (typeof sessionStorage === 'undefined') return;
  try {
    sessionStorage.setItem(MOCK_STATE_KEY, JSON.stringify(fixture));
  } catch {
    // ignore quota errors — mock is best-effort across navigations.
  }
}

export function createMockBackend(initial?: Partial<MockFixture>): MockBackend {
  // Merge order: defaults → caller-provided initial (the YAML fixture) →
  // any persisted state from sessionStorage (so an instance created on
  // /catalog/.../new survives the redirect to /apps/<id>).
  const persisted = readPersistedFixture();
  const fixture: MockFixture = {
    blueprints: {
      ...DEFAULT_FIXTURE.blueprints,
      ...(initial?.blueprints ?? {}),
      ...(persisted?.blueprints ?? {}),
    },
    instances: {
      ...DEFAULT_FIXTURE.instances,
      ...(initial?.instances ?? {}),
      ...(persisted?.instances ?? {}),
    },
    apps: {
      ...DEFAULT_FIXTURE.apps,
      ...(initial?.apps ?? {}),
      ...(persisted?.apps ?? {}),
    },
    endpoints: {
      ...DEFAULT_FIXTURE.endpoints,
      ...(initial?.endpoints ?? {}),
      ...(persisted?.endpoints ?? {}),
    },
  };
  // If persisted state contained a seq hint, restore it so subsequent
  // createInstance calls don't collide with already-minted IDs.
  if (persisted && Object.keys(persisted.apps ?? {}).length > 0) {
    const seqIds = Object.keys(persisted.apps ?? {})
      .map((id) => /^mock-instance-(\d+)$/.exec(id)?.[1])
      .filter((n): n is string => !!n)
      .map(Number);
    if (seqIds.length > 0) mockSeq = Math.max(mockSeq, ...seqIds);
  }

  return {
    getBlueprint(name: string) {
      return fixture.blueprints[name];
    },
    listInstances(blueprint: string, org?: string) {
      const list = fixture.instances[blueprint] ?? [];
      return org ? list.filter((i) => i.org === org) : list;
    },
    getApp(id: string) {
      return fixture.apps[id];
    },
    createInstance(req: CreateInstanceRequest) {
      const bp = fixture.blueprints[req.blueprint];
      if (!bp) {
        throw { code: 'bad-request', message: `unknown blueprint ${req.blueprint}` };
      }
      const existing = fixture.instances[req.blueprint] ?? [];
      if (!bp.multiInstance.enabled && existing.some((i) => i.org === req.org)) {
        throw {
          code: 'conflict',
          message: `Blueprint ${req.blueprint} is singleton-per-Org and ${req.org} already has an instance`,
        };
      }
      const topology = req.topology ?? bp.defaultTopology;
      // #3373 — placement echo. Explicit request placement wins
      // (source: 'instance', vcluster backfilled from the Blueprint default
      // when omitted); otherwise the Blueprint's declared default applies
      // (source: 'blueprint-default'). No placement anywhere → omitted.
      let placement: InstancePlacement | undefined;
      if (req.placement) {
        placement = {
          ...req.placement,
          vcluster: req.placement.vcluster ?? bp.defaultPlacement?.vcluster,
          source: 'instance',
        };
      } else if (bp.defaultPlacement) {
        placement = { ...bp.defaultPlacement, source: 'blueprint-default' };
      }
      const id = mockId();
      const sov = 't01.omani.works';
      const summary: ApplicationSummary = {
        id,
        name: req.name,
        blueprint: req.blueprint,
        org: req.org,
        topology,
        status: 'Pending',
        createdAt: new Date().toISOString(),
      };
      const endpoints: ResolvedEndpoint[] = (bp.endpoints ?? []).map((e) => ({
        ...e,
        hostname: resolveHostname(e.hostnameTemplate, {
          app: req.name,
          org: req.org,
          sov,
        }),
        status: 'Pending',
      }));
      const app: Application = {
        ...summary,
        perCluster: [
          { cluster: 'mgmt-A', role: topology === 'singleton' ? 'singleton' : 'active', status: 'Reconciling' },
        ],
        endpoints,
        placement,
      };
      fixture.instances[req.blueprint] = [...existing, summary];
      fixture.apps[id] = app;
      fixture.endpoints[id] = endpoints;
      bp.instanceCount = (bp.instanceCount ?? 0) + 1;
      persistFixture(fixture);
      return app;
    },
    deleteApp(id: string) {
      const app = fixture.apps[id];
      if (!app) throw { code: 'not-found', message: `app ${id} not found` };
      delete fixture.apps[id];
      delete fixture.endpoints[id];
      const bp = fixture.blueprints[app.blueprint];
      if (bp) bp.instanceCount = Math.max(0, (bp.instanceCount ?? 1) - 1);
      const list = fixture.instances[app.blueprint] ?? [];
      fixture.instances[app.blueprint] = list.filter((i) => i.id !== id);
      persistFixture(fixture);
      return { jobId: 'mock-job-' + mockId() };
    },
    listEndpoints(appId: string) {
      return fixture.endpoints[appId] ?? [];
    },
    upsertEndpoint(appId: string, name: string | null, body: EndpointMutationRequest) {
      const list = fixture.endpoints[appId] ?? [];
      if (name) {
        const idx = list.findIndex((e) => e.name === name);
        if (idx === -1) throw { code: 'not-found', message: `endpoint ${name} not found` };
        list[idx] = {
          ...list[idx],
          ...body,
          hostnameTemplate: list[idx].hostnameTemplate,
          hostname: body.hostname ?? list[idx].hostname,
          pendingPR: {
            prURL: `https://gitea.example/acme/iac/pulls/${list.length + 1}`,
            status: 'open',
          },
        };
      } else {
        list.push({
          name: body.name,
          hostnameTemplate: body.hostname ?? '',
          hostname: body.hostname ?? '',
          port: body.port,
          protocol: body.protocol ?? 'https',
          tls: body.tls ?? true,
          visibility: body.visibility ?? 'public',
          ssoEnabled: body.ssoEnabled ?? false,
          status: 'Pending',
          pendingPR: {
            prURL: `https://gitea.example/acme/iac/pulls/${list.length + 1}`,
            status: 'open',
          },
        });
      }
      fixture.endpoints[appId] = list;
      persistFixture(fixture);
      return {
        prURL: `https://gitea.example/acme/iac/pulls/${list.length}`,
        status: 'open',
        preCheckResults: { kyverno: 'pending', certManager: 'pending', dnsConflict: 'pass' },
      };
    },
    deleteEndpoint(appId: string, name: string) {
      const list = fixture.endpoints[appId] ?? [];
      const before = list.length;
      fixture.endpoints[appId] = list.filter((e) => e.name !== name);
      if (fixture.endpoints[appId].length === before) {
        throw { code: 'not-found', message: `endpoint ${name} not found` };
      }
      persistFixture(fixture);
      return {
        prURL: `https://gitea.example/acme/iac/pulls/${before + 1}`,
        status: 'open' as const,
      };
    },
    getLaunchURL(appId: string, endpoint?: string): LaunchURL {
      const list = fixture.endpoints[appId] ?? [];
      const ep =
        list.find((e) => e.name === endpoint) ??
        list.find((e) => e.launchDefault) ??
        list[0];
      if (!ep) throw { code: 'not-found', message: `no endpoint for ${appId}` };
      if (!ep.ssoEnabled) {
        throw {
          code: 'sso-disabled',
          message: `endpoint ${ep.name} has SSO disabled — silent launch not possible`,
        };
      }
      const proto = ep.tls === false ? 'http' : 'https';
      // Decision #3: silent OIDC with prompt=none + kc_idp_hint=catalyst-pin.
      const url =
        `${proto}://${ep.hostname}/?prompt=none&kc_idp_hint=catalyst-pin` +
        `&t=${Date.now()}`;
      return {
        url,
        endpoint: ep.name,
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      };
    },
  };
}
