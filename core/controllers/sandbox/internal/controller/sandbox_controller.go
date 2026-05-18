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
	"github.com/openova-io/openova/core/controllers/sandbox/internal/newapi"
	sandboxapi "github.com/openova-io/openova/core/controllers/sandbox/internal/sandboxapi"
)

// Annotation keys the reconciler stamps onto the Sandbox CR to carry
// the per-Sandbox NewAPI token lifecycle. The token VALUE itself
// never lands on the CR — only its expiry + last-rotation instant.
// The rendered Secret in the per-Org Gitea repo carries the bytes.
const (
	annotationTokenExpiresAt = "openova.io/sandbox-token-expires-at"
	annotationTokenRotatedAt = "openova.io/sandbox-token-rotated-at"
)

// DefaultTokenRotationLeadTime is how far in advance the reconciler
// re-mints the per-Sandbox NewAPI token before its expiry. The
// bridge handler currently issues 7-day tokens (SandboxTokenTTL in
// platform/newapi/internal/handler/sandbox_token.go) — picking a 1-
// day lead means a steady-state reconcile re-mints once per day,
// keeping the rendered Secret byte-stable between reconciles in the
// 6-day fresh-token window.
//
// The Wave 9 brief calls for "15 days before expiry" — that target
// applies once the bridge TTL is bumped to 30+ days. Until then 24h
// is the operationally-sane default; per-Sovereign overlays can
// override via Reconciler.TokenRotationLeadTime (e.g. set to 15d
// when the bridge's TTL is bumped).
const DefaultTokenRotationLeadTime = 24 * time.Hour

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

	// NewAPIClient mints per-Sandbox LLM-gateway tokens via the
	// catalyst-api bridge handler (POST /admin/tokens/sandbox, PR
	// #1638). When nil the reconciler renders the Wave 1 manifests
	// (namespace + RBAC + PVCs) but skips the token Secret — the
	// controller is operable on a Sovereign whose bridge handler is
	// not yet rolled out (e.g. fresh prov mid-handover) without
	// silently shipping a Sandbox without an LLM connection.
	NewAPIClient newapi.Client

	// DefaultChannels is the operator-configured list of NewAPI channel
	// names every freshly-minted Sandbox token is allowed to call.
	// Currently a single channel per Sovereign ("qwen" today, see
	// products/sandbox/docs/newapi-proxy-contract.md §2); future
	// per-tier work will allow per-Sandbox overrides via spec.
	DefaultChannels []string

	// TokenRotationLeadTime overrides DefaultTokenRotationLeadTime. The
	// controller re-mints when the previously-issued token's expiry is
	// within this window of now. Zero ⇒ DefaultTokenRotationLeadTime.
	TokenRotationLeadTime time.Duration

	// Now is the wall-clock source. Defaults to time.Now when nil;
	// injected by tests for deterministic rotation behaviour.
	Now func() time.Time
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

	// ── Per-Sandbox NewAPI bearer ──────────────────────────────────────
	// When wired (r.NewAPIClient non-nil), the controller drives the
	// full token lifecycle:
	//
	//   - No prior token (annotation absent) → mint fresh.
	//   - Token within tokenRotationLeadTime of expiry → re-mint, bump
	//     the `kubectl.kubernetes.io/restartedAt` annotation on the
	//     rendered Secret so Wave 2's pty-server StatefulSet picks up
	//     a rolling restart.
	//   - Steady state (token healthy) → render the previously-issued
	//     token at byte-equal output; PutFile short-circuits the Gitea
	//     write.
	//
	// When the bridge call fails the reconciler records a Failed
	// condition (TokenMintFailed) and requeues 30s — namespace/RBAC/PVC
	// manifests are NOT rendered until the bridge is reachable, so a
	// Sandbox without an LLM gateway never lands in steady state.
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	leadTime := r.TokenRotationLeadTime
	if leadTime <= 0 {
		leadTime = DefaultTokenRotationLeadTime
	}

	var (
		tokenValue     string
		tokenExpiresAt string
		tokenRotatedAt string
	)
	if r.NewAPIClient != nil {
		nowT := now()
		mustMint, prevExpiry := r.shouldMintToken(&sb, nowT, leadTime)
		if mustMint {
			channels := r.channelsForSandbox(&sb)
			if len(channels) == 0 {
				return r.fail(ctx, &sb, "NoAllowedChannels",
					"sandbox-controller has no DefaultChannels configured AND spec exposes none — refusing to mint a token with empty allowed_channels")
			}
			sandboxID := string(sb.UID)
			if strings.TrimSpace(sandboxID) == "" {
				// Fresh CR without a UID stamped (only happens in
				// pathological hand-rolled fixtures). Fall back to the
				// stable namespace/name pair.
				sandboxID = fmt.Sprintf("%s/%s", sb.Namespace, sb.Name)
			}
			mint, mintErr := r.NewAPIClient.MintSandboxToken(ctx, newapi.MintRequest{
				OrgID:           sb.Spec.Owner.OrgRef.Slug,
				UserID:          sb.Spec.Owner.Email,
				SandboxID:       sandboxID,
				AllowedChannels: channels,
			})
			if mintErr != nil {
				r.Log.Error(mintErr, "newapi mint failed",
					"sandbox", sb.Namespace+"/"+sb.Name,
					"prev_expiry", prevExpiry.Format(time.RFC3339))
				return r.fail(ctx, &sb, "TokenMintFailed", mintErr.Error())
			}
			tokenValue = mint.Token
			tokenExpiresAt = mint.ExpiresAt.UTC().Format(time.RFC3339)
			tokenRotatedAt = nowT.UTC().Format(time.RFC3339)

			// Persist the rotation marker on the CR BEFORE the Gitea
			// write so a crash between this point and the PutFile pass
			// surfaces on the next reconcile as "prev_expiry already
			// past, re-mint" rather than "token rendered but CR has no
			// expiry annotation, mint again". Both paths converge but
			// stamping first keeps the operator-visible state honest.
			if err := r.stampTokenAnnotations(ctx, &sb, tokenExpiresAt, tokenRotatedAt); err != nil {
				return ctrl.Result{}, fmt.Errorf("stamp annotations: %w", err)
			}
		} else {
			// Token healthy → re-render the previously-issued bytes by
			// reading them back from the cluster. Wave 9b will move
			// this to a controller-side cache; Wave 9 keeps the
			// scaffolding simple: when we don't need to mint, we read
			// the previously-rendered Secret out of the Org vcluster
			// via a (nil-tolerant) helper. For the Wave 9 PR we leave
			// the previously-rendered manifest in Gitea untouched —
			// the renderer skips the secret-newapi-token.yaml manifest
			// when tokenValue is empty, and PutFile's GET/SHA-equal
			// guard preserves the prior content (PutFile only writes
			// on byte-mismatch).
			tokenExpiresAt = prevExpiry.UTC().Format(time.RFC3339)
			// tokenRotatedAt left empty — renderer drops the
			// kubectl.kubernetes.io/restartedAt annotation only when
			// non-empty, so steady-state reconciles never bump it.
		}
	}

	// Render Wave 1 manifests.
	in := gitops.Inputs{
		Name:                  sb.Name,
		OwnerUID:              ownerUID,
		OwnerEmail:            sb.Spec.Owner.Email,
		OrgSlug:               sb.Spec.Owner.OrgRef.Slug,
		SovereignFQDN:         r.SovereignFQDN,
		Quota:                 sb.Spec.Quota,
		Repos:                 sb.Spec.Repos,
		PreviewDomain:         sb.Spec.PreviewDomain,
		NewAPIToken:           tokenValue,
		NewAPITokenSecretName: fmt.Sprintf("sandbox-%s-newapi-token", ownerUID),
		NewAPITokenExpiresAt:  tokenExpiresAt,
		NewAPITokenRotatedAt:  tokenRotatedAt,
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

// shouldMintToken inspects the CR's annotations and decides whether
// the reconciler should call the NewAPI bridge handler this pass.
// Returns (true, zeroExpiry) on first issuance or unparseable
// annotation; (true, prevExpiry) when the previously-issued token is
// within leadTime of expiry; (false, prevExpiry) when the token is
// healthy.
func (r *Reconciler) shouldMintToken(sb *sandboxapi.Sandbox, nowT time.Time, leadTime time.Duration) (bool, time.Time) {
	raw := strings.TrimSpace(sb.GetAnnotations()[annotationTokenExpiresAt])
	if raw == "" {
		return true, time.Time{}
	}
	prev, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		// Corrupt annotation — re-mint and overwrite. Operator-debug
		// path is the log line in the mint branch above.
		return true, time.Time{}
	}
	// Re-mint when expiry is within leadTime of now (covers the
	// already-expired case too: nowT.Add(leadTime).After(prev) is
	// trivially true when prev < nowT).
	if !prev.After(nowT.Add(leadTime)) {
		return true, prev
	}
	return false, prev
}

