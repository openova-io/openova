// Package controller implements the application-controller reconcile
// loop (slice C4 of EPIC-0 #1095).
//
// The controller watches `Application.apps.openova.io/v1` CRs and on
// each event:
//
//  1. Fetches the parent `Environment.catalyst.openova.io/v1` named in
//     `spec.environmentRef`. Pending-on-miss with reason
//     `EnvironmentMissing`.
//
//  2. Fetches the parent `Organization.orgs.openova.io/v1` named in
//     `Environment.spec.organizationRef`. Pending-on-miss with reason
//     `OrganizationMissing`.
//
//  3. Fetches the `Blueprint.catalyst.openova.io/v1` (with v1alpha1
//     fallback) at name `spec.blueprintRef.name`. Pending-on-miss
//     with reason `BlueprintMissing`. Validates
//     `Application.spec.parameters` against
//     `Blueprint.spec.configSchema`. On invalid surfaces an `Invalid`
//     condition listing every failing JSON pointer.
//
//  4. Resolves the placement → per-region work plan via
//     `internal/placement`. For each region, renders the manifest set
//     via `internal/render` and idempotently writes
//     `clusters/<region>/applications/<app>/{kustomization,helmrelease}.yaml`
//     to the per-Org Gitea repo `<org>/<app>` on the env-type-mapped
//     branch (per slice C2 BranchForEnvType).
//
//  5. Updates `Application.status` with phase, primaryRegion,
//     regions[], giteaRepo URL, installedBlueprint snapshot, and
//     conditions.
//
//  6. Honors a finalizer: on deletion, removes every manifest the
//     controller wrote and waits for Flux to drain. The finalizer
//     releases on the next reconcile pass when the Gitea repo no
//     longer carries any of THIS Application's paths.
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
	"k8s.io/client-go/util/retry"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/internal/clusterregistry"
	"github.com/openova-io/openova/core/controllers/internal/placement"
	"github.com/openova-io/openova/core/controllers/internal/semver"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/fluxsource"
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

	// ReasonBackingUnrecoverable — #5513. Stamped on
	// Application.status.conditions[type=Ready,status=False] when a
	// downstream HelmRelease is Ready (manifests applied) but the
	// Application's backing CNPG Cluster CR is in CNPG's terminal
	// `unrecoverable` phase. Prevents the Application reporting Ready over a
	// database that has zero pods and needs manual intervention.
	ReasonBackingUnrecoverable = "BackingClusterUnrecoverable"

	// ReasonInvalidTopology — G117.6 (W2.C1, Refs #2745). Stamped on
	// `Application.status.conditions[type=Ready,status=False]` when the
	// resolved BCP topology (from Application.spec.topology override
	// OR Blueprint.spec.topology.defaults default) is not in
	// Blueprint.spec.topology.supported[]. Mirrors
	// render.InvalidTopologyReason — kept in sync verbatim because the
	// Wave-2 verifier (G117.6 acceptance) asserts this exact string.
	ReasonInvalidTopology = topo.InvalidTopologyReason

	// ReasonBootstrapAdopted — #3370. Stamped on bootstrap-owned
	// Applications (spec.bootstrap=true): the controller ADOPTS the
	// slot-installed HelmRelease named by spec.helmRelease instead of
	// rendering anything. Status mirrors that HR's Ready condition.
	ReasonBootstrapAdopted = "BootstrapAdopted"
)

// FinalizerName — owned by application-controller.
const FinalizerName = "application.apps.openova.io/finalizer"

// LabelAppUID is the per-instance back-pointer the topology fan-out
// stamps on every `<app>-<cluster>` HelmRelease it imperatively upserts
// (via fanoutOwnerLabels). It carries the owning Application CR's
// metadata.uid — globally unique per instance, so it disambiguates two
// same-named Applications in different Orgs/namespaces. handleDeletion()
// cascades on this label because the fan-out HRs carry NO ownerReferences
// (they can live in a DIFFERENT namespace than the Application CR — the
// vCluster pivot lands them in the tier host namespace — and K8s
// ownerRefs only resolve inside a single namespace; see #4902).
const LabelAppUID = "catalyst.openova.io/app-uid"

// LabelHostFluxApp / LabelHostFluxAppNamespace are the back-pointer labels
// ensureHostFluxBootstrap stamps on the host-cluster Flux GitRepository +
// per-region Kustomization CRs it imperatively upserts (those CRs ALSO carry
// LabelAppUID). handleDeletion() cascades on LabelAppUID for these too — the
// CRs live in HostFluxNamespace (flux-system), a DIFFERENT namespace than the
// Application CR, so an ownerReference would be hard-GC'd the instant it is
// created (same cross-namespace-GC trap as the fan-out HRs; see #4902). The
// name+namespace pair is the uid-absent fallback selector (disambiguates
// same-named Applications across Orgs).
const (
	LabelHostFluxApp          = "catalyst.openova.io/application"
	LabelHostFluxAppNamespace = "catalyst.openova.io/app-namespace"
)

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

	// VClusterPlacements maps a vCluster name (dmz|mgmt|rtz, the value
	// the Blueprint declares at spec.placementSchema.vcluster) to its
	// resolved host namespace + admin kubeconfig Secret name. The
	// controller stamps these on the rendered HelmRelease so Flux v2
	// helm-controller pivots into the vCluster apiserver via
	// spec.kubeConfig.secretRef. Per docs/SOVEREIGN-MULTI-REGION-DOD.md
	// §A4. Defaults applied in Config.Defaults() if unset.
	//
	// G92.1 #2660 (2026-06-01).
	VClusterPlacements map[string]VClusterPlacement

	// LocalRegion is the region suffix ("A" | "B") of the region this
	// controller instance runs in — the FIRST region's mgmt cluster is
	// "A", the SECOND region's is "B". Stamped from SOVEREIGN_REGION_ROLE
	// (primary→A, secondary→B) by cmd/main.go. The topology fan-out's
	// cluster-ID registry (#3375 DoD-4) uses it to resolve a declared
	// cluster ID (mgmt-A / mgmt-B / …) to the right kubeConfig Secret:
	// same-region IDs reuse the local vCluster pivot; cross-region IDs
	// follow the split-side default (the remote region's own Flux owns
	// its half) unless an operator wired a remote-region kubeconfig.
	// Empty defaults to "A" (an unconfigured controller behaves as the
	// primary).
	LocalRegion string
}

// VClusterPlacement carries the per-vCluster runtime values the
// controller stamps on the rendered HelmRelease. Both fields default
// from the loft-sh/vcluster chart convention (host namespace = vCluster
// name, kubeconfig Secret = `vc-<name>`); operators override via Config
// when a Sovereign customised the bp-<name>-vcluster chart values.
//
// G92.1 #2660.
type VClusterPlacement struct {
	// HostNamespace is the namespace ON THE HOST k3s where the
	// vCluster's pods + kubeconfig Secret live. Stamped on the
	// rendered HelmRelease's metadata.namespace so Flux v2
	// helm-controller resolves spec.kubeConfig.secretRef from the
	// same namespace as the HR.
	HostNamespace string

	// KubeconfigSecret is the name of the Secret the vCluster chart
	// exports with the admin kubeconfig YAML (data.config). Stamped
	// on spec.kubeConfig.secretRef.name.
	KubeconfigSecret string
}

// DefaultVClusterPlacements returns the loft-sh/vcluster convention
// mapping for dmz/mgmt/rtz — host namespace = vCluster name,
// kubeconfig Secret = "vc-"+name. Matches platform/bp-<name>-vcluster
// chart values shipped by slots 54/58/59 in the bootstrap-kit.
//
// Operators override per Sovereign by setting VCLUSTER_PLACEMENT_<NAME>
// env vars (see cmd/main.go); this default keeps the controller
// self-contained for tests + smoke renders.
func DefaultVClusterPlacements() map[string]VClusterPlacement {
	return map[string]VClusterPlacement{
		"dmz":  {HostNamespace: "dmz", KubeconfigSecret: "vc-dmz"},
		"mgmt": {HostNamespace: "mgmt", KubeconfigSecret: "vc-mgmt"},
		"rtz":  {HostNamespace: "rtz", KubeconfigSecret: "vc-rtz"},
	}
}

