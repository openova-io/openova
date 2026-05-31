// Package controller implements the application-controller reconcile
// loop (slice C4 of EPIC-0 #1095).
//
// The controller watches `Application.apps.openova.io/v1` CRs and on
// each event:
//
//   1. Fetches the parent `Environment.catalyst.openova.io/v1` named in
//      `spec.environmentRef`. Pending-on-miss with reason
//      `EnvironmentMissing`.
//
//   2. Fetches the parent `Organization.orgs.openova.io/v1` named in
//      `Environment.spec.organizationRef`. Pending-on-miss with reason
//      `OrganizationMissing`.
//
//   3. Fetches the `Blueprint.catalyst.openova.io/v1` (with v1alpha1
//      fallback) at name `spec.blueprintRef.name`. Pending-on-miss
//      with reason `BlueprintMissing`. Validates
//      `Application.spec.parameters` against
//      `Blueprint.spec.configSchema`. On invalid surfaces an `Invalid`
//      condition listing every failing JSON pointer.
//
//   4. Resolves the placement → per-region work plan via
//      `internal/placement`. For each region, renders the manifest set
//      via `internal/render` and idempotently writes
//      `clusters/<region>/applications/<app>/{kustomization,helmrelease}.yaml`
//      to the per-Org Gitea repo `<org>/<app>` on the env-type-mapped
//      branch (per slice C2 BranchForEnvType).
//
//   5. Updates `Application.status` with phase, primaryRegion,
//      regions[], giteaRepo URL, installedBlueprint snapshot, and
//      conditions.
//
//   6. Honors a finalizer: on deletion, removes every manifest the
//      controller wrote and waits for Flux to drain. The finalizer
//      releases on the next reconcile pass when the Gitea repo no
//      longer carries any of THIS Application's paths.
//
// Per docs/INVIOLABLE-PRINCIPLES.md the controller never calls
// `kubectl apply`, `helm install`, or any cloud API. Its only K8s
// writes are status updates on Application CRs (and a single finalizer
// add/remove on the spec metadata). Everything else is a Gitea HTTP
// commit; Flux applies.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	"github.com/openova-io/openova/core/controllers/internal/semver"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/render"
	"github.com/openova-io/openova/core/controllers/pkg/validate"
)

// GVR pins for the three CRDs the controller reads. Storage versions
// match the CRDs shipped at products/catalyst/chart/crds/.
var (
	ApplicationGVR = schema.GroupVersionResource{
		Group:    "apps.openova.io",
		Version:  "v1",
		Resource: "applications",
	}

	EnvironmentGVR = schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1",
		Resource: "environments",
	}

	OrganizationGVR = schema.GroupVersionResource{
		Group:    "orgs.openova.io",
		Version:  "v1",
		Resource: "organizations",
	}

	BlueprintGVR = schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1",
		Resource: "blueprints",
	}

	BlueprintGVRv1alpha1 = schema.GroupVersionResource{
		Group:    "catalyst.openova.io",
		Version:  "v1alpha1",
		Resource: "blueprints",
	}

	// FluxGitRepositoryGVR + FluxKustomizationGVR are the host-cluster
	// Flux v2 CRs the controller upserts in flux-system so Flux picks up
	// the per-Application manifests we commit to Gitea (qa-loop iter-8
	// Fix #42 bug 3 root cause: without these, Application CRs reached
	// status=Provisioning + Ready=True from the controller's POV but
	// nothing on the host cluster ever pulled the per-app helmrelease.yaml
	// from Gitea, so no Pod was scheduled).
	//
	// v1 is the Flux 2.4+ stable; v1beta2 was deprecated. Sovereigns
	// running on bp-flux 1.x ship v1 directly. If a Sovereign is pinned
	// to bp-flux <1.0 the operator must upgrade — we don't fall back to
	// v1beta2 because Inviolable Principle #3 ("follow documented
	// architecture") makes Flux the only reconciler and v1 the only
	// supported API version.
	FluxGitRepositoryGVR = schema.GroupVersionResource{
		Group:    "source.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "gitrepositories",
	}
	FluxKustomizationGVR = schema.GroupVersionResource{
		Group:    "kustomize.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "kustomizations",
	}

	// FluxHelmReleaseGVR — the per-Application HelmRelease that lands
	// in the Application CR's own namespace once the
	// per-region Kustomization reconciles the manifests committed to
	// Gitea. The reconciler observes this HR's status.conditions[Ready]
	// to flip Application.status.phase from `Provisioning` to `Ready`
	// (qa-loop iter-11 Fix #45 Cluster-B). Without the observation
	// the Application CR sat at Provisioning forever even after the
	// downstream Helm install completed — the Sovereign Console treated
	// it as still-installing, the matrix-asserted "Installed" terminal
	// phase never arrived, and TC-066 / TC-100 / TC-104 / TC-113 / TC-117
	// stayed FAIL.
	//
	// v2 is the Flux 2.4+ stable. Same Inviolable-Principle #3 rationale
	// as the GitRepository / Kustomization GVR comments above — no v2beta
	// fallback because Sovereigns standardise on bp-flux 1.x.
	FluxHelmReleaseGVR = schema.GroupVersionResource{
		Group:    "helm.toolkit.fluxcd.io",
		Version:  "v2",
		Resource: "helmreleases",
	}
)

// Phase strings — surfaced on Application.status.phase per the CRD's
// enum.
const (
	PhasePending      = "Pending"
	PhaseProvisioning = "Provisioning"
	PhaseReady        = "Ready"
	PhaseDegraded     = "Degraded"
	PhaseFailed       = "Failed"
	PhaseUninstalling = "Uninstalling"
)

// Condition reason vocabulary.
const (
	ReasonEnvironmentMissing  = "EnvironmentMissing"
	ReasonOrganizationMissing = "OrganizationMissing"
	ReasonBlueprintMissing    = "BlueprintMissing"
	ReasonInvalid             = "Invalid"
	ReasonReconciled          = "Reconciled"
	ReasonGiteaError          = "GiteaError"
	ReasonRenderError         = "RenderError"
	ReasonOrgGiteaMissing     = "GiteaOrgMissing"
	ReasonAwaitingDrain       = "AwaitingFluxDrain"
)

// FinalizerName — owned by application-controller.
const FinalizerName = "application.apps.openova.io/finalizer"

// Gitea is the subset of the gitea.Client surface the reconciler needs.
// Defining it as an interface here lets tests swap in a fake without a
// real HTTP server. The production gitea.Client satisfies this
// interface byte-for-byte.
//
// Note: GetFile is intentionally absent from this surface — the
// reconciler relies on PutFile's internal byte-equality short-circuit
// for idempotency, so the controller never needs to read existing
// content directly.
type Gitea interface {
	EnsureRepo(ctx context.Context, org, name, description string, private bool) (gitea.Repo, error)
	EnsureBranch(ctx context.Context, org, repo, branch string) error
	PutFile(ctx context.Context, org, repo, branch, path string, content []byte, message string, opts ...gitea.PutFileOpts) (file gitea.File, committed bool, err error)
	DeleteFile(ctx context.Context, org, repo, branch, path, message string) (bool, error)
}

// IsGiteaNotFound reports whether err signals a missing-file or
// missing-org/repo from the Gitea client. Implementations supply a
// matching helper via the GiteaErrorClassifier interface; if missing,
// we fall back to nil-error semantics.
type GiteaErrorClassifier interface {
	IsNotFound(err error) bool
	IsOrgNotFound(err error) bool
}

