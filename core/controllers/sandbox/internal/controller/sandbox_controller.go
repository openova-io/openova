// Package controller hosts the Sandbox reconciler — the Wave 1 + Wave 8
// slice of the Sandbox product (#1615 brief + products/sandbox/docs/
// architecture.md §7).
//
// Per architecture.md §7 the sandbox-controller is the sister of
// organization-controller. It reconciles a Sandbox CR into manifests
// the per-Org Flux Kustomization (host cluster) materializes inside
// the Org vcluster. Wave 8 adds the pty-server StatefulSet + MCP
// Deployment + Service + HTTPRoute (in addition to the Wave-1
// namespace + RBAC + PVCs + placeholder Secret).
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

// Reconciler reconciles Sandbox CRs.
type Reconciler struct {
	client.Client
	Log logr.Logger

	GiteaClient   *gitea.Client
	HostCluster   string
	SovereignFQDN string
	Branch        string
	TenantRepoName string

	// Wave 8 per-Sandbox runtime knobs (plumbed from chart env).
	PtyServerImage        string
	MCPImage              string
	NewapiURL             string
	LLMGatewayTokenSecret string
	BYOSSecretPrefix      string
	IdleTimeoutMinutes    int
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sandboxapi.Sandbox{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sandbox", req.NamespacedName.String())
	log.Info("reconcile")

	var sb sandboxapi.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sb); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get sandbox: %w", err)
	}

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

	in := gitops.Inputs{
		Name:                  sb.Name,
		OwnerUID:              ownerUID,
		OwnerEmail:            sb.Spec.Owner.Email,
		OrgSlug:               sb.Spec.Owner.OrgRef.Slug,
		SovereignFQDN:         r.SovereignFQDN,
		Quota:                 sb.Spec.Quota,
		Repos:                 sb.Spec.Repos,
		PreviewDomain:         sb.Spec.PreviewDomain,
		AgentCatalogue:        sb.Spec.AgentCatalogue,
		PtyServerImage:        r.PtyServerImage,
		MCPImage:              r.MCPImage,
		NewapiURL:             r.NewapiURL,
		LLMGatewayTokenSecret: r.LLMGatewayTokenSecret,
		BYOSSecretPrefix:      r.BYOSSecretPrefix,
		IdleTimeoutMinutes:    r.IdleTimeoutMinutes,
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

	desired := sandboxapi.SandboxStatus{
		Phase:      "Provisioning",
		GitopsPath: prefix,
		Conditions: []sandboxapi.SandboxCondition{
			{
				Type:               "Ready",
				Status:             "True",
				Reason:             "GitopsReconciled",
				Message:            fmt.Sprintf("Wave 1+8 manifests reconciled to gitea %s/%s@%s:%s", sb.Spec.Owner.OrgRef.Slug, repo, branch, prefix),
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