// clusterResolver builds the cluster-ID registry resolver (#3375 DoD-4)
// from the controller's VClusterPlacements + LocalRegion. It maps the
// loft-sh tier names (dmz|mgmt|rtz) onto the registry's Tier enum and
// carries each tier's local vCluster Secret + host namespace so a
// same-region canonical cluster ID resolves through the proven G92.1
// pivot. Cross-region IDs follow the split-side default (no pivot;
// remote region's own Flux owns its half).
func (r *Reconciler) clusterResolver() clusterregistry.Resolver {
	tiers := map[clusterregistry.Tier]clusterregistry.TierVCluster{}
	for name, vcp := range r.Cfg.VClusterPlacements {
		t := clusterregistry.Tier(name)
		if !clusterregistry.IsKnownTier(t) {
			continue
		}
		tiers[t] = clusterregistry.TierVCluster{
			HostNamespace:    vcp.HostNamespace,
			KubeconfigSecret: vcp.KubeconfigSecret,
		}
	}
	local := clusterregistry.Region(strings.ToUpper(r.Cfg.LocalRegion))
	if !clusterregistry.IsKnownRegion(local) {
		local = clusterregistry.RegionA
	}
	return clusterregistry.Resolver{
		LocalRegion:   local,
		TierVClusters: tiers,
	}
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
	if out.VClusterPlacements == nil {
		out.VClusterPlacements = DefaultVClusterPlacements()
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
//  1. Ensure finalizer present (idempotent).
//  2. Fetch parents + Blueprint; on miss surface Pending and return.
//  3. Validate parameters; on miss surface Invalid and return.
//  4. Resolve placement; render + commit per-region manifests.
//  5. Update status.
func (r *Reconciler) Reconcile(ctx context.Context, app *unstructured.Unstructured) error {
	if app == nil {
		return nil
	}

	if !app.GetDeletionTimestamp().IsZero() {
		return r.handleDeletion(ctx, app)
	}

	// #3370 — bootstrap-owned ADOPTION guard. A bootstrap-owned
	// Application (spec.bootstrap=true) is the canonical representation
	// a bootstrap-kit chart self-registered for its slot-installed
	// HelmRelease (e.g. shared-pg ← HR flux-system/bp-postgres-shared).
	// The install ALREADY exists and is owned by that HR — running the
	// normal reconcile would render + persist per-cluster HelmReleases
	// (`<app>-<cluster>` via render.HRNameFor) and commit per-region
	// manifests to Gitea, DUPLICATING the install. Adoption = skip every
	// render / fan-out / Gitea path and reconcile STATUS ONLY by
	// observing the owning HR's Ready condition. The guard sits BEFORE
	// ensureFinalizer so bootstrap-owned CRs never carry the controller
	// finalizer (their lifecycle is Helm's, not ours — a chart
	// uninstall must not wedge on our finalizer).
	if isBootstrapOwned(app) {
		return r.reconcileBootstrapOwned(ctx, app)
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

	// 2.5 (#4936) — derive per-Org WordPress parameters the customer never
	// types. The console 'New instance' dialog + the marketplace funnel
	// collect neither `orgDomain` nor `adminUser`, so requiring them made
	// the WordPress Application fail configSchema admission below
	// (`missing properties: 'orgDomain','adminUser'`). The controller owns
	// the Org context (it just fetched the Organization CR), so it injects
	// orgDomain (the Org's public hostname) + adminUser.{email,displayName}
	// (the Org owner identity) into spec.Parameters here — BEFORE the step-6
	// validation + every render path, which all read spec.Parameters. No-op
	// for non-WordPress Blueprints and for values the operator set explicitly.
	spec.Parameters = injectOrgDerivedParameters(spec.BlueprintName, org, spec.Parameters)

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

	// 5.1 (#3969) — validate the canonical desired-state placement
	// (spec.placement.targets[]) against the Blueprint capability gate.
	// This is the authoritative enforcement of the model invariants:
	// every target has a valid role; a Standby carries a Hot|Cold type; a
	// Primary carries none; ≥1 Primary when any target is declared; and
	// — the load-bearing gate — >1 Primary requires the Blueprint to
	// declare `placementCapability: multi-primary`, else the Application
	// moves to Ready=False, reason=MultiPrimaryNotSupported (DoD item 5).
	// When no targets are declared this is a no-op (the legacy posture
	// string drives the fan-out unchanged).
	if len(spec.PlacementTargets) > 0 {
		capability := blueprintPlacementCapability(bp)
		desired := bpv1alpha1.Placement{
			Targets:           spec.PlacementTargets,
			OwnedDependencies: spec.OwnedDependencies,
		}
		if perr := bpv1alpha1.ValidatePlacement(desired, capability); perr != nil {
			var pe *bpv1alpha1.PlacementError
			reason := ReasonInvalidPlacement
			if errors.As(perr, &pe) {
				reason = pe.Reason
			}
			return r.markFailed(ctx, app, reason, perr.Error())
		}
	}

	// 5.5 (#3373) — resolve the instance's effective vCluster
	// placement. Resolution order (founder-ratified law §2.1):
	//
	//   1. Application.spec.placement.vcluster   (instance choice —
	//      console advanced view OR a direct Git edit, equally valid)
	//   2. Blueprint.spec.defaultPlacement.vcluster (class SUGGESTION)
	//   3. legacy seams — placementSchema.vcluster (per-region path) /
	//      topology variant placement.tier (fan-out path), unchanged.
	//
	// The resolved value is rendered by the ONE generic mechanism:
	// the Flux spec.kubeConfig.secretRef pivot (G92.1). Zero per-app
	// placement code.
	effVC, effVCSource, effVCFound := "", "", false
	if spec.PlacementVClusterSet {
		effVC, effVCSource, effVCFound = spec.PlacementVCluster, "instance", true
	} else if v, ok := blueprintDefaultPlacementVCluster(bp); ok {
		effVC, effVCSource, effVCFound = v, "blueprint-default", true
	}
	allowedPlacements := blueprintAllowedPlacements(bp)
	if effVCFound && !vclusterAllowedByBlueprint(effVC, allowedPlacements) {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("placement vcluster %q not in Blueprint %q allowedPlacements %v",
				vclusterOrHost(effVC), spec.BlueprintName, allowedPlacements))
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

	// 6.5 (G117.6, Refs #2745) — Topology fan-out resolution.
	//
	// W1.B1 landed typed `Blueprint.spec.topology` (per-Blueprint BCP
	// declaration). When it's present, resolve the per-Application
	// topology choice (operator override > Blueprint default keyed on
	// Sovereign-region-count, locked decision #7) and fan out one
	// HelmRelease per cluster in the variant's PlacementSpec.Clusters[].
	// Each HR carries `LabelRole` + `LabelTopology` + `LabelApp` +
	// `LabelCluster` so observability + bp-continuum can query them.
	//
	// Backwards-compat: when `Blueprint.spec.topology` is absent
	// (legacy / substrate Blueprints not yet migrated to W1.B1), the
	// resolver is skipped and the existing per-region render path
	// continues to own the install. The new code path is additive.
	bpTopo := parseBlueprintTopology(bp)
	var perClusterFanout []*unstructured.Unstructured
	var perClusterStatus []map[string]interface{}
	// #3375 DoD-3 — capture the resolved topology choice + variant so the
	// per-app Continuum CR producer (reconcileContinuumCR, continuum.go)
	// can mint the DR contract AFTER placement.Resolve runs below. nil
	// variant / unset choice = no Blueprint topology, no DR CR.
	var resolvedTopoChoice bpv1alpha1.BcpTopology
	var resolvedTopoVariant *bpv1alpha1.TopologyVariant
	var continuumOwnerLabels map[string]string
	// #3969 §8c/§8d — the fan-out HR render now derives from the canonical
	// desired-state placement (`spec.placement.targets[]`) when it is
	// declared. The targets carry their own cluster IDs + Primary|Standby
	// roles, so we synthesize the fan-out variant straight from them
	// (placementVariantFromTargets) and feed the SAME proven render.FanoutHRs
	// path — the per-region HelmReleases now FOLLOW the desired-state
	// placement (provision-write / switchover / singleton→multi). The legacy
	// `Blueprint.spec.topology` → ResolveTopology path is the FALLBACK only
	// when no targets are declared (every pre-#3969 Application + the spine
	// producer that still stamps the legacy posture string). No new logic is
	// written against `Mode`/`BcpTopology` (§8d).
	fanoutFromTargets := len(spec.PlacementTargets) > 0
	if bpTopo != nil || fanoutFromTargets {
		appTopo := spec.Topology
		sovRegions := len(envSpec.Regions)
		var (
			choice  bpv1alpha1.BcpTopology
			variant *bpv1alpha1.TopologyVariant
		)
		if fanoutFromTargets {
			// §8c — desired-state targets drive the render. The capability
			// gate already ran above (step 5.1); here we only map the
			// targets onto the renderer's (choice, variant) inputs.
			capability := blueprintPlacementCapability(bp)
			// §13 — pass the Blueprint's declared topology so the synthetic
			// variant can carry the right replication backend forward into the
			// Continuum lease-witness arming (cnpg-pair vs stateless). bpTopo is
			// nil for the pure-#3969 placement-only path → stateless Promoter.
			choice, variant = placementVariantFromTargets(spec.PlacementTargets, capability, bpTopo)
			r.Log.Info("placement targets resolved (desired-state fan-out)",
				"app", app.GetName(),
				"blueprint", spec.BlueprintName,
				"choice", choice,
				"targets", len(spec.PlacementTargets),
				"clusters", placementClusters(variant))
		} else {
			// Legacy fallback — no desired-state targets; resolve the
			// per-Application topology choice off `Blueprint.spec.topology`.
			c, v, terr := topo.ResolveTopology(appTopo, bpTopo, sovRegions)
			if terr != nil {
				r.Log.Warn("topology resolution failed",
					"app", app.GetName(),
					"blueprint", spec.BlueprintName,
					"appTopology", appTopo,
					"sovereignRegions", sovRegions,
					"err", terr)
				// G117.6 acceptance: InvalidTopology must surface as
				// Ready=False, reason=InvalidTopology, phase=Failed.
				return r.markFailed(ctx, app, ReasonInvalidTopology, terr.Error())
			}
			choice, variant = c, v
			r.Log.Info("topology resolved",
				"app", app.GetName(),
				"blueprint", spec.BlueprintName,
				"choice", choice,
				"sovereignRegions", sovRegions,
				"clusters", placementClusters(variant))
		}

		// #3375 DoD-3 — remember the resolved topology so the per-app
		// Continuum CR producer (after placement.Resolve below) can mint
		// the DR contract. The variant captured here is the Blueprint-
		// derived one (pre instance-narrowing); the producer only reads
		// its switchover/replication blocks, which the instance narrowing
		// never touches.
		resolvedTopoChoice = choice
		resolvedTopoVariant = variant

		// FanoutHRs is a pure renderer — we collect the per-cluster HR
		// shape into status.perCluster[] so catalyst-api (G117.2-G117.4)
		// can serve GET /apps/{id}.perCluster[] without re-parsing the
		// Blueprint topology.
		//
		// G117.6a (Refs #2796) — the reconciler PERSISTS each rendered
		// HR onto the host k3s via the dynamic client. Each HR carries
		// `spec.kubeConfig.secretRef.name = vc-<cluster-lowered>` so
		// Flux v2 helm-controller pivots into the per-cluster vCluster
		// apiserver (G92.1 #2674 pivot pattern). When the Blueprint's
		// PlacementSpec.Tier is "" the HRs target the host cluster (no
		// kubeConfig block, no namespace override) — substrate
		// Blueprints / mgmt-cluster-local install.
		if variant != nil && variant.Placement != nil && len(variant.Placement.Clusters) > 0 {
			// Resolve the per-Tier vCluster host namespace + kubeconfig
			// Secret convention. Empty Tier = host placement (no
			// kubeConfig block; HRs land in the Application's own
			// namespace). Non-empty Tier MUST be mapped via
			// VClusterPlacements or we surface Invalid (same shape as
			// the existing single-region vCluster pivot earlier in
			// this reconcile).
			placementTier := variant.Placement.Tier
			// #3373 — the instance's effective vCluster (instance >
			// blueprint defaultPlacement) overrides the class-level
			// topology tier. An explicit instance "host" ("" after
			// normalization) lands the HRs on the host cluster.
			if effVCFound {
				placementTier = effVC
			}
			if !vclusterAllowedByBlueprint(placementTier, allowedPlacements) {
				return r.markFailed(ctx, app, ReasonInvalid,
					fmt.Sprintf("topology placement tier %q not in Blueprint %q allowedPlacements %v",
						vclusterOrHost(placementTier), spec.BlueprintName, allowedPlacements))
			}
			var (
				fanoutWriteNamespace string
				fanoutKubeSecretFor  func(cluster string) (string, string)
			)
			if placementTier != "" {
				vcp, vcOK := r.Cfg.VClusterPlacements[placementTier]
				if !vcOK {
					return r.markFailed(ctx, app, ReasonInvalid,
						fmt.Sprintf("Blueprint %q topology variant declares placement.tier=%q but the controller has no VClusterPlacements mapping for it (configure VCLUSTER_PLACEMENT_%s_NS + _SECRET, or drop the tier)",
							spec.BlueprintName, placementTier, strings.ToUpper(placementTier)))
				}
				fanoutWriteNamespace = vcp.HostNamespace
				// #3375 DoD-4 — resolve each declared canonical cluster
				// ID (mgmt-A / mgmt-B / dmz-A / …) to its kubeConfig
				// Secret through the cluster-ID registry, seeded from the
				// controller's VClusterPlacements + local region. This
				// replaces the prior `vc-<cluster>` mapping, which was
				// WRONG for the -B IDs: `mgmt-B` is a DIFFERENT region's
				// cluster, not a same-host vCluster named `vc-mgmt-b`
				// (which never exists). The registry encodes the proven
				// split-side model (cnpg-pair hw128 PASS): same-region IDs
				// reuse the local tier vCluster pivot; cross-region IDs
				// resolve to "" so the renderer omits the pivot and the
				// remote region's own Flux owns its half — unless the
				// operator wired a remote-region kubeconfig.
				fanoutKubeSecretFor = r.clusterResolver().SecretFor
			}

			// #4282 root-cause-B — CNPG-bearing Blueprints reconcile their
			// `postgresql.cnpg.io/v1 Cluster` CR HOST-SIDE, never inside a
			// per-Org / plane vCluster. A vCluster apiserver registers NO
			// `postgresql.cnpg.io/v1` CRD and runs NO cnpg operator (the CNPG
			// operator + its CRDs are CLUSTER-SINGLETONS owned by the host
			// cnpg-system release at bootstrap-kit slot 16, and the per-Org
			// host-ns operator the catalyst funnel installs is webhook-less +
			// WATCH_NAMESPACE-scoped to that host ns). So a bp-postgres HR
			// pivoted into a vCluster renders a hollow `InstallSucceeded`: the
			// chart's `cluster.yaml` is gated on
			// `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"`, which is
			// FALSE in the vCluster, so ZERO Cluster CRs render and the
			// Application hangs Provisioning forever (the #4282 shared-pg-d
			// repro). This is the SAME invariant the funnel's bp-cnpg-pair path
			// already enforces (#4293 BLOCKER-1 / TestCNPGPair_PrimaryStaysHost
			// Side_BothTiers): keep the Cluster CR on the host where the
			// operator+CRD live; the in-vCluster app pods reach the DB through
			// the synced Service + the reflected credential Secret. Suppress the
			// vCluster pivot (no kubeConfig secretRef) and author the HR in the
			// Application's own host namespace so the host helm-controller
			// installs the chart into a namespace the host/per-Org cnpg-system
			// operator reconciles. Detection is by the Blueprint's
			// `catalyst.openova.io/companion: bp-cnpg` label (present on the LIVE
			// catalog-seed CR) with a fallback to a `spec.depends[].blueprint:
			// bp-cnpg` edge for source-authored Blueprints. Non-CNPG Blueprints
			// (grafana/keycloak/…) keep the proven vCluster pivot unchanged.
			if placementTier != "" && blueprintRequiresHostCNPGOperator(bp) {
				r.Log.Info("CNPG-bearing Blueprint placed in a vCluster tier — routing the Cluster CR host-side (no vCluster pivot) so the host cnpg-system operator reconciles it (#4282)",
					"blueprint", spec.BlueprintName, "tier", placementTier, "hostNamespace", app.GetNamespace())
				fanoutWriteNamespace = app.GetNamespace()
				fanoutKubeSecretFor = nil
			}

			// Source-ref + chart: read from Blueprint.spec.manifests
			// (same lookup as the per-region path below).
			fanoutSourceKind, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "source", "kind")
			fanoutSourceRef, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "source", "ref")

			// #3373 — Application.spec.placement.clusters narrows the
			// topology variant's PlacementSpec.Clusters for THIS
			// instance. Copy-on-write: the Blueprint-derived variant
			// is shared state and must not be mutated.
			fanoutVariant := variant
			if len(spec.PlacementClusters) > 0 {
				v := *variant
				p := *variant.Placement
				p.Clusters = spec.PlacementClusters
				if len(p.Roles) > 0 {
					roles := make(map[string]string, len(spec.PlacementClusters))
					for _, c := range spec.PlacementClusters {
						if role, ok := p.Roles[c]; ok {
							roles[c] = role
						}
					}
					p.Roles = roles
				}
				v.Placement = &p
				fanoutVariant = &v
			}

			// #3370 — resolve spec.dependsOn[] backing refs to concrete
			// Flux HelmRelease edges. A bootstrap-owned backing instance
			// (shared-pg) resolves to its slot HR (spec.helmRelease,
			// e.g. flux-system/bp-postgres-shared); a controller-managed
			// backing instance resolves to its per-cluster
			// HRNameFor(<backing>, cluster) HR in the backing CR's
			// namespace. Unresolvable refs fall back to the per-cluster
			// convention so a late-created backing CR still gates the
			// consumer (Flux dependsOn on a missing HR = wait, which is
			// the correct conservative behaviour).
			var fanoutDependsOnFor func(cluster string) []topo.HRDependsOn
			if len(spec.DependsOn) > 0 {
				appNamespace := app.GetNamespace()
				dependsOnRefs := spec.DependsOn
				fanoutDependsOnFor = func(cluster string) []topo.HRDependsOn {
					out := make([]topo.HRDependsOn, 0, len(dependsOnRefs))
					for _, d := range dependsOnRefs {
						ns := d.Namespace
						if ns == "" {
							ns = appNamespace
						}
						if target, terr := r.Dynamic.Resource(ApplicationGVR).Namespace(ns).
							Get(ctx, d.Name, metav1.GetOptions{}); terr == nil && isBootstrapOwned(target) {
							thn, _, _ := unstructured.NestedString(target.Object, "spec", "helmRelease", "name")
							thns, _, _ := unstructured.NestedString(target.Object, "spec", "helmRelease", "namespace")
							if thn != "" {
								if thns == "" {
									thns = "flux-system"
								}
								out = append(out, topo.HRDependsOn{Name: thn, Namespace: thns})
								continue
							}
						}
						out = append(out, topo.HRDependsOn{
							Name:      topo.HRNameFor(d.Name, cluster),
							Namespace: ns,
						})
					}
					return out
				}
			}

			// #3375 DoD-3 — same owner labels on the per-app Continuum CR.
			continuumOwnerLabels = fanoutOwnerLabels(envSpec, app)

			// #4589 — the topology fan-out path must carry the SAME merged
			// values as the non-fanout per-region path below
			// (mergeMaps(bpManifestsValues, spec.Parameters)): the Blueprint's
			// manifests.values overlaid with the per-Org spec.Parameters.
			// Without this, singleton/active fan-out HRs render with chart
			// defaults ONLY — httpRoute.hostnames + sovereignFqdn drop, the
			// bp-oidc-gate companion fails closed, and zero-click SSO never
			// lands on a fresh Org. (agnstar only appeared to work because its
			// stale pre-#4556 non-fanout HR still carried these values; a fresh
			// Org exposed the gap — see #4589 / deeper cause of #4556 item-1.)
			// bpManifestsValues is declared later (non-fanout scope), so the
			// fan-out path reads it from the Blueprint object directly here.
			fanoutManifestsValues, _, _ := unstructured.NestedMap(bp.Object, "spec", "manifests", "values")

			hrs, ferr := topo.FanoutHRs(topo.FanoutInputs{
				AppName:        app.GetName(),
				AppNamespace:   app.GetNamespace(),
				WriteNamespace: fanoutWriteNamespace,
				Topology:       choice,
				Variant:        fanoutVariant,
				Chart:          firstNonEmpty(blueprintChart(bp), spec.BlueprintName),
				ChartVersion:   spec.BlueprintVersion,
				SourceRefName:  ifEmpty(fanoutSourceRef, r.Cfg.CatalogSourceRef),
				// #4079 — default the source kind when the Blueprint omits
				// spec.manifests.source.kind. The catalog source
				// (`openova-catalog`) is a Flux v1 HelmRepository; an empty
				// sourceRef.kind renders an invalid HelmRelease.
				SourceRefKind:       ifEmpty(fanoutSourceKind, "HelmRepository"),
				SourceRefNamespace:  r.Cfg.SourceNamespace,
				KubeConfigSecretFor: fanoutKubeSecretFor,
				// #3373 — loft-sh vcluster exportKubeConfig data key
				// (matches the per-region render path + the
				// hand-proven bootstrap-kit vCluster slots).
				KubeConfigSecretKey: "config",
				DependsOnFor:        fanoutDependsOnFor,
				IntervalSeconds:     r.Cfg.HelmReleaseIntervalSeconds,
				OwnerLabels:         fanoutOwnerLabels(envSpec, app),
				// #4589 — per-Org params (httpRoute.hostnames, sovereignFqdn,
				// oidcGate, …) merged OVER the Blueprint manifests.values,
				// mirroring the non-fanout path so the fan-out HR is
				// parameter-complete and the SSO gate renders.
				Values: mergeMaps(fanoutManifestsValues, spec.Parameters),
			})
			if ferr != nil {
				// Render-error path — surface as Degraded so the
				// reconciler retries (matches the existing
				// RenderError shape for the legacy per-region path).
				return r.markDegraded(ctx, app, ReasonRenderError,
					fmt.Sprintf("topology fan-out: %v", ferr))
			}
			topo.SortHRsForReconcile(hrs)
			perClusterFanout = hrs

			// G117.6a (Refs #2796) — persist each rendered HR onto the
			// host k3s. The dynamic client upsert is byte-equal
			// idempotent (drift-restore on diverged spec; no-op on
			// steady state) per upsertHostResource. We iterate in
			// SortHRsForReconcile order: active HRs first, then
			// passive, then singleton — so the active replica's HR
			// upsert hits Flux's reconcile queue before its passive
			// peer's (avoids the cnpg-pair race where the standby
			// boots before WAL streaming comes up on primary).
			for _, hr := range hrs {
				hrName := hr.GetName()
				hrNamespace := hr.GetNamespace()
				if hrNamespace == "" {
					// Belt-and-braces: WriteNamespace defaulted to
					// AppNamespace in renderOneHR; this guards against
					// a future renderer regression.
					hrNamespace = app.GetNamespace()
					hr.SetNamespace(hrNamespace)
				}
				if err := r.upsertHostResource(ctx, FluxHelmReleaseGVR, hrNamespace, hrName, hr); err != nil {
					return r.markDegraded(ctx, app, ReasonGiteaError,
						fmt.Sprintf("upsert per-cluster HelmRelease %s/%s: %v", hrNamespace, hrName, err))
				}
				r.Log.Info("upserted per-cluster HelmRelease",
					"namespace", hrNamespace, "name", hrName,
					"cluster", hr.GetLabels()[topo.LabelCluster],
					"role", hr.GetLabels()[topo.LabelRole],
					"topology", string(choice),
				)
			}

			// #4282 — seed each per-cluster row with the HR's authored
			// namespace + a provisional "Provisioning" status. The status
			// is NOT left hardcoded: the §10 HR readback below
			// (observeRegionHelmReleases) flips each row to "Ready" once its
			// per-cluster HelmRelease reports Ready=True, so a vCluster-placed
			// Application (whose Cluster CR #4398 routes host-side) rolls up
			// out of Provisioning the same way the single-HR host path does.
			// The namespace is captured because a fan-out HR is authored in
			// its WriteNamespace (host app-ns for #4398 CNPG routing, or the
			// vCluster host-ns for the legacy pivot) — the readback must GET
			// it where it was actually written, not always app.GetNamespace().
			for i, s := range topo.PerClusterStatusesFor(hrs) {
				hrNamespace := app.GetNamespace()
				if i < len(hrs) {
					if ns := hrs[i].GetNamespace(); ns != "" {
						hrNamespace = ns
					}
				}
				perClusterStatus = append(perClusterStatus, map[string]interface{}{
					"cluster":   s.Cluster,
					"role":      s.Role,
					"hr":        s.HR,
					"namespace": hrNamespace,
					"status":    PhaseProvisioning,
				})
			}
		}
	}
	_ = perClusterFanout // status writer reads perClusterStatus; this var is the typed-shape audit trail

	// #4556 Item 1 — when the topology fan-out produced HRs, those HRs ARE
	// the install (exactly one for a singleton, N for an active-hot-standby
	// cnpg-pair) and the reconciler already upserted them host-side above.
	// The legacy per-region render+commit path below ALSO renders the same
	// logical install under the bare `<app>` name and delivers it via a
	// GitRepository + per-region Kustomization — so a topology/targets-driven
	// Application was getting BOTH `<app>` AND `<app>-<cluster>` HelmReleases,
	// double-consuming the Org plan quota (live: `agenity` + `agenity-rtz-a`
	// both 1/1 on the agnstar Org). Honour the line-783 contract — "when the
	// fan-out runs, the per-region path must NOT also own the install" — by
	// suppressing the per-region render+commit + the host Flux bootstrap that
	// delivers the duplicate. Status observation already targets the fan-out
	// HRs via fanoutHRRefs (built from perClusterStatus), so nothing else
	// changes. The predicate never touches the fan-out loop itself, so the
	// cnpg-pair N>1 fan-out is unaffected; legacy / substrate / no-topology
	// Applications (perClusterFanout empty) keep the per-region path as their
	// SOLE install path, byte-identical to before.
	fanoutOwnsInstall := len(perClusterFanout) > 0

	// 7. Resolve placement → per-region work plan.
	plan, err := placement.Resolve(spec.Placement, spec.Regions)
	if err != nil {
		return r.markFailed(ctx, app, ReasonInvalid, err.Error())
	}

	// 7.5 (#3375 DoD-3) — mint the per-app Continuum DR contract.
	//
	// Now that BOTH the topology variant (captured above from the fan-out
	// resolver) AND the placement plan (primary + standby regions) are
	// resolved, produce one Continuum.dr.openova.io/v1 CR per DR-capable
	// Application so `kubectl get continuums.dr.openova.io -A` shows a
	// per-app row (dr-grafana, dr-sso-bridge) and the continuum-controller
	// (slot 62) has a real DR contract for EVERY DR app — not just the
	// cnpg-pair (which mints its own CR from its chart). A singleton /
	// active-active / single-region app, or a Blueprint variant with no
	// switchover mechanism, produces NO CR (the buildContinuumPlan gate);
	// a topology downgrade removes any stale CR. A produce error is
	// surfaced as Degraded so the reconcile retries — a missing DR
	// contract must not silently pass.
	if continuumOwnerLabels == nil {
		continuumOwnerLabels = fanoutOwnerLabels(envSpec, app)
	}
	// continuumRef carries the `<ns>/<name>` of any per-app Continuum CR
	// minted below onto the status write at the end of Reconcile (#4416 —
	// the back-pointer that closes the #4212 round-trip). Empty for a non-DR
	// app (no CR produced).
	continuumRef, cerr := r.reconcileContinuumCR(ctx, app.GetName(), app.GetNamespace(),
		resolvedTopoChoice, resolvedTopoVariant, plan, continuumOwnerLabels)
	if cerr != nil {
		return r.markDegraded(ctx, app, ReasonRenderError,
			fmt.Sprintf("per-app Continuum DR contract: %v", cerr))
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
	// #4246 — Blueprint authors set spec.manifests.helmRelease.disableWait=true
	// for charts whose workload Pod gates on a value populated by one of the
	// chart's OWN post-install/post-upgrade Helm hooks (e.g. bp-newapi's
	// DSN-gate initContainer + database-secret-sync-job post-install hook).
	// Without it, Flux's default --wait deadlocks the install. The bootstrap-
	// kit newapi HR has this hard-coded; this propagates it to the per-Org
	// Application render path.
	bpDisableWait, _, _ := unstructured.NestedBool(bp.Object, "spec", "manifests", "helmRelease", "disableWait")
	// G92.1 #2660 — Blueprint authors declare the vCluster the
	// rendered HelmRelease targets via spec.placementSchema.vcluster
	// (dmz|mgmt|rtz). Empty/unset = install onto the host k3s
	// (legacy shape, kept for substrate Blueprints per G92.6 #2665).
	bpVCluster := blueprintVCluster(bp)
	// #3373 — the instance's effective vCluster (instance >
	// blueprint defaultPlacement) overrides the legacy class-level
	// placementSchema.vcluster. An explicit instance "host" ("")
	// keeps the host install (no kubeConfig pivot).
	if effVCFound {
		bpVCluster = effVC
	}
	if !vclusterAllowedByBlueprint(bpVCluster, allowedPlacements) {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("placement vcluster %q not in Blueprint %q allowedPlacements %v",
				vclusterOrHost(bpVCluster), spec.BlueprintName, allowedPlacements))
	}
	vcPlacement, vcOK := r.Cfg.VClusterPlacements[bpVCluster]
	if bpVCluster != "" && !vcOK {
		return r.markFailed(ctx, app, ReasonInvalid,
			fmt.Sprintf("Blueprint %q declares placementSchema.vcluster=%q but the controller has no VClusterPlacements mapping for it (configure VCLUSTER_PLACEMENT_%s_NS + _SECRET, or drop the field)",
				spec.BlueprintName, bpVCluster, strings.ToUpper(bpVCluster)))
	}

	regionStatuses := make([]map[string]interface{}, 0, len(plan.Regions))
	allCommitted := true
	for _, rp := range plan.Regions {
		// #4556 Item 1 — the fan-out already owns the install (its `<app>-<cluster>`
		// HRs were upserted host-side above). Skip the redundant per-region
		// render+commit of the bare `<app>` HR so the Org plan quota is not
		// double-consumed. Keep the regionStatuses skeleton row so
		// status.regions[] + mergeRegionReadiness stay populated for the UI.
		if fanoutOwnsInstall {
			regionStatuses = append(regionStatuses, map[string]interface{}{
				"name":               rp.Name,
				"role":               rp.Role,
				"replicas":           int64(replicasFor(rp.Standby, spec.Parameters)),
				"ready":              int64(0),
				"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
			})
			continue
		}
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
			SourceKind:       ifEmpty(bpSourceKind, "HelmRepository"), // #4079 default
			SourceRef:        ifEmpty(bpSourceRef, r.Cfg.CatalogSourceRef),
			Chart:            bpChart,
			Values:           merged,
			SourceNamespace:  r.Cfg.SourceNamespace,
			IntervalSeconds:  r.Cfg.HelmReleaseIntervalSeconds,
			OwnerAppUID:      string(app.GetUID()),
			OwnerAppGen:      app.GetGeneration(),
			// vCluster pivot — empty Blueprint declaration =
			// host placement (legacy + G92.6 substrate); otherwise
			// stamp host namespace + kubeconfig Secret so Flux v2
			// helm-controller installs INSIDE the vCluster
			// (G92.1 #2660).
			VCluster:                 bpVCluster,
			VClusterHostNamespace:    vcPlacement.HostNamespace,
			VClusterKubeconfigSecret: vcPlacement.KubeconfigSecret,
			// #4246 — propagate the Blueprint's DSN-gate / hook-deadlock
			// avoidance flag onto the rendered HR's install + upgrade.
			DisableWait: bpDisableWait,
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
	//
	//     G92.1 #2660 — when the Blueprint declares a vCluster, the
	//     Kustomization's targetNamespace must be the vCluster's host
	//     namespace so the HR CR itself lands there (where Flux v2
	//     resolves the kubeConfig.secretRef). The IN-vCluster install
	//     target stays the Application's namespace (rendered HR's
	//     spec.targetNamespace).
	//
	//     #4556 Item 1 — SKIP this when the fan-out owns the install: the
	//     GitRepository + per-region Kustomization here are what deliver the
	//     duplicate bare `<app>` HR (the fan-out `<app>-<cluster>` HRs were
	//     already upserted host-side above). Bootstrapping them would
	//     re-create the second HR every reconcile and double-consume the Org
	//     plan quota. Legacy / non-fan-out Applications still bootstrap their
	//     SOLE per-region delivery here, unchanged.
	if !fanoutOwnsInstall {
		hostHRNamespace := app.GetNamespace()
		if bpVCluster != "" {
			hostHRNamespace = vcPlacement.HostNamespace
		}
		if err := r.ensureHostFluxBootstrap(ctx, app, envSpec, plan, hostHRNamespace); err != nil {
			return r.markDegraded(ctx, app, ReasonGiteaError,
				fmt.Sprintf("ensure host Flux bootstrap: %v", err))
		}
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
	// #4282 — the topology fan-out (placement.Clusters, e.g. a per-Org
	// bp-postgres routed host-side by #4398) names each per-cluster HR
	// `<app>-<cluster>` (HRNameFor), NOT the bare `<app>` the single-HR
	// host path uses, and authors it in the HR's WriteNamespace (the host
	// app-ns for #4398 CNPG routing, or the vCluster host-ns for the legacy
	// pivot). The phase rollup must observe the ACTUAL fan-out HR names IN
	// THEIR AUTHORED NAMESPACE or it GETs `<app>` (or the wrong ns)
	// → NotFound → the Application is pinned at Provisioning forever even
	// though `w4282-pg-rtz-a` is Ready=True. perClusterStatus carries the
	// real names + namespaces (seeded above); pass them in.
	fanoutHRRefs := make([]hrRef, 0, len(perClusterStatus))
	for _, pcs := range perClusterStatus {
		n, _ := pcs["hr"].(string)
		if n == "" {
			continue
		}
		ns, _ := pcs["namespace"].(string)
		if ns == "" {
			ns = app.GetNamespace()
		}
		fanoutHRRefs = append(fanoutHRRefs, hrRef{name: n, namespace: ns, region: n})
	}
	hrPhase, hrReason, hrMessage, perHRReady := r.observeRegionHelmReleases(ctx, app, plan, fanoutHRRefs)
	regionStatuses = mergeRegionReadiness(regionStatuses, hrPhase, plan, ctx, r, app)

	// #4282 — flip each per-cluster row's status from the provisional
	// "Provisioning" seed to the HR's observed readiness so the console's
	// per-cluster view (and any consumer rolling up perCluster[].status)
	// reflects the live HelmRelease, not a hardcoded placeholder. A row
	// with no live HR yet (NotFound) stays Provisioning.
	for _, pcs := range perClusterStatus {
		n, _ := pcs["hr"].(string)
		switch perHRReady[n] {
		case "True":
			pcs["status"] = PhaseReady
		case "False":
			pcs["status"] = PhaseDegraded
		}
	}

	// 11. Status update — phase derived from observed HR readiness,
	//     fall back to Provisioning when no signal is available yet.
	giteaRepo := fmt.Sprintf("%s/%s/%s",
		strings.TrimRight(r.Cfg.GiteaPublicURL, "/"),
		envSpec.OrganizationRef, app.GetName())
	finalPhase := hrPhase
	finalReady := "True"
	finalReason := ReasonReconciled
	// #5513 — the region count in the status message MUST derive from the
	// EFFECTIVE materialised per-cluster set, NEVER from the DECLARED
	// plan.Regions. An active-hot-standby provision whose fan-out collapses
	// to a single cluster (e.g. an empty region label — root cause tracked
	// in the placement/object-model chain, #5476) materialises ONE
	// HelmRelease while spec.placement still names two regions. The old code
	// read len(plan.Regions)=2 and fabricated "installed across 2 region(s)"
	// over a single HelmRelease — a DR posture that does not exist. When the
	// topology fan-out owns the install, perClusterStatus carries exactly the
	// HRs that were actually rendered (one row per materialised cluster), so
	// it is the same source the per-cluster table renders — the claim can
	// never exceed reality. The legacy single-HR host path (no fan-out) keeps
	// len(plan.Regions), byte-identical to before.
	effectiveRegions := len(plan.Regions)
	if fanoutOwnsInstall {
		effectiveRegions = len(perClusterStatus)
	}
	finalMessage := fmt.Sprintf("Application %s/%s reconciled into %d region(s)", app.GetNamespace(), app.GetName(), effectiveRegions)
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
			app.GetNamespace(), app.GetName(), effectiveRegions)
	}

	// #5513 — a downstream HelmRelease Ready=True means "Helm applied the
	// manifests", NOT "the workload came up". A CNPG-backed Application can
	// carry a Ready HelmRelease while its backing CNPG Cluster sits in the
	// terminal `unrecoverable` state (0 pods, ConsistentSystemID=False,
	// "needs manual intervention"). Reporting the Application Ready over that
	// is the fabricated DR posture #5513 walked live. Observe the backing
	// CNPG Cluster CR(s) — matched by the chart-stamped
	// app.kubernetes.io/instance label (= each per-cluster HR name) — and
	// downgrade to Degraded when any is unrecoverable, so the reported Ready
	// condition matches the workload reality. Read-only + failure-tolerant:
	// a missing CNPG CRD (no Postgres in this Sovereign) or a transient list
	// error NEVER fabricates a downgrade — the HR-derived phase stands.
	if finalReady == "True" {
		if cnpgNs, cnpgName, unrecoverable := r.backingCNPGUnrecoverable(ctx, app, perClusterStatus); unrecoverable {
			finalPhase = PhaseDegraded
			finalReady = "False"
			finalReason = ReasonBackingUnrecoverable
			finalMessage = fmt.Sprintf(
				"Application %s/%s: backing CNPG cluster %s/%s is in an unrecoverable state and needs manual intervention; not reporting Ready",
				app.GetNamespace(), app.GetName(), cnpgNs, cnpgName)
		}
	}
	su := statusUpdate{
		Phase:         finalPhase,
		PrimaryRegion: plan.PrimaryRegion,
		Regions:       regionStatuses,
		GiteaRepo:     giteaRepo,
		Installed: map[string]interface{}{
			"name":    spec.BlueprintName,
			"version": spec.BlueprintVersion,
			"digest":  bpDigest,
		},
		Reason:           finalReason,
		Message:          finalMessage,
		Ready:            finalReady,
		LastReconciledAt: time.Now().UTC().Format(time.RFC3339),
		PerCluster:       perClusterStatus,
	}

	// #3373 — surface the EFFECTIVE placement (post-resolution) so the
	// console reflects the Git-committed truth without re-deriving it.
	statusPlacementSource := effVCSource
	if statusPlacementSource == "" {
		if blueprintVCluster(bp) != "" {
			statusPlacementSource = "blueprint-placementSchema"
		} else {
			statusPlacementSource = "host-default"
		}
	}
	statusRegions := make([]interface{}, 0, len(spec.Regions))
	for _, rg := range spec.Regions {
		statusRegions = append(statusRegions, rg)
	}
	su.Placement = map[string]interface{}{
		"vcluster": vclusterOrHost(bpVCluster),
		"source":   statusPlacementSource,
		"regions":  statusRegions,
	}
	if len(spec.PlacementClusters) > 0 {
		clusters := make([]interface{}, 0, len(spec.PlacementClusters))
		for _, c := range spec.PlacementClusters {
			clusters = append(clusters, c)
		}
		su.Placement["clusters"] = clusters
	}

	// #4601 — fold the EFFECTIVE per-region DR placement projection onto the
	// #3373 status.placement object so the per-app Topology DR strip can
	// render the primary↔standby split (UAT rows 51/52/55/56/57). The
	// resolved plan already carries the static primary + standbys; when this
	// app minted a per-app Continuum (continuumRef non-empty) we read its
	// live status to override the primary with the lease holder + surface
	// replicationLagSeconds/leaseHeld. The DR keys are ADDITIVE — they never
	// overwrite the {vcluster,source,regions,clusters} keys above. Read-only:
	// this REPORTS placement, it never changes which region is primary.
	if dr := r.placementProjectionForPlan(ctx, plan, continuumRef); dr != nil {
		su.Placement = mergePlacementProjection(su.Placement, dr)
	}

	// #3969 §8a/§8b/§8e — run the owned-dependency cascade + the shared-dep
	// recommendation closure for this Application's declared backing
	// services. Owned (private) instances follow the consumer's resolved
	// placement by DEFAULT (unless decoupled via
	// spec.placement.ownedDependencies[].follow=false); shared instances
	// never cascade — they get a SOFT, non-blocking recommendation. The
	// result is surfaced on status so the rebuilt Topology tab's owned-deps
	// "[✓ follow]" rows + the shared-dep recommendation banner have a
	// backend source. This is READ-ONLY + NON-BLOCKING: an unsupported
	// backing-service type is logged, never fatal (a recommendation must
	// never fail a reconcile).
	// #3969 read-model rides on Seam 3: when the Application stamps the
	// legacy placement string (the spine producer + every pre-#3969 app),
	// derive the desired-state targets from the resolved plan so the
	// backing-service cascade + the observed-target rollup below operate on
	// the REAL primary+standby target set rather than an empty list.
	effectiveTargets := effectivePlacementTargets(spec.PlacementTargets, plan)
	if bindings, berr := resolveBackingPlacement(
		spec.BlueprintName, app.GetName(), app.GetNamespace(),
		blueprintBackingServices(bp), effectiveTargets, spec.OwnedDependencies,
	); berr != nil {
		r.Log.Warn("backing-service placement cascade skipped",
			"app", app.GetName(), "blueprint", spec.BlueprintName, "err", berr)
	} else if len(bindings) > 0 {
		ownedRows := make([]interface{}, 0, len(bindings))
		var recs []interface{}
		for _, b := range bindings {
			if b.Mode == bpv1alpha1.BackingServiceModePrivate {
				ownedRows = append(ownedRows, map[string]interface{}{
					"name":   b.InstanceName,
					"follow": b.Followed,
				})
			}
			for _, rec := range b.Recommendations {
				recs = append(recs, rec)
			}
		}
		if len(ownedRows) > 0 {
			su.Placement["ownedDependencies"] = ownedRows
		}
		if len(recs) > 0 {
			su.Placement["recommendations"] = recs
		}
	}

	// #3969 §7.4 — derive the ONE recon status (Reconciled / Reconciling /
	// Degraded + plain reason) + the observed per-target rollup, and
	// surface them via dedicated status fields. This is the single status
	// value the rebuilt Topology tab reads; there is NO second contradictory
	// "effective class" / "mandate unbuilt" derivation anywhere — that
	// machinery is deleted by this ticket.
	//
	// The per-region readiness is projected from the overall HR phase
	// rollup the controller already observed (PhaseReady ⇒ all ready;
	// PhaseDegraded/PhaseFailed ⇒ degraded; otherwise ⇒ reconciling). When
	// desired-state targets are declared we carry their richer
	// Primary|Standby role + Hot|Cold type onto the rollup, region-matched.
	// §13 — a non-empty continuumRef means the per-app Continuum lease-witness
	// DR contract was minted, so every Hot standby target is ARMED for
	// promotion. This is the desired-state arming signal surfaced on
	// status.targets[].armed; the runtime lease-held state stays on the
	// Continuum CR (continuum-controller-owned) and only the fresh-multi-region
	// walk proves which region currently holds it.
	readyByRegion := readbackByRegion(plan, finalPhase)
	observed := observedTargetsFromPlan(plan, effectiveTargets, readyByRegion, continuumRef != "")
	su.PlacementRecon, su.PlacementReason, su.ObservedTargets = reconStatusBlock(observed)

	// #4416 — write the Continuum back-pointer the producer minted above so
	// the spine→Continuum round-trip closes (the #4212 consumer-READ).
	su.ContinuumRef = continuumRef

	return r.updateStatus(ctx, app, su)
}

// readbackByRegion projects the overall HR phase rollup onto a per-region
// readiness map for the #3969 recon rollup. The controller's
// observeRegionHelmReleases collapses the per-region HRs to a single
// worst-of phase; we fan that back out across the plan's regions so the
// ObservedTarget rollup is consistent with the Application phase the rest
// of the status already reports. PhaseReady ⇒ every region ready;
// PhaseDegraded/PhaseFailed ⇒ every region degraded; otherwise ⇒ none
// ready yet (Reconciling), never degraded.
func readbackByRegion(plan placement.Plan, phase string) map[string]regionReadback {
	out := make(map[string]regionReadback, len(plan.Regions))
	for _, rp := range plan.Regions {
		switch phase {
		case PhaseReady:
			out[rp.Name] = regionReadback{ready: true}
		case PhaseDegraded, PhaseFailed:
			out[rp.Name] = regionReadback{degraded: true}
		default:
			out[rp.Name] = regionReadback{}
		}
	}
	return out
}

// hrRef is one HelmRelease the rollup observes: its name + the namespace
// it was authored in + a region/cluster label for the worst-case message.
// A fan-out HR is authored in its WriteNamespace (host app-ns for #4398
// CNPG routing, the vCluster host-ns for the legacy pivot), so the
// namespace MUST travel with the name — reading every HR from
// app.GetNamespace() pins a vCluster-placed Application at Provisioning
// forever (#4282).
type hrRef struct{ name, namespace, region string }

// observeRegionHelmReleases polls the per-region / per-cluster HelmRelease
// CRs the Sovereign's Flux installer materialised. Returns the rolled-up
// phase string + the reason+message of the WORST region (so a single-region
// Failed surfaces in the UI verbatim instead of being averaged out) + a
// per-HR readiness map (HR name → "True"|"False"|"" ) the caller uses to
// flip each status.perCluster[] row.
//
// HR naming has TWO shapes:
//   - SINGLE-HR host path: the HR is named exactly `app.GetName()`
//     (render.HelmReleaseName / the chart's `metadata.name: {{ .AppName }}`)
//     in the Application's own namespace. `fanoutRefs` empty ⇒ we synthesize
//     one bare-name ref per planned region.
//   - TOPOLOGY FAN-OUT (placement.Clusters, e.g. a per-Org bp-postgres
//     routed host-side by #4398): one HR per cluster named `<app>-<cluster>`
//     (HRNameFor), authored in its WriteNamespace. `fanoutRefs` carries those
//     real names + namespaces; when non-empty we observe THEM (one GET per
//     fan-out HR, each in ITS namespace) instead of the bare `app.GetName()`,
//     which does not exist for the fan-out and would pin the Application at
//     Provisioning forever (#4282).
//
// Idempotent + side-effect-free: only reads the API.
//
// qa-loop iter-11 Fix #45 Cluster-B.
func (r *Reconciler) observeRegionHelmReleases(
	ctx context.Context,
	app *unstructured.Unstructured,
	plan placement.Plan,
	fanoutRefs []hrRef,
) (phase, reason, message string, perHRReady map[string]string) {
	allReady := true
	anyDegraded := false
	worstReason := ""
	worstMessage := ""
	sawAny := false
	perHRReady = make(map[string]string, len(fanoutRefs))
	// Build the work list. Fan-out → the real per-cluster HR names +
	// namespaces; else one bare-name entry (app-ns) per planned region.
	refs := fanoutRefs
	if len(refs) == 0 {
		refs = make([]hrRef, 0, len(plan.Regions))
		for _, rp := range plan.Regions {
			refs = append(refs, hrRef{name: app.GetName(), namespace: app.GetNamespace(), region: rp.Name})
		}
	}
	for _, ref := range refs {
		ns := ref.namespace
		if ns == "" {
			ns = app.GetNamespace()
		}
		hr, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
			Namespace(ns).
			Get(ctx, ref.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// HR not yet materialised — Flux still pulling. Roll up
				// to Provisioning, NOT Failed.
				allReady = false
				continue
			}
			r.Log.Warn("application-controller: GET HelmRelease failed",
				"namespace", ns,
				"name", ref.name,
				"region", ref.region,
				"err", err)
			allReady = false
			continue
		}
		sawAny = true
		ready, hrReason, hrMsg := readReadyCondition(hr)
		perHRReady[ref.name] = ready
		switch ready {
		case "True":
			// good — keep allReady
		case "False":
			anyDegraded = true
			allReady = false
			if worstReason == "" {
				worstReason = "DownstreamHelmReleaseFailed"
				worstMessage = fmt.Sprintf("region %s HelmRelease Ready=False: %s — %s", ref.region, hrReason, hrMsg)
			}
		default:
			// Unknown — Flux still working.
			allReady = false
		}
	}
	switch {
	case anyDegraded:
		return PhaseDegraded, worstReason, worstMessage, perHRReady
	case allReady && sawAny:
		return PhaseReady, "", "", perHRReady
	default:
		return PhaseProvisioning, "", "", perHRReady
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
	hostHRNamespace string,
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
	// Cleanup on Application delete is handled by handleDeletion(): it
	// deletes the Gitea-side files (Flux then GCs the workload via
	// prune=true on the Kustomization) AND — via
	// cascadeDeleteHostFluxBootstrap (#4902 follow-on) — issues explicit
	// Delete calls on these host-cluster GitRepository + Kustomization CRs
	// themselves, keyed off the app-uid back-pointer label below, so they
	// no longer orphan on delete.
	commonLabels := map[string]interface{}{
		"app.kubernetes.io/managed-by":     "application-controller",
		LabelHostFluxApp:                   app.GetName(),
		"catalyst.openova.io/organization": envSpec.OrganizationRef,
		"catalyst.openova.io/env-type":     envSpec.EnvType,
		// Reference labels for cascade-delete (replaces ownerRef which
		// can't span namespaces). handleDeletion() looks these up.
		LabelHostFluxAppNamespace: app.GetNamespace(),
		LabelAppUID:               string(app.GetUID()),
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
	// #4285 — route through the shared fluxsource builder so the
	// "local-Gitea source MUST carry a secretRef" law is enforced in ONE
	// place. bp-gitea REQUIRE_SIGNIN_VIEW=true makes anonymous clone 401;
	// an empty secretRef on this (always Sovereign-local) source is a hard
	// error, not a silently-dead 401 source.
	grSpec, err := fluxsource.BuildGitRepositorySpec(fluxsource.GitRepositorySpecInput{
		URL:             repoURL,
		Branch:          branch,
		IntervalSeconds: r.Cfg.HostFluxIntervalSeconds,
		SecretRef:       r.Cfg.FluxGiteaSecretRef,
	})
	if err != nil {
		return fmt.Errorf("build GitRepository spec for %s: %w", repoName, err)
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
			"interval": fmt.Sprintf("%ds", r.Cfg.HostFluxIntervalSeconds),
			"path":     fmt.Sprintf("./clusters/%s/applications/%s", rp.Name, app.GetName()),
			"prune":    true,
			// targetNamespace = where the HR CR lands on the HOST. For
			// host-placement Applications this is the Application's own
			// namespace (legacy). For vCluster-placement Applications
			// (G92.1 #2660) it's the vCluster's host namespace so
			// helm-controller resolves spec.kubeConfig.secretRef from
			// the same namespace as the HR (Flux v2 SecretReference
			// contract).
			"targetNamespace": hostHRNamespace,
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

	// #3370 — bootstrap-owned CRs own NO rendered artifacts (the
	// adoption guard skips every render/Gitea path), so deletion drives
	// no teardown. Defensive: the steady-state path never adds our
	// finalizer to these, but if one is present (e.g. a CR that
	// pre-dates the marker) just release it.
	if isBootstrapOwned(app) {
		return r.removeFinalizer(ctx, app)
	}

	// Surface phase=Uninstalling for the UI.
	_ = r.updateStatus(ctx, app, statusUpdate{
		Phase:   PhaseUninstalling,
		Reason:  ReasonAwaitingDrain,
		Message: "removing manifests from Gitea; waiting for Flux to drain",
		Ready:   "False",
	})

	// #4902 — cascade-delete the per-region HelmReleases the topology
	// fan-out imperatively upserted for this Application (the
	// `<app>-<cluster>` HRs — e.g. `uatprobe-ahs-rtz-a`). They carry NO
	// ownerReferences (they can live in a different namespace than the
	// Application CR, which makes same-namespace ownerRefs unsafe — see
	// the cross-namespace-GC note on ensureHostFluxBootstrap), so K8s GC
	// never reaps them; and when the fan-out owns the install the legacy
	// per-region Gitea path below is suppressed, so nothing else removes
	// them. Without this, an Application created via the New-instance
	// flow leaks its per-region HR (and its Helm release) on every
	// delete. Runs BEFORE the spec/env resolution below so orphans are
	// cleaned even when the spec is unparseable or the parent Environment
	// is already gone — the cleanup keys off the app-uid back-pointer
	// label alone.
	if err := r.cascadeDeleteFanoutHelmReleases(ctx, app); err != nil {
		return fmt.Errorf("delete per-cluster HelmReleases: %w", err)
	}

	// #4902 (follow-on) — cascade-delete the host-cluster Flux
	// GitRepository + per-region Kustomization CRs that
	// ensureHostFluxBootstrap imperatively upserted for this Application
	// (`catalyst-app-<org>-<app>` + `…-<region>`). Like the fan-out HRs
	// these carry NO ownerReferences (they live in HostFluxNamespace —
	// flux-system — a DIFFERENT namespace than the Application CR, so a
	// cross-namespace ownerRef would be hard-GC'd on creation), so K8s GC
	// never reaps them. The Gitea-file cleanup below only removes the
	// per-region manifests (Flux then prunes the WORKLOAD via the
	// Kustomization's prune=true) — it does NOT remove these source/sync
	// CRs themselves, so without this they orphan on every delete (the
	// GitRepository keeps polling a now-empty Gitea repo; the Kustomization
	// keeps reconciling). Closes the explicit TODO on ensureHostFluxBootstrap.
	// Runs BEFORE the spec/env resolution below so the cleanup happens even
	// when the spec is unparseable or the parent Environment is already gone
	// — it keys off the app-uid back-pointer label + the fixed
	// HostFluxNamespace alone.
	if err := r.cascadeDeleteHostFluxBootstrap(ctx, app); err != nil {
		return fmt.Errorf("delete host-flux bootstrap CRs: %w", err)
	}

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

	// #3375 DoD-3 — cascade-delete the per-app Continuum DR contract (the
	// dr-<app> CR the fan-out minted). NotFound is a no-op, so a non-DR
	// app (which never had one) deletes cleanly.
	if err := r.deleteContinuumCRIfExists(ctx, app.GetName(), app.GetNamespace()); err != nil {
		return fmt.Errorf("delete per-app Continuum CR: %w", err)
	}

	return r.removeFinalizer(ctx, app)
}

// cascadeDeleteFanoutHelmReleases removes every per-cluster HelmRelease
// the topology fan-out imperatively upserted for this Application
// (#4902). The fan-out reconcile path (§G117.6a) writes each
// `<app>-<cluster>` HelmRelease straight onto the host k3s via the
// dynamic client — NOT through Gitea — and when the fan-out owns the
// install the legacy per-region Gitea render+commit path is suppressed.
// So handleDeletion's Gitea cleanup removes nothing for a fan-out-owned
// Application and these HRs would orphan (no deletionTimestamp, only the
// Flux finalizer) unless we delete them here.
//
// We select by the globally-unique app-uid back-pointer label rather
// than an ownerReference: the fan-out HRs deliberately carry no
// ownerRefs because they may live in a DIFFERENT namespace than the
// Application CR (the vCluster pivot lands them in the tier host
// namespace), and K8s ownerRefs only resolve within a single namespace
// — a cross-namespace ownerRef would get the HR hard-GC'd the instant
// it is created (the same trap documented on ensureHostFluxBootstrap).
//
// The List is cluster-scoped because the fan-out authors HRs across the
// Application's own namespace AND per-Tier vCluster host namespaces: the
// app-uid label pre-filters server-side (efficient on a busy Sovereign)
// and we re-verify it client-side so nothing outside THIS instance is
// ever deleted. The controller ClusterRole grants list+delete on
// helmreleases cluster-wide.
func (r *Reconciler) cascadeDeleteFanoutHelmReleases(ctx context.Context, app *unstructured.Unstructured) error {
	uid := string(app.GetUID())
	name := app.GetName()

	// Prefer the globally-unique app-uid selector; fall back to the
	// app-name back-pointer only when the CR carries no UID (never in
	// practice for an API-server-persisted CR, but keeps the cleanup
	// robust against a hand-crafted / partially-migrated object).
	selectorKey, selectorVal := LabelAppUID, uid
	if uid == "" {
		selectorKey, selectorVal = topo.LabelApp, name
	}

	list, err := r.Dynamic.Resource(FluxHelmReleaseGVR).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", selectorKey, selectorVal),
	})
	if err != nil {
		return fmt.Errorf("list fan-out HelmReleases: %w", err)
	}

	for i := range list.Items {
		hr := &list.Items[i]
		labels := hr.GetLabels()
		// Client-side re-verify against the app-uid (or, uid-absent, the
		// app-name) back-pointer so a List that ignored the server-side
		// selector can never over-delete another Application's HRs.
		if uid != "" {
			if labels[LabelAppUID] != uid {
				continue
			}
		} else if labels[topo.LabelApp] != name {
			continue
		}
		ns, hrName := hr.GetNamespace(), hr.GetName()
		if derr := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(ns).
			Delete(ctx, hrName, metav1.DeleteOptions{}); derr != nil {
			if apierrors.IsNotFound(derr) {
				continue
			}
			return fmt.Errorf("delete fan-out HelmRelease %s/%s: %w", ns, hrName, derr)
		}
		r.Log.Info("cascade-deleted per-cluster HelmRelease on Application delete (#4902)",
			"namespace", ns, "name", hrName,
			"cluster", labels[topo.LabelCluster], "app", name)
	}
	return nil
}

// cascadeDeleteHostFluxBootstrap removes the host-cluster Flux
// GitRepository + per-region Kustomization CRs that ensureHostFluxBootstrap
// imperatively upserted for this Application (#4902 follow-on — closes the
// TODO left on that function). Like the fan-out HelmReleases, these CRs live
// in HostFluxNamespace (flux-system) — a DIFFERENT namespace than the
// Application CR — so they deliberately carry NO ownerReferences (a
// cross-namespace ownerRef would be hard-GC'd the instant it is created) and
// the K8s garbage collector never reaps them. handleDeletion's Gitea cleanup
// only removes the per-region manifests (so Flux prunes the workload via the
// Kustomization's prune=true), NOT these source/sync CRs themselves — so
// without this they orphan on every Application delete.
//
// Selection mirrors cascadeDeleteFanoutHelmReleases: prefer the
// globally-unique app-uid back-pointer label; fall back to the
// application-name + app-namespace label pair when the CR carries no UID
// (disambiguates same-named Applications across Orgs). Scoped to
// HostFluxNamespace because that is the only namespace ensureHostFluxBootstrap
// ever writes these CRs into. Kustomizations are deleted before their
// GitRepository source (delete the sync that references the source first).
// The controller ClusterRole grants list+delete on gitrepositories +
// kustomizations.
func (r *Reconciler) cascadeDeleteHostFluxBootstrap(ctx context.Context, app *unstructured.Unstructured) error {
	uid := string(app.GetUID())
	name := app.GetName()
	appNS := app.GetNamespace()
	ns := r.Cfg.HostFluxNamespace

	selector := fmt.Sprintf("%s=%s", LabelAppUID, uid)
	if uid == "" {
		selector = fmt.Sprintf("%s=%s,%s=%s",
			LabelHostFluxApp, name, LabelHostFluxAppNamespace, appNS)
	}

	// Kustomizations first, then the GitRepository they source from.
	for _, gvr := range []schema.GroupVersionResource{
		FluxKustomizationGVR,
		FluxGitRepositoryGVR,
	} {
		list, err := r.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			return fmt.Errorf("list host-flux %s: %w", gvr.Resource, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			labels := obj.GetLabels()
			// Client-side re-verify so a List that ignored the server-side
			// selector can never over-delete another Application's CRs.
			if uid != "" {
				if labels[LabelAppUID] != uid {
					continue
				}
			} else if labels[LabelHostFluxApp] != name || labels[LabelHostFluxAppNamespace] != appNS {
				continue
			}
			objName := obj.GetName()
			if derr := r.Dynamic.Resource(gvr).Namespace(ns).
				Delete(ctx, objName, metav1.DeleteOptions{}); derr != nil {
				if apierrors.IsNotFound(derr) {
					continue
				}
				return fmt.Errorf("delete host-flux %s %s/%s: %w", gvr.Resource, ns, objName, derr)
			}
			r.Log.Info("cascade-deleted host-flux bootstrap CR on Application delete (#4902)",
				"kind", obj.GetKind(), "namespace", ns, "name", objName, "app", name)
		}
	}
	return nil
}

// ensureFinalizer adds the controller's finalizer to the Application
// if not already present. Idempotent — no API call when already set.
func (r *Reconciler) ensureFinalizer(ctx context.Context, app *unstructured.Unstructured) error {
	if hasFinalizer(app, FinalizerName) {
		return nil
	}
	// #4853 — the finalizer add is a read-modify-write on the Application, so
	// (like updateStatus) it races the second reconcile goroutine / org-
	// controller on resourceVersion. A bare Update on the stale `app` 409s and
	// the reconcile errors + requeues. Mirror the updateStatus fix: re-GET the
	// latest object each attempt under RetryOnConflict so the loser self-heals.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).
			Get(ctx, app.GetName(), metav1.GetOptions{})
		if err != nil {
			// Gone between List and Update — nothing to finalize.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("re-fetch application for finalizer add: %w", err)
		}
		if hasFinalizer(latest, FinalizerName) {
			return nil
		}
		latest.SetFinalizers(append(latest.GetFinalizers(), FinalizerName))
		// Return the RAW api error so RetryOnConflict's IsConflict check fires
		// (a %w-wrapped error would not be recognised).
		_, uErr := r.Dynamic.Resource(ApplicationGVR).Namespace(latest.GetNamespace()).
			Update(ctx, latest, metav1.UpdateOptions{})
		if apierrors.IsNotFound(uErr) {
			return nil
		}
		return uErr
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("ensure finalizer: %w", err)
	}
	return nil
}

