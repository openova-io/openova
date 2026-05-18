// Package controller hosts the Sandbox reconciler — the Wave 1 slice
// of the Sandbox product (#1615 brief + products/sandbox/docs/
// architecture.md §7).
//
// Per architecture.md §7 the sandbox-controller is the sister of
// organization-controller. It reconciles a Sandbox CR into manifests
// the per-Org Flux Kustomization (host cluster) materializes inside
// the Org vcluster:
//
//   1. Namespace `sandbox-<owner-uid>` inside the Org vcluster.
//   2. ResourceQuota mirroring spec.quota.
//   3. ServiceAccount `sandbox` + namespace-scoped Role + RoleBinding
//      so Wave 2's pty-server / openova-sandbox-mcp Deployments have
//      JUST the verbs they need inside the Sandbox namespace.
//   4. PVCs per spec.repos[] entry (repo clone target — initContainer
//      lands in Wave 2 alongside the pty-server StatefulSet).
//   5. Placeholder Secret `sandbox-tokens` — filled in Wave 2 by the
//      long-lived org-scoped token issuance flow (architecture.md §6).
//
// The reconciler writes its desired-state manifests into the per-Org
// `catalyst-tenant` Gitea repo at `sandbox/<owner-uid>/` — the EXACT
// same idiom organization-controller already uses for vcluster
// manifests (organization_controller.go:188-225). Flux on the host
// picks it up.
//
// Wave 2 will add the pty-server StatefulSet + openova-sandbox-mcp
// Deployment + HTTPRoutes. Wave 3 ships the UI scaffold.
//
// Idempotency: every "ensure" step is find-or-create + byte-equal
// short-circuit. Re-reconciling on a steady-state CR writes nothing
// downstream.

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/sandbox/internal/gitops"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// Reconciler reconciles Sandbox CRs. Field shape mirrors
// organization-controller's Reconciler — fewer knobs because Wave 1
// only touches Gitea (no Keycloak, no vcluster chart version).
type Reconciler struct {
	client.Client
	Log logr.Logger

	// GiteaClient is the Gitea Admin client used to PutFile manifests
	// into the per-Org `catalyst-tenant` repo. Same client + token
	// organization-controller uses (CATALYST_GITEA_* env).
	GiteaClient *gitea.Client

	// HostCluster is the canonical host-cluster name (e.g.
	// hz-fsn-rtz-prod) — surfaced in logs + may go onto labels in
	// future waves. Not yet written into any rendered manifest because
	// Sandbox lives inside the Org vcluster, not on the host.
	HostCluster string

	// SovereignFQDN is the Sovereign domain (e.g. omantel.omani.works).
	// Goes onto the openova.io/sovereign label of every rendered
	// resource so fleet-wide queries work without label-graph lookup.
	SovereignFQDN string

	// Branch is the Gitea branch the controller writes manifests to.
	// Defaults to "main" — matches organization-controller.
	Branch string

	// TenantRepoName is the per-Org "shared blueprints" repo
	// organization-controller already wrote vcluster manifests into.
	// Defaults to "catalyst-tenant".
	TenantRepoName string
}

// SetupWithManager registers the reconciler. The manager's scheme must
// already have sandboxapi registered.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sandboxapi.Sandbox{}).
		Complete(r)
}

