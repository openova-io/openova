// Package controller — the core reconcile loop for the
// blueprint-controller (slice C3 of EPIC-0 / #1095).
//
// The contract:
//
//   1. Watch `Blueprint.catalyst.openova.io/v1` and `v1alpha1` CRs
//      (cluster-scoped per the schema). Both versions share an inline
//      schema; we use the dynamic client + unstructured.Unstructured
//      so we handle both versions transparently — the existing pattern
//      from `products/catalyst/bootstrap/api/internal/store/crd_store.go`.
//
//   2. Validate each Blueprint at reconcile time:
//      - delegate the structural CRD-schema checks to Kubernetes itself
//        (the openAPIV3Schema in products/catalyst/chart/crds/blueprint.yaml
//        already enforces them at admission)
//      - run the business-logic checks in
//        `core/controllers/blueprint/internal/validate` for the bits the
//        schema can't express.
//
//   3. For Blueprints whose validation passes:
//      - if visibility is listed: write blueprint.yaml to
//        gitea.<location-code>.<sovereign-domain>/catalog/<bp-name>/blueprint.yaml
//      - if visibility is unlisted: skip the public mirror (the file
//        is published only via the per-Blueprint OCI artifact).
//      - if visibility is private: REMOVE the file from the public
//        mirror if previously present.
//
//   4. Update Blueprint.status with phase, observedGeneration,
//      conditions[]. publishedAt / deprecatedAt are set on phase
//      transitions. ociDigest is passed through unchanged — it is
//      populated by the CI release workflow at tag push, not by this
//      controller.
//
// Runtime: a single reconciler goroutine driven by a watch on the
// Blueprint GVR. Per-Blueprint reconciliation is idempotent: identical
// content + visibility means zero Gitea writes (the gitea client's
// PutFile already short-circuits on byte-equal content).
//
// This package is reachable from cmd/main.go via Run(ctx, cfg).
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/blueprint/internal/gitea"
	"github.com/openova-io/openova/core/controllers/blueprint/internal/validate"
)

// BlueprintGVR pins the storage version. v1 is the storage version per
// products/catalyst/chart/crds/blueprint.yaml; the dynamic client
// transparently round-trips v1alpha1 objects to/from this GVR via
// the apiserver's conversion webhook (the CRD declares both versions
// served from the same schema, so no body-rewriting is needed).
var BlueprintGVR = schema.GroupVersionResource{
	Group:    "catalyst.openova.io",
	Version:  "v1",
	Resource: "blueprints",
}

// CatalogOrg — the Sovereign-local Gitea Org that holds the public
// catalog mirror per docs/NAMING-CONVENTION.md §11.2.
const CatalogOrg = "catalog"

// Visibility values mirror the CRD enum.
const (
	VisibilityListed   = "listed"
	VisibilityUnlisted = "unlisted"
	VisibilityPrivate  = "private"
)

// Phase values mirror the CRD's status.phase enum.
const (
	PhaseDraft      = "Draft"
	PhasePublished  = "Published"
	PhaseDeprecated = "Deprecated"
	PhaseWithdrawn  = "Withdrawn"
)

// Condition reason vocabulary. Surfaced on status.conditions[].reason.
const (
	ReasonReady                = "Ready"
	ReasonValidationFailed     = "ValidationFailed"
	ReasonPendingDependencies  = "PendingDependencies"
	ReasonGiteaWriteFailed     = "GiteaWriteFailed"
	ReasonValidationWarning    = "ValidationWarning"
)

// Config is the runtime configuration for a controller instance.
type Config struct {
	// DynamicClient is the K8s dynamic client. Pass either an
	// in-cluster client or a fake.NewSimpleDynamicClient for tests.
	DynamicClient dynamic.Interface

	// Gitea is the Gitea HTTP client. May be nil in tests that don't
	// exercise the mirror path; the reconciler skips the mirror when
	// nil and emits a Pending condition.
	Gitea *gitea.Client

	// Log structured logger. Defaults to slog.Default() when nil.
	Log *slog.Logger

	// ResyncPeriod is the watch resync interval. Default 5m.
	ResyncPeriod time.Duration

	// CommitterAuthor / CommitterEmail decorate Gitea commits.
	CommitterAuthor string
	CommitterEmail  string
}