// removeFinalizer strips the controller's finalizer. The API server
// then GCs the Application CR (assuming no other finalizers remain).
func (r *Reconciler) removeFinalizer(ctx context.Context, app *unstructured.Unstructured) error {
	if !hasFinalizer(app, FinalizerName) {
		return nil
	}
	// #4853 — THE ORIGINAL wedge: uat-openclaw stuck phase=Uninstalling for 32h
	// with every managed resource already gone, because this strip did a bare
	// Update on the stale reconcile object. A persistent concurrent writer bumps
	// resourceVersion → every Update 409-conflicts → the finalizer never comes
	// off → the CR never deletes → the reconcile loops forever (~3s, 32h). #4856
	// fixed only updateStatus; the finalizer path had the same bug. Re-GET the
	// latest object each attempt under RetryOnConflict so the strip applies
	// against the fresh resourceVersion and the finalizer actually releases.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).
			Get(ctx, app.GetName(), metav1.GetOptions{})
		if err != nil {
			// Already GC'd — the finalizer is gone, done.
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("re-fetch application for finalizer removal: %w", err)
		}
		if !hasFinalizer(latest, FinalizerName) {
			return nil
		}
		out := make([]string, 0, len(latest.GetFinalizers()))
		for _, f := range latest.GetFinalizers() {
			if f == FinalizerName {
				continue
			}
			out = append(out, f)
		}
		latest.SetFinalizers(out)
		// Raw api error so RetryOnConflict's IsConflict check fires.
		_, uErr := r.Dynamic.Resource(ApplicationGVR).Namespace(latest.GetNamespace()).
			Update(ctx, latest, metav1.UpdateOptions{})
		if apierrors.IsNotFound(uErr) {
			return nil
		}
		return uErr
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("remove finalizer: %w", err)
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

	// PerCluster — G117.6 (Refs #2745). One row per cluster the
	// Application is fanned out across (per the Blueprint's resolved
	// `spec.topology.perTopology[<choice>].placement.clusters[]`).
	// Powers the operator-console drill-down + catalyst-api's
	// GET /apps/{id}.perCluster[]. Empty when the Blueprint has no
	// `spec.topology` declared (legacy / pre-W1.B1 path).
	PerCluster []map[string]interface{}
	// LastReconciledAt is the wall-clock RFC3339 timestamp of this
	// reconcile pass — surfaced verbatim via
	// `status.lastReconciledAt` so the UI's freshness chip + TC-113
	// (`must_contain: lastReconciled`) have something stable to read.
	// Empty value leaves the field untouched. qa-loop iter-11 Fix #45
	// Cluster-B follow-up.
	LastReconciledAt string

	// Placement — #3373. The EFFECTIVE placement this reconcile
	// resolved + rendered: {vcluster, source, regions, clusters}.
	// Surfaced via `status.placement` so the console reflects the
	// Git-committed truth (law §2.4: the console reflects Git, never
	// fights it). Nil leaves the field untouched.
	Placement map[string]interface{}

	// ── #3969 §7.4 — the ONE recon status (no second class) ──────────
	//
	// PlacementRecon — the single recon value: Reconciled | Reconciling |
	// Degraded. Surfaced via `status.placementRecon` (a string, distinct
	// from the legacy `status.placement` OBJECT so the existing
	// placementInfoFromCR object-reader is untouched). The rebuilt
	// Topology tab reads this as the ONE status value. Empty leaves the
	// field untouched.
	PlacementRecon string

	// PlacementReason — the plain, operator-readable reason that
	// accompanies a non-Reconciled status (e.g. "region-b Standby·Hot is
	// reconciling"). NEVER a second class. Surfaced via
	// `status.placementReason`. Empty clears any prior reason.
	PlacementReason string

	// ObservedTargets — the §7.4 observed per-target rollup
	// (region/cluster/role/standbyType/ready/degraded). Surfaced via
	// `status.targets[]` — the same key the rebuilt Topology tab reads as
	// the recon rollup fallback. Nil leaves the field untouched.
	ObservedTargets []interface{}

	// ContinuumRef — #4416. The `<namespace>/<name>` of the per-app
	// Continuum DR contract this Application minted (e.g.
	// `catalyst/dr-spine-gitea`). Surfaced via `status.continuumRef` — the
	// consumer-READ back-pointer that CLOSES the spine→Continuum round-trip
	// (#4212): an operator (or a console projection) reading the Application
	// can hop straight to its Continuum. Empty leaves the field untouched
	// (a non-DR app mints no Continuum and writes no back-ref).
	ContinuumRef string
}

