// Package controller implements the environment-controller reconcile
// loop (slice C2 of EPIC-0 #1095).
//
// The controller watches `Environment.catalyst.openova.io/v1` CRs
// (cluster-scoped) and reconciles each Environment to:
//
//  1. Verify the per-Org Gitea Org exists (slice C2 brief item 1).
//     Surface `GiteaOrgReady=False` if missing — Environments without
//     their Organization parent are invalid, NOT a panic.
//
//  2. Track the canonical branch name for this Environment in
//     `status.giteaRepoRef.branch` per NAMING §11.2 item 1.
//
//  3. Idempotently write per-vCluster Flux GitRepository manifests
//     (one per `spec.regions[]`) into the Org's Gitea repo at the path
//     `clusters/<host-cluster>/environments/<env-name>/gitrepository.yaml`.
//     Flux on the host cluster reconciles the manifest into the
//     vCluster.
//
//  4. Surface the canonical JetStream subject prefix
//     `ws.{org}-{envType}.>` on `status.jetstreamSubjectPrefix` per
//     NAMING §11.2 item 4. Per-Environment NATS Stream CR creation is
//     OUT OF SCOPE in slice C2 (NACK isn't installed; future slice).
//
//  5. Set `status.phase`, `status.regionCount`, `status.vclusters[]`,
//     `status.observedGeneration`, and the `Ready` condition.
//
// Per docs/INVIOLABLE-PRINCIPLES.md the controller does NOT call
// `kubectl apply`, `helm install`, or any cloud API — Flux is the only
// reconciler in production, and Flux applies the manifests this
// controller writes to Gitea.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	envv1 "github.com/openova-io/openova/core/controllers/environment/api/v1"
	"github.com/openova-io/openova/core/controllers/environment/internal/gitea"
	"github.com/openova-io/openova/core/controllers/environment/internal/gitops"
)

// GiteaClient is the subset of the gitea.Client surface the
// controller needs. Defining it as an interface here lets tests
// inject a fake without spinning up a real Gitea server, AND keeps
// the production gitea.Client free of test-only behavior.
type GiteaClient interface {
	GetOrg(ctx context.Context, org string) (*gitea.Org, error)
	UpsertFile(
		ctx context.Context,
		org, repo, branch, path string,
		content []byte,
		message, authorName, authorEmail string,
	) (committed bool, err error)
}

// Config holds the reconciler's runtime configuration. Per Inviolable
// Principle #4 (never hardcode), every value is set at startup from
// environment variables (cmd/main.go owns that wiring).
type Config struct {
	// FluxNamespace is the K8s namespace where Flux expects its source
	// CRs inside the vCluster. Defaults to "flux-system".
	FluxNamespace string

	// FluxIntervalSeconds is the GitRepository poll interval Flux uses
	// inside the vCluster. Defaults to 60.
	FluxIntervalSeconds int

	// GiteaPublicURL is the externally-visible Gitea base URL Flux
	// inside the vCluster will clone from (NOT the in-cluster service
	// URL the controller's GiteaClient hits). Per NAMING §5.1 this is
	// `gitea.{location-code}.{sovereign-domain}` (e.g.
	// `https://gitea.hfmp.acme.openova.io`).
	GiteaPublicURL string

	// GiteaSecretRef is the in-vCluster Kubernetes Secret holding the
	// Gitea read token Flux uses to clone. Empty means anonymous.
	// Slice C1 (organization-controller) is responsible for creating
	// this Secret inside each vCluster; environment-controller only
	// references it.
	GiteaSecretRef string

	// CommitAuthorName + CommitAuthorEmail are stamped on every commit
	// the controller produces. Per Inviolable Principle #4 these are
	// configurable; per docs/INVIOLABLE-PRINCIPLES.md operator-action
	// audit trail wants a non-human committer here.
	CommitAuthorName  string
	CommitAuthorEmail string

	// EnvRepoSuffix is appended to the Org slug to form the per-Org
	// repo name where the per-Env Flux manifests are committed
	// (e.g. Org `acme` + suffix `-environment` → repo
	// `acme/acme-environment`). Per-Application repos are slice C4's
	// scope; this controller only writes to the Env-scoped repo.
	EnvRepoSuffix string

	// RequeueAfter is how long to wait before re-running the
	// reconcile when nothing else triggered it. Defaults to 5
	// minutes — enough to catch drift in the Gitea-side manifest if
	// an operator hand-edits it, without burning Gitea quota.
	RequeueAfter time.Duration
}

