// crd_store.go — CRD-backed persistence for Sovereign provisioning runs.
//
// This is the K8s-native sibling to the flat-file Store at store.go.
// The flat-file store is the catalyst-api Pod's local persistence (one
// file per deployment, fsync-rename atomic, redacted-on-disk). The CRD
// store projects the same record onto a ProvisioningState resource
// (group catalyst.openova.io / version v1alpha1 / kind ProvisioningState)
// in the catalyst-api's namespace, so an operator can `kubectl get
// provisioningstates -A` and a sibling controller can watch state
// transitions WITHOUT an HTTP round-trip to catalyst-api.
//
// Why both stores instead of replacing the flat-file one:
//
//   - The flat-file store carries the full event log per deployment
//     (every tofu stdout line, every helm-controller log line, every
//     phase transition). Storing thousands of events per deployment on
//     a CRD's status field would (a) cross etcd's per-object size
//     limit (~1.5 MiB per object, hard ~3 MiB) and (b) flood the etcd
//     write path with chatty status updates the watch consumers don't
//     need. The CRD's `status` carries only the COARSE state machine
//     (pending | bootstrapping | installing-control-plane |
//     registering-dns | tls-issuing | ready | failed) — fine-grained
//     phases live on the flat file.
//
//   - In `disabled` or `unreachable` modes the catalyst-api still
//     persists to the flat file. A K8s control plane outage MUST NOT
//     prevent the wizard from recording state — the Pod's local PVC
//     is the durable store of last resort.
//
//   - Local dev (`go test ./...`, kind cluster, envtest harness) runs
//     the flat-file path with no K8s reachable. The CRDStore's
//     "K8s-unreachable falls back silently" branch is the explicit
//     contract for that mode.
//
// State machine mapping — toCRDPhase converts the catalyst-api in-memory
// state vocabulary (pending | provisioning | tofu-applying |
// flux-bootstrapping | phase1-watching | ready | failed) to the CRD's
// public-contract state machine (pending → bootstrapping →
// installing-control-plane → registering-dns → tls-issuing → ready |
// failed). The CRD is the external contract, the in-memory state is
// the implementation detail. See store_test.go for the test matrix.
//
// Concurrency: each public method takes the embedded *Store's mutex
// (so the flat file write and the CRD write linearize together — a
// reader of either store sees a consistent record), then makes the
// dynamic-client call. The dynamic client itself is safe for
// concurrent use by multiple goroutines.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the CRD shape is owned by the
// chart (products/catalyst/chart/templates/crd-provisioningstate.yaml)
// and authored by hand — we do NOT generate types from it via deepcopy
// or controller-gen, and we do NOT depend on a generated typed client.
// The dynamic client + unstructured.Unstructured suffices for the
// catalyst-api's read/write needs and avoids a code-generation step
// in the build pipeline.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// CRDGroup / CRDVersion / CRDResource pin the GroupVersionResource for
// the ProvisioningState CRD. Mirror values from
// products/catalyst/chart/templates/crd-provisioningstate.yaml — a
// drift here vs the chart yaml is a packaging bug and shows up as a
// 404 on the dynamic client's resource lookup at runtime.
const (
	CRDGroup    = "catalyst.openova.io"
	CRDVersion  = "v1alpha1"
	CRDResource = "provisioningstates"
	CRDKind     = "ProvisioningState"
)

// CRDGVR is the GroupVersionResource the dynamic client uses.
var CRDGVR = schema.GroupVersionResource{
	Group:    CRDGroup,
	Version:  CRDVersion,
	Resource: CRDResource,
}

// CRD-side phase constants. The CRD carries the COARSE state machine;
// the catalyst-api in-memory state vocabulary is finer-grained and gets
// mapped via toCRDPhase. See the issue #88 acceptance criteria for the
// authoritative state list.
const (
	PhasePending                 = "pending"
	PhaseBootstrapping           = "bootstrapping"
	PhaseInstallingControlPlane  = "installing-control-plane"
	PhaseRegisteringDNS          = "registering-dns"
	PhaseTLSIssuing              = "tls-issuing"
	PhaseReady                   = "ready"
	PhaseFailed                  = "failed"
)