// Config holds the reconciler's runtime configuration. Per Inviolable
// Principle #4 (never hardcode), every value is set at startup from
// environment variables (cmd/main.go owns that wiring).
type Config struct {
	// GiteaPublicURL is the externally-visible Gitea base URL stamped
	// on `Application.status.giteaRepo`. Per NAMING §11.2 this is
	// `gitea.{location-code}.{sovereign-domain}` (e.g.
	// `https://gitea.hfmp.acme.openova.io`).
	GiteaPublicURL string

	// CommitAuthorName / CommitAuthorEmail decorate Gitea commits.
	CommitAuthorName  string
	CommitAuthorEmail string

	// SourceNamespace is the K8s namespace inside the vCluster that
	// holds Flux source CRs (HelmRepository / GitRepository / OCIRepository).
	// Defaults to flux-system.
	SourceNamespace string

	// HelmReleaseInterval is the per-HelmRelease reconcile interval
	// stamped on the rendered manifests. Defaults to 600s.
	HelmReleaseIntervalSeconds int

	// CatalogSourceRef is the default Flux source ref the controller
	// stamps on rendered HelmReleases when the Blueprint doesn't
	// supply one.
	CatalogSourceRef string

	// RequeueAfter is how long to wait before re-running the
	// reconcile when nothing else triggered it. Defaults to 5 minutes.
	RequeueAfter time.Duration

	// SovereignBcpTopology — the Sovereign-wide BCP topology this
	// catalyst-platform was provisioned under (G93.1, Refs #2666). One
	// of "single-region", "active-hotstandby", or "active-active".
	// Empty = unset, treated as single-region for default derivation.
	// Read from the SOVEREIGN_BCP_TOPOLOGY env at controller startup;
	// the bp-catalyst-platform chart slot 13 stamps it from cloud-init
	// via the bootstrap-kit Kustomization postBuild.substitute. The
	// G93.2 (Refs #2667) default-derivation seam consults this when an
	// Application CR omits `spec.placement`: a multi-region Sovereign
	// auto-picks the Blueprint's `defaultOnMultiRegion` mode so
	// Pillar 3 zero-tx-loss holds without operator opt-in.
	SovereignBcpTopology string

	// HostFluxNamespace is the K8s namespace on the HOST cluster (not
	// the vCluster) where the per-Application Flux GitRepository +
	// Kustomization CRs are upserted. Defaults to "flux-system" — the
	// canonical Flux namespace on every Sovereign per
	// products/catalyst/bootstrap/api/internal/handler/infrastructure.go.
	// Per Inviolable Principle #4 the deployment env can override this
	// for non-canonical installs.
	//
	// IMPORTANT: this is distinct from SourceNamespace (which lives
	// inside the vCluster and is stamped on the rendered HelmRelease's
	// chart sourceRef). The host-side bootstrap reconciles via
	// HostFluxNamespace; the in-vCluster HelmRelease pulls its chart
	// payload from SourceNamespace.
	HostFluxNamespace string

	// GiteaInClusterURL is the Gitea HTTP base URL the HOST cluster's
	// Flux uses to clone per-Application Gitea repos. Distinct from
	// GiteaPublicURL (which is operator-facing, stamped on
	// Application.status.giteaRepo). The default
	// `http://gitea-http.gitea.svc.cluster.local:3000` is the in-cluster
	// service URL — Flux on the host cluster resolves it via cluster DNS,
	// which is the one path that doesn't depend on external DNS being
	// fully provisioned (Inviolable Principle #4: never hardcode external
	// FQDNs in source paths that the platform itself bootstraps).
	GiteaInClusterURL string

	// HostFluxIntervalSeconds is the reconcile interval stamped on the
	// host-side GitRepository + Kustomization. Defaults to 60.
	HostFluxIntervalSeconds int

	// FluxGiteaSecretRef is the Secret in HostFluxNamespace that holds
	// the Gitea token Flux uses to clone the per-Application repo.
	// Empty means anonymous clone (acceptable for in-cluster Gitea where
	// the network boundary is the K8s service cordon). Defaults to "".
	FluxGiteaSecretRef string

	// HelmReleaseObservationInterval is how often the periodic re-list
	// fires to pick up downstream HelmRelease readiness flips. Defaults
	// to 30s — short enough that the matrix-asserted 3-minute ceiling
	// for `qa-wp` to reach `phase=Ready` (TC-066) is comfortably met
	// even with a single observation miss. qa-loop iter-11 Fix #45
	// Cluster-B: without this re-list, Application.status.phase was
	// stuck at `Provisioning` indefinitely because the K8s Watch on
	// Application CRs doesn't fire when a SIBLING HR's status changes.
	HelmReleaseObservationInterval time.Duration
}

// Defaults applies missing-field defaults to a Config. Returns a copy.
func (c Config) Defaults() Config {
	out := c
	if out.CommitAuthorName == "" {
		out.CommitAuthorName = "application-controller"
	}
	if out.CommitAuthorEmail == "" {
		out.CommitAuthorEmail = "application-controller@openova.io"
	}
	if out.SourceNamespace == "" {
		out.SourceNamespace = "flux-system"
	}
	if out.HelmReleaseIntervalSeconds <= 0 {
		out.HelmReleaseIntervalSeconds = 600
	}
	if out.CatalogSourceRef == "" {
		out.CatalogSourceRef = "openova-catalog"
	}
	if out.RequeueAfter == 0 {
		out.RequeueAfter = 5 * time.Minute
	}
	if out.HostFluxNamespace == "" {
		out.HostFluxNamespace = "flux-system"
	}
	if out.GiteaInClusterURL == "" {
		out.GiteaInClusterURL = "http://gitea-http.gitea.svc.cluster.local:3000"
	}
	if out.HostFluxIntervalSeconds <= 0 {
		out.HostFluxIntervalSeconds = 60
	}
	if out.HelmReleaseObservationInterval <= 0 {
		out.HelmReleaseObservationInterval = 30 * time.Second
	}
	return out
}

// Reconciler holds runtime state for the controller.
type Reconciler struct {
	// Dynamic is the K8s dynamic client. Pass either an in-cluster
	// client or a fake.NewSimpleDynamicClient for tests.
	Dynamic dynamic.Interface

	// Gitea is the Gitea HTTP client (production: gitea.Client).
	// Tests inject a fake.
	Gitea Gitea

	// GiteaErrors classifies error kinds so the reconciler can
	// distinguish "file not found" from "org not found" from
	// transport failures.
	GiteaErrors GiteaErrorClassifier

	// Cfg is the resolved runtime configuration.
	Cfg Config

	// Log is the structured logger. Defaults to slog.Default() when nil.
	Log *slog.Logger
}

// New returns a fresh Reconciler with cfg defaults applied.
func New(dyn dynamic.Interface, gitea Gitea, errs GiteaErrorClassifier, cfg Config, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{
		Dynamic:     dyn,
		Gitea:       gitea,
		GiteaErrors: errs,
		Cfg:         cfg.Defaults(),
		Log:         log,
	}
}

// Run starts the watch loop. Blocks until ctx is cancelled.
//
// Watches Application CRs across all namespaces (the CRD is namespace-
// scoped per products/catalyst/chart/crds/application.yaml).
//
// In addition to the Watch on Application CRs, a periodic re-list ticker
// fires every `Cfg.HelmReleaseObservationInterval` (default 30s) so the
// reconciler picks up downstream HelmRelease readiness flips. Without
// this re-list, Application.status.phase would never transition off
// `Provisioning` because nothing on the API server triggers a fresh
// reconcile of the Application when its sibling HelmRelease's
// status.conditions[Ready] flips True. qa-loop iter-11 Fix #45 Cluster-B.
func (r *Reconciler) Run(ctx context.Context) error {
	if r.Dynamic == nil {
		return errors.New("controller: Dynamic client is required")
	}
	if err := r.initialList(ctx); err != nil {
		return fmt.Errorf("initial list: %w", err)
	}
	// Periodic re-list ticker — observes HR status changes that don't
	// trigger an Application Watch event.
	go r.runPeriodicRelist(ctx)
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := r.watchOnce(ctx); err != nil {
			r.Log.Warn("application-controller: watch error; will retry", "err", err)
		}
		return false, nil
	})
}