// Defaults applies missing-field defaults to a Config. Returns a copy.
func (c Config) Defaults() Config {
	out := c
	if out.FluxNamespace == "" {
		out.FluxNamespace = "flux-system"
	}
	if out.FluxIntervalSeconds == 0 {
		out.FluxIntervalSeconds = 60
	}
	if out.CommitAuthorName == "" {
		out.CommitAuthorName = "environment-controller"
	}
	if out.CommitAuthorEmail == "" {
		out.CommitAuthorEmail = "environment-controller@openova.io"
	}
	if out.EnvRepoSuffix == "" {
		out.EnvRepoSuffix = "-environment"
	}
	if out.RequeueAfter == 0 {
		out.RequeueAfter = 5 * time.Minute
	}
	return out
}

// EnvironmentReconciler is the controller-runtime Reconciler.
type EnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Gitea  GiteaClient
	Cfg    Config
}

// SetupWithManager registers the reconciler with the controller-runtime
// manager. Watches Environment CRs cluster-scoped.
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Cfg.RequeueAfter == 0 {
		r.Cfg = r.Cfg.Defaults()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("environment-controller").
		For(&envv1.Environment{}).
		Complete(r)
}

// Reconcile implements the desired-state loop.
func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("environment", req.Name)

	var env envv1.Environment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Deleted between dequeue and Get — nothing to do.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get environment: %w", err)
	}

	cfg := r.Cfg.Defaults()

	// 1. Validate spec invariants the CRD schema doesn't catch (the
	//    schema enforces enums + minItems but cannot enforce
	//    placement-vs-regions cardinality). DR-as-envType is already
	//    blocked by the schema's enum on spec.envType.
	if err := validateSpec(env.Spec); err != nil {
		return r.markFailed(ctx, &env, "InvalidSpec", err.Error())
	}

	// Compute derived values used throughout.
	envName := env.Name
	if envName == "" {
		// Defensive — controller-runtime would not enqueue an empty name,
		// but a fake-client test could.
		return ctrl.Result{}, errors.New("environment name is empty")
	}
	branch := gitops.BranchForEnvType(env.Spec.EnvType)
	subjectPrefix := gitops.JetStreamSubjectPrefix(env.Spec.OrganizationRef, env.Spec.EnvType)

	// 2. Verify the parent Organization's Gitea Org exists.
	org, err := r.Gitea.GetOrg(ctx, env.Spec.OrganizationRef)
	if err != nil {
		if errors.Is(err, gitea.ErrOrgNotFound) {
			logger.Info("gitea org not found — surfacing condition", "org", env.Spec.OrganizationRef)
			return r.markPending(ctx, &env, branch, subjectPrefix,
				"GiteaOrgMissing",
				fmt.Sprintf("Gitea org %q does not exist; create the parent Organization first", env.Spec.OrganizationRef),
				cfg.RequeueAfter,
			)
		}
		return r.markDegraded(ctx, &env, branch, subjectPrefix, "GiteaOrgLookupError", err.Error(), cfg.RequeueAfter)
	}
	logger.V(1).Info("gitea org confirmed", "org", org.Username)

	// 3. Render and idempotently commit the per-vCluster Flux
	//    GitRepository manifest for each region.
	repo := env.Spec.OrganizationRef + cfg.EnvRepoSuffix
	repoURL := fmt.Sprintf("%s/%s/%s.git",
		strings.TrimRight(cfg.GiteaPublicURL, "/"),
		env.Spec.OrganizationRef,
		repo,
	)

	vclusters := make([]envv1.EnvironmentVClusterStatus, 0, len(env.Spec.Regions))
	now := metav1.Now()
	allReady := true

	for _, region := range env.Spec.Regions {
		host := gitops.HostClusterName(region, env.Spec.EnvType)
		path := gitops.GitRepositoryPath(host, envName)

		manifest, err := gitops.RenderGitRepository(gitops.RenderInputs{
			EnvName:         envName,
			Namespace:       cfg.FluxNamespace,
			RepoURL:         repoURL,
			Branch:          branch,
			IntervalSeconds: cfg.FluxIntervalSeconds,
			SecretRef:       cfg.GiteaSecretRef,
			OwnerEnvUID:     string(env.UID),
			OwnerEnvGen:     env.Generation,
		})
		if err != nil {
			return r.markDegraded(ctx, &env, branch, subjectPrefix, "RenderError", err.Error(), cfg.RequeueAfter)
		}

		message := fmt.Sprintf("environment-controller: reconcile %s on %s", envName, host)
		committed, err := r.Gitea.UpsertFile(
			ctx,
			env.Spec.OrganizationRef,
			repo,
			branch,
			path,
			manifest,
			message,
			cfg.CommitAuthorName,
			cfg.CommitAuthorEmail,
		)
		if err != nil {
			if errors.Is(err, gitea.ErrRepoNotFound) {
				logger.Info("gitea repo not found — re-queueing",
					"org", env.Spec.OrganizationRef, "repo", repo)
				return r.markPending(ctx, &env, branch, subjectPrefix,
					"GiteaRepoMissing",
					fmt.Sprintf("Gitea repo %q does not yet exist under org %q; organization-controller (C1) creates it",
						repo, env.Spec.OrganizationRef),
					cfg.RequeueAfter,
				)
			}
			allReady = false
			vclusters = append(vclusters, envv1.EnvironmentVClusterStatus{
				Host:               host,
				Name:               env.Spec.OrganizationRef,
				Phase:              "Failed",
				LastTransitionTime: now,
			})
			logger.Error(err, "upsert gitrepository.yaml failed", "host", host, "path", path)
			continue
		}
		if committed {
			logger.Info("wrote gitrepository.yaml", "host", host, "path", path)
		}
		vclusters = append(vclusters, envv1.EnvironmentVClusterStatus{
			Host:               host,
			Name:               env.Spec.OrganizationRef,
			Phase:              "Provisioning", // Flux still has to apply it
			LastTransitionTime: now,
		})
	}

	// 4. Update status.
	phase := envv1.PhaseReady
	if !allReady {
		phase = envv1.PhaseDegraded
	}
	env.Status.Phase = phase
	env.Status.RegionCount = int32(len(env.Spec.Regions))
	env.Status.VClusters = vclusters
	env.Status.GiteaRepoRef = envv1.EnvironmentGiteaRepoRef{
		Org:    env.Spec.OrganizationRef,
		Branch: branch,
	}
	env.Status.JetstreamSubjectPrefix = subjectPrefix
	env.Status.ObservedGeneration = env.Generation
	setCondition(&env, envv1.ConditionGiteaOrgReady, envv1.ConditionTrue, "OrgFound",
		fmt.Sprintf("Gitea org %q exists", env.Spec.OrganizationRef))
	if allReady {
		setCondition(&env, envv1.ConditionGitRepositoryWritten, envv1.ConditionTrue, "Reconciled",
			fmt.Sprintf("All %d region(s) have gitrepository.yaml committed", len(env.Spec.Regions)))
		setCondition(&env, envv1.ConditionReady, envv1.ConditionTrue, "Reconciled",
			"Environment reconciled successfully")
	} else {
		setCondition(&env, envv1.ConditionGitRepositoryWritten, envv1.ConditionFalse, "PartialFailure",
			"At least one region's gitrepository.yaml failed to commit")
		setCondition(&env, envv1.ConditionReady, envv1.ConditionFalse, "PartialFailure",
			"Environment partially reconciled — see vClusters status")
	}

	if err := r.Status().Update(ctx, &env); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	if !allReady {
		return ctrl.Result{RequeueAfter: cfg.RequeueAfter}, nil
	}
	return ctrl.Result{RequeueAfter: cfg.RequeueAfter}, nil
}