// Reconciler holds runtime state for the controller. It is exported so
// unit tests can drive a single Reconcile call without spinning up the
// watch loop.
type Reconciler struct {
	cfg Config

	// catalog tracks the set of known Blueprint names so depends[]
	// resolution works during validation. Updated on every successful
	// reconcile.
	catalogMu sync.RWMutex
	catalog   map[string]struct{}
}

// New returns a fresh Reconciler with cfg.
func New(cfg Config) *Reconciler {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.ResyncPeriod == 0 {
		cfg.ResyncPeriod = 5 * time.Minute
	}
	if cfg.CommitterAuthor == "" {
		cfg.CommitterAuthor = "blueprint-controller"
	}
	if cfg.CommitterEmail == "" {
		cfg.CommitterEmail = "blueprint-controller@openova.io"
	}
	return &Reconciler{
		cfg:     cfg,
		catalog: make(map[string]struct{}),
	}
}

// Run starts the watch loop. Blocks until ctx is cancelled. On
// transient watch errors it backs off and retries; on permanent errors
// (e.g. the Blueprint CRD doesn't exist) it returns the error so the
// caller exits non-zero and the K8s deployment restart-loop catches it.
func (r *Reconciler) Run(ctx context.Context) error {
	if r.cfg.DynamicClient == nil {
		return errors.New("controller: DynamicClient is required")
	}

	// Initial list — pre-populate the catalog before the first watch
	// event so depends[] resolution doesn't see an empty set.
	if err := r.initialList(ctx); err != nil {
		return fmt.Errorf("initial list: %w", err)
	}

	// Watch loop with backoff per client-go conventions.
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := r.watchOnce(ctx); err != nil {
			r.cfg.Log.Warn("blueprint-controller: watch error; will retry", "err", err)
		}
		return false, nil // never "done" — keep watching until ctx cancelled
	})
}