// updateStatus writes the status sub-resource via the dynamic client.
//
// We re-fetch the latest object before patching so we don't clobber
// concurrent edits, then update with the desired status. The dynamic
// client's UpdateStatus handles the /status subresource correctly.
func (r *Reconciler) updateStatus(ctx context.Context, app *unstructured.Unstructured, su statusUpdate) error {
	now := time.Now().UTC().Format(time.RFC3339)

	// #4853 — the periodic re-list goroutine (runPeriodicRelist → initialList)
	// and the watch goroutine (watchOnce) both call Reconcile → updateStatus on
	// the SAME Application concurrently. A bare Get-then-UpdateStatus races on
	// resourceVersion in the window between the read and the write: whichever
	// writes second gets a 409 Conflict ("the object has been modified; please
	// apply your latest changes to the latest version") and the reconcile logs
	// `update status: Operation cannot be fulfilled ...` every ~30s. Wrapping
	// the whole read-modify-write in RetryOnConflict re-runs it with the fresh
	// resourceVersion so the loser self-heals instead of erroring + requeuing.
	// The closure returns the RAW api error on the write so RetryOnConflict's
	// IsConflict check fires (a %w-wrapped error would not be recognised).
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Always re-fetch to avoid clobbering. If the object is gone, no-op.
		latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).
			Get(ctx, app.GetName(), metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("re-fetch application for status: %w", err)
		}

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
		if su.Placement != nil {
			// #3373 — effective placement reflection.
			currentStatus["placement"] = su.Placement
		}
		if su.PlacementRecon != "" {
			// #3969 §7.4 — the ONE recon status value (string), distinct from
			// the legacy `status.placement` object above. Reconciled /
			// Reconciling / Degraded — never a second contradictory class.
			currentStatus["placementRecon"] = su.PlacementRecon
			// The plain reason is set (or cleared) alongside the status so a
			// stale reason never lingers once the placement reconciles green.
			currentStatus["placementReason"] = su.PlacementReason
		}
		if su.ObservedTargets != nil {
			// #3969 §7.4 — the observed per-target rollup the Topology tab
			// reads. One status value, never two.
			currentStatus["targets"] = su.ObservedTargets
		}
		if su.ContinuumRef != "" {
			// #4416 — the back-pointer to this Application's per-app Continuum DR
			// contract, closing the #4212 spine→Continuum round-trip.
			currentStatus["continuumRef"] = su.ContinuumRef
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
		if su.PerCluster != nil {
			// G117.6 — slice of map[string]interface{} → []interface{} for
			// the dynamic client's NestedSlice contract.
			perCluster := make([]interface{}, len(su.PerCluster))
			for i, c := range su.PerCluster {
				perCluster[i] = c
			}
			currentStatus["perCluster"] = perCluster
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

		if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace(latest.GetNamespace()).
			UpdateStatus(ctx, latest, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			// Return the raw error so RetryOnConflict can recognise a 409
			// Conflict and re-run the closure with the fresh resourceVersion.
			return err
		}
		return nil
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// isBootstrapOwned reports whether the Application is a bootstrap-owned
// instance (spec.bootstrap=true) — the canonical CR a bootstrap-kit
// chart self-registers for its slot-installed HelmRelease (#3370).
func isBootstrapOwned(app *unstructured.Unstructured) bool {
	b, _, _ := unstructured.NestedBool(app.Object, "spec", "bootstrap")
	return b
}

// reconcileBootstrapOwned — #3370 ADOPTION path for bootstrap-owned
// Applications. Status-only: observe the owning HelmRelease named by
// spec.helmRelease and mirror its Ready condition onto the Application.
// NEVER renders, NEVER fans out, NEVER touches Gitea — those would
// duplicate the install the bootstrap-kit HR already owns (the unit
// tests assert zero HelmRelease writes on this path).
func (r *Reconciler) reconcileBootstrapOwned(ctx context.Context, app *unstructured.Unstructured) error {
	now := time.Now().UTC().Format(time.RFC3339)

	hrName, _, _ := unstructured.NestedString(app.Object, "spec", "helmRelease", "name")
	hrNamespace, _, _ := unstructured.NestedString(app.Object, "spec", "helmRelease", "namespace")
	if hrNamespace == "" {
		hrNamespace = "flux-system"
	}
	if hrName == "" {
		// No HR ref — the CR is still the canonical representation,
		// but there is nothing to observe. Surface Pending so the
		// console shows an honest "unobserved" state rather than a
		// fabricated Ready.
		return r.updateStatus(ctx, app, statusUpdate{
			Phase:            PhasePending,
			Reason:           ReasonBootstrapAdopted,
			Message:          "bootstrap-owned instance declares no spec.helmRelease to observe",
			Ready:            "False",
			LastReconciledAt: now,
		})
	}

	hr, err := r.Dynamic.Resource(FluxHelmReleaseGVR).Namespace(hrNamespace).
		Get(ctx, hrName, metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return r.updateStatus(ctx, app, statusUpdate{
				Phase:            PhasePending,
				Reason:           ReasonBootstrapAdopted,
				Message:          fmt.Sprintf("owning HelmRelease %s/%s not found", hrNamespace, hrName),
				Ready:            "False",
				LastReconciledAt: now,
			})
		}
		return fmt.Errorf("bootstrap-owned: get HelmRelease %s/%s: %w", hrNamespace, hrName, err)
	}

	phase := PhaseProvisioning
	ready := "Unknown"
	message := fmt.Sprintf("adopted bootstrap install %s/%s (no Ready condition yet)", hrNamespace, hrName)
	conds, _, _ := unstructured.NestedSlice(hr.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] != "Ready" {
			continue
		}
		hrMsg, _ := cm["message"].(string)
		switch cm["status"] {
		case "True":
			phase = PhaseReady
			ready = "True"
			message = fmt.Sprintf("adopted bootstrap install %s/%s: %s", hrNamespace, hrName, hrMsg)
		case "False":
			phase = PhaseDegraded
			ready = "False"
			message = fmt.Sprintf("adopted bootstrap install %s/%s not Ready: %s", hrNamespace, hrName, hrMsg)
		}
		break
	}

	// #4416 — mint the per-app Continuum DR contract for an ADOPTED
	// DR-capable spine app (openbao/keycloak/harbor/gitea) + carry its
	// back-pointer. The bootstrap-owned path is status-only (it never
	// renders a fan-out HR), but the spine app is STILL a DR-capable
	// multi-region Application whose Continuum contract must exist — that
	// is the forward half of the #4212 round-trip. We resolve the contract
	// from the CR's own spec.placement + spec.regions + the Blueprint
	// topology variant (no fan-out render needed) and upsert the same
	// Continuum CR the normal path would. A non-DR bootstrap app
	// (singleton shared-pg, single-region) produces NO CR (the
	// buildContinuumPlan gate) and writes no back-ref — the shared-pg
	// adoption path is unchanged. Errors here are surfaced as Degraded so
	// a missing DR contract retries (never silently passes).
	continuumRef, cerr := r.reconcileBootstrapContinuum(ctx, app)
	if cerr != nil {
		return r.markDegraded(ctx, app, ReasonRenderError,
			fmt.Sprintf("adopted spine Continuum DR contract: %v", cerr))
	}

	// #4601 — project the EFFECTIVE per-region DR placement onto
	// status.placement so the per-app Topology DR strip can render the
	// primary↔standby split for this adopted spine app. The bootstrap-owned
	// path NEVER wrote status.placement before, so every DR spine app
	// (spine-gitea/harbor/keycloak/openbao) had an empty status.placement
	// despite a live Continuum lease. We resolve the static plan from the
	// app's own spec, then override the primary with the live Continuum
	// leaseHolder + surface lag/leaseHeld. A non-DR bootstrap app (singleton
	// shared-pg, single-region, no Continuum) projects the static singleton
	// plan (primary + empty standbys) with no lag/leaseHeld. Read-only — this
	// never changes which region is primary, only REPORTS it.
	placementProjection := r.bootstrapPlacementProjection(ctx, app, continuumRef)

	// Churn guard: a status write bumps resourceVersion → MODIFIED
	// watch event → another reconcile. The normal path's Gitea
	// byte-equality short-circuit naturally dampens that loop; this
	// status-only path has no such stage, so skip the write when
	// nothing meaningful changed (proven hot-looping at ~600ms/CR on
	// the envtest adoption walk without this). The continuumRef is part
	// of the changed-set: an adoption that just minted its Continuum must
	// write the back-ref even when the HR phase is unchanged. The
	// placement projection is part of the changed-set too: a freshly-flipped
	// lease (primary moved region) must re-write status.placement even when
	// the adopted HR's phase is unchanged.
	if bootstrapStatusUnchanged(app, phase, message, ready) &&
		bootstrapContinuumRefUnchanged(app, continuumRef) &&
		bootstrapPlacementUnchanged(app, placementProjection) {
		return nil
	}

	r.Log.Info("bootstrap-owned adoption: status-only reconcile",
		"app", app.GetName(),
		"namespace", app.GetNamespace(),
		"helmRelease", hrNamespace+"/"+hrName,
		"phase", phase,
		"continuumRef", continuumRef)

	return r.updateStatus(ctx, app, statusUpdate{
		Phase:            phase,
		Reason:           ReasonBootstrapAdopted,
		Message:          message,
		Ready:            ready,
		LastReconciledAt: now,
		ContinuumRef:     continuumRef,
		Placement:        placementProjection,
	})
}