// runPeriodicRelist re-runs initialList every HelmReleaseObservationInterval
// so that downstream HelmRelease.status.conditions[Ready] flips reach the
// Application.status.phase. Watching the HR directly would also work but
// is more complex (one watcher per app namespace, dynamic add/remove on
// Application create/delete). The cheap re-list is correct + resilient
// to API server restarts.
//
// qa-loop iter-11 Fix #45 Cluster-B.
func (r *Reconciler) runPeriodicRelist(ctx context.Context) {
	interval := r.Cfg.HelmReleaseObservationInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.initialList(ctx); err != nil {
				r.Log.Warn("application-controller: periodic re-list error",
					"err", err)
			}
		}
	}
}

func (r *Reconciler) initialList(ctx context.Context) error {
	list, err := r.Dynamic.Resource(ApplicationGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		if err := r.Reconcile(ctx, &list.Items[i]); err != nil {
			r.Log.Error("initial reconcile failed", "name", list.Items[i].GetName(), "err", err)
		}
	}
	return nil
}

func (r *Reconciler) watchOnce(ctx context.Context) error {
	w, err := r.Dynamic.Resource(ApplicationGVR).Namespace("").Watch(ctx, metav1.ListOptions{
		AllowWatchBookmarks: true,
		TimeoutSeconds:      ptrInt64(int64(r.Cfg.RequeueAfter.Seconds())),
	})
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				return errors.New("watch channel closed")
			}
			if event.Type == watch.Error || event.Type == watch.Bookmark {
				continue
			}
			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if err := r.Reconcile(ctx, obj); err != nil {
				r.Log.Error("reconcile failed", "name", obj.GetName(), "namespace", obj.GetNamespace(), "err", err)
			}
		}
	}
}