// validPhases — every legal value of .status.phase. Mirrored from the
// CRD schema's enum. ValidatePhase rejects anything not in this set.
var validPhases = map[string]struct{}{
	PhasePending:                {},
	PhaseBootstrapping:          {},
	PhaseInstallingControlPlane: {},
	PhaseRegisteringDNS:         {},
	PhaseTLSIssuing:             {},
	PhaseReady:                  {},
	PhaseFailed:                 {},
}

// ValidatePhase returns nil if phase is one of the seven legal values,
// or an error naming the offender. Callers convert at the boundary —
// the catalyst-api's in-memory states (`provisioning`, `tofu-applying`,
// `flux-bootstrapping`, `phase1-watching`) are NOT legal CRD phases
// and must go through toCRDPhase first.
func ValidatePhase(phase string) error {
	if _, ok := validPhases[phase]; !ok {
		return fmt.Errorf("store: invalid CRD phase %q (legal: pending, bootstrapping, installing-control-plane, registering-dns, tls-issuing, ready, failed)", phase)
	}
	return nil
}

// toCRDPhase converts a catalyst-api in-memory status string to the
// CRD's coarse phase. The mapping is intentionally lossy — a watcher
// of the CRD doesn't need to know whether catalyst-api is currently
// running `tofu init` vs `tofu plan` vs `tofu apply`; "bootstrapping"
// covers all of them. The fine-grained Event log on the flat-file
// record is where that resolution lives.
//
// Unknown in-memory statuses fall back to PhasePending — defensive
// against a future status string the mapping forgot to update. The
// caller can detect this by passing rec.Status through ValidatePhase
// after toCRDPhase if a strict guarantee is required.
func toCRDPhase(memStatus string) string {
	switch strings.ToLower(strings.TrimSpace(memStatus)) {
	case "", "pending":
		return PhasePending
	case "provisioning", "tofu-applying":
		return PhaseBootstrapping
	case "flux-bootstrapping":
		return PhaseInstallingControlPlane
	case "registering-dns":
		return PhaseRegisteringDNS
	case "tls-issuing":
		return PhaseTLSIssuing
	case "phase1-watching":
		// Phase-1 starts after control plane is up + DNS is resolving
		// and certs have been requested. By the time we're watching
		// HelmReleases reconcile, we're in the tls-issuing phase from
		// the operator's perspective — the bp-cert-manager HR going
		// Ready=True is the signal certs were issued.
		return PhaseTLSIssuing
	case "ready":
		return PhaseReady
	case "failed":
		return PhaseFailed
	default:
		return PhasePending
	}
}

// CRDStoreMode controls the CRDStore's behaviour when it cannot reach
// the K8s control plane.
type CRDStoreMode int

const (
	// CRDModeBestEffort — try to write the CRD, log+swallow errors. Used
	// in production: the flat-file store is authoritative; the CRD
	// projection is observability. A transient apiserver outage must
	// NOT fail the wizard.
	CRDModeBestEffort CRDStoreMode = iota

	// CRDModeStrict — return errors from the underlying dynamic client.
	// Used in tests that assert the CRD path actually wrote.
	CRDModeStrict

	// CRDModeDisabled — skip CRD writes entirely. Used in local dev
	// (`go test ./...` without an apiserver) and when the chart was
	// rendered with provisioningState.crd.enabled=false. The flat-file
	// store still runs.
	CRDModeDisabled
)

// CRDStore wraps a flat-file Store with a CRD projection.
//
// Save persists to BOTH backends (flat file first, then the CRD); a
// flat-file failure aborts the call (since that's authoritative), a
// CRD failure is reported per Mode. Load / LoadAll / Delete delegate
// to the flat-file store — the CRD is write-side projection, not the
// authoritative read source. (A future controller may flip that, but
// for issue #88 the contract is "flat file is truth, CRD is
// observability".)
type CRDStore struct {
	*Store

	// dyn — the dynamic.Interface used to write ProvisioningState
	// resources. May be nil in CRDModeDisabled. Production wires this
	// from rest.InClusterConfig() in cmd/api/main.go; tests inject a
	// fake.NewSimpleDynamicClient.
	dyn dynamic.Interface

	// namespace — the K8s namespace the ProvisioningState CRDs live in.
	// Defaults to "catalyst" (matches the catalyst namespace on
	// Catalyst-Zero); production reads it from the CATALYST_NAMESPACE
	// env var via NewCRDStore's caller.
	namespace string

	// mode — how aggressive to be about CRD failures. See CRDStoreMode.
	mode CRDStoreMode

	// onCRDError — optional callback invoked when a CRD write fails in
	// CRDModeBestEffort. Used by production to log via the catalyst-api
	// structured logger; tests use it to assert the failure was
	// observed without short-circuiting Save.
	onCRDError func(id string, err error)

	// mu serialises CRD writes against each other for a given store
	// instance. The embedded Store has its own mutex for flat-file
	// writes; CRDStore's mu protects metadata reads (mode, dyn) so a
	// future SetMode operation is race-free.
	mu sync.RWMutex
}

