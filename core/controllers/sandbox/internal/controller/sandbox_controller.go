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

	// D31 active-hot-standby — Sovereign-level toggle + region pair the
	// controller threads from its chart env (SOVEREIGN_ENABLE_HOT_STANDBY,
	// SOVEREIGN_PRIMARY_REGION, SOVEREIGN_REPLICA_REGION) into every
	// per-Sandbox MCP Pod via gitops.Inputs. The MCP server's
	// sandbox.db.provision handler reads them at call time and renders a
	// primary + replica Cluster.postgresql.cnpg.io pair when valid.
	// Default-empty keeps every existing Sandbox on single-Cluster CNPG
	// (zero regression). Bootstrap-kit slot 61 wires the per-Sovereign
	// overlay's envsubst placeholders into the bp-sandbox HelmRelease
	// values; the chart surfaces them as the controller's env.
	EnableHotStandby string
	PrimaryRegion    string
	ReplicaRegion    string

	// Wave 9 — NewAPI bridge client used by Reconcile to mint
	// per-Sandbox LLM-gateway tokens (POST /admin/tokens/sandbox,
	// PR #1638). When nil the reconciler renders the Wave 1+8
	// manifests but skips the token-mint path — the controller is
	// operable on a Sovereign whose bridge handler is not yet rolled
	// out (e.g. fresh prov mid-handover) without silently shipping a
	// Sandbox without an LLM connection. main.go logs a warning in
	// that case.
	NewAPIClient newapi.Client

	// DefaultChannels is the operator-configured list of NewAPI
	// channel names every freshly-minted Sandbox token is allowed to
	// call. Currently a single channel per Sovereign ("qwen" today,
	// see products/sandbox/docs/newapi-proxy-contract.md §2); future
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

	// ── Per-Sandbox NewAPI bearer ──────────────────────────────────────
	// When wired (r.NewAPIClient non-nil), the controller drives the
	// full token lifecycle:
	//
	//   - No prior token (annotation absent) → mint fresh.
	//   - Token within tokenRotationLeadTime of expiry → re-mint, bump
	//     the `kubectl.kubernetes.io/restartedAt` annotation on the
	//     rendered Secret so Wave 8's pty-server StatefulSet picks up
	//     a rolling restart.
	//   - Steady state (token healthy) → leave the previously-rendered
	//     Secret manifest in Gitea untouched (PutFile's byte-equal
	//     guard short-circuits).
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
			// Tier-bound MCP capabilities (PR #1671) — derived from
			// spec.capabilities (operator override) or spec.planId via
			// sandboxapi.ResolveCapabilities. Empty list is permitted by
			// the bridge handler and produces an introspection-only
			// token; the controller never short-circuits on a missing
			// capability list because the operator can grant on-demand
			// by patching spec.capabilities.
			caps := sandboxapi.ResolveCapabilities(&sb.Spec)
			mint, mintErr := r.NewAPIClient.MintSandboxToken(ctx, newapi.MintRequest{
				OrgID:           sb.Spec.Owner.OrgRef.Slug,
				UserID:          sb.Spec.Owner.Email,
				SandboxID:       sandboxID,
				AllowedChannels: channels,
				Capabilities:    caps,
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
			tokenExpiresAt = prevExpiry.UTC().Format(time.RFC3339)
			// tokenRotatedAt left empty — renderer drops the
			// kubectl.kubernetes.io/restartedAt annotation only when
			// non-empty, so steady-state reconciles never bump it.
		}
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
		IdleScalingDisabled:   sb.Spec.IdleScaling != nil && !sb.Spec.IdleScaling.Enabled,
		NewAPIToken:           tokenValue,
		NewAPITokenSecretName: fmt.Sprintf("sandbox-%s-newapi-token", ownerUID),
		NewAPITokenExpiresAt:  tokenExpiresAt,
		NewAPITokenRotatedAt:  tokenRotatedAt,
		EnableHotStandby:      r.EnableHotStandby,
		PrimaryRegion:         r.PrimaryRegion,
		ReplicaRegion:         r.ReplicaRegion,
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
// minted token. Wave 9: the operator-supplied DefaultChannels are
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

// sanitizeEmail converts an email into a DNS-label-safe leaf.
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