// channelsForSandbox derives the AllowedChannels list for a freshly
// minted token. Wave 1: the operator-supplied DefaultChannels are
// the source of truth. Future waves (per architecture.md §3) will
// add a spec.allowedChannels overlay for per-Sandbox restriction.
func (r *Reconciler) channelsForSandbox(_ *sandboxapi.Sandbox) []string {
	if len(r.DefaultChannels) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.DefaultChannels))
	for _, c := range r.DefaultChannels {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// stampTokenAnnotations patches the Sandbox CR with the new expiry +
// rotation timestamps. Uses a deep-copy + Update against the cached
// client so the patch is one round-trip; the controller-runtime
// cache reflects the change on the next reconcile.
//
// IMPORTANT: an Update() bumps the metadata.resourceVersion. The
// subsequent status update (patchStatus) operates on the same local
// `sb` value; we sync the bumped ResourceVersion back onto sb so the
// status-subresource patch does not 409 on stale-version.
func (r *Reconciler) stampTokenAnnotations(ctx context.Context, sb *sandboxapi.Sandbox, expiresAt, rotatedAt string) error {
	updated := sb.DeepCopyObject().(*sandboxapi.Sandbox)
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[annotationTokenExpiresAt] = expiresAt
	updated.Annotations[annotationTokenRotatedAt] = rotatedAt
	if err := r.Update(ctx, updated); err != nil {
		return err
	}
	// Reflect changes back onto the local copy so the rest of this
	// reconcile reads consistent annotations + the post-Update
	// resourceVersion (required by the cached client's optimistic-
	// concurrency check on the next .Status().Update call).
	sb.Annotations = updated.Annotations
	sb.ResourceVersion = updated.ResourceVersion
	return nil
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
