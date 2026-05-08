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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitea"
	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// userAccessGVR is the cluster-scoped UserAccess CR group/version/kind
// the reconciler writes per Org owner. Defined in
// platform/crossplane-claims/chart/templates/xrds/useraccess.yaml as
// access.openova.io/v1alpha1; slice C5 will replace the Crossplane
// Composition that materializes them with an in-cluster reconciler.
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
			// CR was deleted — nothing to do (delete-handling is
			// out of scope for slice C1; a future slice may add
			// a finalizer that purges the Keycloak group + Gitea
			// Org. For now we leave them in place to preserve
			// audit history per founder direction on
			// "Sovereigns retain customer data unless explicit
			// purge").
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get organization: %w", err)
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

	// 1. Keycloak group.
	kcAttrs := map[string][]string{
		"org":  {org.Spec.Slug},
		"tier": {org.Spec.Tier},
	}
	kcID, kcPath, kcRealm, err := r.Keycloak.EnsureGroup(ctx, "/"+org.Spec.Slug, kcAttrs)
	if err != nil {
		return r.fail(ctx, &org, "KeycloakGroupFailed", err.Error())
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
		if _, err := r.GiteaClient.PutFile(ctx,
			gOrg.Username, repoName, path, branch, data,
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

	// 5. Status update.
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
		GiteaOrg: orgapi.GiteaOrgStatus{
			Name:  gOrg.Username,
			Repos: []string{repoName},
		},
		Conditions: []orgapi.Condition{
			{
				Type:               "Ready",
				Status:             "True",
				Reason:             "Reconciled",
				Message:            "vCluster HelmRelease + Keycloak group + Gitea Org reconciled",
				LastTransitionTime: metav1.NewTime(time.Now()),
			},
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

// upsertUserAccess writes (or updates) a single-grant UserAccess CR
// scoped to the Organization. The CR shape mirrors
// platform/crossplane-claims/chart/tests/fixtures/useraccess-sample.yaml.
//
// Slice C5 (useraccess-controller) replaces the Crossplane Composition
// that currently materializes the RoleBindings — but the CR shape is
// the same.
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
	desired.SetLabels(map[string]string{
		"openova.io/organization":      org.Spec.Slug,
		"openova.io/sovereign":         org.Spec.SovereignRef,
		"openova.io/managed-by":        "organization-controller",
		"app.kubernetes.io/managed-by": "catalyst",
	})
	// OwnerReferences makes the UserAccess CR garbage-collected when the
	// Organization is deleted — no orphaned RoleBindings.
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
