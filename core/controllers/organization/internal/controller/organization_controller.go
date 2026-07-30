// Package controller hosts the Organization reconciler. Per ADR-0001
// §2.1 (Flux is the only deployment path) and §2.2 (Crossplane is
// cloud-only — K8s-to-K8s reconciliation is in-cluster controllers'
// job), the reconciler:
//
//  1. Ensures a Keycloak group exists at /<slug> with attributes
//     {org=[<slug>], tier=[<tier>]}.
//  2. Ensures a Gitea Org exists with username=<slug>.
//  3. Renders + writes a vCluster HelmRelease into the per-Org Gitea
//     repo (auto-created if absent).
//  3b. Find-or-creates the host-cluster Flux GitRepository + Kustomization
//     that reconcile that per-Org repo's ./vcluster tree onto the host
//     cluster (per_org_flux.go, PR #3700 §4.3). This is what makes the
//     vCluster manifest from step 3 actually get APPLIED — previously the
//     design assumed a manual one-off `clusters/<host>/tenants/<slug>/`
//     Kustomization the Sovereign-admin set up by hand, which never ran on
//     a fresh prov, leaving the manifest as orphaned bytes (Pending).
//  4. Writes one UserAccess CR per spec.owners[] entry — slice C5's
//     useraccess-controller materializes the RoleBindings.
//  5. Updates Organization.status with the reconciled IDs/paths +
//     conditions + observedGeneration.
//
// The reconciler is idempotent: running on a steady-state CR is a
// no-op (every "ensure" is a find-or-create with byte-equal short-
// circuit on PutFile).

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	"github.com/openova-io/openova/core/controllers/organization/internal/iacbootstrap"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

// TenantEventPublisher is the narrow publish seam the deletion path uses to
// emit the canonical `tenant.deleted` domain event (#5364). *natsbus.Publisher
// satisfies it in production; unit tests inject a fake. The interface (rather
// than a concrete *natsbus.Publisher field) keeps the Reconciler testable and
// lets main.go leave the field nil on a no-NATS install (Catalyst-Zero), where
// the emit degrades to a logged no-op.
type TenantEventPublisher interface {
	Publish(ctx context.Context, subject string, ev *natsbus.Event) error
}

// userAccessGVR is the CLUSTER-scoped UserAccess CR group/version/kind
// the reconciler writes per Org owner. Defined in
// platform/crossplane-claims/chart/templates/crds/useraccess.yaml as
// access.openova.io/v1alpha1.
//
// NOTE on scope (Refs #4773): UserAccess is a plain CLUSTER-scoped CRD.
// The former Crossplane-Claim coupling (which made the Claim namespaced)
// is gone; more importantly the resource MUST be cluster-scoped because
// the useraccess-controller materializes RoleBindings into the Org's own
// namespace (≠ wherever the CR lives) and ClusterRoleBindings for
// wildcard grants, owning each via an ownerReference — a namespaced
// owner cannot own a cross-namespace RoleBinding (the GC would delete it)
// nor a cluster-scoped ClusterRoleBinding (invalid ownerRef). So the CR
// is written cluster-scoped: NO metadata.namespace, and Get/Create route
// to the cluster path. This mirrors Organization itself being cluster-
// scoped so it can own children across namespaces.
var userAccessGVR = schema.GroupVersionResource{
	Group:    "access.openova.io",
	Version:  "v1alpha1",
	Resource: "useraccesses",
}

// userAccessGVK is the matching kind (used for unstructured.Object).
var userAccessGVK = schema.GroupVersionKind{
	Group:   "access.openova.io",
	Version: "v1alpha1",
	Kind:    "UserAccess",
}