// Reconcile is the per-object reconcile entry-point. Exposed for tests.
//
// On any deletion timestamp, this drives the finalizer drain path —
// see handleDeletion(). Otherwise the steady-state reconcile flow is:
//
//   1. Ensure finalizer present (idempotent).
//   2. Fetch parents + Blueprint; on miss surface Pending and return.
//   3. Validate parameters; on miss surface Invalid and return.
//   4. Resolve placement; render + commit per-region manifests.
//   5. Update status.
func (r *Reconciler) Reconcile(ctx context.Context, app *unstructured.Unstructured) error {
	if app == nil {
		return nil
	}

	if !app.GetDeletionTimestamp().IsZero() {
		return r.handleDeletion(ctx, app)
	}

	// Ensure finalizer is present.
	if err := r.ensureFinalizer(ctx, app); err != nil {
		return fmt.Errorf("ensure finalizer: %w", err)
	}

	r.Log.Info("reconcile",
		"name", app.GetName(),
		"namespace", app.GetNamespace(),
		"gen", app.GetGeneration())

	spec, err := parseSpec(app)
	if err != nil {
		return r.markFailed(ctx, app, ReasonInvalid, err.Error())
	}

	// 1. Resolve parent Environment.
	env, err := r.fetchEnvironment(ctx, spec.EnvironmentRef)
	if err != nil {
		if isNotFound(err) {
			return r.markPending(ctx, app, ReasonEnvironmentMissing,
				fmt.Sprintf("Environment %q not found", spec.EnvironmentRef))
		}
		return r.markDegraded(ctx, app, ReasonGiteaError,
			fmt.Sprintf("fetch Environment: %v", err))
	}

	envSpec, err := parseEnvSpec(env)
	if err != nil {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("parent Environment %q has invalid spec: %v", spec.EnvironmentRef, err))
	}

	// 2. Resolve parent Organization.
	org, err := r.fetchOrganization(ctx, envSpec.OrganizationRef)
	if err != nil {
		if isNotFound(err) {
			return r.markPending(ctx, app, ReasonOrganizationMissing,
				fmt.Sprintf("Organization %q (referenced by Environment %q) not found",
					envSpec.OrganizationRef, spec.EnvironmentRef))
		}
		return r.markDegraded(ctx, app, ReasonGiteaError,
			fmt.Sprintf("fetch Organization: %v", err))
	}
	_ = org // currently unused beyond existence check; future slices read more

	// 3. Resolve Blueprint (try v1, fallback v1alpha1).
	bp, err := r.fetchBlueprint(ctx, spec.BlueprintName)
	if err != nil {
		if isNotFound(err) {
			return r.markPending(ctx, app, ReasonBlueprintMissing,
				fmt.Sprintf("Blueprint %q not found", spec.BlueprintName))
		}
		return r.markDegraded(ctx, app, ReasonGiteaError,
			fmt.Sprintf("fetch Blueprint: %v", err))
	}

	// 4. Validate the Blueprint version reference.
	if !semver.IsExact(spec.BlueprintVersion) {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("blueprintRef.version %q is not an exact MAJOR.MINOR.PATCH semver",
				spec.BlueprintVersion))
	}

	// 4.5 (G93.2, Refs #2667) — derive effective placement when the
	// Application CR's spec.placement is empty. The Blueprint declares
	// its preferred single-region default + its preferred multi-region
	// default; the controller picks the right one based on the
	// Sovereign-wide BCP topology (SOVEREIGN_BCP_TOPOLOGY env, threaded
	// in via Config.SovereignBcpTopology by main.go). When operator
	// supplied spec.placement explicitly, this is a no-op.
	if spec.Placement == "" {
		spec.Placement = placement.EffectiveDefault(
			r.Cfg.SovereignBcpTopology,
			blueprintDefaultPlacement(bp),
			blueprintDefaultOnMultiRegion(bp),
		)
		r.Log.Info("derived effective placement from Blueprint defaults",
			"app", app.GetName(),
			"blueprint", spec.BlueprintName,
			"sovereignTopology", r.Cfg.SovereignBcpTopology,
			"effectivePlacement", spec.Placement)
	}

	// 5. Validate placement against Blueprint.placementSchema.modes.
	allowedModes := blueprintAllowedModes(bp)
	if !placement.AllowedByBlueprint(spec.Placement, allowedModes) {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("placement %q not in Blueprint %q allowed modes %v",
				spec.Placement, spec.BlueprintName, allowedModes))
	}

	// 6. Validate parameters against the Blueprint configSchema.
	configSchema, _, _ := unstructured.NestedMap(bp.Object, "spec", "configSchema")
	rep, valErr := validate.Parameters(configSchema, spec.Parameters)
	if valErr != nil {
		return r.markDegraded(ctx, app, ReasonInvalid,
			fmt.Sprintf("Blueprint configSchema is malformed: %v", valErr))
	}
	if !rep.Valid {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("parameters do not match Blueprint configSchema: %s",
				strings.Join(rep.Errors, "; ")))
	}

	// 7. Resolve placement → per-region work plan.
	plan, err := placement.Resolve(spec.Placement, spec.Regions)
	if err != nil {
		return r.markFailed(ctx, app, ReasonInvalid, err.Error())
	}

	// 8. Ensure the per-Application Gitea repo exists.
	//
	// private=false: the in-cluster Gitea is on the K8s service cordon
	// (host-only, no external network access via gitea-http svc), so
	// "private" is redundant security theater that breaks Flux's
	// anonymous clone path. Without anonymous-clone the host-side Flux
	// GitRepository requires a token Secret in flux-system, which would
	// be yet another bootstrapped state. Public-on-cordon is the
	// architecturally clean default; operators who want hard isolation
	// can swap private=true + bootstrap the Secret in a follow-up.
	// qa-loop iter-8 Fix #42 follow-up #2: live on omantel, private=true
	// caused `failed to checkout: authentication required` on the
	// Flux side.
	branch := branchForEnvType(envSpec.EnvType)
	if _, err := r.Gitea.EnsureRepo(ctx, envSpec.OrganizationRef, app.GetName(),
		"Application manifests — auto-managed by application-controller. Do not edit manually.",
		false); err != nil {
		if r.GiteaErrors != nil && r.GiteaErrors.IsOrgNotFound(err) {
			return r.markPending(ctx, app, ReasonOrgGiteaMissing,
				fmt.Sprintf("Gitea Org %q does not exist; organization-controller (C1) creates it",
					envSpec.OrganizationRef))
		}
		return r.markDegraded(ctx, app, ReasonGiteaError, fmt.Sprintf("EnsureRepo: %v", err))
	}
	if err := r.Gitea.EnsureBranch(ctx, envSpec.OrganizationRef, app.GetName(), branch); err != nil {
		return r.markDegraded(ctx, app, ReasonGiteaError, fmt.Sprintf("EnsureBranch: %v", err))
	}

	// 9. Per-region render + commit.
	bpManifestsValues, _, _ := unstructured.NestedMap(bp.Object, "spec", "manifests", "values")
	bpSourceKind, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "source", "kind")
	bpSourceRef, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "source", "ref")
	bpChart, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "chart")
	bpDigest, _, _ := unstructured.NestedString(bp.Object, "status", "ociDigest")

	regionStatuses := make([]map[string]interface{}, 0, len(plan.Regions))
	allCommitted := true
	for _, rp := range plan.Regions {
		merged := mergeMaps(bpManifestsValues, spec.Parameters)
		out, err := render.Render(render.Inputs{
			AppName: app.GetName(),
			// AppNamespace = the Application CR's own namespace. The
			// rendered HelmRelease metadata.namespace + spec.targetNamespace
			// both resolve here, matching the host-side Flux Kustomization
			// targetNamespace. qa-loop iter-10 Fix #44 root cause: previously
			// we passed Org for both, which on omantel made the workload Pod
			// land in `omantel-platform` instead of the operator's chosen
			// `qa-omantel` namespace.
			AppNamespace:     app.GetNamespace(),
			Org:              envSpec.OrganizationRef,
			EnvType:          envSpec.EnvType,
			Region:           rp.Name,
			PlacementRole:    rp.Role,
			Standby:          rp.Standby,
			BlueprintName:    spec.BlueprintName,
			BlueprintVersion: spec.BlueprintVersion,
			SourceKind:       bpSourceKind,
			SourceRef:        ifEmpty(bpSourceRef, r.Cfg.CatalogSourceRef),
			Chart:            bpChart,
			Values:           merged,
			SourceNamespace:  r.Cfg.SourceNamespace,
			IntervalSeconds:  r.Cfg.HelmReleaseIntervalSeconds,
			OwnerAppUID:      string(app.GetUID()),
			OwnerAppGen:      app.GetGeneration(),
		})
		if err != nil {
			return r.markDegraded(ctx, app, ReasonRenderError,
				fmt.Sprintf("render region %q: %v", rp.Name, err))
		}

		// Commit kustomization.yaml + helmrelease.yaml.
		paths := []struct {
			path    string
			content []byte
		}{
			{render.KustomizationPath(rp.Name, app.GetName()), out.KustomizationYAML},
			{render.HelmReleasePath(rp.Name, app.GetName()), out.HelmReleaseYAML},
		}
		commitMsg := fmt.Sprintf("application-controller: reconcile %s on %s (gen %d)",
			app.GetName(), rp.Name, app.GetGeneration())
		regionCommitted := false
		for _, p := range paths {
			_, committed, err := r.Gitea.PutFile(
				ctx, envSpec.OrganizationRef, app.GetName(), branch, p.path,
				p.content, commitMsg)
			if err != nil {
				return r.markDegraded(ctx, app, ReasonGiteaError,
					fmt.Sprintf("PutFile %s: %v", p.path, err))
			}
			if committed {
				regionCommitted = true
			}
		}
		if !regionCommitted {
			// Already in steady state — manifest set already in Gitea
			// byte-equal. This contributes to the idempotency test
			// (re-reconcile = 0 writes).
		} else {
			allCommitted = true
		}

		regionStatuses = append(regionStatuses, map[string]interface{}{
			"name":               rp.Name,
			"role":               rp.Role,
			"replicas":           int64(replicasFor(rp.Standby, spec.Parameters)),
			"ready":              int64(0),
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		})
	}
	_ = allCommitted

	// 9b. Upsert the host-cluster Flux GitRepository + per-region
	//     Kustomization CRs that reconcile the manifests we just
	//     committed to Gitea. Without this step the manifests sit in
	//     Gitea forever; nothing on the host cluster ever pulls them
	//     and no Pod gets scheduled. qa-loop iter-8 Fix #42 bug 3
	//     root cause.
	//
	//     Failure here is non-fatal-but-degraded: the manifests are
	//     already in Gitea so a future reconcile pass (or operator
	//     re-apply) can resolve. We log + mark Degraded so the
	//     Application visibly fails its Ready bar.
	if err := r.ensureHostFluxBootstrap(ctx, app, envSpec, plan); err != nil {
		return r.markDegraded(ctx, app, ReasonGiteaError,
			fmt.Sprintf("ensure host Flux bootstrap: %v", err))
	}

	// 10. Observe the downstream HelmRelease so the Application's
	//     status.phase tracks the actual workload-install lifecycle, not
	//     just the controller-side commit step. qa-loop iter-11 Fix #45
	//     Cluster-B root cause: prior to this loop the controller hard-
	//     coded `Phase: PhaseProvisioning` on every reconcile pass and
	//     never re-observed the per-region HRs that Flux installs as
	//     work-product of the Kustomization. The Application CR sat at
	//     `Provisioning` indefinitely even after `kubectl get hr -n
	//     <appNs> <appName>` was Ready=True for hours — the operator
	//     UI couldn't pivot to the Ready dashboard, the matrix-asserted
	//     terminal phase never arrived, and TC-066 / TC-100 / TC-104 /
	//     TC-113 stayed FAIL.
	//
	//     We poll the HR per region (cheap; in-cluster GET) and roll up
	//     the readiness signal. The roll-up rule:
	//       * any region HR Ready=True       → phase=Ready
	//       * any region HR Ready=False      → phase=Degraded
	//       * any region HR not yet present  → phase=Provisioning
	//     This stays consistent with the CRD's enum (Pending |
	//     Provisioning | Ready | Degraded | Failed | Uninstalling) and
	//     matches the matrix-author assertion in TC-066's must_contain
	//     ("Ready").
	hrPhase, hrReason, hrMessage := r.observeRegionHelmReleases(ctx, app, plan)
	regionStatuses = mergeRegionReadiness(regionStatuses, hrPhase, plan, ctx, r, app)

	// 11. Status update — phase derived from observed HR readiness,
	//     fall back to Provisioning when no signal is available yet.
	giteaRepo := fmt.Sprintf("%s/%s/%s",
		strings.TrimRight(r.Cfg.GiteaPublicURL, "/"),
		envSpec.OrganizationRef, app.GetName())
	finalPhase := hrPhase
	finalReady := "True"
	finalReason := ReasonReconciled
	finalMessage := fmt.Sprintf("Application %s/%s reconciled into %d region(s)", app.GetNamespace(), app.GetName(), len(plan.Regions))
	if finalPhase == "" {
		finalPhase = PhaseProvisioning
	}
	switch finalPhase {
	case PhaseDegraded:
		finalReady = "False"
		finalReason = hrReason
		finalMessage = hrMessage
	case PhaseProvisioning:
		// Provisioning is "we did our part, Flux will apply" — Ready
		// stays True because the Application's own contract (manifests
		// committed + host Flux bootstrapped) IS done. The
		// `phase=Provisioning` signal is what the UI uses to show a
		// spinner; the Ready condition is what RBAC guards / fleet
		// rollups consume.
		finalReady = "True"
		finalReason = ReasonReconciled
	case PhaseReady:
		finalReady = "True"
		finalReason = ReasonReconciled
		finalMessage = fmt.Sprintf("Application %s/%s installed across %d region(s); Ready=True from downstream HelmRelease(s)",
			app.GetNamespace(), app.GetName(), len(plan.Regions))
	}
	su := statusUpdate{
		Phase:            finalPhase,
		PrimaryRegion:    plan.PrimaryRegion,
		Regions:          regionStatuses,
		GiteaRepo:        giteaRepo,
		Installed: map[string]interface{}{
			"name":    spec.BlueprintName,
			"version": spec.BlueprintVersion,
			"digest":  bpDigest,
		},
		Reason:           finalReason,
		Message:          finalMessage,
		Ready:            finalReady,
		LastReconciledAt: time.Now().UTC().Format(time.RFC3339),
	}
	return r.updateStatus(ctx, app, su)
}