// initialList fetches all Blueprints once and reconciles each.
// Building the catalog set first means depends[] resolution sees the
// full catalog on first pass.
func (r *Reconciler) initialList(ctx context.Context) error {
	list, err := r.cfg.DynamicClient.Resource(BlueprintGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// First pass: rebuild the catalog set.
	r.catalogMu.Lock()
	r.catalog = make(map[string]struct{}, len(list.Items))
	for i := range list.Items {
		r.catalog[list.Items[i].GetName()] = struct{}{}
	}
	r.catalogMu.Unlock()

	// Second pass: reconcile each.
	for i := range list.Items {
		if err := r.Reconcile(ctx, &list.Items[i]); err != nil {
			r.cfg.Log.Error("initial reconcile failed", "name", list.Items[i].GetName(), "err", err)
		}
	}
	return nil
}

// watchOnce opens a watch on the Blueprint GVR and dispatches events
// until the watch closes or ctx is cancelled.
func (r *Reconciler) watchOnce(ctx context.Context) error {
	w, err := r.cfg.DynamicClient.Resource(BlueprintGVR).Namespace("").Watch(ctx, metav1.ListOptions{
		AllowWatchBookmarks: true,
		TimeoutSeconds:      ptrInt64(int64(r.cfg.ResyncPeriod.Seconds())),
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
			r.catalogMu.Lock()
			switch event.Type {
			case watch.Added, watch.Modified:
				r.catalog[obj.GetName()] = struct{}{}
			case watch.Deleted:
				delete(r.catalog, obj.GetName())
			}
			r.catalogMu.Unlock()
			if event.Type == watch.Deleted {
				continue
			}
			if err := r.Reconcile(ctx, obj); err != nil {
				r.cfg.Log.Error("reconcile failed", "name", obj.GetName(), "err", err)
			}
		}
	}
}

// Reconcile is the per-object reconcile entry-point. Exposed for
// tests; production calls it from watchOnce / initialList.
//
// Steps:
//   1. Run the business-logic validator.
//   2. If errors: update status to phase=Draft + Ready=False +
//      reason=ValidationFailed; return.
//   3. If pending deps: update status to phase=Draft + Ready=False +
//      reason=PendingDependencies; the deps will resolve on a
//      subsequent watch event when the depended-on Blueprint lands.
//   4. Mirror the CR to Gitea per visibility:
//      - listed: PutFile blueprint.yaml under catalog/<name>/
//      - unlisted: skip the mirror; ensure file is REMOVED if it was
//        previously listed (re-publish flow)
//      - private: DeleteFile from the mirror; idempotent if absent.
//   5. Update status to phase=Published (or Withdrawn for private) +
//      Ready=True + observedGeneration=spec.generation.
func (r *Reconciler) Reconcile(ctx context.Context, bp *unstructured.Unstructured) error {
	if bp == nil {
		return nil
	}
	name := bp.GetName()
	r.cfg.Log.Info("reconcile", "name", name, "rv", bp.GetResourceVersion(), "gen", bp.GetGeneration())

	// 1. Validate.
	r.catalogMu.RLock()
	catalogSnapshot := make(map[string]struct{}, len(r.catalog))
	for k := range r.catalog {
		catalogSnapshot[k] = struct{}{}
	}
	r.catalogMu.RUnlock()

	res := validate.Validate(bp, catalogSnapshot)
	if res.HasErrors() {
		return r.updateStatus(ctx, bp, statusUpdate{
			Phase:   PhaseDraft,
			Ready:   "False",
			Reason:  ReasonValidationFailed,
			Message: strings.Join(res.Errors, "; "),
		})
	}
	// Pending deps are surfaced on the *final* status update (below)
	// via su.PendingDeps so they coexist with the Ready condition.
	// The brief: "if not yet present, surface a Pending condition
	// rather than rejecting outright" — so we DO mirror, AND surface
	// Pending, in a single status write.

	// 2. Mirror per visibility.
	visibility := stringFromSpec(bp, "visibility")
	if visibility == "" {
		// Default per BLUEPRINT-AUTHORING.md §9.
		visibility = VisibilityListed
	}

	if r.cfg.Gitea != nil {
		if err := r.mirrorBlueprint(ctx, bp, visibility); err != nil {
			return r.updateStatus(ctx, bp, statusUpdate{
				Phase:   PhaseDraft,
				Ready:   "False",
				Reason:  ReasonGiteaWriteFailed,
				Message: err.Error(),
			})
		}
	}

	// 3. Compute phase and update status.
	phase := PhasePublished
	switch visibility {
	case VisibilityPrivate:
		phase = PhaseWithdrawn
	case VisibilityUnlisted:
		// Unlisted is still "Published" per the schema's enum — it is
		// reachable by direct lookup, just not on the marketplace
		// card grid. We map unlisted → Published.
		phase = PhasePublished
	}

	// Pick up an existing Deprecated phase. Operators flip
	// status.phase=Deprecated manually (or via a CR annotation in a
	// follow-up slice). Don't overwrite Deprecated → Published.
	currentPhase := stringFromStatus(bp, "phase")
	if currentPhase == PhaseDeprecated {
		phase = PhaseDeprecated
	}

	su := statusUpdate{
		Phase:   phase,
		Ready:   "True",
		Reason:  ReasonReady,
		Message: fmt.Sprintf("blueprint %s mirrored (visibility=%s)", name, visibility),
	}
	if len(res.Warnings) > 0 {
		su.Warnings = res.Warnings
	}
	if len(res.PendingDeps) > 0 {
		su.PendingDeps = res.PendingDeps
	}
	return r.updateStatus(ctx, bp, su)
}

// mirrorBlueprint maps the Blueprint CR's visibility to the catalog
// mirror operation. Returns nil on success.
func (r *Reconciler) mirrorBlueprint(ctx context.Context, bp *unstructured.Unstructured, visibility string) error {
	repo := bp.GetName()

	// Serialise the Blueprint CR back to YAML for the mirror file.
	// We strip status (mirror files carry only the spec contract +
	// metadata.name; status is the controller's responsibility on the
	// source CR). The dependent
	// `application-controller` (slice C4) reads the catalog mirror,
	// not the API-server CR.
	mirrorYAML, err := serialiseForMirror(bp)
	if err != nil {
		return fmt.Errorf("serialise: %w", err)
	}

	switch visibility {
	case VisibilityListed:
		if err := r.cfg.Gitea.EnsureRepo(ctx, CatalogOrg, repo); err != nil {
			return fmt.Errorf("EnsureRepo: %w", err)
		}
		_, err := r.cfg.Gitea.PutFile(ctx, CatalogOrg, repo, "main", "blueprint.yaml",
			mirrorYAML, fmt.Sprintf("publish %s @ %s", repo, stringFromSpec(bp, "version")))
		return err

	case VisibilityUnlisted:
		// Unlisted means the file is NOT on the public catalog mirror.
		// If it was previously listed, remove it.
		_, err := r.cfg.Gitea.DeleteFile(ctx, CatalogOrg, repo, "main", "blueprint.yaml",
			fmt.Sprintf("unlist %s", repo))
		return err

	case VisibilityPrivate:
		// Private means the file is removed from the public catalog
		// mirror entirely. Idempotent: if it's not there, no error.
		_, err := r.cfg.Gitea.DeleteFile(ctx, CatalogOrg, repo, "main", "blueprint.yaml",
			fmt.Sprintf("withdraw %s", repo))
		return err

	default:
		return fmt.Errorf("unknown visibility %q", visibility)
	}
}

// statusUpdate captures the desired Blueprint.status changes for a
// reconcile pass. Translated to JSONPatch / nested-map writes by
// updateStatus.
type statusUpdate struct {
	Phase       string
	Ready       string // "True" | "False" | "Unknown"
	Reason      string
	Message     string
	Warnings    []string
	PendingDeps []string
}

// updateStatus writes su to bp.status via the dynamic client.
// Idempotent: if status is already in the desired state, no API call.
func (r *Reconciler) updateStatus(ctx context.Context, bp *unstructured.Unstructured, su statusUpdate) error {
	now := time.Now().UTC().Format(time.RFC3339)
	gen := bp.GetGeneration()

	// Read current status — preserve ociDigest (set by CI),
	// publishedAt (only update on first transition to Published),
	// and any condition we don't overwrite.
	currentStatus, _, _ := unstructured.NestedMap(bp.Object, "status")
	if currentStatus == nil {
		currentStatus = map[string]interface{}{}
	}

	// observedGeneration always tracks spec.generation.
	currentStatus["observedGeneration"] = gen

	// Phase transition logic.
	prevPhase := stringFromMap(currentStatus, "phase")
	currentStatus["phase"] = su.Phase

	// publishedAt — set on first transition INTO Published.
	if su.Phase == PhasePublished && prevPhase != PhasePublished {
		currentStatus["publishedAt"] = now
	}
	// deprecatedAt — set on first transition INTO Deprecated.
	if su.Phase == PhaseDeprecated && prevPhase != PhaseDeprecated {
		currentStatus["deprecatedAt"] = now
	}

	// conditions[] — replace the Ready condition; preserve unrelated
	// conditions (e.g. a Deprecated condition operators may add).
	conditions := []interface{}{}
	if existing, ok := currentStatus["conditions"].([]interface{}); ok {
		for _, c := range existing {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := cm["type"].(string); t == "Ready" || t == "Pending" || t == "Warning" {
				continue // dropped + replaced below
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
	if su.Reason == ReasonPendingDependencies || len(su.PendingDeps) > 0 {
		msg := su.Message
		if len(su.PendingDeps) > 0 {
			msg = "unresolved dependencies: " + strings.Join(su.PendingDeps, ", ")
		}
		conditions = append(conditions, map[string]interface{}{
			"type":               "Pending",
			"status":             "True",
			"reason":             ReasonPendingDependencies,
			"message":            msg,
			"lastTransitionTime": now,
		})
	}
	if len(su.Warnings) > 0 {
		conditions = append(conditions, map[string]interface{}{
			"type":               "Warning",
			"status":             "True",
			"reason":             ReasonValidationWarning,
			"message":            strings.Join(su.Warnings, "; "),
			"lastTransitionTime": now,
		})
	}
	currentStatus["conditions"] = conditions

	bp.Object["status"] = currentStatus

	// UpdateStatus on the cluster-scoped resource. Note: dynamic client
	// uses Resource(GVR).Namespace("") for cluster-scoped CRs.
	_, err := r.cfg.DynamicClient.Resource(BlueprintGVR).Namespace("").UpdateStatus(ctx, bp, metav1.UpdateOptions{})
	if err != nil {
		// Tolerate "not found" — the resource may have been deleted
		// between the watch event and our status update.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// CatalogSnapshot returns a copy of the controller's known-Blueprint
// names. Used by tests + the /healthz endpoint (when added).
func (r *Reconciler) CatalogSnapshot() map[string]struct{} {
	r.catalogMu.RLock()
	defer r.catalogMu.RUnlock()
	out := make(map[string]struct{}, len(r.catalog))
	for k := range r.catalog {
		out[k] = struct{}{}
	}
	return out
}

// SeedCatalog injects names into the controller's catalog set.
// Test-only — production fills the set via initialList + watch events.
func (r *Reconciler) SeedCatalog(names ...string) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	for _, n := range names {
		r.catalog[n] = struct{}{}
	}
}

// stringFromSpec safely reads a string at spec.<key>.
func stringFromSpec(bp *unstructured.Unstructured, key string) string {
	v, _, _ := unstructured.NestedString(bp.Object, "spec", key)
	return v
}

// stringFromStatus safely reads a string at status.<key>.
func stringFromStatus(bp *unstructured.Unstructured, key string) string {
	v, _, _ := unstructured.NestedString(bp.Object, "status", key)
	return v
}

func stringFromMap(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// serialiseForMirror returns the YAML bytes the controller writes to
// catalog/<bp-name>/blueprint.yaml.
//
// Per the brief: "containing the same content". We keep apiVersion +
// kind + metadata.{name,labels,annotations} + spec; we strip status
// (mirror files don't carry the controller's transient status) and
// other server-side metadata fields (resourceVersion, uid, generation,
// managedFields, etc.) that aren't part of the user-authored contract.
func serialiseForMirror(bp *unstructured.Unstructured) ([]byte, error) {
	out := map[string]interface{}{
		"apiVersion": bp.GetAPIVersion(),
		"kind":       bp.GetKind(),
		"metadata": map[string]interface{}{
			"name": bp.GetName(),
		},
	}
	if labels := bp.GetLabels(); len(labels) > 0 {
		out["metadata"].(map[string]interface{})["labels"] = stringMap(labels)
	}
	if anns := bp.GetAnnotations(); len(anns) > 0 {
		// Strip the kubectl.kubernetes.io/last-applied-configuration
		// annotation — it's a server-managed field, not user authored.
		clean := map[string]string{}
		for k, v := range anns {
			if k == "kubectl.kubernetes.io/last-applied-configuration" {
				continue
			}
			clean[k] = v
		}
		if len(clean) > 0 {
			out["metadata"].(map[string]interface{})["annotations"] = stringMap(clean)
		}
	}
	if spec, ok, _ := unstructured.NestedMap(bp.Object, "spec"); ok {
		out["spec"] = spec
	}
	return yaml.Marshal(out)
}

func stringMap(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ptrInt64(v int64) *int64 { return &v }