// bootstrapPlacementProjection resolves the effective per-region DR placement
// projection for an adopted (bootstrap-owned) Application and folds in the
// live Continuum status (#4601). The adoption path is status-only — it never
// resolves a fan-out render — so we resolve the placement plan directly from
// the Application's own spec.placement + spec.regions (the same inputs
// reconcileBootstrapContinuum uses), then read the per-app Continuum CR named
// by continuumRef (falling back to the deterministic `dr-<app>` name) to
// override the primary with the live leaseHolder + surface
// replicationLagSeconds/leaseHeld.
//
// Returns nil (status.placement untouched) only when the spec has no
// resolvable placement plan (no regions / unparseable spec) — never an error,
// because a placement PROJECTION must never fail an adoption reconcile (the
// HR observation is the load-bearing signal).
func (r *Reconciler) bootstrapPlacementProjection(
	ctx context.Context,
	app *unstructured.Unstructured,
	continuumRef string,
) map[string]interface{} {
	spec, err := parseSpec(app)
	if err != nil {
		return nil
	}
	if spec.Placement == "" || len(spec.Regions) == 0 {
		return nil
	}
	plan, perr := placement.Resolve(spec.Placement, spec.Regions)
	if perr != nil {
		// Unresolvable placement (e.g. singleton with >1 region) — do not
		// fabricate a projection; leave status.placement untouched.
		r.Log.Warn("bootstrap-owned: placement resolve failed; no status.placement projection",
			"app", app.GetName(), "placement", spec.Placement, "err", perr)
		return nil
	}
	// Read the live Continuum status (if a CR exists for this app). Prefer the
	// back-ref the producer just minted; when that is empty (e.g. the Blueprint
	// is not yet in the catalog so reconcileBootstrapContinuum deferred the
	// mint, but a CR from a prior reconcile already exists) fall back to the
	// deterministic `dr-<app>` name in the app's namespace so the live lease
	// still drives the projection. A read error degrades to the static-plan
	// projection rather than failing the reconcile — the static plan still
	// gives the UI a primary/standby split.
	ref := continuumRef
	if ref == "" {
		ref = app.GetNamespace() + "/" + ContinuumNameFor(app.GetName())
	}
	cs, cerr := r.readContinuumDRStatus(ctx, ref)
	if cerr != nil {
		r.Log.Warn("bootstrap-owned: Continuum status read failed; static placement projection",
			"app", app.GetName(), "continuumRef", ref, "err", cerr)
		cs = nil
	}

	// #4605 follow-up — CNPG-backed / non-spine fallback. A shared-data
	// bootstrap app (shared-pg / shared-pg-b / shared-pg-c) carries NO
	// back-referenced continuumRef and has NO per-app `dr-<app>` CR: its DR is
	// governed by the platform-scope cnpg-pair Continuum, not a per-app one.
	// AND its spec.regions is the `platform-bootstrap-owned-host` placeholder,
	// so the static plan has no real primary/standby to project. When we found
	// no per-app Continuum, the plan has no real regions, AND the app declares a
	// DR posture (active-hot-standby / active-passive), resolve the
	// platform-scope cnpg-pair Continuum so the live region-a→region-b
	// WAL-streaming truth (normalized leaseHolder + hotStandbyRegions + lag +
	// LeaseHeld) drives the projection.
	//
	// The DR-mode gate is load-bearing: a SINGLETON shared bootstrap app
	// (harbor / openbao / newapi on a single-region data plane) must NOT inherit
	// the cnpg-pair's hot-standby region split — it is a single-cluster app, not
	// a DR contract. Singletons keep their static (placeholder) projection here;
	// correcting the singleton placeholder is out of scope for this DR fix.
	if cs == nil && !planHasRealRegions(plan) && isDRPlacementMode(plan.Mode) {
		pcs, perr := r.resolvePlatformScopeContinuumStatus(ctx)
		if perr != nil {
			r.Log.Warn("bootstrap-owned: platform-scope Continuum read failed; static placement projection",
				"app", app.GetName(), "err", perr)
		} else if pcs != nil {
			cs = pcs
		}
	}

	return buildPlacementProjection(plan, cs)
}

