// Package k8slease implements witness.Client over a native
// coordination.k8s.io/v1 Lease object in the control-plane cluster.
//
// WHY THIS EXISTS (#3829 / #3375)
// ──────────────────────────────
// The two K-Cont-3 witnesses both depend on infrastructure that a
// real, air-gapped Sovereign does not have:
//
//   - cloudflare-kv  needs an external Cloudflare Worker + KV namespace
//   - an API token. A self-sovereign / air-gapped Sovereign has no
//     egress to Cloudflare at all (Pillar 5 / ADR-0002).
//   - dns-quorum     was shipped as a never-completed Phase-1 POC: the
//     factory ALWAYS builds the client with a nil TXTWriter, so every
//     Acquire fails at writeQuorum with "Writer not configured (Phase-1
//     POC needs PDM /v1/txt — K-Cont-{4|5})". The read side also points
//     at placeholder resolvers (10.43.0.10/11/12 — only .10 is kube-dns,
//     .11/.12 are unallocated ClusterIPs) so readQuorum never reaches a
//     2-of-3 majority → ErrQuorumUnavailable every 10s. NET RESULT on
//     omantel.biz: the cnpg-pair Continuum CR sat Degraded forever,
//     LeaseHeld=False, leaseHolder empty → the console Topology tab had
//     no live lease/standby/lag to render and fell back to "singleton".
//
// The k8s-lease witness closes that gap with ZERO external dependency:
// the control plane already talks to its own kube API, the
// continuum-controller's ClusterRole ALREADY grants
// coordination.k8s.io/leases {get,list,watch,create,update,patch,delete}
// (see products/continuum/chart/templates/rbac.yaml), and a Lease is a
// durable, replicated etcd object with built-in optimistic-concurrency
// (resourceVersion) — exactly the atomic CAS the witness contract
// requires.
//
// WIRE SHAPE
// ──────────
//
//	One Lease object per Continuum slot, named  cw-<encodedSlot>  in the
//	configured namespace (default: the controller's own namespace).
//	'/' in the slot ('<ns>/<name>') is replaced with '-' to keep the
//	object name DNS-label-safe.
//
//	spec.holderIdentity        = the region currently holding the lease
//	                             (witness.State.Holder). Empty/absent =
//	                             free slot.
//	spec.leaseDurationSeconds  = ttl in seconds.
//	spec.acquireTime           = first-acquisition time (preserved on
//	                             same-holder re-acquire → State.AcquiredAt)
//	spec.renewTime             = last renew time. ExpiresAt is computed
//	                             as renewTime + leaseDurationSeconds.
//	metadata.annotations[dr.openova.io/generation]
//	                           = the monotonic CAS counter (witness.State
//	                             .Generation). Bumped on every Acquire /
//	                             Renew / Release write.
//
// CAS PROTOCOL
// ────────────
// The K8s apiserver enforces optimistic concurrency via resourceVersion:
// an Update with a stale resourceVersion is rejected with a Conflict
// (409). We thread the just-read resourceVersion into every write; a
// Conflict means another writer (the standby region's controller, or a
// racing reconcile) moved first → we surface the contract sentinel
// (ErrLeaseHeldByAnother on Acquire, ErrLeaseLost on Renew) so the
// caller never double-promotes. This is the SAME safety posture the
// in-memory + cloudflare-kv impls provide, backed by etcd instead of a
// process map / a CF Worker.
//
// EXPIRY
// ──────
// Expiry is wall-clock: a slot whose renewTime + leaseDurationSeconds is
// in the past is takeable by anyone. The holder region renews every
// renewSeconds (<< ttl) so a live primary never lets its slot expire;
// when the primary's controller dies (region kill), the slot expires
// after ttl and the standby's controller can Acquire it — the failover
// path.
package k8slease

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// GenerationAnnotation carries the monotonic CAS counter on the Lease
// object's metadata.annotations. The Lease's own resourceVersion is the
// apiserver-enforced CAS token; this annotation is the witness-contract
// Generation surfaced on Continuum.status (so jq / the UI sees a stable
// counter, not the opaque etcd resourceVersion).
const GenerationAnnotation = "dr.openova.io/generation"