// markPending sets phase=Pending + a NotReady condition, persists,
// and re-queues. Used for "valid spec, parent missing" cases.
func (r *EnvironmentReconciler) markPending(
	ctx context.Context,
	env *envv1.Environment,
	branch, subjectPrefix, reason, message string,
	requeue time.Duration,
) (ctrl.Result, error) {
	env.Status.Phase = envv1.PhasePending
	env.Status.RegionCount = int32(len(env.Spec.Regions))
	env.Status.GiteaRepoRef = envv1.EnvironmentGiteaRepoRef{
		Org:    env.Spec.OrganizationRef,
		Branch: branch,
	}
	env.Status.JetstreamSubjectPrefix = subjectPrefix
	env.Status.ObservedGeneration = env.Generation
	setCondition(env, envv1.ConditionGiteaOrgReady, envv1.ConditionFalse, reason, message)
	setCondition(env, envv1.ConditionReady, envv1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status (pending): %w", err)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// markDegraded sets phase=Degraded for transient failures (non-404
// Gitea errors, render errors). Re-queues so the controller retries.
func (r *EnvironmentReconciler) markDegraded(
	ctx context.Context,
	env *envv1.Environment,
	branch, subjectPrefix, reason, message string,
	requeue time.Duration,
) (ctrl.Result, error) {
	env.Status.Phase = envv1.PhaseDegraded
	env.Status.RegionCount = int32(len(env.Spec.Regions))
	env.Status.GiteaRepoRef = envv1.EnvironmentGiteaRepoRef{
		Org:    env.Spec.OrganizationRef,
		Branch: branch,
	}
	env.Status.JetstreamSubjectPrefix = subjectPrefix
	env.Status.ObservedGeneration = env.Generation
	setCondition(env, envv1.ConditionReady, envv1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status (degraded): %w", err)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// markFailed is the "spec is invalid" terminal: status surfaces the
// failure but we don't requeue (changing the spec re-triggers via the
// informer).
func (r *EnvironmentReconciler) markFailed(
	ctx context.Context,
	env *envv1.Environment,
	reason, message string,
) (ctrl.Result, error) {
	env.Status.Phase = envv1.PhaseFailed
	env.Status.RegionCount = int32(len(env.Spec.Regions))
	env.Status.ObservedGeneration = env.Generation
	setCondition(env, envv1.ConditionReady, envv1.ConditionFalse, reason, message)
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status (failed): %w", err)
	}
	return ctrl.Result{}, nil
}

// setCondition upserts a condition by Type. Last-transition-time is
// only refreshed when the Status flips.
func setCondition(env *envv1.Environment, condType, status, reason, message string) {
	now := metav1.Now()
	for i := range env.Status.Conditions {
		if env.Status.Conditions[i].Type == condType {
			if env.Status.Conditions[i].Status != status {
				env.Status.Conditions[i].LastTransitionTime = now
			}
			env.Status.Conditions[i].Status = status
			env.Status.Conditions[i].Reason = reason
			env.Status.Conditions[i].Message = message
			return
		}
	}
	env.Status.Conditions = append(env.Status.Conditions, envv1.EnvironmentCondition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// validateSpec catches what the OpenAPI schema cannot:
//
//   - placement=single-region requires len(regions)==1
//   - placement=multi-region requires len(regions)>=2
//
// Both are documented constraints in the CRD's schema description but
// not enforceable via the structural-schema rules (the schema would
// need a custom CEL validation rule we don't ship here; future slice).
func validateSpec(spec envv1.EnvironmentSpec) error {
	switch spec.Placement {
	case "single-region":
		if len(spec.Regions) != 1 {
			return fmt.Errorf("placement=single-region requires exactly 1 entry in regions[], got %d", len(spec.Regions))
		}
	case "multi-region":
		if len(spec.Regions) < 2 {
			return fmt.Errorf("placement=multi-region requires at least 2 entries in regions[], got %d", len(spec.Regions))
		}
	default:
		return fmt.Errorf("invalid placement %q (expected single-region|multi-region)", spec.Placement)
	}
	return nil
}

// Compile-time assertion that EnvironmentReconciler implements the
// controller-runtime Reconciler interface.
var _ reconcile.Reconciler = (*EnvironmentReconciler)(nil)