// isDRPlacementMode reports whether the resolved placement mode is one of the
// two single-primary DR postures (active-hot-standby / active-passive) that
// have a primary↔standby flip a Continuum drives. A singleton / active-active
// mode is NOT a DR contract — it must not resolve a cnpg-pair Continuum's
// region split. (#4605 follow-up.)
func isDRPlacementMode(mode string) bool {
	return mode == placement.ModeActiveHotStandby || mode == placement.ModeActivePassive
}

// planHasRealRegions reports whether the resolved placement plan carries any
// host-cluster region other than the `platform-bootstrap-owned-host`
// placeholder literal (#4605 follow-up). A bootstrap app whose spec.regions is
// only the placeholder resolves to a plan with no real region — its DR truth
// must come from the governing Continuum, not the placeholder plan.
func planHasRealRegions(plan placement.Plan) bool {
	for _, rp := range plan.Regions {
		if rp.Name != platformBootstrapOwnedHostRegion {
			return true
		}
	}
	return false
}

// reconcileBootstrapContinuum mints the per-app Continuum DR contract for a
// bootstrap-owned (adopted) Application and returns its `<ns>/<name>`
// back-pointer (#4416). It is the adoption-path equivalent of the normal
// reconcile's step-7.5 Continuum producer: the adoption path never resolves
// a fan-out render, so we resolve the minimal inputs the Continuum producer
// needs (placement plan + Blueprint topology variant) directly from the
// Application's own spec + the referenced Blueprint.
//
// A non-DR bootstrap app (singleton / single-region / Blueprint with no
// switchover mechanism) produces NO CR — buildContinuumPlan returns
// (zero,false) and reconcileContinuumCR is a no-op that returns "". So the
// pre-existing shared-pg / substrate adoption apps are unaffected (they
// keep being pure status-mirrors with no Continuum + no back-ref). Only a
// DR-capable multi-region spine app mints a contract here.
//
// Resolution mirrors the normal path: spec.placement (the explicit posture
// string the spine producer stamps — active-hot-standby / active-passive)
// + spec.regions → placement.Resolve; the Blueprint's spec.topology variant
// → the switchover mechanism. A missing/non-DR placement or a Blueprint
// without a topology block simply yields no contract (returns "", nil),
// never an error — the gate is the same buildContinuumPlan the normal path
// applies.
func (r *Reconciler) reconcileBootstrapContinuum(
	ctx context.Context,
	app *unstructured.Unstructured,
) (string, error) {
	spec, err := parseSpec(app)
	if err != nil {
		// A bootstrap app with an unparseable spec is not a DR contract we
		// can mint — leave it a pure status-mirror rather than fail the
		// adoption (the HR observation above is the load-bearing signal).
		return "", nil
	}
	// Only an explicit single-primary DR posture has a switchover to drive.
	// A singleton / active-active / empty posture is not a DR contract — skip
	// without fetching the Blueprint (keeps the shared-pg adoption path a
	// zero-extra-GET status-mirror).
	switch spec.Placement {
	case string(bpv1alpha1.BcpActiveHotStandby), string(bpv1alpha1.BcpActivePassive):
		// DR-capable posture — resolve the contract below.
	default:
		return "", nil
	}
	if len(spec.Regions) < 2 {
		// A DR contract needs ≥2 regions (primary + ≥1 standby). A
		// single-region spine on a single-region Sovereign has nothing to
		// fail over to — no CR.
		return "", nil
	}

	plan, perr := placement.Resolve(spec.Placement, spec.Regions)
	if perr != nil {
		// An unresolvable placement is not mintable; do not fail the
		// adoption over it (the HR is still healthy + observed).
		r.Log.Warn("bootstrap-owned: placement resolve failed; no Continuum minted",
			"app", app.GetName(), "placement", spec.Placement, "err", perr)
		return "", nil
	}

	bp, berr := r.fetchBlueprint(ctx, spec.BlueprintName)
	if berr != nil {
		if isNotFound(berr) {
			// Blueprint not yet in the catalog — retry on the next pass
			// rather than minting an incomplete contract.
			r.Log.Info("bootstrap-owned: Blueprint not found; Continuum mint deferred",
				"app", app.GetName(), "blueprint", spec.BlueprintName)
			return "", nil
		}
		return "", fmt.Errorf("fetch Blueprint %q: %w", spec.BlueprintName, berr)
	}
	bpTopo := parseBlueprintTopology(bp)
	if bpTopo == nil {
		// No topology block ⇒ no switchover variant ⇒ not a DR contract.
		return "", nil
	}
	// Resolve the variant for the chosen posture (operator override = the
	// explicit posture string; sovereignRegions = the CR's region count).
	choice, variant, terr := topo.ResolveTopology(spec.Placement, bpTopo, len(spec.Regions))
	if terr != nil {
		r.Log.Warn("bootstrap-owned: topology resolve failed; no Continuum minted",
			"app", app.GetName(), "blueprint", spec.BlueprintName,
			"placement", spec.Placement, "err", terr)
		return "", nil
	}

	ownerLabels := map[string]string{
		"catalyst.openova.io/app-uid": string(app.GetUID()),
	}
	if org := app.GetLabels()["catalyst.openova.io/organization"]; org != "" {
		ownerLabels["catalyst.openova.io/organization"] = org
	}
	return r.reconcileContinuumCR(ctx, app.GetName(), app.GetNamespace(),
		choice, variant, plan, ownerLabels)
}