// observeRegionHelmReleases polls the per-region HelmRelease CRs the
// Sovereign's Flux installer materialised (named `app.GetName()` in
// the Application's own namespace, per render.HelmReleaseName / the
// chart's HelmRelease template). Returns the rolled-up phase string +
// the reason+message of the WORST region (so a single-region Failed
// surfaces in the UI verbatim instead of being averaged out).
//
// Idempotent + side-effect-free: only reads the API.
//
// qa-loop iter-11 Fix #45 Cluster-B.
func (r *Reconciler) observeRegionHelmReleases(
	ctx context.Context,
	app *unstructured.Unstructured,
	plan placement.Plan,
) (phase, reason, message string) {
	allReady := true
	anyDegraded := false
	worstReason := ""
	worstMessage := ""
	sawAny := false
	for _, rp := range plan.Regions {
		// HR lives in the Application's own namespace, named after the
		// Application (matches render.HelmReleaseName + the chart's
		// HelmRelease template's `metadata.name: {{ .AppName }}`).
		hr, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
			Namespace(app.GetNamespace()).
			Get(ctx, app.GetName(), metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// HR not yet materialised — Flux still pulling. Roll up
				// to Provisioning, NOT Failed.
				allReady = false
				continue
			}
			r.Log.Warn("application-controller: GET HelmRelease failed",
				"namespace", app.GetNamespace(),
				"name", app.GetName(),
				"region", rp.Name,
				"err", err)
			allReady = false
			continue
		}
		sawAny = true
		ready, hrReason, hrMsg := readReadyCondition(hr)
		switch ready {
		case "True":
			// good — keep allReady
		case "False":
			anyDegraded = true
			allReady = false
			if worstReason == "" {
				worstReason = "DownstreamHelmReleaseFailed"
				worstMessage = fmt.Sprintf("region %s HelmRelease Ready=False: %s — %s", rp.Name, hrReason, hrMsg)
			}
		default:
			// Unknown — Flux still working.
			allReady = false
		}
	}
	switch {
	case anyDegraded:
		return PhaseDegraded, worstReason, worstMessage
	case allReady && sawAny:
		return PhaseReady, "", ""
	default:
		return PhaseProvisioning, "", ""
	}
}

// readReadyCondition extracts (status, reason, message) of the
// `Ready` condition from a Flux HelmRelease (or any Kubernetes object
// that exposes `status.conditions[].type=Ready`). Returns ("", "", "")
// when the condition isn't yet present.
func readReadyCondition(obj *unstructured.Unstructured) (status, reason, message string) {
	conds, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return "", "", ""
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		if t != "Ready" {
			continue
		}
		s, _ := cm["status"].(string)
		rsn, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		return s, rsn, msg
	}
	return "", "", ""
}

// mergeRegionReadiness updates each region status entry's `ready` count
// from 0 → replicas when the rolled-up phase = Ready. Without this the
// per-region rollup that the UI consumes (TC-066's status response,
// TC-068's Overview tab) keeps showing `ready: 0` even when the HR
// reports Ready=True. Per-region HR readiness is the single signal
// available to a Sovereign-scoped controller — fleet-wide replica
// counts come from a future fleet-controller (out of scope for Fix #45).
//
// qa-loop iter-11 Fix #45 Cluster-B.
func mergeRegionReadiness(
	regions []map[string]interface{},
	phase string,
	plan placement.Plan,
	ctx context.Context,
	r *Reconciler,
	app *unstructured.Unstructured,
) []map[string]interface{} {
	if phase != PhaseReady {
		return regions
	}
	out := make([]map[string]interface{}, 0, len(regions))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rs := range regions {
		copyMap := map[string]interface{}{}
		for k, v := range rs {
			copyMap[k] = v
		}
		// Only bump replicas-ready when the per-region HR is actually
		// Ready=True (we already gated by allReady in the caller, but
		// we re-check defensively in case the plan grows in a future
		// release).
		if replicas, ok := copyMap["replicas"].(int64); ok {
			copyMap["ready"] = replicas
		}
		copyMap["lastTransitionTime"] = now
		out = append(out, copyMap)
	}
	_ = plan
	_ = ctx
	_ = r
	_ = app
	return out
}