// Reconcile is the controller-runtime entry point.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sandbox", req.NamespacedName.String())
	log.Info("reconcile")

	var sb sandboxapi.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sb); err != nil {
		if apierrors.IsNotFound(err) {
			// CR deleted — delete-handling is out of scope for Wave 1.
			// A future wave may add a finalizer that purges the
			// per-Sandbox namespace + Gitea-repo path. For now we
			// leave them in place (matches organization-controller's
			// founder direction on "retain customer data unless
			// explicit purge").
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get sandbox: %w", err)
	}

	// Drift check: Wave 1 requires spec.owner.orgRef.slug present + a
	// non-empty owner email. Mirrors organization-controller's
	// SlugMetadataMismatch handling — surface as a Failed condition
	// instead of silently producing broken downstream artifacts.
	if strings.TrimSpace(sb.Spec.Owner.OrgRef.Slug) == "" {
		return r.fail(ctx, &sb, "OwnerOrgRefMissing",
			"spec.owner.orgRef.slug must be non-empty (the parent Organization slug)")
	}
	if strings.TrimSpace(sb.Spec.Owner.Email) == "" {
		return r.fail(ctx, &sb, "OwnerEmailMissing",
			"spec.owner.email must be non-empty")
	}

	ownerUID := sanitizeEmail(sb.Spec.Owner.Email)
	if ownerUID == "" {
		return r.fail(ctx, &sb, "OwnerEmailInvalid",
			fmt.Sprintf("spec.owner.email %q did not yield a DNS-safe owner UID", sb.Spec.Owner.Email))
	}

	// Render Wave 1 manifests.
	in := gitops.Inputs{
		Name:          sb.Name,
		OwnerUID:      ownerUID,
		OwnerEmail:    sb.Spec.Owner.Email,
		OrgSlug:       sb.Spec.Owner.OrgRef.Slug,
		SovereignFQDN: r.SovereignFQDN,
		Quota:         sb.Spec.Quota,
		Repos:         sb.Spec.Repos,
		PreviewDomain: sb.Spec.PreviewDomain,
	}
	manifests, err := gitops.Render(in)
	if err != nil {
		return r.fail(ctx, &sb, "ManifestRenderFailed", err.Error())
	}

	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	repo := r.TenantRepoName
	if repo == "" {
		repo = "catalyst-tenant"
	}

	// Write under sandbox/<owner-uid>/ in the per-Org repo. The repo
	// already exists — organization-controller's reconcile loop
	// EnsureRepo's it. We never auto-create it (Sandbox depends on
	// Organization having been reconciled first; if the operator
	// applied a Sandbox before its Organization the PutFile errors
	// surface as a Failed condition rather than silently bootstrapping
	// a half-configured Org-level repo).
	prefix := fmt.Sprintf("sandbox/%s", ownerUID)
	for path, data := range manifests {
		fullPath := fmt.Sprintf("%s/%s", prefix, path)
		if _, _, err := r.GiteaClient.PutFile(ctx,
			sb.Spec.Owner.OrgRef.Slug, repo, branch, fullPath, data,
			fmt.Sprintf("sandbox-controller: reconcile %s for sandbox %s/%s",
				fullPath, sb.Namespace, sb.Name)); err != nil {
			return r.fail(ctx, &sb, "GitopsWriteFailed",
				fmt.Sprintf("write %s: %s", fullPath, err))
		}
	}

	// Status update — Ready=True + Provisioning phase. Flux on the host
	// is what actually creates the namespace / RBAC / PVCs inside the
	// Org vcluster; the controller only certifies that the desired
	// state has landed in Git. Wave 2 will add the cluster-side
	// readiness condition.
	desired := sandboxapi.SandboxStatus{
		Phase:      "Provisioning",
		GitopsPath: prefix,
		Conditions: []sandboxapi.SandboxCondition{
			{
				Type:               "Ready",
				Status:             "True",
				Reason:             "GitopsReconciled",
				Message:            fmt.Sprintf("Wave 1 manifests reconciled to gitea %s/%s@%s:%s", sb.Spec.Owner.OrgRef.Slug, repo, branch, prefix),
				LastTransitionTime: metav1.NewTime(time.Now()),
			},
		},
		ObservedGeneration: sb.Generation,
	}
	if err := r.patchStatus(ctx, &sb, desired); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch status: %w", err)
	}

	log.Info("reconcile ok",
		"org", sb.Spec.Owner.OrgRef.Slug,
		"owner_uid", ownerUID,
		"gitops_path", prefix,
		"files", len(manifests),
	)
	return ctrl.Result{}, nil
}

// fail records a Failed condition + the non-zero observedGeneration so
// the operator console can surface the error. Drift errors (missing
// orgRef / invalid email) DO NOT requeue — they require operator
// action. Other errors requeue after 30s (matches
// organization-controller's cadence).
func (r *Reconciler) fail(ctx context.Context, sb *sandboxapi.Sandbox, reason, message string) (ctrl.Result, error) {
	r.Log.Error(errors.New(reason), message,
		"sandbox", sb.Namespace+"/"+sb.Name,
		"owner", sb.Spec.Owner.Email)
	st := sandboxapi.SandboxStatus{
		Phase: "Failed",
		Conditions: []sandboxapi.SandboxCondition{
			{
				Type:               "Ready",
				Status:             "False",
				Reason:             reason,
				Message:            message,
				LastTransitionTime: metav1.NewTime(time.Now()),
			},
		},
		ObservedGeneration: sb.Generation,
	}
	_ = r.patchStatus(ctx, sb, st)
	switch reason {
	case "OwnerOrgRefMissing", "OwnerEmailMissing", "OwnerEmailInvalid":
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) patchStatus(ctx context.Context, sb *sandboxapi.Sandbox, desired sandboxapi.SandboxStatus) error {
	updated := sb.DeepCopyObject().(*sandboxapi.Sandbox)
	updated.Status = desired
	return r.Status().Update(ctx, updated)
}

// sanitizeEmail converts an email into a DNS-label-safe leaf:
// "ceo@acme.com" → "ceo-at-acme-com". Identical convention to
// organization-controller's sanitizeEmail
// (organization_controller.go:424-438) — keeping the two
// implementations in lockstep means the same owner email produces the
// same UID across both controllers' rendered resources.
func sanitizeEmail(email string) string {
	out := strings.ToLower(strings.TrimSpace(email))
	out = strings.ReplaceAll(out, "@", "-at-")
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, "+", "-plus-")
	out = strings.ReplaceAll(out, "_", "-")
	if len(out) > 200 {
		out = out[:200]
	}
	out = strings.Trim(out, "-")
	return out
}