// Reconciler reconciles Organization CRs. The fields are
// dependency-injected from cmd/main.go (live) or controller_test.go
// (fakes). Per the package doc, all "ensure" operations are
// idempotent.
type Reconciler struct {
	client.Client
	Log logr.Logger

	// Keycloak is the Keycloak Admin client (interface for testability).
	Keycloak KeycloakClient

	// TenantEventPublisher publishes the canonical `tenant.deleted` domain
	// event on Org-CR finalizer teardown so the provisioning service's
	// tenant.deleted consumer runs the SECOND half of Org teardown — pruning
	// the `org-tenants` gitops dir (the <slug> Namespace manifest + tenant
	// HelmReleases from the org-tenants Flux Kustomization inventory). Without
	// this, a raw `kubectl delete organization` fires ONLY the org-controller
	// half of teardown (per-Org vCluster Flux + tenant-networking) and never
	// publishes tenant.deleted, so the org-tenants Kustomization perpetually
	// recreates the ns + HRs (#5364). Nil on a no-NATS install (Catalyst-Zero /
	// NATS_URL unset) — the publish then degrades to a logged no-op, matching
	// pre-#5364 behavior. Interface for testability + nil-safety.
	TenantEventPublisher TenantEventPublisher

	// PerOrgRealmEnabled gates the per-Org Keycloak realm auto-wiring
	// (#3084 Part 2). When true, Reconcile find-or-creates a realm named
	// after the Org slug (alongside the always-on Sovereign-realm group)
	// and the finalizer tears it down on Org delete. Defaults to true in
	// production (cmd/main.go reads CATALYST_PER_ORG_REALM_ENABLED, which
	// is unset == enabled, consistent with the always-on group path); the
	// unit-test Reconciler leaves it false unless a test opts in. When
	// false the controller surfaces status.perOrgRealm.state=Disabled.
	PerOrgRealmEnabled bool

	// GiteaClient is the Gitea Admin client.
	GiteaClient *gitea.Client

	// HostCluster is the canonical host-cluster name (e.g.
	// hz-fsn-rtz-prod) — drives the openova.io/host-cluster label and
	// the Organization.status.vcluster.hostCluster field.
	HostCluster string

	// EnvRegionProvider / EnvRegionCode / EnvRegionBuildingBlock seed the
	// single region of the Environment CR the controller auto-ensures for
	// the Org (issue #4077, environment_ensure.go). They only need to be
	// CRD-valid (provider/buildingBlock enums; region `^[a-z]{3}[a-z0-9]?$`)
	// — the Application install pivots through the Blueprint topology +
	// VClusterPlacements, not these region strings, and the real host
	// cluster is named via the explicit `hostCluster` override (=
	// HostCluster). When HostCluster is already a canonical
	// `{provider}-{region}-{bb}-{envType}` name (Hetzner) those segments
	// win; on Huawei (where CATALYST_HOST_CLUSTER is the Sovereign FQDN)
	// these configured values are used. Per Inviolable Principle #4 they
	// are read from env (CATALYST_ENV_REGION_PROVIDER / _CODE /
	// _BUILDING_BLOCK); empty falls back to the canonical huawei/mee/rtz.
	EnvRegionProvider      string
	EnvRegionCode          string
	EnvRegionBuildingBlock string

	// VClusterChartVersion is the vcluster chart SemVer constraint.
	// Per Inviolable Principle #4 it's read from env, never
	// hardcoded.
	VClusterChartVersion string

	// VClusterHelmRepoName / Namespace identify the Flux
	// HelmRepository used to source the vcluster chart. Defaults:
	// loft / vcluster-system (matches clusters/contabo-mkt/tenants/
	// test/).
	VClusterHelmRepoName      string
	VClusterHelmRepoNamespace string

	// VClusterImageRegistry is the Sovereign-local Harbor host the
	// per-Org vCluster images pull through (proxy-cache). Default
	// harbor.openova.io. Per Inviolable Principle #4 it's read from
	// env (CATALYST_VCLUSTER_IMAGE_REGISTRY), never hardcoded — cutover
	// Step-04 flips it to harbor.<sovereign-fqdn>. See gitops.Inputs
	// for the harbor-proxy-pull admission rationale (#3760).
	VClusterImageRegistry string

	// Branch is the Gitea branch the controller writes manifests to.
	// Defaults to "main".
	Branch string

	// GiteaInClusterURL is the in-cluster Gitea base URL the per-Org Flux
	// GitRepository clones from (e.g.
	// http://gitea-http.gitea.svc.cluster.local:3000). Drives the
	// per-Org vCluster Flux loop (per_org_flux.go, PR #3700 §4.3). When
	// empty the loop is skipped (vclusterReadiness keeps reporting
	// Pending) — a unit-test Reconciler that only exercises the
	// Keycloak/Gitea code leaves it empty. Per Inviolable Principle #4
	// it is read from env (CATALYST_GITEA_INCLUSTER_URL), never hardcoded.
	GiteaInClusterURL string

	// FluxNamespace is where the per-Org Flux GitRepository + Kustomization
	// CRs are created. Defaults to "flux-system" (matches the
	// application-controller's HostFluxNamespace + the chart's
	// catalog-sovereign / org-tenants Flux sources).
	FluxNamespace string

	// FluxIntervalSeconds is the reconcile interval stamped on the per-Org
	// Flux GitRepository + Kustomization. Defaults to 60. Per Inviolable
	// Principle #4 it is operator-overridable (env).
	FluxIntervalSeconds int

	// FluxGiteaSecretRef is the name of a Flux basic-auth Secret (in
	// FluxNamespace) holding the Gitea PAT the per-Org GitRepository uses
	// to clone (bp-gitea sets REQUIRE_SIGNIN_VIEW=true → anonymous clone
	// 401s). Empty == anonymous, matching the application-controller's
	// FluxGiteaSecretRef default.
	FluxGiteaSecretRef string

	// FederationSecretNamespace is the K8s namespace where the
	// `spec.identity.federationConfig.clientSecretRef` Secret lives.
	// Defaults to "catalyst-controllers" (the namespace the controller
	// itself runs in). Per Inviolable Principle #5, the operator's
	// secret-handling story is "put the SealedSecret in the controller's
	// namespace; the controller never reaches across namespaces" —
	// keeps RBAC scope minimal.
	FederationSecretNamespace string

	// UserAccessNamespace is the namespace where the per-Org UserAccess
	// Claim CRs are written. Defaults to "catalyst-system" — the
	// Sovereign-wide IAM namespace per
	// products/catalyst/chart/templates/qa-fixtures/useraccess-qa-user1.yaml.
	// Per Inviolable Principle #4 (never hardcode), the deployment env
	// CATALYST_USERACCESS_NAMESPACE overrides this for non-canonical
	// installs.
	UserAccessNamespace string

	// IacBootstrapGitea is the narrow GiteaClient seam the
	// iacbootstrap package uses. Production wires the same
	// `*gitea.Client` as `GiteaClient` above; tests inject a fake. Nil
	// disables the bootstrap flow (the Reconcile loop surfaces
	// state=Disabled).
	IacBootstrapGitea iacbootstrap.GiteaClient

	// IacBootstrapTokens is the per-Org robot-token store (OpenBao
	// KV-v2 in production). Nil disables the bootstrap flow.
	IacBootstrapTokens iacbootstrap.TokenStore

	// ──────────────────────────────────────────────────────────────
	// Per-pool console-TLS wiring (issue #4075).
	//
	// When an Organization picks a free-subdomain hostname under a pool
	// parent zone (e.g. `console.<slug>.omani.homes`), the Cilium
	// console Gateway needs (a) a `*.<parentDomain>` TLS Secret to
	// terminate on and (b) a listener bound to that hostname+Secret.
	// Neither exists out of the box — the bootstrap-rendered console
	// Gateway only carries the apex `*.<sovFQDN>` listener + cert. The
	// result is ERR_CONNECTION_CLOSED on the customer-Org console URL
	// (no SNI match → no cert → TLS handshake closed), which blocked
	// the agentic customer journey (#4075). reconcileTenantConsoleTLS
	// closes that gap by issuing a cert-manager Certificate per pool
	// parent and appending a matching listener to the console Gateway.
	// All values flow through env per Inviolable Principle #4.

	// ConsoleGatewayName / ConsoleGatewayNamespace identify the
	// dedicated console Cilium Gateway (#4053 blast-radius isolation)
	// the per-Org console HTTPRoute attaches to and the per-pool
	// `*.<parentDomain>` listener is appended to. Defaults:
	// cilium-gateway-console / kube-system (matches
	// clusters/_template/sovereign-tls/cilium-gateway-console.yaml).
	ConsoleGatewayName      string
	ConsoleGatewayNamespace string

	// ConsoleTLSClusterIssuer is the cert-manager ClusterIssuer that
	// solves the per-pool `*.<parentDomain>` Certificate. It MUST be a
	// DNS-01 issuer whose solver covers the pool parent zone (a
	// wildcard SAN cannot be issued via HTTP-01). Default:
	// letsencrypt-dns01-prod-powerdns (the PowerDNS DNS-01 ClusterIssuer
	// every Sovereign installs via bp-cert-manager-powerdns-webhook).
	ConsoleTLSClusterIssuer string

	// ConsoleTLSCertNamespace is the namespace the per-pool wildcard
	// Certificate + Secret are written to. MUST equal the console
	// Gateway's namespace so the Gateway's certificateRefs (Secret,
	// same-namespace by default) resolve without a ReferenceGrant.
	// Default: kube-system.
	ConsoleTLSCertNamespace string

	// ── Per-Org console HTTPRoute backends (#4186) ────────────────────────
	// The per-Org console host (console.<slug>.<pool>) is served by the
	// catalyst-ui SPA + catalyst-api (the SAME Sovereign console + API the
	// operator front door uses), NOT by an Org-namespace product backend.
	// The route + these Services live in ConsoleRouteNamespace (catalyst-
	// system) so the backendRefs resolve same-namespace without a
	// ReferenceGrant — matching the hand-applied `issue-4075-live-unblock`
	// route the funnel previously needed. All env-overridable (Principle #4).

	// ConsoleRouteNamespace is the namespace the per-Org console HTTPRoute is
	// written to. MUST be the namespace where catalyst-api + catalyst-ui
	// Services live so backendRefs resolve same-namespace. Default:
	// catalyst-system.
	ConsoleRouteNamespace string
	// ConsoleAPIService / ConsoleAPIPort — catalyst-api Service (the auth
	// handover + /api/ + /catalyst/ backend). Default catalyst-api:8080.
	ConsoleAPIService string
	ConsoleAPIPort    int32
	// ConsoleUIService / ConsoleUIPort — catalyst-ui Service (the SPA catch-
	// all `/` backend). Default catalyst-ui:80.
	ConsoleUIService string
	ConsoleUIPort    int32

	// ── Per-Org pool-DNS A-record write (issue #4236) ─────────────────────────
	// The org-controller writes `console.<slug>.<parentDomain>` +
	// `*.<slug>.<parentDomain>` A-records to the CENTRAL PowerDNS so a fresh
	// marketplace-funnel signup's console host resolves (the #4179 final layer:
	// #4222's pool-DNS writer was wired only into the catalyst-api BSS pipeline,
	// which the marketplace funnel never runs). See tenant_dns.go. All values
	// flow through env per Inviolable Principle #4 — no hardcoded endpoint/IP.

	// PoolPowerDNSURL / PoolPowerDNSAPIKey identify the CENTRAL PowerDNS
	// (https://pdns.openova.io) authoritative for the shared omani.* subdomain
	// pool. The key is the same central credential #4218 reflects into
	// catalyst-system as `pool-powerdns-api-credentials` (the org-controller runs
	// in catalyst-system too, so it reads that bridged secret directly). Empty →
	// the pool-DNS write is a logged no-op (single-PowerDNS / Catalyst-Zero).
	// Env: CATALYST_POOL_POWERDNS_API_URL / CATALYST_POOL_POWERDNS_API_KEY.
	PoolPowerDNSURL    string
	PoolPowerDNSAPIKey string

	// PoolPowerDNSSecretName / PoolPowerDNSSecretNamespace let the reconciler
	// read the central pool key LIVE at reconcile time when the env-frozen
	// PoolPowerDNSAPIKey is empty. This is the #4290/#4179 self-heal: the env
	// var is a `secretKeyRef ... optional:true` that the kubelet binds ONCE at
	// Pod start. On a fresh prov the org-controller Pod almost always starts
	// BEFORE bp-reflector has filled `catalyst-system/pool-powerdns-api-credentials`
	// from the source `cert-manager/powerdns-api-credentials` (the reflector
	// depends on the sovereign-tls Kustomization seeding the source first, then a
	// reconcile cycle). With the env frozen empty and NOTHING rolling the Pod when
	// the secret later lands, the writer stays `have_key:false` PERMANENTLY — the
	// exact defect this fixes. Reading the Secret live each reconcile makes the
	// per-Org pool A-record write self-heal zero-touch the moment the key lands,
	// with no Pod restart. Env: CATALYST_POOL_POWERDNS_SECRET_NAME (default
	// `pool-powerdns-api-credentials`) read from the controller's own namespace
	// (CATALYST_POOL_POWERDNS_SECRET_NAMESPACE / POD_NAMESPACE, default
	// `catalyst-system`). The Secret's `api-key` key carries the value.
	PoolPowerDNSSecretName      string
	PoolPowerDNSSecretNamespace string

	// PoolPowerDNSSourceSecretName / PoolPowerDNSSourceSecretNamespace name the
	// reflector SOURCE secret the bridged `pool-powerdns-api-credentials`
	// destination is filled FROM (`cert-manager/powerdns-api-credentials`, the
	// ${POWERDNS_API_KEY} central key bp-cert-manager-powerdns-webhook uses for
	// DNS-01). #4475 §2 — the LAST-RESORT self-heal: bp-reflector only re-emits
	// into the destination ON a reconcile cycle AFTER the source exists, and on a
	// fresh prov the source is created LATE (cert-manager DNS-01 solver) — the
	// reflector logged `Source could not be found` on its first pass and then
	// never re-scanned the target after the source appeared, so the bridged
	// DESTINATION stays empty and `console.<slug>.<pool>` stays NXDOMAIN even
	// though the SOURCE key is sitting in cert-manager. Reading the SOURCE secret
	// directly when the destination is empty sidesteps the reflector entirely:
	// the moment cert-manager creates the source, the very next Org reconcile
	// resolves the key and writes the A-records — zero-touch, no reflector re-emit
	// and no Pod roll. Env: CATALYST_POOL_POWERDNS_SOURCE_SECRET_NAME (default
	// `powerdns-api-credentials`) / CATALYST_POOL_POWERDNS_SOURCE_SECRET_NAMESPACE
	// (default `cert-manager`). The Secret's `api-key` key carries the value.
	PoolPowerDNSSourceSecretName      string
	PoolPowerDNSSourceSecretNamespace string

	// PoolPowerDNSHTTPClient overrides the HTTP client used for the central-pdns
	// PATCH (tests inject a client pointed at an httptest server). Nil → a 30s
	// default client.
	PoolPowerDNSHTTPClient httpDoer

	// TenantConsoleLBIPv4 is the dedicated console-ELB EIP (#4053) the per-Org
	// console host resolves to (e.g. 212.72.24.33) — sourced from the
	// sovereign-fqdn ConfigMap `consoleLBIP` key the chart renders from
	// global.sovereignConsoleLBIP (ultimately tofu output console_load_balancer_ip).
	// Env: CATALYST_TENANT_CONSOLE_LB_IPV4.
	TenantConsoleLBIPv4 string
	// TenantPrimaryLBIPv4 is the primary/shared LB IP used as a fallback target
	// when no dedicated console LB exists (single-LB / pre-#4053 Sovereign), so
	// the record still resolves. Env: CATALYST_OTECH_INGRESS_IPV4 (the same key
	// catalyst-api's tenant DNS provisioner reads).
	TenantPrimaryLBIPv4 string
}

// httpDoer is the narrow HTTP seam the pool-DNS writer needs — *http.Client
// satisfies it; tests inject a stub pointed at an httptest server.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// SetupWithManager registers the reconciler with the controller-runtime
// manager. The manager's scheme must already have orgapi registered.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&orgapi.Organization{}).
		Complete(r)
}