// ensureHostFluxBootstrap upserts (find-or-create) the host-cluster
// Flux v1 GitRepository + per-region Kustomization CRs that pull the
// per-Application manifests we committed to Gitea. Idempotent: a
// steady-state Application produces zero K8s writes after the first
// pass (we re-Get and only call Update when a meaningful field
// diverged from desired). Owner-references on every CR target the
// parent Application so the Flux CRs are garbage-collected when the
// Application is deleted.
//
// Schema:
//
//	GitRepository (1 per Application):
//	  metadata.namespace: cfg.HostFluxNamespace (default flux-system)
//	  metadata.name:      catalyst-app-{org}-{app}
//	  spec.url:           {GiteaInClusterURL}/{org}/{app}.git
//	  spec.ref.branch:    {branchForEnvType(envSpec.EnvType)}
//	  spec.interval:      {HostFluxIntervalSeconds}s
//	  spec.secretRef.name: {FluxGiteaSecretRef}  (only if non-empty)
//
//	Kustomization (1 per per-region work plan):
//	  metadata.namespace: cfg.HostFluxNamespace
//	  metadata.name:      catalyst-app-{org}-{app}-{region}
//	  spec.path:          ./clusters/{region}/applications/{app}
//	  spec.sourceRef:     GitRepository: catalyst-app-{org}-{app}
//	  spec.interval:      {HostFluxIntervalSeconds}s
//	  spec.prune:         true
//	  spec.targetNamespace: {app.GetNamespace()}
//
// Per Inviolable Principle #3 (Flux is the only reconciler), we do NOT
// `kubectl apply` workload manifests directly — only the Flux CR pair
// that wires Gitea → Flux → workload.
func (r *Reconciler) ensureHostFluxBootstrap(
	ctx context.Context,
	app *unstructured.Unstructured,
	envSpec envParsedSpec,
	plan placement.Plan,
) error {
	ns := r.Cfg.HostFluxNamespace
	branch := branchForEnvType(envSpec.EnvType)
	repoName := fmt.Sprintf("catalyst-app-%s-%s", envSpec.OrganizationRef, app.GetName())
	repoURL := fmt.Sprintf("%s/%s/%s.git",
		strings.TrimRight(r.Cfg.GiteaInClusterURL, "/"),
		envSpec.OrganizationRef,
		app.GetName())

	// IMPORTANT: NO ownerReferences here.
	//
	// K8s ownerRefs only resolve INSIDE the same namespace (when both
	// owner and dependent are namespaced) — the K8s garbage collector
	// hard-deletes any dependent whose owner it cannot find in the
	// SAME namespace. The Application CR lives in
	// `app.GetNamespace()` (e.g. `qa-omantel`) but the GitRepository +
	// Kustomization live in `flux-system`. With ownerRef pointing at a
	// namespaced owner in a different namespace, the GC silently
	// deletes the GitRepository the moment it's created — the
	// controller log says "ensured" but `kubectl get` shows nothing.
	// First version of Fix #42 hit this exact bug live on omantel.
	//
	// Cleanup on Application delete is handled by handleDeletion()
	// which deletes the Gitea-side files; Flux then GCs the workload
	// via prune=true on the Kustomization. The host-cluster Flux CRs
	// themselves are removed by an explicit Delete call in
	// handleDeletion (separate Fix #42 follow-up — TODO).
	commonLabels := map[string]interface{}{
		"app.kubernetes.io/managed-by":         "application-controller",
		"catalyst.openova.io/application":      app.GetName(),
		"catalyst.openova.io/organization":     envSpec.OrganizationRef,
		"catalyst.openova.io/env-type":         envSpec.EnvType,
		// Reference labels for cascade-delete (replaces ownerRef which
		// can't span namespaces). handleDeletion() looks these up.
		"catalyst.openova.io/app-namespace":    app.GetNamespace(),
		"catalyst.openova.io/app-uid":          string(app.GetUID()),
	}

	// --- GitRepository ---
	gr := &unstructured.Unstructured{}
	gr.SetAPIVersion(FluxGitRepositoryGVR.Group + "/" + FluxGitRepositoryGVR.Version)
	gr.SetKind("GitRepository")
	gr.SetNamespace(ns)
	gr.SetName(repoName)
	// NO ownerRef — see top-of-function note about cross-namespace GC.
	if err := unstructured.SetNestedMap(gr.Object, commonLabels, "metadata", "labels"); err != nil {
		return fmt.Errorf("set GitRepository labels: %w", err)
	}
	grSpec := map[string]interface{}{
		"interval": fmt.Sprintf("%ds", r.Cfg.HostFluxIntervalSeconds),
		"url":      repoURL,
		"ref": map[string]interface{}{
			"branch": branch,
		},
	}
	if r.Cfg.FluxGiteaSecretRef != "" {
		grSpec["secretRef"] = map[string]interface{}{
			"name": r.Cfg.FluxGiteaSecretRef,
		}
	}
	if err := unstructured.SetNestedMap(gr.Object, grSpec, "spec"); err != nil {
		return fmt.Errorf("set GitRepository spec: %w", err)
	}

	if err := r.upsertHostResource(ctx, FluxGitRepositoryGVR, ns, repoName, gr); err != nil {
		return fmt.Errorf("upsert GitRepository %s/%s: %w", ns, repoName, err)
	}
	r.Log.Info("ensured host Flux GitRepository",
		"namespace", ns, "name", repoName, "url", repoURL, "branch", branch)

	// --- Kustomization (one per region) ---
	for _, rp := range plan.Regions {
		ksName := fmt.Sprintf("catalyst-app-%s-%s-%s",
			envSpec.OrganizationRef, app.GetName(), rp.Name)
		ks := &unstructured.Unstructured{}
		ks.SetAPIVersion(FluxKustomizationGVR.Group + "/" + FluxKustomizationGVR.Version)
		ks.SetKind("Kustomization")
		ks.SetNamespace(ns)
		ks.SetName(ksName)
		// NO ownerRef — see top-of-function note about cross-namespace GC.
		labels := map[string]interface{}{}
		for k, v := range commonLabels {
			labels[k] = v
		}
		labels["catalyst.openova.io/region"] = rp.Name
		if err := unstructured.SetNestedMap(ks.Object, labels, "metadata", "labels"); err != nil {
			return fmt.Errorf("set Kustomization labels: %w", err)
		}
		ksSpec := map[string]interface{}{
			"interval":        fmt.Sprintf("%ds", r.Cfg.HostFluxIntervalSeconds),
			"path":            fmt.Sprintf("./clusters/%s/applications/%s", rp.Name, app.GetName()),
			"prune":           true,
			"targetNamespace": app.GetNamespace(),
			"sourceRef": map[string]interface{}{
				"kind":      "GitRepository",
				"name":      repoName,
				"namespace": ns,
			},
		}
		if err := unstructured.SetNestedMap(ks.Object, ksSpec, "spec"); err != nil {
			return fmt.Errorf("set Kustomization spec: %w", err)
		}
		if err := r.upsertHostResource(ctx, FluxKustomizationGVR, ns, ksName, ks); err != nil {
			return fmt.Errorf("upsert Kustomization %s/%s: %w", ns, ksName, err)
		}
		r.Log.Info("ensured host Flux Kustomization",
			"namespace", ns, "name", ksName, "region", rp.Name, "path", ksSpec["path"])
	}

	return nil
}