// leaseGVR is the dynamic-client resource for coordination.k8s.io/v1
// Leases. We use the dynamic client (not the typed coordinationv1
// client) to stay consistent with the rest of the continuum-controller,
// which is dynamic-only per ADR-0001 §2.7.
var leaseGVR = schema.GroupVersionResource{
	Group:    "coordination.k8s.io",
	Version:  "v1",
	Resource: "leases",
}

// DynLeaseAccess is the minimal dynamic-client surface the witness
// needs. dynamic.Interface satisfies it; tests inject a fake.
type DynLeaseAccess interface {
	Resource(schema.GroupVersionResource) dynamic.NamespaceableResourceInterface
}

func init() {
	witness.Register("k8s-lease", factory)
}

// K8sLeaseClient implements witness.Client over one coordination Lease.
//
// Concurrent-safe: every method is a read-modify-write against the
// apiserver, which is the arbitration point (resourceVersion CAS). No
// process-local mutable state.
type K8sLeaseClient struct {
	// Dyn is the dynamic client bound to the control-plane cluster.
	Dyn DynLeaseAccess

	// Namespace is where the Lease object lives. Defaults to the
	// controller namespace via the factory.
	Namespace string

	// Slot is the per-CR identifier (`<ns>/<name>`); encoded into the
	// Lease object name.
	Slot string

	// LeaseName is the resolved Lease object name (cw-<encodedSlot>).
	LeaseName string
}

// New constructs a K8sLeaseClient. dyn + namespace + slot required.
func New(dyn DynLeaseAccess, namespace, slot string) (*K8sLeaseClient, error) {
	if dyn == nil {
		return nil, errors.New("k8slease: dynamic client is required")
	}
	if strings.TrimSpace(namespace) == "" {
		return nil, errors.New("k8slease: namespace is required")
	}
	if strings.TrimSpace(slot) == "" {
		return nil, errors.New("k8slease: slot is required")
	}
	return &K8sLeaseClient{
		Dyn:       dyn,
		Namespace: strings.TrimSpace(namespace),
		Slot:      strings.TrimSpace(slot),
		LeaseName: leaseObjectName(slot),
	}, nil
}

// factory is the witness.Factory registered at init() time.
//
// cfg keys:
//
//	slot       (string)  REQUIRED — `<ns>/<name>` (stamped by the
//	                     reconciler's selectWitness).
//	namespace  (string)  OPTIONAL — Lease namespace. The reconciler
//	                     stamps "leaseNamespace" from CATALYST_LEASE_NS /
//	                     the controller namespace; the CR may also set
//	                     leaseClient.config.namespace to pin one.
//
// The k8s-lease witness needs NO SecretReader (no external creds) — the
// in-cluster ServiceAccount token authenticates to the kube API. The
// dynamic client is injected via cfg["dyn"] by the reconciler (the
// witness package can't import client-go construction without a cycle,
// so the controller passes its already-built dynamic client through the
// config map).
func factory(cfg map[string]any, _ witness.SecretReader) (witness.Client, error) {
	slot, _ := cfg["slot"].(string)
	namespace, _ := cfg["namespace"].(string)
	if namespace == "" {
		// Forward-compat alternate key the reconciler may stamp.
		namespace, _ = cfg["leaseNamespace"].(string)
	}
	dyn, ok := cfg["dyn"].(DynLeaseAccess)
	if !ok || dyn == nil {
		return nil, errors.New("k8slease: dynamic client not provided in cfg[\"dyn\"] " +
			"(the continuum-controller must stamp its dynamic client into the witness config)")
	}
	return New(dyn, namespace, slot)
}

