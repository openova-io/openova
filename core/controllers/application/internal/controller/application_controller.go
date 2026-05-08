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

	"github.com/openova-io/openova/core/controllers/application/internal/placement"
	"github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/application/internal/semver"
	"github.com/openova-io/openova/core/controllers/application/internal/validate"
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
	EnsureRepo(ctx context.Context, org, repo string) error
	EnsureBranch(ctx context.Context, org, repo, branch string) error
	PutFile(ctx context.Context, org, repo, branch, path string, content []byte, message string) (sha string, committed bool, err error)
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
func (r *Reconciler) Run(ctx context.Context) error {
	if r.Dynamic == nil {
		return errors.New("controller: Dynamic client is required")
	}
	if err := r.initialList(ctx); err != nil {
		return fmt.Errorf("initial list: %w", err)
	}
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := r.watchOnce(ctx); err != nil {
			r.Log.Warn("application-controller: watch error; will retry", "err", err)
		}
		return false, nil
	})
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
	branch := branchForEnvType(envSpec.EnvType)
	if err := r.Gitea.EnsureRepo(ctx, envSpec.OrganizationRef, app.GetName()); err != nil {
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
			AppName:                    app.GetName(),
			Org:                        envSpec.OrganizationRef,
			EnvType:                    envSpec.EnvType,
			Region:                     rp.Name,
			PlacementRole:              rp.Role,
			Standby:                    rp.Standby,
			BlueprintName:              spec.BlueprintName,
			BlueprintVersion:           spec.BlueprintVersion,
			SourceKind:                 bpSourceKind,
			SourceRef:                  ifEmpty(bpSourceRef, r.Cfg.CatalogSourceRef),
			Chart:                      bpChart,
			Values:                     merged,
			SourceNamespace:            r.Cfg.SourceNamespace,
			IntervalSeconds:            r.Cfg.HelmReleaseIntervalSeconds,
			OwnerAppUID:                string(app.GetUID()),
			OwnerAppGen:                app.GetGeneration(),
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

	// 10. Status update.
	giteaRepo := fmt.Sprintf("%s/%s/%s",
		strings.TrimRight(r.Cfg.GiteaPublicURL, "/"),
		envSpec.OrganizationRef, app.GetName())
	su := statusUpdate{
		Phase:         PhaseProvisioning, // Flux still has to apply
		PrimaryRegion: plan.PrimaryRegion,
		Regions:       regionStatuses,
		GiteaRepo:     giteaRepo,
		Installed: map[string]interface{}{
			"name":    spec.BlueprintName,
			"version": spec.BlueprintVersion,
			"digest":  bpDigest,
		},
		Reason:  ReasonReconciled,
		Message: fmt.Sprintf("Application %s/%s reconciled into %d region(s)", app.GetNamespace(), app.GetName(), len(plan.Regions)),
		Ready:   "True",
	}
	return r.updateStatus(ctx, app, su)
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
	Phase         string
	PrimaryRegion string
	Regions       []map[string]interface{}
	GiteaRepo     string
	Installed     map[string]interface{}
	Reason        string
	Message       string
	Ready         string // "True" | "False" | "Unknown"
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

	pl, _, _ := unstructured.NestedString(app.Object, "spec", "placement")
	if pl == "" {
		return out, errors.New("spec.placement is required")
	}
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