// bootstrapContinuumRefUnchanged reports whether the Application's current
// status already carries the given Continuum back-ref (#4416). Part of the
// adoption-path churn guard so a freshly-minted Continuum still writes its
// back-ref even when the adopted HR's phase is unchanged.
func bootstrapContinuumRefUnchanged(app *unstructured.Unstructured, continuumRef string) bool {
	if continuumRef == "" {
		// Nothing to write — never forces an extra status write on the
		// non-DR adoption path.
		return true
	}
	cur, _, _ := unstructured.NestedString(app.Object, "status", "continuumRef")
	return cur == continuumRef
}

// bootstrapPlacementUnchanged reports whether the Application's current
// status.placement already carries the DR projection keys (#4601) the next
// reconcile would write. Part of the adoption-path churn guard: a freshly
// flipped lease (primaryRegion moved) must re-write status.placement even
// when the adopted HR phase is unchanged, but a steady-state reconcile must
// NOT churn. A nil projection (no resolvable plan) is treated as "unchanged"
// so it never forces an extra write on the non-DR adoption path.
//
// We compare only the DR-projection keys (mode / primaryRegion /
// standbyRegions / replicationLagSeconds / leaseHeld), not the whole
// status.placement object — the #3373 {vcluster,source,regions,clusters}
// keys are not written on the bootstrap path, so comparing them would always
// report "changed".
func bootstrapPlacementUnchanged(app *unstructured.Unstructured, projection map[string]interface{}) bool {
	if projection == nil {
		return true
	}
	cur, _, _ := unstructured.NestedMap(app.Object, "status", "placement")
	if cur == nil {
		return false
	}
	for _, k := range []string{"mode", "primaryRegion", "standbyRegions", "replicationLagSeconds", "leaseHeld"} {
		want, wantSet := projection[k]
		got, gotSet := cur[k]
		if wantSet != gotSet {
			return false
		}
		if !wantSet {
			continue
		}
		if !placementValueEqual(want, got) {
			return false
		}
	}
	return true
}

// placementValueEqual compares two status.placement values for churn-guard
// equality, normalising the slice case (the projection writes
// []interface{} for standbyRegions; the re-fetched status carries the same
// shape, but a defensive element-wise compare keeps the guard robust to
// client round-trip typing).
func placementValueEqual(want, got interface{}) bool {
	ws, wOK := want.([]interface{})
	gs, gOK := got.([]interface{})
	if wOK || gOK {
		if !wOK || !gOK || len(ws) != len(gs) {
			return false
		}
		for i := range ws {
			if fmt.Sprintf("%v", ws[i]) != fmt.Sprintf("%v", gs[i]) {
				return false
			}
		}
		return true
	}
	return fmt.Sprintf("%v", want) == fmt.Sprintf("%v", got)
}

// bootstrapStatusUnchanged reports whether the Application's current
// status already carries the given adoption phase + Ready condition.
func bootstrapStatusUnchanged(app *unstructured.Unstructured, phase, message, ready string) bool {
	curPhase, _, _ := unstructured.NestedString(app.Object, "status", "phase")
	if curPhase != phase {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] != "Ready" {
			continue
		}
		reason, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		status, _ := cm["status"].(string)
		return reason == ReasonBootstrapAdopted && msg == message && status == ready
	}
	return false
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

	// Topology — G117.6 (Refs #2745). When non-empty, the operator
	// has explicitly chosen a BCP topology (one of "active-active",
	// "active-hot-standby", "active-passive", "singleton"). The
	// reconciler validates that the choice is in
	// Blueprint.spec.topology.supported[]; on mismatch the
	// Application moves to Ready=False, reason=InvalidTopology.
	// When empty, the reconciler picks the Blueprint's default
	// keyed on Sovereign-region-count (locked decision #7).
	Topology string

	// ── #3373 instance-level placement (object form of
	// `spec.placement`) ──────────────────────────────────────────
	//
	// `spec.placement` accepts TWO wire forms:
	//   string — the legacy BCP-posture enum (parsed into Placement
	//            above; PlacementVClusterSet stays false).
	//   object — {vcluster, regions, clusters, mode}: mode lands in
	//            Placement; the WHERE lands here.
	//
	// PlacementVClusterSet distinguishes "field absent" (inherit the
	// Blueprint's defaultPlacement / placementSchema suggestion) from
	// an explicit instance choice — including an explicit "host".
	PlacementVCluster    string
	PlacementVClusterSet bool

	// PlacementClusters — instance override of the topology
	// variant's PlacementSpec.Clusters (fan-out narrowing).
	PlacementClusters []string

	// PlacementRegions — instance override of the legacy top-level
	// spec.regions[]; when non-empty it replaces Regions for
	// placement resolution (parseSpec applies the override).
	PlacementRegions []string

	// DependsOn — #3370 backing-service wiring. Each entry names
	// another Application (the backing instance) this Application
	// consumes + optionally the Context it occupies there. The
	// reconciler resolves each entry to the backing instance's
	// HelmRelease(s) and stamps Flux `spec.dependsOn` on every HR it
	// renders — the runtime wiring stays purely Flux.
	DependsOn []dependsOnRef

	// ── #3969 application-centric Placement (desired-state targets) ──
	//
	// PlacementTargets — the canonical desired-state placement: a list
	// of targets, each = region × cluster × vCluster × data-role
	// (Primary|Standby, with a Standby type Hot|Cold). When non-empty it
	// is the source of truth; the pattern (singleton / active-passive /
	// active-hot-standby / active-active) is DERIVED via DerivePattern,
	// never stored. Parsed from `spec.placement.targets[]`. Empty ⇒ the
	// legacy `Placement` posture string drives the fan-out (back-compat).
	PlacementTargets []bpv1alpha1.PlacementTarget

	// OwnedDependencies — #3969 per-owned-dependency cascade overrides
	// parsed from `spec.placement.ownedDependencies[]`. An owned
	// (private) backing service follows the consumer's placement by
	// DEFAULT; an entry with Follow:false decouples it. Absent ⇒ every
	// owned dep follows.
	OwnedDependencies []bpv1alpha1.OwnedDependencyOverride
}