// Acquire implements witness.Client. Read-modify-write with
// resourceVersion CAS: a free/expired/own slot is takeable; a live slot
// held by another region returns ErrLeaseHeldByAnother.
func (c *K8sLeaseClient) Acquire(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	obj, st, found, err := c.read(ctx)
	if err != nil {
		return witness.State{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)

	takeable := st.Holder == "" || !now.Before(st.ExpiresAt) || st.Holder == holder
	if !takeable {
		return st, witness.ErrLeaseHeldByAnother
	}

	acquiredAt := now
	if st.Holder == holder && now.Before(st.ExpiresAt) && !st.AcquiredAt.IsZero() {
		acquiredAt = st.AcquiredAt
	}
	next := witness.State{
		Holder:     holder,
		AcquiredAt: acquiredAt,
		ExpiresAt:  now.Add(ttl),
		Generation: st.Generation + 1,
	}
	if err := c.write(ctx, obj, found, next, ttl, now); err != nil {
		if apierrors.IsConflict(err) {
			// Lost the CAS race — another writer moved first. Never
			// promote blind.
			return st, witness.ErrLeaseHeldByAnother
		}
		return witness.State{}, err
	}
	return next, nil
}

// Renew implements witness.Client. Extends the slot iff `holder` still
// owns it and it has not expired; otherwise ErrLeaseLost.
func (c *K8sLeaseClient) Renew(ctx context.Context, holder string, ttl time.Duration) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	obj, st, found, err := c.read(ctx)
	if err != nil {
		return witness.State{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if !found || st.Holder != holder || !now.Before(st.ExpiresAt) {
		return st, witness.ErrLeaseLost
	}
	next := witness.State{
		Holder:     holder,
		AcquiredAt: st.AcquiredAt,
		ExpiresAt:  now.Add(ttl),
		Generation: st.Generation + 1,
	}
	if err := c.write(ctx, obj, found, next, ttl, now); err != nil {
		if apierrors.IsConflict(err) {
			// Another writer completed a CAS while we were mid-renew →
			// we lost the lease.
			return st, witness.ErrLeaseLost
		}
		return witness.State{}, err
	}
	return next, nil
}

// Release implements witness.Client. Idempotent: a non-holder Release is
// a no-op. Clears holderIdentity but bumps generation so a subsequent
// Acquire sees a non-zero baseline (matches the other impls).
func (c *K8sLeaseClient) Release(ctx context.Context, holder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	obj, st, found, err := c.read(ctx)
	if err != nil {
		return err
	}
	if !found || st.Holder == "" || st.Holder != holder {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	released := witness.State{Generation: st.Generation + 1}
	if err := c.write(ctx, obj, found, released, 0, now); err != nil {
		if apierrors.IsConflict(err) {
			// Someone else already moved the slot — Release is
			// idempotent, so swallow.
			return nil
		}
		return err
	}
	return nil
}

// Read implements witness.Client.
func (c *K8sLeaseClient) Read(ctx context.Context) (witness.State, error) {
	if err := ctx.Err(); err != nil {
		return witness.State{}, err
	}
	_, st, _, err := c.read(ctx)
	if err != nil {
		return witness.State{}, err
	}
	return st, nil
}

// read GETs the Lease and projects it into a witness.State. A NotFound
// Lease is the "free slot" case (found=false, zero State). The returned
// *unstructured.Unstructured is the live object (with resourceVersion)
// to thread into the subsequent write for CAS; nil when not found.
func (c *K8sLeaseClient) read(ctx context.Context) (*unstructured.Unstructured, witness.State, bool, error) {
	obj, err := c.Dyn.Resource(leaseGVR).Namespace(c.Namespace).Get(ctx, c.LeaseName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, witness.State{}, false, nil
	}
	if err != nil {
		return nil, witness.State{}, false, fmt.Errorf("k8slease: get Lease %s/%s: %w", c.Namespace, c.LeaseName, err)
	}
	st, perr := leaseToState(obj)
	if perr != nil {
		return obj, witness.State{}, true, perr
	}
	return obj, st, true, nil
}

// write CREATEs (when the Lease is absent) or UPDATEs (threading the
// existing resourceVersion for CAS) the Lease to reflect `next`.
func (c *K8sLeaseClient) write(ctx context.Context, existing *unstructured.Unstructured, found bool, next witness.State, ttl time.Duration, now time.Time) error {
	obj := buildLeaseObject(c.Namespace, c.LeaseName, c.Slot, next, ttl)
	res := c.Dyn.Resource(leaseGVR).Namespace(c.Namespace)
	if !found {
		_, err := res.Create(ctx, obj, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			// Lost the create race — treat as a CAS conflict so the
			// caller surfaces ErrLeaseHeldByAnother / ErrLeaseLost.
			return apierrors.NewConflict(
				schema.GroupResource{Group: leaseGVR.Group, Resource: leaseGVR.Resource},
				c.LeaseName, errors.New("k8slease: lost create race"))
		}
		return err
	}
	// Thread the live resourceVersion so the apiserver enforces CAS.
	obj.SetResourceVersion(existing.GetResourceVersion())
	// Preserve any unrelated metadata (labels the chart/operator added).
	if labels := existing.GetLabels(); len(labels) > 0 {
		merged := obj.GetLabels()
		for k, v := range labels {
			if _, taken := merged[k]; !taken {
				merged[k] = v
			}
		}
		obj.SetLabels(merged)
	}
	_, err := res.Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// leaseToState projects a coordination Lease's spec into a
// witness.State. A Lease with no holderIdentity is the free slot.
func leaseToState(obj *unstructured.Unstructured) (witness.State, error) {
	st := witness.State{}
	holder, _, _ := unstructured.NestedString(obj.Object, "spec", "holderIdentity")
	st.Holder = holder

	durSecs, _, _ := unstructured.NestedInt64(obj.Object, "spec", "leaseDurationSeconds")

	if acq, ok, _ := unstructured.NestedString(obj.Object, "spec", "acquireTime"); ok && acq != "" {
		if t, err := time.Parse(time.RFC3339, acq); err == nil {
			st.AcquiredAt = t.UTC()
		}
	}
	var renew time.Time
	if rn, ok, _ := unstructured.NestedString(obj.Object, "spec", "renewTime"); ok && rn != "" {
		if t, err := time.Parse(time.RFC3339, rn); err == nil {
			renew = t.UTC()
		}
	}
	if !renew.IsZero() && durSecs > 0 {
		st.ExpiresAt = renew.Add(time.Duration(durSecs) * time.Second)
	}

	if ann := obj.GetAnnotations(); ann != nil {
		if g, ok := ann[GenerationAnnotation]; ok && g != "" {
			if n, err := strconv.ParseInt(g, 10, 64); err == nil {
				st.Generation = n
			}
		}
	}
	return st, nil
}

// buildLeaseObject renders the Lease Unstructured for a target State.
// A cleared State (Holder=="") writes an empty holderIdentity so the
// slot reads as free, while still bumping the generation annotation.
func buildLeaseObject(namespace, name, slot string, st witness.State, ttl time.Duration) *unstructured.Unstructured {
	spec := map[string]interface{}{}
	if st.Holder != "" {
		spec["holderIdentity"] = st.Holder
		if ttl > 0 {
			spec["leaseDurationSeconds"] = int64(ttl / time.Second)
		}
		if !st.AcquiredAt.IsZero() {
			spec["acquireTime"] = st.AcquiredAt.UTC().Format(microRFC3339)
		}
		if !st.ExpiresAt.IsZero() && ttl > 0 {
			// renewTime = expiresAt - ttl (the moment of this write).
			spec["renewTime"] = st.ExpiresAt.Add(-ttl).UTC().Format(microRFC3339)
		}
	}

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "coordination.k8s.io/v1",
		"kind":       "Lease",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "continuum-controller",
				"openova.io/witness":           "k8s-lease",
			},
			"annotations": map[string]interface{}{
				GenerationAnnotation:       strconv.FormatInt(st.Generation, 10),
				"dr.openova.io/slot":       slot,
				"dr.openova.io/lease-kind": "continuum",
			},
		},
		"spec": spec,
	}}
	return obj
}

// microRFC3339 matches the MicroTime serialization the apiserver uses
// for Lease spec.acquireTime / spec.renewTime (metav1.MicroTime). Using
// the canonical encoding avoids a needless rewrite-on-read drift.
const microRFC3339 = "2006-01-02T15:04:05.000000Z07:00"

// leaseObjectName encodes a Continuum slot (`<ns>/<name>`) into a
// DNS-label-safe Lease object name. '/' → '-', prefixed with "cw-"
// (continuum-witness) so the lease roster is self-describing.
func leaseObjectName(slot string) string {
	return "cw-" + strings.ReplaceAll(strings.TrimSpace(slot), "/", "-")
}

// Compile-time assertion that K8sLeaseClient satisfies witness.Client.
var _ witness.Client = (*K8sLeaseClient)(nil)