// Reconcile is the controller-runtime entry point. It is called for
// every event on Organization CRs.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("organization", req.Name)
	log.Info("reconcile")

	var org orgapi.Organization
	if err := r.Get(ctx, req.NamespacedName, &org); err != nil {
		if apierrors.IsNotFound(err) {
			// CR was deleted — nothing to do here. The iac-bootstrap
			// finalizer runs in the deletion-timestamp branch below;
			// by the time the CR has fully tombstoned the finalizer
			// has already been removed.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get organization: %w", err)
	}

	// Deletion path (G117 W2.C3): if the CR has been marked for
	// deletion AND still carries the iac-bootstrap finalizer, run
	// iacbootstrap.Teardown then drop the finalizer so the CR can
	// tombstone. Per the K8s finalizer pattern, returning early here
	// before the Keycloak/Gitea ensure steps prevents recreating
	// artifacts we're about to delete.
	if !org.DeletionTimestamp.IsZero() {
		// #5364 — emit the canonical `tenant.deleted` event so the provisioning
		// service's tenant.deleted consumer runs the SECOND half of Org teardown:
		// pruning the `org-tenants` gitops dir (the <slug> Namespace manifest +
		// tenant HelmReleases from the org-tenants Flux Kustomization inventory).
		// The teardowns below are only the FIRST half (per-Org vCluster Flux +
		// tenant-networking); the org-tenants dir is owned by the provisioning
		// service and is pruned ONLY on a tenant.deleted event. Before this fix a
		// raw `kubectl delete organization` never published tenant.deleted (only
		// tenant-service's DELETE /api/organizations/{id} route did), so the
		// org-tenants Kustomization perpetually recreated the ns + HRs.
		//
		// Published FIRST (before dropping any finalizer) + BEST-EFFORT: a nil
		// publisher (no NATS) or a publish failure logs + continues — it NEVER
		// blocks finalizer removal, so a NATS hiccup cannot wedge the CR in
		// Terminating. We are unambiguously in the delete path (DeletionTimestamp
		// is set), which is the only guard the consumer's idempotent git prune
		// needs against a day-2-reinstall race. A duplicate publish across
		// finalizer-requeue passes is harmless (the consumer prune is idempotent).
		r.publishTenantDeleted(ctx, &org)

		// Per-Org vCluster Flux teardown (PR #3700 §4.3). The Flux CR pair
		// carries NO ownerRef (cross-namespace GC trap), so the GC won't
		// reap it — delete explicitly. Best-effort + absent-as-success +
		// no-op when GiteaInClusterURL is unset. Run first so Flux stops
		// reconciling (and prune:true GCs the vCluster) before the other
		// teardowns remove the Gitea Org / Keycloak realm.
		if err := r.teardownPerOrgFlux(ctx, &org); err != nil {
			log.Error(err, "per-org-flux teardown")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		// Per-Org tenant-networking teardown (#4459). The console Gateway
		// listener pair, the per-Org wildcard Certificate (+ its TLS Secret),
		// the per-Org console HTTPRoute, and the central-PowerDNS pool
		// A-records the up-path created all live OUTSIDE the Flux-pruned
		// `<slug>` namespace (the listener is on the SHARED console Gateway in
		// kube-system; the Certificate is in kube-system; the HTTPRoute is in
		// catalyst-system; the DNS records are off-cluster) and carry NO
		// ownerRef the GC follows — so without an explicit teardown they leak
		// on every Org delete (dead listeners ACCUMULATE on the shared
		// Gateway; a stale A-record points a re-prov at a dead console IP).
		// Gated on the same engaged-feature signal as the up-path (a pool
		// parentDomain), best-effort + absent-as-success, so a re-reconcile is
		// idempotent. Run before dropping the finalizers so the CR still holds
		// while we clean up.
		if err := r.teardownTenantNetworking(ctx, &org); err != nil {
			log.Error(err, "tenant-networking teardown")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		// Drop the tenant-networking finalizer once its cascade has run. This
		// finalizer is what GUARANTEES the cascade fires (it holds the CR even
		// when iac-bootstrap + per-Org-realm are unwired); removing it lets the
		// CR proceed toward tombstone once its artifacts are gone.
		if containsFinalizer(org.Finalizers, TenantNetworkingFinalizer) {
			if removeTenantNetworkingFinalizer(&org) {
				if uerr := r.Update(ctx, &org); uerr != nil {
					return ctrl.Result{}, fmt.Errorf("remove tenant-networking finalizer: %w", uerr)
				}
				// Update changed resourceVersion; requeue so the next reconcile
				// operates on the latest copy before touching the other finalizers.
				return ctrl.Result{Requeue: true}, nil
			}
		}
		if containsFinalizer(org.Finalizers, IacBootstrapFinalizer) {
			if err := r.teardownIacBootstrap(ctx, &org); err != nil {
				log.Error(err, "iac-bootstrap teardown")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			if removeIacBootstrapFinalizer(&org) {
				if uerr := r.Update(ctx, &org); uerr != nil {
					return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", uerr)
				}
				// Update changed resourceVersion; requeue so the next
				// reconcile operates on the latest copy before dropping
				// the per-Org-realm finalizer.
				return ctrl.Result{Requeue: true}, nil
			}
		}
		// Per-Org realm teardown (#3084 Part 2): delete the realm AFTER
		// the iac-bootstrap teardown so the auth boundary is the last
		// artifact removed. DeleteRealm is absent-as-success.
		if containsFinalizer(org.Finalizers, PerOrgRealmFinalizer) {
			if err := r.teardownPerOrgRealm(ctx, &org); err != nil {
				log.Error(err, "per-org-realm teardown")
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			if removePerOrgRealmFinalizer(&org) {
				if uerr := r.Update(ctx, &org); uerr != nil {
					return ctrl.Result{}, fmt.Errorf("remove per-org-realm finalizer: %w", uerr)
				}
			}
		}
		return ctrl.Result{}, nil
	}

	// Drift check: a CR's slug field must match the resource name
	// (the CRD enforces this on admission, but a manual `kubectl
	// edit` could diverge). Surface as a Failed condition rather than
	// silently re-creating downstream artifacts under the new slug.
	if org.Spec.Slug != org.Name {
		return r.fail(ctx, &org, "SlugMetadataMismatch",
			fmt.Sprintf("spec.slug=%q != metadata.name=%q (CRs are immutable on slug after admission)",
				org.Spec.Slug, org.Name))
	}

	// Add the iac-bootstrap finalizer on first observation so a
	// subsequent delete can trigger iacbootstrap.Teardown. Only persists
	// when wiring is present (we don't add a finalizer for a flow that
	// will never run).
	if r.IacBootstrapGitea != nil && r.IacBootstrapTokens != nil {
		if ensureIacBootstrapFinalizer(&org) {
			if err := r.Update(ctx, &org); err != nil {
				return ctrl.Result{}, fmt.Errorf("add iac-bootstrap finalizer: %w", err)
			}
			// Update changes resourceVersion; the next reconcile will
			// pick up the latest copy via the informer.
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// Add the per-Org-realm finalizer on first observation (when the
	// feature is enabled) so a subsequent delete triggers DeleteRealm
	// (#3084 Part 2). Same requeue-after-add pattern as iac-bootstrap.
	if r.PerOrgRealmEnabled {
		if ensurePerOrgRealmFinalizer(&org) {
			if err := r.Update(ctx, &org); err != nil {
				return ctrl.Result{}, fmt.Errorf("add per-org-realm finalizer: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// Add the tenant-networking finalizer on first observation when the Org
	// engages the tenant-networking up-path (a pool parentDomain → console TLS
	// + Gateway listener + HTTPRoute + pool DNS). This GUARANTEES the cascade
	// teardown (#4459) fires on delete even when iac-bootstrap + per-Org-realm
	// are unwired — those would otherwise be the only finalizers holding the CR
	// long enough for the deletion branch to run. Same requeue-after-add pattern.
	if orgEngagesTenantNetworking(&org) {
		if ensureTenantNetworkingFinalizer(&org) {
			if err := r.Update(ctx, &org); err != nil {
				return ctrl.Result{}, fmt.Errorf("add tenant-networking finalizer: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// ── Console-serving FIRST (issue #4999) ─────────────────────────────
	// The per-Org customer console (console.<slug>.<parentDomain>) is HOW THE
	// OWNER SIGNS IN — zero-click /auth/org-handover → catalyst_session cookie →
	// /jobs. Per the tenant_route.go design intent it MUST come up as soon as the
	// Org has a public hostname, INDEPENDENT of every app-provisioning step below
	// (Keycloak realm, Gitea, per-Org Flux, Federation, the #4991/#4992
	// provisioning-RBAC / vcluster-mirror gap) — any of which can `r.fail(...)`.
	// Before #4999 the DNS + TLS + HTTPRoute trio ran at the END of Reconcile, so
	// an earlier fatal step aborted BEFORE the console route was ever written and
	// the customer's /auth/org-handover 503'd (the hw240 2nd-Org walk-stranger-two
	// symptom: the 1st Org converged fully so its route landed, the 2nd tripped an
	// earlier step and never got a console route). Running the trio FIRST +
	// best-effort (log + requeue, never block) guarantees every funnel Org can
	// sign in even while its purchased app is still converging or has failed.
	// No-op when spec.tenantPublic.parentDomain is empty.
	consoleServingDegraded := r.reconcileConsoleServing(ctx, &org)

	// 1. Keycloak group (in the Sovereign realm).
	kcAttrs := map[string][]string{
		"org":  {org.Spec.Slug},
		"tier": {org.Spec.Tier},
	}
	kcID, kcPath, kcRealm, err := r.Keycloak.EnsureGroup(ctx, "/"+org.Spec.Slug, kcAttrs)
	if err != nil {
		return r.fail(ctx, &org, "KeycloakGroupFailed", err.Error())
	}

	// 1b. Per-Org Keycloak realm (#3084 Part 2). Find-or-create a realm
	// named after the Org slug so Tier-3 per-Org SSO has its own
	// isolation boundary (distinct from the Sovereign-realm group above).
	// The realm is auth-critical — a hard failure fails the whole
	// reconcile so it requeues (unlike iac-bootstrap which is non-fatal).
	// When the feature flag is off this returns State=Disabled / ok=true.
	realmStatus, realmOK := r.reconcilePerOrgRealm(ctx, &org)
	if !realmOK {
		return r.fail(ctx, &org, "PerOrgRealmFailed", realmStatus.LastError)
	}

	// 2. Gitea Org.
	gOrg, err := r.GiteaClient.EnsureOrg(ctx,
		org.Spec.Slug,
		org.Spec.DisplayName,
		fmt.Sprintf("Catalyst Organization %s on Sovereign %s",
			org.Spec.Slug, org.Spec.SovereignRef),
		"private")
	if err != nil {
		return r.fail(ctx, &org, "GiteaOrgFailed", err.Error())
	}

	// 3. vCluster HelmRelease in the per-Org Gitea repo. The repo is
	// the Org-level "shared-blueprints" repo per NAMING §11.2 — every
	// Org has at least one repo where Catalyst manifests land. We
	// auto-create it if absent.
	const repoName = "catalyst-tenant"
	if _, err := r.GiteaClient.EnsureRepo(ctx,
		gOrg.Username, repoName,
		"vCluster + tenant manifests reconciled by Flux on the host cluster",
		true); err != nil {
		return r.fail(ctx, &org, "GiteaRepoFailed", err.Error())
	}

	manifests, err := gitops.Render(gitops.Inputs{
		Slug:                      org.Spec.Slug,
		DisplayName:               org.Spec.DisplayName,
		Tier:                      org.Spec.Tier,
		PlanSlug:                  org.Spec.PlanSlug,
		SovereignFQDN:             org.Spec.SovereignRef,
		HostCluster:               r.HostCluster,
		VClusterChartVersion:      r.VClusterChartVersion,
		VClusterHelmRepoName:      r.VClusterHelmRepoName,
		VClusterHelmRepoNamespace: r.VClusterHelmRepoNamespace,
		VClusterImageRegistry:     r.VClusterImageRegistry,
	})
	if err != nil {
		return r.fail(ctx, &org, "ManifestRenderFailed", err.Error())
	}

	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	for path, data := range manifests {
		// #4384 — the funnel/day-2 cart install commits the customer's
		// purchased Applications into THIS repo's `vcluster/apps/` tree and
		// read-modify-writes `vcluster/apps/kustomization.yaml` to enumerate
		// them (alongside the NP boundary baseline). If the controller
		// kept overwriting that index with its baseline-only list on every
		// reconcile, it would CLOBBER the funnel's app entries → Flux's
		// `kustomize build ./vcluster/apps` would drop the customer's apps and
		// the wordpress HelmRelease would never land.
		//
		// #4993 — `vcluster/host-apps/kustomization.yaml` has the same
		// contract: the funnel merges the host-native per-app HTTPRoutes
		// (app-<x>-hostroute.yaml, the durable fix for the vcluster-tier 404,
		// since loft vcluster 0.33.4 syncs no httproutes) into that index
		// alongside the CNP + provisioning-rbac baseline.
		//
		// #5104 — both indexes are RECONCILING MERGE-WRITES now, not the former
		// seed-only skip. Pure seed-only left the #4992 vcluster target-ns
		// `namespace.yaml` structurally dead for every vcluster-tier funnel
		// Org: the funnel committed its merged index FIRST (without
		// namespace.yaml — its merge didn't know the doc), the controller then
		// saw the index present and never re-added its baseline entry, while
		// still committing the (now orphaned) namespace.yaml file on every
		// reconcile → Flux wedged permanently on `namespaces "<slug>" not
		// found` and the purchased app never deployed (hw255, 2/2 Orgs). The
		// merge unions the controller's rendered baseline INTO the existing
		// index, NEVER pruning funnel entries, strips #4567-class stale
		// entries (a CNP listed in apps/), and skips the write when the
		// resource set is already complete — steady state stays write-free and
		// already-wedged Orgs self-heal on their next reconcile.
		if path == "vcluster/apps/kustomization.yaml" ||
			path == "vcluster/host-apps/kustomization.yaml" {
			existing, gerr := r.GiteaClient.GetFile(ctx, gOrg.Username, repoName, branch, path)
			switch {
			case gerr == nil:
				existingBytes, derr := existing.Decoded()
				if derr != nil {
					return r.fail(ctx, &org, "GitopsWriteFailed",
						fmt.Sprintf("decode %s: %s", path, derr))
				}
				merge := gitops.MergeHostAppsKustomizationIndex
				if path == "vcluster/apps/kustomization.yaml" {
					merge = gitops.MergeAppsKustomizationIndex
				}
				merged, changed := merge(string(existingBytes), string(data))
				if !changed {
					// Index already carries every baseline entry (and nothing
					// to strip) — leave the funnel-authored bytes untouched.
					continue
				}
				data = []byte(merged)
			case errors.Is(gerr, gitea.ErrFileNotFound):
				// Genuinely absent → fall through to seed the rendered index.
			default:
				return r.fail(ctx, &org, "GitopsWriteFailed",
					fmt.Sprintf("probe %s: %s", path, gerr))
			}
		}
		if _, _, err := r.GiteaClient.PutFile(ctx,
			gOrg.Username, repoName, branch, path, data,
			fmt.Sprintf("organization-controller: reconcile %s for %s", path, org.Spec.Slug)); err != nil {
			// #5305 — a concurrent writer (the funnel cart-install, the
			// authoritative read-modify-write owner of the shared
			// `vcluster/apps/kustomization.yaml` index) landed a commit on the
			// SAME branch HEAD between our read and our write, so this PutFile lost
			// the compare-and-swap. That is the funnel CONVERGING — the desired end
			// state — not a controller error. Failing the whole reconcile here
			// re-ran the ENTIRE PutFile burst on the requeue, re-perturbing the
			// branch HEAD and starving the funnel's finite compare-and-swap budget
			// (the purchased app never landed — hw282). Soft-skip the lost CAS: the
			// next reconcile re-verifies (by which point the funnel's index is
			// visible and the merge is a byte-equal no-op). A genuine write failure
			// (auth, malformed manifest, repo gone) still fails the reconcile.
			if isConcurrentWriterCASLoss(err) {
				log.Info("per-Org repo write lost the compare-and-swap to a concurrent funnel commit — soft-skipping so the funnel (the shared-index owner) converges instead of being re-bursted over (#5305)",
					"organization", org.Spec.Slug, "path", path)
				continue
			}
			return r.fail(ctx, &org, "GitopsWriteFailed",
				fmt.Sprintf("write %s: %s", path, err))
		}
	}

	// 3b. Per-Org vCluster Flux loop (PR #3700 §4.3 — the deferred half of
	// #3687). Find-or-create the host-cluster Flux GitRepository +
	// Kustomization that reconcile the per-Org `catalyst-tenant` repo's
	// ./vcluster tree onto the host cluster — so the vCluster HelmRelease we
	// just PUT to Gitea is actually APPLIED, not left as orphaned bytes.
	// Without this, vclusterReadiness() reports phase=Pending forever (no
	// Flux source watches the repo). Non-fatal: a Flux-CR write failure is
	// requeued (the manifest already landed in Gitea) rather than failing
	// the whole Org reconcile. Skipped (no-op) when GiteaInClusterURL is
	// unset — see per_org_flux.go.
	if err := r.reconcilePerOrgFlux(ctx, &org); err != nil {
		return r.fail(ctx, &org, "PerOrgFluxFailed", err.Error())
	}

	// 4. UserAccess per owner. The CR is cluster-scoped (per the
	// existing XRD) so the name is deterministic from
	// (slug, sanitized-email).
	for _, o := range org.Spec.Owners {
		if err := r.upsertUserAccess(ctx, &org, o); err != nil {
			return r.fail(ctx, &org, "UserAccessFailed", err.Error())
		}
	}

	// 5. Federation IdP wire-up (slice F2). When
	// `spec.identity.federationProvider` is set, reconcile the per-Org
	// Keycloak IdP + claim mappers into the Sovereign realm. When empty,
	// best-effort delete any stray IdP for the Org's deterministic
	// alias (handles the "Org dropped federation" path).
	idpCond, mappersCond, fedErr := r.reconcileFederation(ctx, &org)
	if fedErr != nil {
		return r.fail(ctx, &org, idpCond.Reason, fedErr.Error())
	}

	// 5a/5b. Console-serving trio (pool DNS + console TLS + console HTTPRoute)
	// moved to the TOP of Reconcile — see reconcileConsoleServing / the
	// `consoleServingDegraded` call above (issue #4999). Landing it here (after
	// the vCluster/Gitea/Flux/Federation steps that can `r.fail(...)`) meant a
	// funnel Org that tripped any earlier step never got a console route → the
	// owner's /auth/org-handover 503'd. It now runs first + best-effort so the
	// customer can always sign in; a transient failure is folded into the
	// end-of-Reconcile requeue via consoleServingDegraded.

	// 5c. Per-Org IaC repo bootstrap (G117.3 / W2.C3 — ADR-0009).
	// Provisions the canonical `<slug>/iac` repo, the `<slug>-iac-bot`
	// robot user, branch protection on main, and the OpenBao-backed
	// robot token. Idempotent: a steady-state Org produces zero Gitea
	// writes. Failure is non-fatal for the Org's other outputs — we
	// surface the lastError on status.iacBootstrap and rely on the
	// 30s requeue (Ready failure path) to retry.
	iacStatus := r.reconcileIacBootstrap(ctx, &org)
	if iacStatus.State == "Failed" {
		log.Error(errors.New("iac-bootstrap"), iacStatus.LastError,
			"organization", org.Name)
	}

	// 6. Status update. #3687 (fold #3669): the vCluster phase + the
	// Ready condition derive from a LIVE readback of the vCluster
	// HelmRelease the controller wrote (+ the per-Org Namespace), NOT
	// from "I PUT the file." The old code hardcoded Phase=Provisioning
	// and Ready=True the instant the Gitea PutFile returned — reporting a
	// green Org over orphaned bytes when no Flux source watched the repo.
	// Now Ready=True requires the HR to actually be Ready AND the
	// namespace to exist; until then it sits False with a reason naming
	// what is pending, and the reconcile requeues so the status converges
	// as Flux brings the vCluster up.
	//
	// #4339 (Refs #4292): the readback is TIER-AWARE. A host-tier Org
	// (""/s/free — the same gitops.BoundaryIsVcluster gate that decides the
	// boundary primitive) renders NO vcluster HR, so there is nothing to
	// wait on — the host `<slug>` namespace IS the boundary. Gating those
	// Orgs on a `vcluster` HR that is correctly never authored wedged them
	// at Ready=False:VClusterProvisioning forever, so ensureEnvironment
	// never ran and the apps-install phase was never reached. We pass the
	// plan slug through so vclusterReadiness can short-circuit to host-ns
	// readiness for host-tier and only wait on the HR for the vcluster tier.
	vcPhase, vcReady, vcMsg := r.vclusterReadiness(ctx, org.Spec.Slug, org.Spec.PlanSlug)

	// 6a. Auto-ensure the parent Environment CR (issue #4077, Refs #3687)
	// once the vCluster can actually host Applications. Without this, every
	// app install into the Org wedges at Ready=False:EnvironmentMissing
	// because nothing on the Org-onboarding path ever created the
	// `<slug>-<envType>` Environment the application-controller resolves.
	// Gated on vcReady so the Environment appears exactly when the Org is
	// host-ready, never over a vCluster that isn't up. Idempotent
	// (find-or-create, no spec overwrite). A failure requeues via fail()
	// rather than silently dropping the gap — the app install depends on it.
	if vcReady {
		if err := r.ensureEnvironment(ctx, &org); err != nil {
			return r.fail(ctx, &org, "EnvironmentEnsureFailed", err.Error())
		}
	}

	// 6b. POSTCONDITION VERIFICATION (#5395). vclusterReadiness above reads back
	// exactly ONE of the artifacts this reconciler authors (the vCluster HR, or
	// for a host-tier Org merely the `<slug>` namespace). Everything else — the
	// plan ResourceQuota + LimitRange authored into the per-Org repo, and the
	// per-Org console Gateway listener pair — was fire-and-forget: nothing ever
	// read it back, and reconcileConsoleServing's only output (`degraded`) fed a
	// requeue and never reached status. So an Org could be missing its console
	// listener (every HTTPRoute Accepted=False NoMatchingListenerHostname → the
	// customer's console, mail, and apps all unreachable) or its purchased plan
	// cap, and STILL report phase=Ready + Ready=True on every surface. Live on
	// hw290: gamma-corp, 6/6 routes unrouted, Org green.
	//
	// verifyProvisioned re-reads what the controller claims to have provisioned.
	// A proven-absent artifact holds BOTH the phase and the Ready condition back
	// — the phase matters because the console derives an Org's "Active" badge
	// from status.vcluster.phase FIRST and only falls through to the Ready
	// condition (products/catalyst/bootstrap/api/internal/handler/org_list_from_cr.go
	// orgStateFromCR), so downgrading the condition alone would have left the
	// operator-visible state green.
	//
	// Deliberately NOT a retry: a retry would keep re-appending the listener
	// while the Org still read Active, so a permanent failure would stay
	// invisible. Naming the missing artifact is the fix; the requeue below is
	// the self-heal that follows from it — and because a non-empty result keeps
	// the requeue alive, this also closes the drift hole where a Ready Org
	// (which returns ctrl.Result{} and has no Gateway watch) could lose its
	// listener to an external rewrite and never re-converge.
	//
	// ensureEnvironment above stays gated on vcReady, NOT on this: an Org with a
	// broken console edge must still get its Environment, or a cosmetic status
	// fix would become a functional regression for the app-install path.
	postconditions := provisioningPostconditions{}
	if vcReady {
		postconditions = r.verifyProvisioned(ctx, &org)
		for _, u := range postconditions.Unverifiable {
			// NOT evidence of absence (RBAC not yet rolled out, apiserver blip,
			// CRD absent on a Catalyst-Zero install). Log + requeue; never
			// fabricate a red from a failed probe — one botched RBAC rollout
			// would otherwise red-flag every Org on the Sovereign at once.
			log.Info("provisioning postcondition could not be verified (not treated as missing) — requeueing",
				"organization", org.Spec.Slug, "probe", u)
		}
		if !postconditions.complete() {
			log.Info("Organization is NOT fully provisioned — holding Ready back (#5395)",
				"organization", org.Spec.Slug, "missing", postconditions.Missing)
			// The console reads Active off the phase first — hold it too.
			vcPhase = "Provisioning"
		}
	}

	readyCond := orgapi.Condition{
		Type:               "Ready",
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	switch {
	case vcReady && !postconditions.complete():
		readyCond.Status = "False"
		readyCond.Reason = "ProvisioningIncomplete"
		readyCond.Message = postconditions.message()
	case vcReady:
		readyCond.Status = "True"
		readyCond.Reason = "Reconciled"
		// #4813 (status honesty): word the Ready message off the SAME
		// #4292/#4339 tier gate vclusterReadiness used, so it names the
		// boundary that ACTUALLY backs the Org. A host-tier (""/s/free) Org
		// authors NO vCluster HelmRelease — its host `<slug>` namespace IS the
		// boundary — so asserting "vCluster HelmRelease Ready" for it is a
		// false green over absent backing (Ready-over-absent-backing
		// anti-pattern, cf. #3687 / #856). Only the vcluster tier
		// (m/l/xl/flexi) authors + waits on the HR, so only it may claim it.
		readyCond.Message = readyOrgMessage(org.Spec.PlanSlug)
	default:
		readyCond.Status = "False"
		// #5502 (Refs #4813/#4292/#4339): name the pending reason off the SAME
		// tier gate vclusterReadiness + readyOrgMessage use. vcMsg was already
		// tier-aware here; the machine-readable Reason was not, so a host-tier
		// Org waiting on its host NAMESPACE reported VClusterProvisioning and
		// sent the diagnosis hunting a vCluster that is correctly never
		// authored for that tier.
		readyCond.Reason = pendingBoundaryReason(org.Spec.PlanSlug)
		readyCond.Message = vcMsg
	}
	desired := orgapi.OrganizationStatus{
		VCluster: vclusterStatusFor(org.Spec.Slug, r.HostCluster, org.Spec.PlanSlug, vcPhase),
		KeycloakGroup: orgapi.KeycloakGroupStatus{
			ID:    kcID,
			Path:  kcPath,
			Realm: kcRealm,
		},
		PerOrgRealm: realmStatus,
		GiteaOrg: orgapi.GiteaOrgStatus{
			Name:  gOrg.Username,
			Repos: []string{repoName},
		},
		IacBootstrap: iacStatus,
		Conditions: []orgapi.Condition{
			readyCond,
			idpCond,
			mappersCond,
			iacBootstrapCondition(iacStatus),
			perOrgRealmCondition(realmStatus),
		},
		ObservedGeneration: org.Generation,
	}
	if err := r.patchStatus(ctx, &org, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	log.Info("reconcile ok",
		"keycloak_group", kcID,
		"gitea_org", gOrg.Username,
		"vcluster", org.Spec.Slug,
		"vcluster_phase", vcPhase,
		"vcluster_ready", vcReady)

	// Requeue until the vCluster HR is Ready so status.vcluster.phase +
	// the Ready condition converge as Flux applies the Git-authored
	// manifest. A steady-state (Ready) Org needs no periodic requeue —
	// the Organization watch re-triggers on spec changes; the HR's own
	// Flux reconcile drives the cluster state. While provisioning we poll
	// the readback every 30s (matching the fail() cadence).
	// Requeue while the vCluster is still coming up OR the console-serving trio
	// (#4999) reported a transient failure — so the console DNS/TLS/HTTPRoute
	// retry on the 30s cadence even for an Org that is otherwise Ready (a
	// host-tier Org goes Ready immediately, so without this a transient console
	// route write would sit un-retried until the next spec change).
	//
	// #5395 — ALSO requeue while any provisioning postcondition is missing or
	// could not be verified. This is what makes the check self-healing rather
	// than merely diagnostic: each pass re-runs reconcileConsoleServing (which
	// re-appends a listener that was never written or was rewritten away) and
	// re-reads the boundary tree, and the Org converges to Ready=True the moment
	// the artifacts are genuinely there. Without it a Ready Org returns
	// ctrl.Result{} and — since SetupWithManager registers no Gateway watch —
	// would never look again.
	if !vcReady || consoleServingDegraded ||
		!postconditions.complete() || len(postconditions.Unverifiable) > 0 {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// publishTenantDeleted best-effort emits the canonical `tenant.deleted` domain
// event so the provisioning service's tenant.deleted consumer runs the SECOND
// half of Org teardown — pruning the `org-tenants` gitops dir (the <slug>
// Namespace manifest + tenant HelmReleases). See the Reconcile deletion-branch
// comment + the TenantEventPublisher field doc for the split-teardown root
// cause (#5364).
//
// Contract (all enforced here so callers stay a single line):
//   - Nil publisher (no NATS / Catalyst-Zero) → silent no-op: the Org-delete
//     path degrades to its pre-#5364 behavior, never a wedge.
//   - The payload carries {id, slug} both set to the Org slug. The consumer
//     keys its teardown off `slug` (a tenant UUID is not required); `id` is set
//     non-empty so the envelope matches what tenant-service emits.
//   - The publish is BOUNDED (5s ctx) and BEST-EFFORT: a marshal error, a nil
//     typed publisher, a broker stall, or a Nak all log a warning and RETURN —
//     the finalizer removal in the caller proceeds regardless, so a NATS hiccup
//     can never strand the CR in Terminating. The consumer's git prune is
//     idempotent, so the next tenant.deleted (or a live re-walk) reconverges.
func (r *Reconciler) publishTenantDeleted(ctx context.Context, org *orgapi.Organization) {
	if r.TenantEventPublisher == nil {
		// No NATS wired (NATS_URL unset) — degrade to pre-#5364 behavior. The
		// org-controller half of teardown still runs; only the org-tenants prune
		// (which requires the bus) is skipped, exactly as before this fix.
		return
	}
	slug := org.Spec.Slug
	if slug == "" {
		slug = org.Name
	}
	ev, err := natsbus.NewEvent("tenant.deleted", "organization-controller", slug,
		map[string]string{
			// The provisioning consumer unmarshals {id, slug} and keys teardown
			// off slug (consumer.go handleTenantDeleted). id is set to the slug so
			// the envelope is non-empty + shape-compatible with tenant-service.
			"id":   slug,
			"slug": slug,
		})
	if err != nil {
		r.Log.Error(err, "tenant.deleted: build event failed — skipping publish (finalizer proceeds; #5364)",
			"slug", slug)
		return
	}
	// Bound the wait so a broker stall cannot hang the delete. On timeout/err we
	// log + continue — the finalizer must not wedge on a NATS hiccup.
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.TenantEventPublisher.Publish(pubCtx, natsbus.SubjectTenantDeleted, ev); err != nil {
		r.Log.Error(err, "tenant.deleted: publish failed — org-tenants gitops prune deferred (finalizer still proceeds; consumer prune is idempotent on the next tenant.deleted / re-walk; #5364)",
			"slug", slug, "subject", natsbus.SubjectTenantDeleted)
		return
	}
	r.Log.Info("tenant.deleted published — provisioning consumer will prune the org-tenants gitops dir (ns + tenant HelmReleases) (#5364)",
		"slug", slug, "subject", natsbus.SubjectTenantDeleted)
}

// reconcileConsoleServing brings up the per-Org customer console EDGE — the
// pool DNS A-record (console.<slug>.<parent> + *.<slug>.<parent>), the console
// Gateway per-Org TLS listener+cert, and the console HTTPRoute (which wires
// /auth/org-handover → catalyst-api and / → catalyst-ui) — for an Org that has
// a public pool hostname (spec.tenantPublic.parentDomain). It is called FIRST in
// Reconcile (issue #4999) so the owner can SIGN IN regardless of the
// app-provisioning steps' outcome. All three are best-effort: a failure is
// logged and returned as degraded=true so the caller requeues, but NEVER aborts
// the reconcile — the console edge must not gate on, nor be gated by, the
// vCluster/app pipeline. DNS is ordered first so the name resolves the moment
// the cert/route land. Every step is a no-op when parentDomain is empty (the Org
// has no public hostname yet → it is reached via the Sovereign-wide
// `*.<sovFQDN>` tenant-wildcard route), so degraded stays false.
func (r *Reconciler) reconcileConsoleServing(ctx context.Context, org *orgapi.Organization) (degraded bool) {
	log := r.Log.WithValues("organization", org.Name)
	if _, err := r.reconcileTenantDNS(ctx, org); err != nil {
		log.Error(err, "tenant pool-DNS reconcile (transient — requeue)")
		degraded = true
	}
	if _, err := r.reconcileTenantConsoleTLS(ctx, org); err != nil {
		log.Error(err, "tenant console TLS reconcile (transient — requeue)")
		degraded = true
	}
	if _, err := r.reconcileTenantRoute(ctx, org); err != nil {
		log.Error(err, "tenant console HTTPRoute reconcile (transient — requeue)")
		degraded = true
	}
	return degraded
}

// readyOrgMessage words the Organization's Ready=True condition message off the
// SAME #4292/#4339 tier gate that decides the boundary primitive
// (gitops.BoundaryIsVcluster) + that vclusterReadiness keys off. This keeps the
// human-readable status HONEST about what actually backs the Org (#4813):
//
//   - vcluster tier (m/l/xl/flexi): a real vCluster HelmRelease was authored +
//     is Ready, so the message names it.
//   - host tier (""/s/free): NO vCluster HR is ever authored — the host `<slug>`
//     namespace IS the boundary. vclusterReadiness returns Ready as soon as that
//     namespace is Active (no HR to wait on), so claiming "vCluster HelmRelease
//     Ready" here would be a false green over absent backing (the
//     Ready-over-absent-backing anti-pattern flagged in #4813, cf. #3687 / #856).
//     The message instead names the host namespace boundary that truly exists.
func readyOrgMessage(planSlug string) string {
	if gitops.BoundaryIsVcluster(planSlug) {
		return "vCluster HelmRelease Ready + Keycloak group + Gitea Org reconciled"
	}
	return "host namespace Active + Keycloak group + Gitea Org reconciled (namespace-isolated tier — no vCluster authored)"
}

// pendingBoundaryReason names the artifact a not-yet-Ready Org is ACTUALLY
// waiting on, keyed off the same #4292/#4339 tier gate
// (gitops.BoundaryIsVcluster) that vclusterReadiness and readyOrgMessage use.
//
// #4813 made the Ready=True *message* tier-aware for exactly this reason, but
// the Ready=False *Reason* stayed hardcoded to "VClusterProvisioning". Reason
// is the machine-readable field — it is what `kubectl get org -o jsonpath`,
// operators, and tooling filter on — so a host-tier (""/s/free) Org, which
// authors NO vCluster HelmRelease and whose host `<slug>` namespace IS the
// boundary, reported that it was waiting on a vCluster.
//
// Observed cost (#5502): on hw291 the Org `uatcorp` (plan=s → namespace by
// design) sat at Ready=False:VClusterProvisioning, and the acceptance walk
// duly went hunting — `kubectl get vclusters -A` returned "No resources
// found" in both regions and vcluster StatefulSets were 0, all of which is
// the CORRECT state for that tier. The real missing artifact was the
// `uatcorp` namespace. The honest explanation was already sitting in the
// message; the Reason contradicted it.
//
// The vcluster tier keeps the original string, so nothing that legitimately
// waits on a vCluster changes.
func pendingBoundaryReason(planSlug string) string {
	if gitops.BoundaryIsVcluster(planSlug) {
		return "VClusterProvisioning"
	}
	return "NamespaceProvisioning"
}

// vclusterStatusFor returns the status.vcluster block to stamp on the
// Organization, keyed off the SAME #4292/#4339 tier gate
// (gitops.BoundaryIsVcluster) that decides whether a vCluster is authored at
// all (#5489):
//
//   - vcluster tier (m/l/xl/flexi): the controller authors a real vCluster
//     HelmRelease, so the block carries name + hostCluster + the phase
//     vclusterReadiness derived — exactly the pre-#5489 shape.
//   - host tier (""/s/free): NO vCluster is ever authored. The old
//     unconditional stamp wrote status.vcluster{name, hostCluster, phase:
//     Ready} over that absence, and the CRD printer column
//     (products/catalyst/chart/crds/organization.yaml, .status.vcluster.phase)
//     surfaced it as `vCluster: Ready` on `kubectl get organizations -o wide`
//     — a fabricated object beside an honest Ready message (#4813 fixed the
//     message; the field kept lying). The zero value serializes as an
//     empty/absent block, which the printer column renders as blank — the
//     honest representation of "no vCluster exists for this Org".
//
// The console directory is unaffected: orgStateFromCR (bootstrap api,
// org_list_from_cr.go) reads phase first and falls through to the Ready
// condition, which this controller still stamps tier-honestly via
// readyOrgMessage.
func vclusterStatusFor(slug, hostCluster, planSlug, vcPhase string) orgapi.VClusterStatus {
	if !gitops.BoundaryIsVcluster(planSlug) {
		return orgapi.VClusterStatus{}
	}
	return orgapi.VClusterStatus{
		Name:        slug,
		HostCluster: hostCluster,
		Phase:       vcPhase,
	}
}

// vclusterReadiness reads back the vCluster HelmRelease the controller
// authored (name "vcluster" in namespace <slug>, matching the
// gitops.vclusterTemplate) plus the per-Org Namespace, and derives the
// reported phase + a Ready bool + a human message (#3687 fold #3669).
//
// Phase ladder:
//   - "Ready"        — the HR's Ready condition is True AND the namespace
//     exists. Only here does the Org go Ready=True.
//   - "Provisioning" — the HR exists but is not yet Ready (Flux applied it,
//     the vCluster pod is still coming up), OR the HR's
//     Ready condition is explicitly False/unknown.
//   - "Pending"      — neither the HR nor the namespace is visible yet:
//     the manifest was written to Git but no Flux source
//     has applied it (the "orphaned bytes" case the old
//     code masked as Ready). Surfaced honestly so the
//     operator sees the gap instead of a false green.
//
// Keys only off slug + HostCluster (both on the CR) → generic for any
// Org/host, no per-org-name special-casing (#3687 §7d). Read failures
// other than NotFound degrade to Provisioning (transient API blips
// shouldn't flip a healthy Org to Pending) and requeue.
//
// #4339 (Refs #4292): TIER-AWARE. For a host-tier Org (""/s/free — same
// gitops.BoundaryIsVcluster gate that decides the boundary primitive) the
// controller authors NO vcluster HR; the host `<slug>` namespace IS the
// boundary. So host-tier readiness keys solely off the namespace existing —
// waiting on a `vcluster` HR that is correctly never rendered would wedge the
// Org at Ready=False forever and never reach the apps-install phase. Only the
// vcluster tier (m/l/xl/flexi) reads back + waits on the HR.
func (r *Reconciler) vclusterReadiness(ctx context.Context, slug, planSlug string) (phase string, ready bool, message string) {
	nsExists := false
	ns := &corev1.Namespace{}
	switch err := r.Get(ctx, types.NamespacedName{Name: slug}, ns); {
	case err == nil:
		nsExists = true
	case apierrors.IsNotFound(err):
		nsExists = false
	default:
		return "Provisioning", false,
			fmt.Sprintf("vCluster namespace readback error (transient): %s", err)
	}

	// Host-tier boundary: no vcluster HR is ever authored (gitops.Render omits
	// vcluster.yaml from ./vcluster for ""/s/free). Readiness = the host ns is
	// Active. The phase string stays "Ready"/"Pending" so the existing status
	// projection + the vcReady-gated ensureEnvironment / apps phase proceed.
	if !gitops.BoundaryIsVcluster(planSlug) {
		if nsExists {
			return "Ready", true, ""
		}
		return "Pending", false,
			"host-tier Org namespace not yet Active (no vCluster HR is authored for this tier; the host namespace is the boundary)"
	}

	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	switch err := r.Get(ctx, types.NamespacedName{Namespace: slug, Name: "vcluster"}, hr); {
	case apierrors.IsNotFound(err):
		// No HR in the cluster yet. The manifest was PUT to the per-Org
		// Gitea repo and the per-Org Flux GitRepository + Kustomization
		// were created (step 3b / per_org_flux.go) — this is the transient
		// window before Flux has cloned the repo and applied ./vcluster.
		// Still surfaced honestly as Pending (not a false green) so the
		// operator sees the vCluster is not up yet; the 30s requeue walks
		// it to Provisioning → Ready as Flux converges.
		return "Pending", false,
			"vCluster HelmRelease not yet applied by Flux (manifest authored in Gitea + per-Org Flux source created; awaiting first reconcile of ./vcluster)"
	case err != nil:
		return "Provisioning", false,
			fmt.Sprintf("vCluster HelmRelease readback error (transient): %s", err)
	}

	hrReady := helmReleaseReady(hr)
	if hrReady && nsExists {
		return "Ready", true, ""
	}
	if !nsExists {
		return "Provisioning", false,
			"vCluster HelmRelease present but the per-Org namespace is not Active yet"
	}
	return "Provisioning", false,
		"vCluster HelmRelease applied; awaiting its Ready condition (vCluster pod still coming up)"
}

// helmReleaseReady reports whether a Flux HelmRelease's `Ready` condition
// is True. Flux v2 sets status.conditions[type=Ready].status to
// "True"/"False"/"Unknown" once it has attempted a reconcile.
func helmReleaseReady(hr *unstructured.Unstructured) bool {
	conds, found, err := unstructured.NestedSlice(hr.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, ci := range conds {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		t, _, _ := unstructured.NestedString(c, "type")
		if t != "Ready" {
			continue
		}
		s, _, _ := unstructured.NestedString(c, "status")
		return s == "True"
	}
	return false
}

// fail records a Failed condition + non-zero observedGeneration so the
// operator console can surface the error. Returns a controller-runtime
// requeue so transient failures (network blips) self-heal.
func (r *Reconciler) fail(ctx context.Context, org *orgapi.Organization, reason, message string) (ctrl.Result, error) {
	r.Log.Error(errors.New(reason), message,
		"organization", org.Name,
		"slug", org.Spec.Slug)
	st := orgapi.OrganizationStatus{
		Conditions: []orgapi.Condition{
			{
				Type:               "Ready",
				Status:             "False",
				Reason:             reason,
				Message:            message,
				LastTransitionTime: metav1.NewTime(time.Now()),
			},
		},
		ObservedGeneration: org.Generation,
	}
	// Best-effort patch — if it fails we still want the requeue.
	_ = r.patchStatus(ctx, org, st)
	// Drift errors don't requeue (they need operator action). Other
	// reasons do.
	if reason == "SlugMetadataMismatch" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) patchStatus(ctx context.Context, org *orgapi.Organization, desired orgapi.OrganizationStatus) error {
	// #5305 — CONVERGE-IDEMPOTENT status write (the root-cause fix). This
	// reconciler watches the Organization with SetupWithManager's DEFAULT
	// (all-events) filter, so a Status().Update re-enqueues THIS reconcile from
	// its own output. The Ready condition stamped LastTransitionTime=now() on
	// every pass, so `desired` ALWAYS differed from the live status → the
	// reconciler wrote status → re-reconciled → wrote again, hot-looping
	// sub-second the whole time the Org provisioned. Each pass re-ran step-3's
	// per-Org `catalyst-tenant` repo PutFile burst (including the shared
	// `vcluster/apps/kustomization.yaml` merge-write), turning the controller
	// into a CONTINUOUS competing writer on the branch HEAD — one the funnel
	// cart-install's finite compare-and-swap budget could never out-run, so the
	// purchased app never landed (hw282). Carry forward each condition's
	// LastTransitionTime when its (Status,Reason,Message) is unchanged, then SKIP
	// the write entirely when the resulting status is byte-equal to the live one.
	// The reconciler now re-fires only on a real state change or the explicit 30s
	// provisioning requeue, so its per-Org repo writes collapse to a FINITE seed
	// burst the funnel's retry budget straddles.
	desired.Conditions = carryForwardConditionTimestamps(org.Status.Conditions, desired.Conditions)
	if reflect.DeepEqual(org.Status, desired) {
		return nil
	}
	updated := org.DeepCopyObject().(*orgapi.Organization)
	updated.Status = desired
	return r.Status().Update(ctx, updated)
}

// carryForwardConditionTimestamps returns `desired` with each condition's
// LastTransitionTime replaced by the matching LIVE condition's timestamp when
// the condition has NOT transitioned (same Status/Reason/Message). A genuine
// transition keeps the freshly-stamped time. Without this the Ready condition's
// unconditional now() stamp made every reconcile's status differ from the live
// one, defeating the byte-equal skip in patchStatus and hot-looping the
// controller into a continuous branch writer (#5305).
func carryForwardConditionTimestamps(live, desired []orgapi.Condition) []orgapi.Condition {
	byType := make(map[string]orgapi.Condition, len(live))
	for _, c := range live {
		byType[c.Type] = c
	}
	out := make([]orgapi.Condition, len(desired))
	for i, d := range desired {
		if prev, ok := byType[d.Type]; ok &&
			prev.Status == d.Status && prev.Reason == d.Reason && prev.Message == d.Message {
			d.LastTransitionTime = prev.LastTransitionTime
		}
		out[i] = d
	}
	return out
}

// isConcurrentWriterCASLoss reports whether a per-Org repo PutFile error is a
// transient compare-and-swap loss to a concurrent writer on the same branch
// HEAD (the funnel cart-install racing the controller's #5104 index merge-write)
// rather than a permanent failure. Gitea surfaces the loss as a 409 (git
// ref-lock / CAS) or a 422 whose body names the file-level precondition failure
// ("sha does not match" on an update, "already exists" on a create). #5305.
func isConcurrentWriterCASLoss(err error) bool {
	if err == nil {
		return false
	}
	var he *gitea.HTTPError
	if errors.As(err, &he) {
		if he.Status == http.StatusConflict {
			return true
		}
		if he.Status == http.StatusUnprocessableEntity {
			b := strings.ToLower(he.Body)
			return strings.Contains(b, "exist") ||
				strings.Contains(b, "does not match") ||
				strings.Contains(b, "cannot lock ref")
		}
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "sha does not match") ||
		strings.Contains(s, "cannot lock ref") ||
		strings.Contains(s, "repository file already exists")
}

// upsertUserAccess writes (or updates) a single-grant UserAccess CR
// scoped to the Organization. The CR shape mirrors
// platform/crossplane-claims/chart/tests/fixtures/useraccess-sample.yaml.
//
// The CR is CLUSTER-scoped (Refs #4773) — no metadata.namespace — because
// the useraccess-controller materializes the RoleBinding into the Org's
// own namespace (≠ where the CR lives) and owns it via an ownerReference;
// a namespaced owner cannot own a cross-namespace RoleBinding (the GC
// would delete it). See the top-of-file note on userAccessGVR for the
// full ownerRef rationale.
//
// Slice C5 (useraccess-controller) consumes the CR and materialises the
// RoleBindings.
func (r *Reconciler) upsertUserAccess(ctx context.Context, org *orgapi.Organization, owner orgapi.OrganizationOwner) error {
	// Sanitize the email into a label-safe leaf: "ceo@acme.com" →
	// "ceo-at-acme-com". Keeps the CR name deterministic + RFC 1123-
	// compliant.
	emailLeaf := sanitizeEmail(owner.Email)
	name := fmt.Sprintf("%s-%s", org.Spec.Slug, emailLeaf)

	// Map Org owner role to UserAccess role. Per the unified design
	// §6.2 "owner" is the highest tier; the existing XRD uses the
	// admin/editor/viewer naming. Map owner→admin, admin→admin,
	// developer→editor, viewer→viewer.
	role := mapOwnerToUARole(owner.Role)

	desired := unstructured.Unstructured{}
	desired.SetGroupVersionKind(userAccessGVK)
	desired.SetName(name)
	// No SetNamespace: UserAccess is cluster-scoped (Refs #4773).
	desired.SetLabels(map[string]string{
		"openova.io/organization":      org.Spec.Slug,
		"openova.io/sovereign":         org.Spec.SovereignRef,
		"openova.io/managed-by":        "organization-controller",
		"app.kubernetes.io/managed-by": "catalyst",
	})
	// OwnerReferences only work cluster-internally; safe across
	// namespaces because Organization CRs are cluster-scoped.
	desired.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: orgapi.GroupVersion.String(),
			Kind:       "Organization",
			Name:       org.Name,
			UID:        org.UID,
			Controller: ptrBool(true),
		},
	})
	desired.Object["spec"] = map[string]any{
		"user": map[string]any{
			"keycloakSubject": owner.Email,
		},
		"sovereignRef": org.Spec.Slug, // per-Org scope; per-Sovereign scope is the parent
		"applications": []any{
			map[string]any{
				"app":  org.Spec.Slug, // Org-level grant; namespaces resolve to the vCluster ns
				"role": role,
				"namespaces": []any{
					org.Spec.Slug,
				},
			},
		},
	}

	// Find-or-create using the runtime client. We use unstructured so
	// the controller doesn't need a generated-types dependency on the
	// access.openova.io group (which lives under platform/crossplane-
	// claims/ today).
	current := unstructured.Unstructured{}
	current.SetGroupVersionKind(userAccessGVK)
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get UserAccess %s: %w", name, err)
		}
		// Create.
		if err := r.Create(ctx, &desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("create UserAccess %s: %w", name, err)
		}
		return nil
	}
	// Update: copy desired spec onto current (preserves resourceVersion).
	current.Object["spec"] = desired.Object["spec"]
	current.SetLabels(desired.GetLabels())
	if err := r.Update(ctx, &current); err != nil {
		return fmt.Errorf("update UserAccess %s: %w", name, err)
	}
	return nil
}

// sanitizeEmail converts an email into a DNS-label-safe leaf. The
// transformation is reversible by an operator reading the CR name
// (the leaf is informational; the authoritative email is on
// spec.user.keycloakSubject).
func sanitizeEmail(email string) string {
	out := strings.ToLower(email)
	out = strings.ReplaceAll(out, "@", "-at-")
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, "+", "-plus-")
	out = strings.ReplaceAll(out, "_", "-")
	// Trim to leave room for the "{slug}-" prefix in a 253-char DNS
	// name. 200 is comfortably under the bound.
	if len(out) > 200 {
		out = out[:200]
	}
	// Strip leading/trailing dashes (RFC 1123).
	out = strings.Trim(out, "-")
	return out
}

// mapOwnerToUARole maps Organization.spec.owners[].role → UserAccess
// applications[].role per the existing XRD's role enum.
func mapOwnerToUARole(role string) string {
	switch strings.ToLower(role) {
	case "owner", "admin":
		return "admin"
	case "developer":
		return "editor"
	case "viewer":
		return "viewer"
	default:
		return "viewer"
	}
}

func ptrBool(b bool) *bool { return &b }

// federationAlias returns the deterministic per-Org IdP alias.
// Convention (per F2 brief): `<provider>-<slug>` — e.g.
// "azure-sso-acme". Two Orgs federating to the same upstream Azure AD
// tenant get distinct realm-side IdP instances so claim-mapper config
// + first-broker-login flow stay isolated per-Org.
func federationAlias(provider, slug string) string {
	return fmt.Sprintf("%s-%s", provider, slug)
}

// reconcileFederation is the F2 wire-up. Returns the two status
// conditions (IdentityProviderConfigured + IdentityProviderClaimMappersConfigured)
// and a non-nil error iff the reconcile failed in a way that should
// requeue. On the empty-federation path it best-effort deletes any
// stray IdP from a previous federation config and returns Ready=False
// with reason=NoFederation (which the access-matrix UI renders as a
// neutral "—" cell, not an error).
func (r *Reconciler) reconcileFederation(ctx context.Context, org *orgapi.Organization) (orgapi.Condition, orgapi.Condition, error) {
	now := metav1.NewTime(time.Now())
	provider := strings.TrimSpace(org.Spec.Identity.FederationProvider)
	if provider == "" {
		// No federation requested — best-effort delete any stray IdP
		// for THIS Org's deterministic aliases. We try every supported
		// provider's alias to handle the "operator switched provider
		// then dropped it" path.
		for _, p := range []string{"azure-sso", "okta", "generic-oidc"} {
			alias := federationAlias(p, org.Spec.Slug)
			if err := r.Keycloak.DeleteIdentityProvider(ctx, alias); err != nil {
				// Best-effort: log + continue. The ErrIdentityProviderNotFound
				// case is already swallowed by the LiveKeycloak impl.
				r.Log.V(1).Info("federation cleanup: delete idp non-fatal",
					"alias", alias, "err", err.Error())
			}
		}
		return orgapi.Condition{
				Type:               "IdentityProviderConfigured",
				Status:             "False",
				Reason:             "NoFederation",
				Message:            "spec.identity.federationProvider is empty — no IdP configured",
				LastTransitionTime: now,
			},
			orgapi.Condition{
				Type:               "IdentityProviderClaimMappersConfigured",
				Status:             "False",
				Reason:             "NoFederation",
				Message:            "no IdP → no claim mappers",
				LastTransitionTime: now,
			},
			nil
	}

	cfg := org.Spec.Identity.FederationConfig
	alias := federationAlias(provider, org.Spec.Slug)

	// Resolve client secret from the K8s Secret named in
	// clientSecretRef. Per Inviolable Principle #5 the plaintext value
	// stays in memory only long enough to be PUT/POSTed onto Keycloak.
	clientSecret, err := r.readClientSecret(ctx, cfg.ClientSecretRef)
	if err != nil {
		return orgapi.Condition{
				Type:               "IdentityProviderConfigured",
				Status:             "False",
				Reason:             "SecretMissing",
				Message:            fmt.Sprintf("read clientSecretRef: %s", err),
				LastTransitionTime: now,
			},
			orgapi.Condition{
				Type:               "IdentityProviderClaimMappersConfigured",
				Status:             "False",
				Reason:             "SecretMissing",
				Message:            "secret unavailable",
				LastTransitionTime: now,
			},
			fmt.Errorf("federation: read client secret: %w", err)
	}

	// Build the IdentityProvider representation. ProviderID is "oidc"
	// for every supported provider in F2 — Azure-AD-specific UX (the
	// "microsoft" provider) can layer on later without changing the
	// reconciler shape.
	displayName := fmt.Sprintf("%s — %s", org.Spec.DisplayName, provider)
	idp := KCIdentityProvider{
		Alias:                     alias,
		DisplayName:               displayName,
		ProviderID:                "oidc",
		Enabled:                   true,
		FirstBrokerLoginFlowAlias: "first broker login",
		Config:                    buildIdPConfig(cfg, clientSecret),
	}

	if err := r.Keycloak.EnsureIdentityProvider(ctx, idp); err != nil {
		return orgapi.Condition{
				Type:               "IdentityProviderConfigured",
				Status:             "False",
				Reason:             "KCUnreachable",
				Message:            fmt.Sprintf("EnsureIdentityProvider: %s", err),
				LastTransitionTime: now,
			},
			orgapi.Condition{
				Type:               "IdentityProviderClaimMappersConfigured",
				Status:             "False",
				Reason:             "KCUnreachable",
				Message:            "IdP not yet reconciled",
				LastTransitionTime: now,
			},
			fmt.Errorf("federation: ensure idp: %w", err)
	}

	idpReason := providerReason(provider)
	idpCond := orgapi.Condition{
		Type:               "IdentityProviderConfigured",
		Status:             "True",
		Reason:             idpReason,
		Message:            fmt.Sprintf("IdP %q reconciled in realm", alias),
		LastTransitionTime: now,
	}

	// Reconcile claim mappers. Empty list is fine — Keycloak's defaults
	// for `email` / `name` / `given_name` still fire.
	for _, cm := range cfg.ClaimMappers {
		if cm.Src == "" || cm.Dest == "" {
			continue
		}
		mapper := KCIdentityProviderMapper{
			Name:                   fmt.Sprintf("%s-to-%s", cm.Src, sanitizeMapperName(cm.Dest)),
			IdentityProviderAlias:  alias,
			IdentityProviderMapper: "oidc-user-attribute-idp-mapper",
			Config: map[string]string{
				"claim":          cm.Src,
				"user.attribute": cm.Dest,
				"syncMode":       "INHERIT",
			},
		}
		if err := r.Keycloak.EnsureIdentityProviderMapper(ctx, alias, mapper); err != nil {
			return idpCond,
				orgapi.Condition{
					Type:               "IdentityProviderClaimMappersConfigured",
					Status:             "False",
					Reason:             "KCUnreachable",
					Message:            fmt.Sprintf("EnsureIdentityProviderMapper %q: %s", mapper.Name, err),
					LastTransitionTime: now,
				},
				fmt.Errorf("federation: ensure mapper %q: %w", mapper.Name, err)
		}
	}

	mappersCond := orgapi.Condition{
		Type:   "IdentityProviderClaimMappersConfigured",
		Status: "True",
		Reason: idpReason,
		Message: fmt.Sprintf("%d claim mapper(s) reconciled on IdP %q",
			len(cfg.ClaimMappers), alias),
		LastTransitionTime: now,
	}
	return idpCond, mappersCond, nil
}

// providerReason maps the provider enum to the canonical condition
// reason string. Stable across reconciles so the access-matrix UI can
// branch on Reason.
func providerReason(provider string) string {
	switch provider {
	case "azure-sso":
		return "AzureSSOConfigured"
	case "okta":
		return "OktaConfigured"
	case "generic-oidc":
		return "OIDCConfigured"
	default:
		return "OIDCConfigured"
	}
}

// readClientSecret fetches the OIDC client secret from the K8s Secret
// named in `clientSecretRef`. Per Inviolable Principle #5 the value
// stays in memory only long enough to be handed to the Keycloak client.
func (r *Reconciler) readClientSecret(ctx context.Context, ref orgapi.OrganizationClientSecretRefSpec) (string, error) {
	if ref.Name == "" {
		return "", errors.New("federationConfig.clientSecretRef.name is empty")
	}
	key := ref.Key
	if key == "" {
		key = "client-secret"
	}
	ns := r.FederationSecretNamespace
	if ns == "" {
		ns = "catalyst-controllers"
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &sec); err != nil {
		return "", fmt.Errorf("get Secret %s/%s: %w", ns, ref.Name, err)
	}
	val, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("Secret %s/%s missing key %q", ns, ref.Name, key)
	}
	if len(val) == 0 {
		return "", fmt.Errorf("Secret %s/%s key %q is empty", ns, ref.Name, key)
	}
	return string(val), nil
}

// buildIdPConfig assembles the Keycloak IdP `config` map from the
// Organization's federationConfig + the resolved client secret. Per
// Keycloak's quirk every value is stringified (booleans → "true"/"false").
//
// The `tenantId` field is surfaced as `openova.tenantId` (a Catalyst-
// specific marker key) so operator-debug can tag which tenant a given
// IdP is bound to without conflicting with Keycloak's own config.
func buildIdPConfig(cfg orgapi.OrganizationFederationConf, clientSecret string) map[string]string {
	out := map[string]string{
		"clientId":          cfg.ClientID,
		"clientSecret":      clientSecret,
		"clientAuthMethod":  "client_secret_post",
		"defaultScope":      "openid profile email",
		"validateSignature": "true",
		"useJwksUrl":        "true",
		"syncMode":          "IMPORT",
	}
	if cfg.AuthorizationURL != "" {
		out["authorizationUrl"] = cfg.AuthorizationURL
	}
	if cfg.TokenURL != "" {
		out["tokenUrl"] = cfg.TokenURL
	}
	if cfg.JwksURL != "" {
		out["jwksUrl"] = cfg.JwksURL
	}
	if cfg.Issuer != "" {
		out["issuer"] = cfg.Issuer
	}
	if cfg.TenantID != "" {
		out["openova.tenantId"] = cfg.TenantID
	}
	return out
}

// sanitizeMapperName trims the Catalyst attribute name to a Keycloak-
// safe mapper-name suffix. Keycloak accepts spaces and slashes in
// mapper names but the access-matrix UI renders them more cleanly when
// hyphenated.
func sanitizeMapperName(s string) string {
	out := strings.ToLower(s)
	out = strings.ReplaceAll(out, "/", "-")
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, " ", "-")
	out = strings.ReplaceAll(out, ":", "-")
	out = strings.Trim(out, "-")
	return out
}
