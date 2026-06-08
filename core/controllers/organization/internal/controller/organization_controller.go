// Package controller hosts the Organization reconciler. Per ADR-0001
// §2.1 (Flux is the only deployment path) and §2.2 (Crossplane is
// cloud-only — K8s-to-K8s reconciliation is in-cluster controllers'
// job), the reconciler:
//
//  1. Ensures a Keycloak group exists at /<slug> with attributes
//     {org=[<slug>], tier=[<tier>]}.
//  2. Ensures a Gitea Org exists with username=<slug>.
//  3. Renders + writes a vCluster HelmRelease into the per-Org Gitea
//     repo (auto-created if absent). Flux on the host cluster picks up
//     the manifest from the per-Org `clusters/<host>/tenants/<slug>/`
//     Kustomization (a one-off, set up by the Sovereign-admin once).
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
)

// userAccessGVR is the namespace-scoped UserAccess CR group/version/kind
// the reconciler writes per Org owner. Defined in
// platform/crossplane-claims/chart/templates/xrds/useraccess.yaml as
// access.openova.io/v1alpha1.
//
// NOTE on scope: Crossplane Claims are namespace-scoped by convention
// (the underlying XR — XUserAccess — is cluster-scoped, but the *Claim*
// the controller writes is namespaced). The runtime CRD on a live
// Sovereign therefore reports `spec.scope: Namespaced`, and a Get/Create
// without a namespace fails with `an empty namespace may not be set
// when a resource name is provided`. Reconciler.UserAccessNamespace
// (env CATALYST_USERACCESS_NAMESPACE, default `catalyst-system`)
// matches the qa-fixtures convention at
// products/catalyst/chart/templates/qa-fixtures/useraccess-qa-user1.yaml
// — the Sovereign-wide namespace where every Sovereign-IAM CR lives.
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

	// Branch is the Gitea branch the controller writes manifests to.
	// Defaults to "main".
	Branch string

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
		SovereignFQDN:             org.Spec.SovereignRef,
		HostCluster:               r.HostCluster,
		VClusterChartVersion:      r.VClusterChartVersion,
		VClusterHelmRepoName:      r.VClusterHelmRepoName,
		VClusterHelmRepoNamespace: r.VClusterHelmRepoNamespace,
	})
	if err != nil {
		return r.fail(ctx, &org, "ManifestRenderFailed", err.Error())
	}

	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	for path, data := range manifests {
		if _, _, err := r.GiteaClient.PutFile(ctx,
			gOrg.Username, repoName, branch, path, data,
			fmt.Sprintf("organization-controller: reconcile %s for %s", path, org.Spec.Slug)); err != nil {
			return r.fail(ctx, &org, "GitopsWriteFailed",
				fmt.Sprintf("write %s: %s", path, err))
		}
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

	// 5b. Per-tenant public-hostname HTTPRoute (issue #1629 follow-up).
	// When `spec.tenantPublic.parentDomain` is set, render a Gateway-API
	// HTTPRoute attaching `<subdomain>.<parentDomain>` to the supplied
	// backend Service on the canonical cilium-gateway. No-op when the
	// field is empty — Orgs that don't yet have a public hostname keep
	// working via the Sovereign-wide `*.<sovFQDN>` tenant-wildcard
	// route. Failure is non-fatal for the Org's other reconciliation
	// outputs (Keycloak group + Gitea Org + vCluster manifests already
	// landed) so we requeue instead of marking the whole Org Failed.
	if _, err := r.reconcileTenantRoute(ctx, &org); err != nil {
		return r.fail(ctx, &org, "TenantRouteFailed", err.Error())
	}

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

	// 6. Status update — Ready=True plus the per-step federation
	// conditions (always present so the access-matrix UI can render
	// the federation column without conditional logic).
	desired := orgapi.OrganizationStatus{
		VCluster: orgapi.VClusterStatus{
			Name:        org.Spec.Slug,
			HostCluster: r.HostCluster,
			Phase:       "Provisioning", // Flux still has to spin up the HelmRelease
		},
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
			{
				Type:               "Ready",
				Status:             "True",
				Reason:             "Reconciled",
				Message:            "vCluster HelmRelease + Keycloak group + Gitea Org reconciled",
				LastTransitionTime: metav1.NewTime(time.Now()),
			},
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
		"vcluster", org.Spec.Slug)
	return ctrl.Result{}, nil
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
	updated := org.DeepCopyObject().(*orgapi.Organization)
	updated.Status = desired
	return r.Status().Update(ctx, updated)
}

// upsertUserAccess writes (or updates) a single-grant UserAccess Claim
// CR scoped to the Organization. The CR shape mirrors
// platform/crossplane-claims/chart/tests/fixtures/useraccess-sample.yaml.
//
// The CR is written into r.UserAccessNamespace (default
// `catalyst-system`) per the qa-fixtures convention at
// products/catalyst/chart/templates/qa-fixtures/useraccess-qa-user1.yaml.
// Crossplane Claims are namespace-scoped on the live API server (the
// underlying XR is cluster-scoped) — Get/Create without a namespace
// fails with `an empty namespace may not be set when a resource name is
// provided`. See organization_controller.go top-of-file note on
// userAccessGVR for the full rationale.
//
// Slice C5 (useraccess-controller) consumes the Claim and materialises
// the RoleBindings.
func (r *Reconciler) upsertUserAccess(ctx context.Context, org *orgapi.Organization, owner orgapi.OrganizationOwner) error {
	// Sanitize the email into a label-safe leaf: "ceo@acme.com" →
	// "ceo-at-acme-com". Keeps the CR name deterministic + RFC 1123-
	// compliant.
	emailLeaf := sanitizeEmail(owner.Email)
	name := fmt.Sprintf("%s-%s", org.Spec.Slug, emailLeaf)
	ns := r.UserAccessNamespace
	if ns == "" {
		ns = "catalyst-system"
	}

	// Map Org owner role to UserAccess role. Per the unified design
	// §6.2 "owner" is the highest tier; the existing XRD uses the
	// admin/editor/viewer naming. Map owner→admin, admin→admin,
	// developer→editor, viewer→viewer.
	role := mapOwnerToUARole(owner.Role)

	desired := unstructured.Unstructured{}
	desired.SetGroupVersionKind(userAccessGVK)
	desired.SetName(name)
	desired.SetNamespace(ns)
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
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get UserAccess %s/%s: %w", ns, name, err)
		}
		// Create.
		if err := r.Create(ctx, &desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil
			}
			return fmt.Errorf("create UserAccess %s/%s: %w", ns, name, err)
		}
		return nil
	}
	// Update: copy desired spec onto current (preserves resourceVersion).
	current.Object["spec"] = desired.Object["spec"]
	current.SetLabels(desired.GetLabels())
	if err := r.Update(ctx, &current); err != nil {
		return fmt.Errorf("update UserAccess %s/%s: %w", ns, name, err)
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