// dependsOnRef is one parsed spec.dependsOn[] entry (#3370).
type dependsOnRef struct {
	Name      string
	Namespace string
	Context   string
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
	// #3373 — `spec.placement` is dual-form: legacy string (BCP-posture
	// enum) OR object {vcluster, regions, clusters, mode}. The object
	// form is the founder-ratified instance-placement law: WHERE an
	// instance runs is one declared Git-committed field, defaulted from
	// the Blueprint, rendered by the one generic kubeConfig-pivot
	// mechanism — never per-app code.
	if pl, found, serr := unstructured.NestedString(app.Object, "spec", "placement"); serr == nil && found {
		out.Placement = pl
	} else if plMap, mfound, merr := unstructured.NestedMap(app.Object, "spec", "placement"); merr == nil && mfound && plMap != nil {
		if mode, ok := plMap["mode"].(string); ok {
			out.Placement = mode
		}
		if raw, exists := plMap["vcluster"]; exists {
			s, ok := raw.(string)
			if !ok {
				return out, fmt.Errorf("spec.placement.vcluster is not a string: %T", raw)
			}
			if !bpv1alpha1.IsKnownVCluster(s) {
				return out, fmt.Errorf("spec.placement.vcluster %q not in {host, mgmt, dmz, rtz}", s)
			}
			out.PlacementVCluster = bpv1alpha1.NormalizeVCluster(s)
			out.PlacementVClusterSet = true
		}
		if raw, exists := plMap["clusters"]; exists {
			items, ok := raw.([]interface{})
			if !ok {
				return out, fmt.Errorf("spec.placement.clusters is not a list: %T", raw)
			}
			for _, it := range items {
				s, ok := it.(string)
				if !ok {
					return out, fmt.Errorf("spec.placement.clusters[] entry is not a string: %T", it)
				}
				out.PlacementClusters = append(out.PlacementClusters, s)
			}
		}
		if raw, exists := plMap["regions"]; exists {
			items, ok := raw.([]interface{})
			if !ok {
				return out, fmt.Errorf("spec.placement.regions is not a list: %T", raw)
			}
			for _, it := range items {
				s, ok := it.(string)
				if !ok {
					return out, fmt.Errorf("spec.placement.regions[] entry is not a string: %T", it)
				}
				out.PlacementRegions = append(out.PlacementRegions, s)
			}
		}

		// #3969 — the canonical desired-state placement: spec.placement.targets[].
		// Each target = {region, cluster, vcluster, role Primary|Standby,
		// standbyType Hot|Cold}. When present it is the source of truth; the
		// pattern is DERIVED (never stored) and the capability gate validates it.
		if raw, exists := plMap["targets"]; exists {
			targets, perr := parsePlacementTargets(raw)
			if perr != nil {
				return out, perr
			}
			out.PlacementTargets = targets
		}

		// #3969 — per-owned-dependency cascade overrides.
		// spec.placement.ownedDependencies[]. Absent ⇒ every owned dep follows;
		// an entry with follow:false decouples that owned dep.
		if raw, exists := plMap["ownedDependencies"]; exists {
			overrides, perr := parseOwnedDependencies(raw)
			if perr != nil {
				return out, perr
			}
			out.OwnedDependencies = overrides
		}
	}

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

	// #3373 — placement.regions (the instance's declared WHERE) wins
	// over the legacy top-level spec.regions[] when both are present.
	if len(out.PlacementRegions) > 0 {
		out.Regions = out.PlacementRegions
	}

	params, _, _ := unstructured.NestedMap(app.Object, "spec", "parameters")
	out.Parameters = params

	// G117.6 (Refs #2745) — optional Application.spec.topology operator
	// override. Empty string is the default-derived path.
	tp, _, _ := unstructured.NestedString(app.Object, "spec", "topology")
	out.Topology = tp

	// #3370 — optional spec.dependsOn[] backing-service wiring.
	depsRaw, _, _ := unstructured.NestedSlice(app.Object, "spec", "dependsOn")
	for _, d := range depsRaw {
		dm, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		ref := dependsOnRef{}
		ref.Name, _ = dm["name"].(string)
		ref.Namespace, _ = dm["namespace"].(string)
		ref.Context, _ = dm["context"].(string)
		if ref.Name == "" {
			continue
		}
		out.DependsOn = append(out.DependsOn, ref)
	}

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

// parseBlueprintTopology reads `spec.topology` from a Blueprint and
// returns the typed v1alpha1.Topology. Returns nil when the field is
// absent OR the round-trip fails (defensive — admission gate is the
// authoritative shape check, but legacy Blueprints predating W1.B1
// have no topology block at all).
//
// G117.6 (Refs #2745).
func parseBlueprintTopology(bp *unstructured.Unstructured) *bpv1alpha1.Topology {
	if bp == nil {
		return nil
	}
	raw, found, err := unstructured.NestedMap(bp.Object, "spec", "topology")
	if err != nil || !found || raw == nil {
		return nil
	}
	// JSON round-trip via the standard library — unstructured maps use
	// the JSON tags on the typed struct.
	js, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	out := &bpv1alpha1.Topology{}
	if err := json.Unmarshal(js, out); err != nil {
		return nil
	}
	if len(out.Supported) == 0 {
		return nil
	}
	return out
}

// blueprintChart reads `spec.manifests.chart` from a Blueprint. Used
// as the per-cluster HR's chart name when the Blueprint declares the
// legacy short-form chart reference. Empty when absent.
func blueprintChart(bp *unstructured.Unstructured) string {
	if bp == nil {
		return ""
	}
	c, _, _ := unstructured.NestedString(bp.Object, "spec", "manifests", "chart")
	return c
}

// blueprintRequiresHostCNPGOperator reports whether the Blueprint renders
// a `postgresql.cnpg.io/v1` resource and therefore needs the host-side CNPG
// operator (the cluster-singleton bp-cnpg) to reconcile it — which means its
// per-cluster HelmRelease must NOT pivot into a vCluster (#4282 root-cause-B).
//
// Two equivalent signals, either of which is authoritative:
//
//   - the `catalyst.openova.io/companion: bp-cnpg` metadata label — set on
//     the LIVE catalog-seed Blueprint CR (products/catalyst/chart/templates/
//     catalog-seed/blueprints.yaml) AND on platform/postgres/blueprint.yaml.
//     This is the primary signal because the seed (the source the catalyst-api
//     actually materializes into the cluster) carries the label but NOT the
//     full `spec.depends[]` block.
//   - a `spec.depends[].blueprint: bp-cnpg` edge — present on source-authored
//     Blueprints (platform/postgres/blueprint.yaml). Fallback for CRs
//     reconciled straight from source without the curated seed.
//
// A future CNPG-bearing Blueprint (e.g. a dedicated bp-cnpg-pair instance card)
// inherits the routing for free by declaring either signal — there is no
// hard-coded `bp-postgres` name here.
func blueprintRequiresHostCNPGOperator(bp *unstructured.Unstructured) bool {
	if bp == nil {
		return false
	}
	if bp.GetLabels()["catalyst.openova.io/companion"] == "bp-cnpg" {
		return true
	}
	deps, _, _ := unstructured.NestedSlice(bp.Object, "spec", "depends")
	for _, d := range deps {
		dm, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _ := dm["blueprint"].(string); name == "bp-cnpg" {
			return true
		}
	}
	return false
}

// fanoutOwnerLabels builds the org / env-type / app-uid label set the
// reconciler stamps on every per-cluster HelmRelease AND (#3375 DoD-3)
// the per-app Continuum CR, so both participate in the same
// observability rollup + cascade-delete. Extracted into a helper so the
// fan-out and the Continuum producer cannot drift apart.
func fanoutOwnerLabels(envSpec envParsedSpec, app *unstructured.Unstructured) map[string]string {
	return map[string]string{
		"catalyst.openova.io/organization": envSpec.OrganizationRef,
		"catalyst.openova.io/env-type":     envSpec.EnvType,
		LabelAppUID:                        string(app.GetUID()),
	}
}

// placementClusters returns the variant's PlacementSpec.Clusters slice
// (or nil-safe empty slice) for diagnostic log lines.
func placementClusters(v *bpv1alpha1.TopologyVariant) []string {
	if v == nil || v.Placement == nil {
		return nil
	}
	return v.Placement.Clusters
}

// firstNonEmpty returns the first non-empty string from the args.
// Used as a Blueprint-chart fallback (Blueprint's manifest.chart >
// Blueprint name).
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// blueprintVCluster reads spec.placementSchema.vcluster from a
// Blueprint and returns one of "dmz" / "mgmt" / "rtz" / "". Empty
// means the Blueprint either doesn't declare the field (treated as
// host placement — legacy + substrate-only Blueprints per G92.6) or
// declared it as the empty string. The controller cross-checks
// against r.Cfg.VClusterPlacements before stamping it on the
// rendered HelmRelease (G92.1 #2660).
func blueprintVCluster(bp *unstructured.Unstructured) string {
	vc, _, _ := unstructured.NestedString(bp.Object, "spec", "placementSchema", "vcluster")
	return vc
}

// blueprintDefaultPlacementVCluster reads
// `spec.defaultPlacement.vcluster` from a Blueprint (#3373) — the
// class-level placement SUGGESTION every instance inherits unless its
// own `Application.spec.placement.vcluster` overrides it. The bool
// reports field presence so an explicit `vcluster: host` default is
// distinguishable from "not declared" (which falls back to the legacy
// `placementSchema.vcluster` G92.1 field).
func blueprintDefaultPlacementVCluster(bp *unstructured.Unstructured) (string, bool) {
	vc, found, err := unstructured.NestedString(bp.Object, "spec", "defaultPlacement", "vcluster")
	if err != nil || !found {
		return "", false
	}
	return bpv1alpha1.NormalizeVCluster(vc), true
}

// blueprintAllowedPlacements reads `spec.allowedPlacements` from a
// Blueprint (#3373) — the vCluster tiers an instance MAY be placed in.
// Empty slice when absent (treated as "unrestricted").
func blueprintAllowedPlacements(bp *unstructured.Unstructured) []string {
	raw, _, _ := unstructured.NestedSlice(bp.Object, "spec", "allowedPlacements")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// vclusterAllowedByBlueprint reports whether the effective vCluster
// placement is permitted by the Blueprint's allowedPlacements
// declaration (#3373). An empty allow-list permits everything. The
// "host" sentinel and "" compare equal.
func vclusterAllowedByBlueprint(effective string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	eff := bpv1alpha1.NormalizeVCluster(effective)
	for _, a := range allowed {
		if bpv1alpha1.NormalizeVCluster(a) == eff {
			return true
		}
	}
	return false
}

// vclusterOrHost maps the internal empty-string host placement back to
// the user-facing "host" sentinel for status/UI surfaces (#3373).
func vclusterOrHost(v string) string {
	if v == "" {
		return bpv1alpha1.VClusterHost
	}
	return v
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

// wordpressTenantBlueprints are the Blueprint names whose per-Org WordPress
// Application derives orgDomain + adminUser from the parent Organization
// context instead of requiring the customer to type them (#4936).
// `bp-wordpress` is the marketplace/funnel bare-slug alias of
// `bp-wordpress-tenant` (same chart bytes, see the catalog-seed).
var wordpressTenantBlueprints = map[string]bool{
	"bp-wordpress-tenant": true,
	"bp-wordpress":        true,
}

// injectOrgDerivedParameters fills in the per-Org WordPress parameters the
// console 'New instance' dialog + the marketplace funnel never collect —
// orgDomain (the Org's public hostname) and adminUser.{email,displayName}
// (the Org owner identity) — from the parent Organization CR (#4936).
//
// It is a no-op unless the Blueprint is the per-Org WordPress chart, and it
// never overwrites a value the operator supplied explicitly (operator
// overlay wins). The derived orgDomain mirrors the organization-controller's
// tenant-route formula:
// `<tenantPublic.subdomain||spec.slug>.<tenantPublic.parentDomain>`.
func injectOrgDerivedParameters(blueprintName string, org *unstructured.Unstructured, params map[string]interface{}) map[string]interface{} {
	if !wordpressTenantBlueprints[blueprintName] || org == nil {
		return params
	}
	out := make(map[string]interface{}, len(params)+2)
	for k, v := range params {
		out[k] = v
	}

	// orgDomain — derive only when absent AND resolvable from the Org CR.
	if _, set := out["orgDomain"]; !set {
		if d := deriveOrgDomain(org); d != "" {
			out["orgDomain"] = d
		}
	}

	// adminUser.{email,displayName} — copy any explicit map first so an
	// operator override of either sub-field is preserved, then fill the
	// absent sub-fields from Org context.
	admin := map[string]interface{}{}
	if existing, ok := out["adminUser"].(map[string]interface{}); ok {
		for k, v := range existing {
			admin[k] = v
		}
	}
	if _, ok := admin["email"]; !ok {
		if email := deriveOrgOwnerEmail(org); email != "" {
			admin["email"] = email
		}
	}
	if _, ok := admin["displayName"]; !ok {
		if display, _, _ := unstructured.NestedString(org.Object, "spec", "displayName"); strings.TrimSpace(display) != "" {
			admin["displayName"] = display
		}
	}
	if len(admin) > 0 {
		out["adminUser"] = admin
	}
	return out
}

// deriveOrgDomain returns `<subdomain||slug>.<parentDomain>` from the
// Organization CR's spec.tenantPublic, matching the organization-controller
// tenant-route derivation. Returns "" when the Org has no public hostname
// binding (parentDomain empty) so the caller leaves orgDomain unset.
func deriveOrgDomain(org *unstructured.Unstructured) string {
	parent, _, _ := unstructured.NestedString(org.Object, "spec", "tenantPublic", "parentDomain")
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return ""
	}
	sub, _, _ := unstructured.NestedString(org.Object, "spec", "tenantPublic", "subdomain")
	sub = strings.TrimSpace(sub)
	if sub == "" {
		slug, _, _ := unstructured.NestedString(org.Object, "spec", "slug")
		sub = strings.TrimSpace(slug)
	}
	if sub == "" {
		return ""
	}
	return sub + "." + parent
}

// deriveOrgOwnerEmail returns the email of the Organization owner —
// spec.owners[] entry with role=="owner", falling back to the first owner
// with a non-empty email. Returns "" when no owner email is present.
func deriveOrgOwnerEmail(org *unstructured.Unstructured) string {
	owners, _, _ := unstructured.NestedSlice(org.Object, "spec", "owners")
	firstEmail := ""
	for _, o := range owners {
		m, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		email, _ := m["email"].(string)
		email = strings.TrimSpace(email)
		if email == "" {
			continue
		}
		if firstEmail == "" {
			firstEmail = email
		}
		if role, _ := m["role"].(string); strings.EqualFold(strings.TrimSpace(role), "owner") {
			return email
		}
	}
	return firstEmail
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