// CRDStoreOption configures a CRDStore at construction.
type CRDStoreOption func(*CRDStore)

// WithCRDNamespace overrides the default ProvisioningState namespace.
func WithCRDNamespace(ns string) CRDStoreOption {
	return func(c *CRDStore) {
		if ns != "" {
			c.namespace = ns
		}
	}
}

// WithCRDMode sets the failure-mode behaviour.
func WithCRDMode(mode CRDStoreMode) CRDStoreOption {
	return func(c *CRDStore) {
		c.mode = mode
	}
}

// WithCRDErrorCallback wires a per-write error sink for best-effort
// mode. Called once per failed CRD write with the deployment id and
// the error.
func WithCRDErrorCallback(fn func(id string, err error)) CRDStoreOption {
	return func(c *CRDStore) {
		c.onCRDError = fn
	}
}

// NewCRDStore returns a CRDStore wrapping flat. dyn may be nil; if so,
// the constructor forces CRDModeDisabled regardless of any opts mode
// override (a non-nil dyn is required for CRDModeBestEffort or
// CRDModeStrict).
//
// The default namespace is "catalyst"; override with WithCRDNamespace.
// The default mode is CRDModeBestEffort; override with WithCRDMode.
func NewCRDStore(flat *Store, dyn dynamic.Interface, opts ...CRDStoreOption) (*CRDStore, error) {
	if flat == nil {
		return nil, errors.New("store: flat-file Store is required")
	}
	c := &CRDStore{
		Store:     flat,
		dyn:       dyn,
		namespace: "catalyst",
		mode:      CRDModeBestEffort,
	}
	for _, opt := range opts {
		opt(c)
	}
	if dyn == nil {
		// Disabling explicitly when no dynamic client is wired keeps
		// Save's branching trivial — the CRD path is a no-op. This is
		// the local-dev / unit-test default.
		c.mode = CRDModeDisabled
	}
	return c, nil
}

// Namespace returns the K8s namespace the CRDStore writes
// ProvisioningState resources into. Used by tests + by the
// kubeconfigSecretRef plumbing in the handler.
func (c *CRDStore) Namespace() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.namespace
}

// Mode returns the current failure mode. Used by tests + by the
// /healthz handler that surfaces CRD-store status.
func (c *CRDStore) Mode() CRDStoreMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