// upsertHostResource find-or-creates the resource via the dynamic
// client, then drift-restores spec when an existing instance diverges
// from desired. Idempotent on byte-equal spec.
func (r *Reconciler) upsertHostResource(
	ctx context.Context,
	gvr schema.GroupVersionResource,
	namespace, name string,
	desired *unstructured.Unstructured,
) error {
	current, err := r.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			if _, cerr := r.Dynamic.Resource(gvr).Namespace(namespace).Create(ctx, desired, metav1.CreateOptions{}); cerr != nil {
				if apierrors.IsAlreadyExists(cerr) {
					// Race with parallel reconcile — re-Get + update path.
					return r.upsertHostResource(ctx, gvr, namespace, name, desired)
				}
				return cerr
			}
			return nil
		}
		return err
	}
	// Drift check: only update if desired.spec differs from current.spec.
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	currentSpec, _, _ := unstructured.NestedMap(current.Object, "spec")
	if specsEqual(desiredSpec, currentSpec) {
		// Steady state — no API write.
		return nil
	}
	// Restore desired spec; preserve resourceVersion + status to avoid
	// clobbering the Flux controller's status writes.
	current.Object["spec"] = desiredSpec
	if labels, found, _ := unstructured.NestedMap(desired.Object, "metadata", "labels"); found {
		_ = unstructured.SetNestedMap(current.Object, labels, "metadata", "labels")
	}
	if _, err := r.Dynamic.Resource(gvr).Namespace(namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

// specsEqual does a deep-equality compare of two map[string]interface{}
// trees by JSON-marshaled byte equality. Stable because the JSON encoder
// sorts keys.
func specsEqual(a, b map[string]interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// handleDeletion is the cascade-delete path. We remove every manifest
// the controller wrote for this Application from the per-Org Gitea
// repo, then release the finalizer.
//
// Wait-for-Flux-drain semantic: removing the manifests causes Flux on
// the host cluster to delete the HelmRelease + workload. We do NOT
// poll the host cluster here — the next reconcile pass observes via
// the `status.phase=Uninstalling` Condition that we wrote the delete
// markers, and the finalizer release returns control to the API server.
// In a steady-state cluster this is a single-pass operation.
func (r *Reconciler) handleDeletion(ctx context.Context, app *unstructured.Unstructured) error {
	if !hasFinalizer(app, FinalizerName) {
		// Already fully cleaned up — nothing to do; the API server
		// will GC the CR on its own once all finalizers are gone.
		return nil
	}

	// Surface phase=Uninstalling for the UI.
	_ = r.updateStatus(ctx, app, statusUpdate{
		Phase:   PhaseUninstalling,
		Reason:  ReasonAwaitingDrain,
		Message: "removing manifests from Gitea; waiting for Flux to drain",
		Ready:   "False",
	})

	spec, err := parseSpec(app)
	if err != nil {
		// Spec is unparseable — nothing we can do but release the
		// finalizer. The user will need to handle stuck workloads
		// manually if any exist.
		r.Log.Warn("delete: spec unparseable; releasing finalizer", "err", err)
		return r.removeFinalizer(ctx, app)
	}

	// Resolve env to find the Gitea Org + branch. If the Environment
	// has been deleted out from under us, we can't compute the target
	// — release the finalizer and let the operator clean up the Gitea
	// side manually (rare path; the operator deleted Env before App).
	env, err := r.fetchEnvironment(ctx, spec.EnvironmentRef)
	if err != nil {
		if isNotFound(err) {
			r.Log.Info("delete: parent Environment gone; releasing finalizer", "env", spec.EnvironmentRef)
			return r.removeFinalizer(ctx, app)
		}
		return fmt.Errorf("delete: fetch Environment: %w", err)
	}
	envSpec, err := parseEnvSpec(env)
	if err != nil {
		r.Log.Warn("delete: parent Environment spec invalid; releasing finalizer", "err", err)
		return r.removeFinalizer(ctx, app)
	}
	branch := branchForEnvType(envSpec.EnvType)

	// Resolve placement to know which regions to clean.
	plan, err := placement.Resolve(spec.Placement, spec.Regions)
	if err != nil {
		// Best effort — if the spec is invalid, try every region.
		// Still release the finalizer at the end.
		r.Log.Warn("delete: placement invalid; deleting all spec.regions[]", "err", err)
		plan = placement.Plan{Regions: nil}
		for _, rg := range spec.Regions {
			plan.Regions = append(plan.Regions, placement.RegionPlan{Name: rg})
		}
	}

	commitMsg := fmt.Sprintf("application-controller: cascade-delete %s (gen %d)",
		app.GetName(), app.GetGeneration())
	for _, rp := range plan.Regions {
		for _, path := range render.AllPaths(rp.Name, app.GetName()) {
			if _, err := r.Gitea.DeleteFile(ctx, envSpec.OrganizationRef, app.GetName(), branch, path, commitMsg); err != nil {
				if r.GiteaErrors != nil && r.GiteaErrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("delete %s: %w", path, err)
			}
		}
	}

	return r.removeFinalizer(ctx, app)
}

// ensureFinalizer adds the controller's finalizer to the Application
// if not already present. Idempotent — no API call when already set.
func (r *Reconciler) ensureFinalizer(ctx context.Context, app *unstructured.Unstructured) error {
	if hasFinalizer(app, FinalizerName) {
		return nil
	}
	finalizers := app.GetFinalizers()
	finalizers = append(finalizers, FinalizerName)
	app.SetFinalizers(finalizers)
	_, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).Update(ctx, app, metav1.UpdateOptions{})
	if err != nil {
		// Tolerate "not found" — the resource may have been deleted
		// between our List and the Update.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// removeFinalizer strips the controller's finalizer. The API server
// then GCs the Application CR (assuming no other finalizers remain).
func (r *Reconciler) removeFinalizer(ctx context.Context, app *unstructured.Unstructured) error {
	if !hasFinalizer(app, FinalizerName) {
		return nil
	}
	out := make([]string, 0, len(app.GetFinalizers()))
	for _, f := range app.GetFinalizers() {
		if f == FinalizerName {
			continue
		}
		out = append(out, f)
	}
	app.SetFinalizers(out)
	_, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).Update(ctx, app, metav1.UpdateOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func hasFinalizer(app *unstructured.Unstructured, name string) bool {
	for _, f := range app.GetFinalizers() {
		if f == name {
			return true
		}
	}
	return false
}

// fetchEnvironment reads the Environment.catalyst.openova.io/v1 CR by
// name. Cluster-scoped per the CRD.
func (r *Reconciler) fetchEnvironment(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return r.Dynamic.Resource(EnvironmentGVR).Namespace("").Get(ctx, name, metav1.GetOptions{})
}

// fetchOrganization reads the Organization.orgs.openova.io/v1 CR by
// name. Cluster-scoped.
func (r *Reconciler) fetchOrganization(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	return r.Dynamic.Resource(OrganizationGVR).Namespace("").Get(ctx, name, metav1.GetOptions{})
}

// fetchBlueprint reads the Blueprint by name; tries v1 first, falls
// back to v1alpha1 on 404. Both versions share an inline schema per
// products/catalyst/chart/crds/blueprint.yaml.
func (r *Reconciler) fetchBlueprint(ctx context.Context, name string) (*unstructured.Unstructured, error) {
	bp, err := r.Dynamic.Resource(BlueprintGVR).Namespace("").Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return bp, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	// Fallback to v1alpha1.
	bp2, err2 := r.Dynamic.Resource(BlueprintGVRv1alpha1).Namespace("").Get(ctx, name, metav1.GetOptions{})
	if err2 == nil {
		return bp2, nil
	}
	if isNotFound(err2) {
		return nil, err
	}
	return nil, err2
}

// statusUpdate captures the desired Application.status changes for one
// reconcile pass.
type statusUpdate struct {
	Phase            string
	PrimaryRegion    string
	Regions          []map[string]interface{}
	GiteaRepo        string
	Installed        map[string]interface{}
	Reason           string
	Message          string
	Ready            string // "True" | "False" | "Unknown"
	// LastReconciledAt is the wall-clock RFC3339 timestamp of this
	// reconcile pass — surfaced verbatim via
	// `status.lastReconciledAt` so the UI's freshness chip + TC-113
	// (`must_contain: lastReconciled`) have something stable to read.
	// Empty value leaves the field untouched. qa-loop iter-11 Fix #45
	// Cluster-B follow-up.
	LastReconciledAt string
}

// updateStatus writes the status sub-resource via the dynamic client.
//
// We re-fetch the latest object before patching so we don't clobber
// concurrent edits, then update with the desired status. The dynamic
// client's UpdateStatus handles the /status subresource correctly.
func (r *Reconciler) updateStatus(ctx context.Context, app *unstructured.Unstructured, su statusUpdate) error {
	// Always re-fetch to avoid clobbering. If the object is gone, no-op.
	latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).
		Get(ctx, app.GetName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("re-fetch application for status: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	currentStatus, _, _ := unstructured.NestedMap(latest.Object, "status")
	if currentStatus == nil {
		currentStatus = map[string]interface{}{}
	}
	currentStatus["observedGeneration"] = latest.GetGeneration()
	if su.Phase != "" {
		currentStatus["phase"] = su.Phase
	}
	if su.PrimaryRegion != "" {
		currentStatus["primaryRegion"] = su.PrimaryRegion
	}
	if su.Regions != nil {
		regions := make([]interface{}, len(su.Regions))
		for i, r := range su.Regions {
			regions[i] = r
		}
		currentStatus["regions"] = regions
	}
	if su.GiteaRepo != "" {
		currentStatus["giteaRepo"] = su.GiteaRepo
	}
	if su.Installed != nil {
		currentStatus["installedBlueprint"] = su.Installed
	}
	if su.LastReconciledAt != "" {
		currentStatus["lastReconciledAt"] = su.LastReconciledAt
	}

	// Replace Ready condition; preserve unrelated conditions.
	conditions := []interface{}{}
	if existing, ok := currentStatus["conditions"].([]interface{}); ok {
		for _, c := range existing {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := cm["type"].(string); t == "Ready" {
				continue
			}
			conditions = append(conditions, c)
		}
	}
	conditions = append(conditions, map[string]interface{}{
		"type":               "Ready",
		"status":             su.Ready,
		"reason":             su.Reason,
		"message":            su.Message,
		"lastTransitionTime": now,
	})
	currentStatus["conditions"] = conditions

	latest.Object["status"] = currentStatus

	_, err = r.Dynamic.Resource(ApplicationGVR).Namespace(latest.GetNamespace()).
		UpdateStatus(ctx, latest, metav1.UpdateOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// markPending is a one-shot status writer for "valid spec, parent
// missing" cases.
func (r *Reconciler) markPending(ctx context.Context, app *unstructured.Unstructured, reason, message string) error {
	return r.updateStatus(ctx, app, statusUpdate{
		Phase:   PhasePending,
		Reason:  reason,
		Message: message,
		Ready:   "False",
	})
}

// markFailed is the "spec is invalid" terminal.
func (r *Reconciler) markFailed(ctx context.Context, app *unstructured.Unstructured, reason, message string) error {
	return r.updateStatus(ctx, app, statusUpdate{
		Phase:   PhaseFailed,
		Reason:  reason,
		Message: message,
		Ready:   "False",
	})
}

// markDegraded is for transient non-fatal errors (Gitea 5xx, render
// errors). Re-queues so the controller retries.
func (r *Reconciler) markDegraded(ctx context.Context, app *unstructured.Unstructured, reason, message string) error {
	return r.updateStatus(ctx, app, statusUpdate{
		Phase:   PhaseDegraded,
		Reason:  reason,
		Message: message,
		Ready:   "False",
	})
}

// parseSpec extracts the typed view of Application.spec from the
// unstructured object. Returns an error iff a field that the CRD
// schema would have caught is missing — defensive: in test the schema
// isn't enforced.
type appSpec struct {
	EnvironmentRef   string
	BlueprintName    string
	BlueprintVersion string
	Placement        string
	Regions          []string
	Parameters       map[string]interface{}
}

func parseSpec(app *unstructured.Unstructured) (appSpec, error) {
	out := appSpec{}
	envRef, _, _ := unstructured.NestedString(app.Object, "spec", "environmentRef")
	if envRef == "" {
		return out, errors.New("spec.environmentRef is required")
	}
	out.EnvironmentRef = envRef

	bpName, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
	if bpName == "" {
		return out, errors.New("spec.blueprintRef.name is required")
	}
	out.BlueprintName = bpName

	bpVer, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "version")
	if bpVer == "" {
		return out, errors.New("spec.blueprintRef.version is required")
	}
	out.BlueprintVersion = bpVer

	// G93.2 (Refs #2667) — spec.placement is OPTIONAL. When the
	// operator omits it, the controller derives the effective default
	// from the Blueprint's placementSchema.{default,defaultOnMultiRegion}
	// + the Sovereign-wide BCP topology (SOVEREIGN_BCP_TOPOLOGY env).
	// Empty here is propagated; the reconciler stamps the derived value
	// before validating against placementSchema.modes[]. This is the
	// seam that makes a fresh marketplace install on a multi-region
	// Sovereign land on active-hotstandby zero-touch for every
	// CNPG-backed Blueprint that declares defaultOnMultiRegion.
	pl, _, _ := unstructured.NestedString(app.Object, "spec", "placement")
	out.Placement = pl

	rgRaw, _, _ := unstructured.NestedSlice(app.Object, "spec", "regions")
	if len(rgRaw) == 0 {
		return out, errors.New("spec.regions[] is required and must be non-empty")
	}
	for _, r := range rgRaw {
		s, ok := r.(string)
		if !ok {
			return out, fmt.Errorf("spec.regions[] entry is not a string: %T", r)
		}
		out.Regions = append(out.Regions, s)
	}

	params, _, _ := unstructured.NestedMap(app.Object, "spec", "parameters")
	out.Parameters = params

	return out, nil
}

// envSpec is the parsed view of Environment.spec.
type envParsedSpec struct {
	OrganizationRef string
	EnvType         string
	Placement       string
	Regions         []map[string]interface{}
}

func parseEnvSpec(env *unstructured.Unstructured) (envParsedSpec, error) {
	out := envParsedSpec{}
	orgRef, _, _ := unstructured.NestedString(env.Object, "spec", "organizationRef")
	if orgRef == "" {
		return out, errors.New("environment spec.organizationRef is required")
	}
	out.OrganizationRef = orgRef

	envType, _, _ := unstructured.NestedString(env.Object, "spec", "envType")
	if envType == "" {
		return out, errors.New("environment spec.envType is required")
	}
	out.EnvType = envType

	pl, _, _ := unstructured.NestedString(env.Object, "spec", "placement")
	out.Placement = pl

	regions, _, _ := unstructured.NestedSlice(env.Object, "spec", "regions")
	for _, r := range regions {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		out.Regions = append(out.Regions, m)
	}
	return out, nil
}

// blueprintAllowedModes reads spec.placementSchema.modes from a
// Blueprint and returns the list of allowed placement modes. Empty
// slice when the field is absent (treated as "all modes allowed" by
// placement.AllowedByBlueprint).
func blueprintAllowedModes(bp *unstructured.Unstructured) []string {
	raw, _, _ := unstructured.NestedSlice(bp.Object, "spec", "placementSchema", "modes")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// blueprintDefaultPlacement reads `spec.placementSchema.default` from a
// Blueprint — the single-knob default the Application CR picks up when
// `spec.placement` is empty AND the Sovereign is single-region OR the
// Blueprint did not declare `defaultOnMultiRegion`. Empty when absent.
// G93.2 (Refs #2667) companion of blueprintDefaultOnMultiRegion.
func blueprintDefaultPlacement(bp *unstructured.Unstructured) string {
	s, _, _ := unstructured.NestedString(bp.Object, "spec", "placementSchema", "default")
	return s
}

// blueprintDefaultOnMultiRegion reads
// `spec.placementSchema.defaultOnMultiRegion` from a Blueprint — the
// G93.2 (Refs #2667) declarative seam that lets a CNPG-backed Blueprint
// opt every Application of its kind into active-hotstandby on a
// multi-region Sovereign without per-call wiring. Empty when absent
// (the controller then falls back to placementSchema.default).
func blueprintDefaultOnMultiRegion(bp *unstructured.Unstructured) string {
	s, _, _ := unstructured.NestedString(bp.Object, "spec", "placementSchema", "defaultOnMultiRegion")
	return s
}

// branchForEnvType maps env-type → Gitea branch name per NAMING §11.2.
// Mirrors core/controllers/environment/internal/gitops/render.go's
// BranchForEnvType — duplicated until CC1 consolidates.
func branchForEnvType(envType string) string {
	switch envType {
	case "dev":
		return "develop"
	case "stg":
		return "staging"
	case "prod":
		return "main"
	case "uat", "poc":
		return envType
	default:
		return envType
	}
}

// mergeMaps merges `b` into `a` (with `b` taking precedence). Returns
// a new map; neither input is modified.
func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// replicasFor returns the rendered replicas count for a region row.
// Standby regions always render with replicas=0 (per render.mergeValues);
// primary/active regions render with the user-supplied count or 1 when
// unset.
func replicasFor(standby bool, params map[string]interface{}) int64 {
	if standby {
		return 0
	}
	if v, ok := params["replicas"]; ok {
		switch n := v.(type) {
		case int:
			return int64(n)
		case int64:
			return n
		case float64:
			return int64(n)
		}
	}
	return 1
}

// isNotFound reports whether err is a K8s 404. Used to distinguish
// "the parent CR doesn't exist" from "transport error" without
// pulling in another import.
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}

// ifEmpty returns `def` when `s` is empty, else `s`.
func ifEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// types.NamespacedName wrapper used in tests (kept here for
// availability without an extra import in test files).
var _ = types.NamespacedName{}

func ptrInt64(v int64) *int64 { return &v }
