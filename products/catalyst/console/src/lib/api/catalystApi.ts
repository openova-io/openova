// G117 — catalyst-api client.
//
// Wraps every endpoint declared in docs/api/catalyst-api-openapi.yaml.
// Two modes:
//   - live  : fetches against /catalyst/v1 (proxied by vite/nginx to the
//             per-Sovereign catalyst-api)
//   - mock  : when window.__MOCK_API__ is populated by /src/lib/api/mock.ts
//             (dev runs with MOCK_API=1; Playwright tests inject it)
//
// All methods throw ApiError on non-2xx with the parsed error envelope.

import type {
  Application,
  ApplicationSummary,
  CatalogBlueprint,
  CreateInstanceRequest,
  EndpointMutationRequest,
  EndpointPR,
  LaunchURL,
  ResolvedEndpoint,
} from './types';

const BASE = '/catalyst/v1';

declare global {
  // Populated by mock harness at boot when MOCK_API=1.
  // eslint-disable-next-line no-var
  var __MOCK_API__: MockBackend | undefined;
  interface Window {
    __MOCK_API__?: MockBackend;
  }
}

export interface MockBackend {
  getBlueprint(name: string): CatalogBlueprint | undefined;
  listInstances(blueprint: string, org?: string): ApplicationSummary[];
  getApp(id: string): Application | undefined;
  createInstance(req: CreateInstanceRequest): Application;
  deleteApp(id: string): { jobId: string };
  listEndpoints(appId: string): ResolvedEndpoint[];
  upsertEndpoint(
    appId: string,
    name: string | null,
    body: EndpointMutationRequest,
  ): EndpointPR;
  deleteEndpoint(appId: string, name: string): EndpointPR;
  getLaunchURL(appId: string, endpoint?: string): LaunchURL;
}

function mock(): MockBackend | undefined {
  if (typeof window === 'undefined') return undefined;
  return window.__MOCK_API__;
}

async function http<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { accept: 'application/json' };
  const opts: RequestInit = { method, headers };
  if (body !== undefined) {
    headers['content-type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  // Live mode — proxy through vite/nginx.
  const res = await fetch(`${BASE}${path}`, opts);
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { code: 'invalid-json', message: text };
    }
  }
  if (!res.ok) {
    const err = (parsed && typeof parsed === 'object'
      ? (parsed as Record<string, unknown>)
      : { code: `http-${res.status}`, message: text }) as {
      code: string;
      message: string;
    };
    throw err;
  }
  return parsed as T;
}

export const catalystApi = {
  async getBlueprint(name: string): Promise<CatalogBlueprint> {
    const m = mock();
    if (m) {
      const bp = m.getBlueprint(name);
      if (!bp) throw { code: 'not-found', message: `blueprint ${name} not found` };
      return bp;
    }
    return http<CatalogBlueprint>('GET', `/catalog/${encodeURIComponent(name)}`);
  },

  async listBlueprintInstances(
    blueprint: string,
    org?: string,
  ): Promise<ApplicationSummary[]> {
    const m = mock();
    if (m) return m.listInstances(blueprint, org);
    const q = org ? `?org=${encodeURIComponent(org)}` : '';
    const res = await http<{ items: ApplicationSummary[] }>(
      'GET',
      `/catalog/${encodeURIComponent(blueprint)}/instances${q}`,
    );
    return res.items ?? [];
  },

  async getApp(id: string): Promise<Application> {
    const m = mock();
    if (m) {
      const a = m.getApp(id);
      if (!a) throw { code: 'not-found', message: `app ${id} not found` };
      return a;
    }
    return http<Application>('GET', `/apps/${encodeURIComponent(id)}`);
  },

  async createInstance(req: CreateInstanceRequest): Promise<Application> {
    const m = mock();
    if (m) return m.createInstance(req);
    return http<Application>('POST', `/apps/instances`, req);
  },

  async deleteApp(id: string): Promise<{ jobId: string }> {
    const m = mock();
    if (m) return m.deleteApp(id);
    return http<{ jobId: string }>('DELETE', `/apps/${encodeURIComponent(id)}`);
  },

  async listAppEndpoints(appId: string): Promise<ResolvedEndpoint[]> {
    const m = mock();
    if (m) return m.listEndpoints(appId);
    const res = await http<{ items: ResolvedEndpoint[] }>(
      'GET',
      `/apps/${encodeURIComponent(appId)}/endpoints`,
    );
    return res.items ?? [];
  },

  async createAppEndpoint(
    appId: string,
    body: EndpointMutationRequest,
  ): Promise<EndpointPR> {
    const m = mock();
    if (m) return m.upsertEndpoint(appId, null, body);
    return http<EndpointPR>(
      'POST',
      `/apps/${encodeURIComponent(appId)}/endpoints`,
      body,
    );
  },

  async patchAppEndpoint(
    appId: string,
    name: string,
    body: EndpointMutationRequest,
  ): Promise<EndpointPR> {
    const m = mock();
    if (m) return m.upsertEndpoint(appId, name, body);
    return http<EndpointPR>(
      'PATCH',
      `/apps/${encodeURIComponent(appId)}/endpoints/${encodeURIComponent(name)}`,
      body,
    );
  },

  async deleteAppEndpoint(appId: string, name: string): Promise<EndpointPR> {
    const m = mock();
    if (m) return m.deleteEndpoint(appId, name);
    return http<EndpointPR>(
      'DELETE',
      `/apps/${encodeURIComponent(appId)}/endpoints/${encodeURIComponent(name)}`,
    );
  },

  async getLaunchURL(appId: string, endpoint?: string): Promise<LaunchURL> {
    const m = mock();
    if (m) return m.getLaunchURL(appId, endpoint);
    const q = endpoint ? `?endpoint=${encodeURIComponent(endpoint)}` : '';
    return http<LaunchURL>(
      'GET',
      `/apps/${encodeURIComponent(appId)}/launch-url${q}`,
    );
  },
};

export type CatalystApi = typeof catalystApi;
