// G117 — TypeScript types mirroring docs/api/catalyst-api-openapi.yaml.
//
// Hand-authored rather than codegen because the OpenAPI surface is small
// (4 path groups, ~8 schemas) and we want a single dependency-free file
// the Svelte runtime can import without a generator step. Keep in lockstep
// with docs/api/catalyst-api-openapi.yaml — drift is caught by the
// `endpoints-shape` Playwright assertion in catalog-multi-instance.spec.ts.

export type BcpTopology =
  | 'active-active'
  | 'active-hot-standby'
  | 'active-passive'
  | 'singleton';

export type EndpointProtocol = 'https' | 'http' | 'grpc' | 'tcp' | 'udp';
export type EndpointVisibility = 'public' | 'private' | 'internal';
export type EndpointStatus =
  | 'Pending'
  | 'Provisioning'
  | 'Ready'
  | 'Degraded'
  | 'Failed';
export type CertificateStatus = 'Pending' | 'Issued' | 'Failed';
export type ApplicationStatus =
  | 'Pending'
  | 'Reconciling'
  | 'Ready'
  | 'Degraded'
  | 'Failed'
  | 'Uninstalling';
export type ClusterRole = 'active' | 'passive' | 'singleton';
export type ClusterStatus =
  | 'Pending'
  | 'Reconciling'
  | 'Ready'
  | 'Degraded'
  | 'Failed';

export interface EndpointDeclaration {
  name: string;
  hostnameTemplate: string;
  port?: number;
  protocol?: EndpointProtocol;
  tls?: boolean;
  visibility?: EndpointVisibility;
  launchDefault?: boolean;
  ssoEnabled?: boolean;
}

export interface ResolvedEndpoint extends EndpointDeclaration {
  hostname: string;
  status: EndpointStatus;
  certificateStatus?: CertificateStatus;
  launchURL?: string;
  pendingPR?: { prURL: string; status: string };
}

export interface CatalogBlueprint {
  name: string;
  version: string;
  title: string;
  description?: string;
  family?: string;
  section?: string;
  instanceCount: number;
  supportedTopologies: BcpTopology[];
  defaultTopology: BcpTopology;
  multiInstance: { enabled: boolean; maxPerOrg?: number };
  sso?: { realm?: string; silentLogin?: boolean };
  endpoints?: EndpointDeclaration[];
}

export interface ApplicationSummary {
  id: string;
  name: string;
  blueprint: string;
  org: string;
  topology: BcpTopology;
  status: ApplicationStatus;
  createdAt?: string;
}

export interface PerClusterStatus {
  cluster: string;
  role: ClusterRole;
  status: ClusterStatus;
  hr?: string;
  message?: string;
}

export interface Application extends ApplicationSummary {
  perCluster?: PerClusterStatus[];
  endpoints?: ResolvedEndpoint[];
}

export interface CreateInstanceRequest {
  blueprint: string;
  org: string;
  name: string;
  topology?: BcpTopology;
  values?: Record<string, unknown>;
}

export interface EndpointMutationRequest {
  name: string;
  hostname?: string;
  port?: number;
  protocol?: EndpointProtocol;
  tls?: boolean;
  visibility?: EndpointVisibility;
  ssoEnabled?: boolean;
}

export interface EndpointPR {
  prURL: string;
  status: 'open' | 'merged' | 'closed' | 'failed-precheck';
  preCheckResults?: {
    kyverno?: 'pending' | 'pass' | 'fail';
    certManager?: 'pending' | 'pass' | 'fail';
    dnsConflict?: 'pending' | 'pass' | 'fail';
  };
}

export interface LaunchURL {
  url: string;
  expiresAt: string;
  endpoint?: string;
}

export interface ApiError {
  code: string;
  message: string;
  details?: Record<string, unknown>;
}