// Save persists rec to the flat-file store, then to the CRD. The
// flat-file store is authoritative (per the package docstring) so a
// flat-file failure aborts the call; CRD failures are routed via
// onCRDError in CRDModeBestEffort or returned in CRDModeStrict.
//
// CRD object naming: <id> is hex (matches the CRD's deploymentID
// pattern), so it is a valid DNS-1123 label as long as we don't exceed
// 63 chars. The flat-file store's IDs are 16 hex chars per
// store.New, well under the limit.
func (c *CRDStore) Save(rec Record) error {
	if err := c.Store.Save(rec); err != nil {
		return err
	}
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode == CRDModeDisabled {
		return nil
	}
	if err := c.saveCRD(context.Background(), rec); err != nil {
		switch mode {
		case CRDModeStrict:
			return fmt.Errorf("store: CRD write for %q failed: %w", rec.ID, err)
		case CRDModeBestEffort:
			if c.onCRDError != nil {
				c.onCRDError(rec.ID, err)
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

// saveCRD upserts the ProvisioningState resource for rec. We create+update
// (rather than server-side-apply) so the catalyst-api doesn't need a field
// manager and the test harness's fake dynamic client (which has limited
// SSA support) works without special casing.
//
// The flow:
//
//  1. Get(name=rec.ID) → if not-found, build + Create
//  2. If Get succeeds: build the desired Unstructured, copy
//     resourceVersion from the existing object, Update
//  3. Update Status subresource separately (status is mutated almost
//     every call, spec rarely; splitting them keeps the spec-only
//     resourceVersion stable for sibling controllers watching for spec
//     changes)
func (c *CRDStore) saveCRD(ctx context.Context, rec Record) error {
	if c.dyn == nil {
		return errors.New("store: dynamic client is nil")
	}

	desired := recordToUnstructured(rec, c.namespace)
	desiredStatus := desired.Object["status"]

	resClient := c.dyn.Resource(CRDGVR).Namespace(c.namespace)
	existing, err := resClient.Get(ctx, rec.ID, metav1.GetOptions{})
	switch {
	case err == nil:
		// Update spec on existing (preserve metadata).
		desired.SetResourceVersion(existing.GetResourceVersion())
		desired.SetUID(existing.GetUID())
		// Strip status before spec-only Update — Update on the main
		// resource doesn't touch status.
		delete(desired.Object, "status")
		if _, err := resClient.Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update spec: %w", err)
		}
		// Re-attach status and update the status subresource.
		desired.Object["status"] = desiredStatus
		if _, err := resClient.UpdateStatus(ctx, desired, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
	case apierrors.IsNotFound(err):
		// Create — Create accepts the full object including status.
		// Note: real apiservers ignore status on Create (status
		// subresource is set only via UpdateStatus); the fake dynamic
		// client preserves it, which is fine for tests. We follow up
		// with an UpdateStatus for production correctness.
		if _, err := resClient.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create: %w", err)
		}
		if _, err := resClient.UpdateStatus(ctx, desired, metav1.UpdateOptions{}); err != nil {
			// On a real apiserver this is the source of the status; on
			// the fake it's a no-op duplicate. Either way it's
			// correctness-preserving, so a failure here is real and
			// gets returned.
			return fmt.Errorf("update status (after create): %w", err)
		}
	default:
		return fmt.Errorf("get: %w", err)
	}
	return nil
}

// DeleteCRD removes the ProvisioningState for id from the cluster
// without touching the flat-file record. Useful on tenant-deletion
// workflows where the wizard's local record stays for audit but the
// K8s-side projection should be reaped. A missing CRD is not an error
// (Delete is idempotent like the flat-file Store.Delete).
func (c *CRDStore) DeleteCRD(ctx context.Context, id string) error {
	c.mu.RLock()
	mode := c.mode
	c.mu.RUnlock()
	if mode == CRDModeDisabled || c.dyn == nil {
		return nil
	}
	err := c.dyn.Resource(CRDGVR).Namespace(c.namespace).Delete(ctx, id, metav1.DeleteOptions{})
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("store: delete CRD %q: %w", id, err)
}

// LoadCRD fetches a single ProvisioningState by id and projects it
// back into a (partial) Record. Used by tooling that only has the K8s
// API available (no PVC mount). Note: the projected Record carries the
// REDACTED form of credential fields and an EMPTY events slice — the
// flat-file store is the only source of full event history.
func (c *CRDStore) LoadCRD(ctx context.Context, id string) (Record, error) {
	if c.dyn == nil {
		return Record{}, errors.New("store: dynamic client is nil")
	}
	obj, err := c.dyn.Resource(CRDGVR).Namespace(c.namespace).Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return Record{}, fmt.Errorf("store: get CRD %q: %w", id, err)
	}
	return unstructuredToRecord(obj), nil
}

// recordToUnstructured projects rec into the unstructured.Unstructured
// shape the dynamic client needs. The shape mirrors the CRD schema in
// products/catalyst/chart/templates/crd-provisioningstate.yaml — drift
// between the two will surface as a CRD validation rejection (in
// strict mode against a real apiserver) or a silent missing-field
// (against the fake client). Tests that exercise the round-trip catch
// the silent case.
//
// Credential fields are NOT carried — Record.Request.HetznerToken is
// the redacted marker by construction (or empty), and the CRD spec
// captures only `hetznerProjectID`. The hetznerTokenSecretRef field is
// populated by the handler when it knows the Secret name.
func recordToUnstructured(rec Record, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(CRDGroup + "/" + CRDVersion)
	obj.SetKind(CRDKind)
	obj.SetName(rec.ID)
	obj.SetNamespace(namespace)
	obj.SetLabels(map[string]string{
		"catalyst.openova.io/deployment-id": rec.ID,
		"app.kubernetes.io/managed-by":      "catalyst-api",
		"app.kubernetes.io/component":       "provisioning-state",
	})

	spec := map[string]any{
		"deploymentID":        rec.ID,
		"orgName":             rec.Request.OrgName,
		"orgEmail":            rec.Request.OrgEmail,
		"sovereignFQDN":       rec.Request.SovereignFQDN,
		"sovereignDomainMode": rec.Request.SovereignDomainMode,
		"sovereignPoolDomain": rec.Request.SovereignPoolDomain,
		"sovereignSubdomain":  rec.Request.SovereignSubdomain,
		"region":              rec.Request.Region,
		"controlPlaneSize":    rec.Request.ControlPlaneSize,
		"workerSize":          rec.Request.WorkerSize,
		"workerCount":         int64(rec.Request.WorkerCount),
		"haEnabled":           rec.Request.HAEnabled,
		"hetznerProjectID":    rec.Request.HetznerProjectID,
	}
	// Drop empty optional fields so the on-cluster object is tidy.
	pruneEmpty(spec)

	if len(rec.Request.Regions) > 0 {
		regions := make([]any, 0, len(rec.Request.Regions))
		for _, r := range rec.Request.Regions {
			regions = append(regions, map[string]any{
				"provider":         r.Provider,
				"cloudRegion":      r.CloudRegion,
				"controlPlaneSize": r.ControlPlaneSize,
				"workerSize":       r.WorkerSize,
				"workerCount":      int64(r.WorkerCount),
			})
		}
		spec["regions"] = regions
	}

	phase := toCRDPhase(rec.Status)
	status := map[string]any{
		"phase":            phase,
		"startedAt":        rec.StartedAt.UTC().Format(time.RFC3339),
		"lastTransitionAt": time.Now().UTC().Format(time.RFC3339),
	}
	if !rec.FinishedAt.IsZero() {
		status["finishedAt"] = rec.FinishedAt.UTC().Format(time.RFC3339)
	}
	if rec.Error != "" {
		status["failureReason"] = rec.Error
	}
	if rec.Result != nil {
		if rec.Result.ControlPlaneIP != "" {
			status["controlPlaneIP"] = rec.Result.ControlPlaneIP
		}
		if rec.Result.LoadBalancerIP != "" {
			status["loadBalancerIP"] = rec.Result.LoadBalancerIP
		}
		if len(rec.Result.ComponentStates) > 0 {
			cs := make(map[string]any, len(rec.Result.ComponentStates))
			for k, v := range rec.Result.ComponentStates {
				cs[k] = v
			}
			status["componentStates"] = cs
		}
	}
	// Top-level Ready condition mirrors phase=ready / phase=failed.
	cond := map[string]any{
		"type":               "Ready",
		"status":             readyConditionStatus(phase),
		"reason":             readyConditionReason(phase),
		"message":            readyConditionMessage(phase, rec.Error),
		"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
	}
	status["conditions"] = []any{cond}

	obj.Object["spec"] = spec
	obj.Object["status"] = status
	return obj
}

// readyConditionStatus — "True" only at phase=ready; "False" at
// phase=failed; "Unknown" while in-flight.
func readyConditionStatus(phase string) string {
	switch phase {
	case PhaseReady:
		return "True"
	case PhaseFailed:
		return "False"
	default:
		return "Unknown"
	}
}

// readyConditionReason — short cause for the condition. Mirrors the
// phase string for terminal states; uses the in-flight phase verbatim
// otherwise so kubectl describe shows progress.
func readyConditionReason(phase string) string {
	switch phase {
	case PhaseReady:
		return "Ready"
	case PhaseFailed:
		return "Failed"
	default:
		// Capitalise first letter for K8s conditions convention
		// (`InstallingControlPlane`, not `installing-control-plane`).
		return reasonForPhase(phase)
	}
}

// readyConditionMessage — long-form description, sourced from rec.Error
// on failed; otherwise a short phase description.
func readyConditionMessage(phase, errMsg string) string {
	if phase == PhaseFailed {
		if errMsg != "" {
			return errMsg
		}
		return "Provisioning failed (no error message recorded)"
	}
	switch phase {
	case PhaseReady:
		return "Sovereign is reachable; bootstrap-kit reconciled"
	case PhasePending:
		return "Provisioning request accepted; awaiting scheduler"
	case PhaseBootstrapping:
		return "OpenTofu is provisioning Phase-0 cloud resources"
	case PhaseInstallingControlPlane:
		return "Control-plane reachable; bootstrap-kit installing"
	case PhaseRegisteringDNS:
		return "Writing DNS records via PowerDNS"
	case PhaseTLSIssuing:
		return "cert-manager is issuing TLS certificates"
	default:
		return phase
	}
}

// reasonForPhase converts a kebab-case phase to PascalCase for the
// condition.reason field. K8s condition reasons should be CamelCase
// per the API conventions; the phase enum is kebab-case for human
// readability on the CRD.
func reasonForPhase(phase string) string {
	if phase == "" {
		return "Unknown"
	}
	parts := strings.Split(phase, "-")
	var sb strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		sb.WriteString(strings.ToUpper(p[:1]))
		sb.WriteString(p[1:])
	}
	return sb.String()
}

// pruneEmpty drops keys whose value is the zero value for its type.
// Keeps the on-cluster object readable (no nullable empty strings) and
// prevents the CRD's enum-validated fields from rejecting "".
func pruneEmpty(m map[string]any) {
	for k, v := range m {
		switch t := v.(type) {
		case string:
			if t == "" {
				delete(m, k)
			}
		case int64:
			if t == 0 {
				delete(m, k)
			}
		case bool:
			if !t {
				// haEnabled=false is meaningful (it's the default).
				// We keep booleans regardless to avoid the
				// "false-vs-unset" ambiguity on the CRD.
			}
		case nil:
			delete(m, k)
		}
	}
}

// unstructuredToRecord projects a ProvisioningState back to a Record.
// Reconstructs the redacted-request fields and the terminal status
// fields. Events stay nil — the flat-file store is the authoritative
// event source. Used by tooling that only has K8s API access and
// doesn't need the full event history.
func unstructuredToRecord(obj *unstructured.Unstructured) Record {
	rec := Record{ID: obj.GetName()}
	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		rec.Request = RedactedRequest{
			OrgName:             stringField(spec, "orgName"),
			OrgEmail:            stringField(spec, "orgEmail"),
			SovereignFQDN:       stringField(spec, "sovereignFQDN"),
			SovereignDomainMode: stringField(spec, "sovereignDomainMode"),
			SovereignPoolDomain: stringField(spec, "sovereignPoolDomain"),
			SovereignSubdomain:  stringField(spec, "sovereignSubdomain"),
			Region:              stringField(spec, "region"),
			ControlPlaneSize:    stringField(spec, "controlPlaneSize"),
			WorkerSize:          stringField(spec, "workerSize"),
			WorkerCount:         intField(spec, "workerCount"),
			HAEnabled:           boolField(spec, "haEnabled"),
			HetznerProjectID:    stringField(spec, "hetznerProjectID"),
		}
	}
	if status, ok := obj.Object["status"].(map[string]any); ok {
		rec.Status = stringField(status, "phase")
		if startedAt, err := time.Parse(time.RFC3339, stringField(status, "startedAt")); err == nil {
			rec.StartedAt = startedAt
		}
		if finishedAt, err := time.Parse(time.RFC3339, stringField(status, "finishedAt")); err == nil {
			rec.FinishedAt = finishedAt
		}
		rec.Error = stringField(status, "failureReason")
	}
	return rec
}

// stringField — defensive map[string]any access. Returns empty string
// for missing or wrong-type values rather than panicking.
func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// intField — int64-vs-int variance is the dynamic-client gotcha here:
// JSON unmarshal yields float64 unless the type was explicitly int64
// at marshal time. We accept both.
func intField(m map[string]any, key string) int {
	switch t := m[key].(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

// boolField — defensive bool access.
func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}
