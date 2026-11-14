package inventory

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/source"
)

// Reconciler keeps the Machines of one MachineSource per reconcile. It is the only
// writer of Machine objects. Reader lists what a source of type nodes reads; in the
// manager it is the cached client, so the list comes from the Node informer.
type Reconciler struct {
	Client client.Client
	Reader client.Reader
	Now    func() time.Time
}

// SetupWithManager registers the controller: one reconcile per MachineSource when its
// spec changes, and one reconcile of every MachineSource of type nodes when a Node is
// added, removed or changes a fact a Machine records.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("inventory").
		For(newObject(v1alpha1.MachineSourceKind), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.NodeSources), builder.WithPredicates(NodeFactsChanged())).
		Complete(r)
}

// NodeSources maps a Node event to every MachineSource of type nodes.
func (r *Reconciler) NodeSources(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(v1alpha1.MachineSourceListKind)
	if err := r.Client.List(ctx, list); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list MachineSources for a Node event")
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		if t, _, _ := unstructured.NestedString(list.Items[i].Object, "spec", "type"); t == source.TypeNodes {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: list.Items[i].GetNamespace(), Name: list.Items[i].GetName()}})
		}
	}
	return out
}

// NodeFactsChanged passes every Node that is added or removed, and an update only when
// the Sighting made from the Node changes. Status heartbeats pass no event.
func NodeFactsChanged() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok1 := e.ObjectOld.(*corev1.Node)
			newNode, ok2 := e.ObjectNew.(*corev1.Node)
			return !ok1 || !ok2 || !reflect.DeepEqual(source.NodeSighting(oldNode), source.NodeSighting(newNode))
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// SourceFor returns the Source that reads a MachineSource.
func (r *Reconciler) SourceFor(ms *v1alpha1.MachineSource) (source.Source, error) {
	if ms.Spec.Type == source.TypeNodes {
		return &source.Nodes{Reader: r.Reader, Now: r.Now}, nil
	}
	return nil, fmt.Errorf("spec.type %q is not a source type of this controller", ms.Spec.Type)
}

// Reconcile enumerates one MachineSource and brings its Machines in line with it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	u := newObject(v1alpha1.MachineSourceKind)
	if err := r.Client.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	ms, err := v1alpha1.MachineSourceFromUnstructured(u)
	if err != nil {
		return ctrl.Result{}, err
	}
	settings, err := SettingsOf(ms.Spec.Stale)
	if err != nil {
		return ctrl.Result{}, r.writeSource(ctx, ms, false, "InvalidSpec", err.Error())
	}
	if ms.Spec.Suspended {
		return ctrl.Result{}, r.writeSource(ctx, ms, false, "Suspended", "spec.suspended is true: nothing is enumerated")
	}
	src, err := r.SourceFor(ms)
	if err != nil {
		return ctrl.Result{}, r.writeSource(ctx, ms, false, "InvalidSpec", err.Error())
	}
	now := r.now()
	e, enumErr := src.Enumerate(ctx)
	if enumErr != nil {
		e = source.Enumeration{StartedAt: now, FinishedAt: r.now()}
	}
	e.ID = NewULID(now)

	machines, err := r.listMachines(ctx, ms)
	if err != nil {
		return ctrl.Result{}, err
	}
	var active []string
	for id, m := range machines {
		if m.Status.Phase == v1alpha1.PhaseActive {
			active = append(active, id)
		}
	}
	sweep := DecideSweep(settings, e, enumErr, active, ms.Status.Held, now)
	ms.Status.Held = sweep.Held

	listed := map[string]bool{}
	var created int32
	for _, sight := range e.Sightings {
		listed[sight.ExternalID] = true
		if machines[sight.ExternalID] == nil {
			created++
		}
		if err := r.writeMachine(ctx, ms, machines[sight.ExternalID], sight, e); err != nil {
			return ctrl.Result{}, err
		}
	}
	stale := metav1.NewTime(e.FinishedAt)
	for _, id := range sweep.Mark {
		m := machines[id]
		m.Status.Phase, m.Status.StaleSince = v1alpha1.PhaseStale, &stale
		if err := r.putMachine(ctx, m, false); err != nil {
			return ctrl.Result{}, err
		}
	}
	var next time.Time
	for id, m := range machines {
		if listed[id] {
			continue
		}
		step, due := Age(settings, m.Status, now)
		switch step {
		case Retire:
			m.Status.Phase = v1alpha1.PhaseRetired
			if err := r.putMachine(ctx, m, false); err != nil {
				return ctrl.Result{}, err
			}
		case Purge:
			if err := r.deleteMachine(ctx, m); err != nil {
				return ctrl.Result{}, err
			}
		}
		if !due.IsZero() && (next.IsZero() || due.Before(next)) {
			next = due
		}
	}

	ms.Status.LastEnumeration = lastEnumeration(e, created, int32(len(sweep.Mark))) // #nosec G115 -- a count of Machines in one namespace
	reason, message, ready := "Enumerated", fmt.Sprintf("enumeration %s listed %d machines", e.ID, len(e.Sightings)), true
	if enumErr != nil {
		reason, message, ready = "EnumerationFailed", enumErr.Error(), false
	}
	if err := r.writeSource(ctx, ms, ready, reason, message, MayMarkStale(e, enumErr)); err != nil {
		return ctrl.Result{}, err
	}
	if enumErr != nil {
		return ctrl.Result{}, enumErr
	}
	if next.IsZero() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// machine is one Machine with the object it was read from, so that a write carries
// the resourceVersion it read.
type machine struct {
	*v1alpha1.Machine
	obj *unstructured.Unstructured
}

func (r *Reconciler) listMachines(ctx context.Context, ms *v1alpha1.MachineSource) (map[string]*machine, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(v1alpha1.MachineListKind)
	if err := r.Client.List(ctx, list, client.InNamespace(ms.Namespace), client.MatchingLabels{v1alpha1.LabelSource: ms.Name}); err != nil {
		return nil, fmt.Errorf("list Machines of %s/%s: %w", ms.Namespace, ms.Name, err)
	}
	out := map[string]*machine{}
	for i := range list.Items {
		m, err := v1alpha1.MachineFromUnstructured(&list.Items[i])
		if err != nil {
			return nil, err
		}
		if m.Spec.SourceRef.Name == ms.Name {
			out[m.Spec.ExternalID] = &machine{Machine: m, obj: &list.Items[i]}
		}
	}
	return out, nil
}

// writeMachine creates the Machine of a sighting, or updates the one that exists.
func (r *Reconciler) writeMachine(ctx context.Context, ms *v1alpha1.MachineSource, m *machine, sight source.Sighting, e source.Enumeration) error {
	seen := metav1.NewTime(e.FinishedAt)
	isNew := m == nil
	if isNew {
		class := string(sight.Class)
		m = &machine{Machine: &v1alpha1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: MachineName(ms.Name, class, sight.ExternalID), Namespace: ms.Namespace},
			Spec:       v1alpha1.MachineSpec{SourceRef: v1alpha1.SourceRef{Name: ms.Name}, ExternalID: sight.ExternalID, Class: class},
			Status:     v1alpha1.MachineStatus{Phase: v1alpha1.PhaseActive, FirstSeen: &seen},
		}}
	}
	st := &m.Status
	Revive(st)
	st.DisplayName, st.ProviderID = sight.DisplayName, sight.ProviderID
	st.Addresses, st.PrimaryAddress = nil, source.PrimaryAddress(sight.Addresses)
	for _, a := range sight.Addresses {
		st.Addresses = append(st.Addresses, v1alpha1.Address{Type: a.Type, IP: a.IP, MAC: a.MAC, Network: a.Network})
	}
	st.Fingerprint = &v1alpha1.Fingerprint{FirmwareUUID: sight.Fingerprint.FirmwareUUID, InstanceUUID: sight.Fingerprint.InstanceUUID, Serial: sight.Fingerprint.Serial, MachineID: sight.Fingerprint.MachineID}
	st.OS = &v1alpha1.OS{Family: sight.OSFamily, Version: sight.OSVersion, Arch: sight.Arch, Kernel: sight.Kernel}
	if sight.Class == source.ClassNode {
		st.Ownership, st.OwnerRef = v1alpha1.OwnershipDeclared, &v1alpha1.OwnerRef{APIVersion: "v1", Kind: "Node", Name: sight.ExternalID}
	} else {
		st.Ownership, st.OwnerRef = v1alpha1.OwnershipUnmanaged, nil
	}
	st.LastSeen, st.LastEnumerationID = &seen, e.ID
	return r.putMachine(ctx, m, isNew)
}

// labelsOf are the five labels of a Machine: the only labels it may carry.
func labelsOf(m *v1alpha1.Machine) map[string]string {
	family := "unknown"
	if m.Status.OS != nil && m.Status.OS.Family != "" {
		family = m.Status.OS.Family
	}
	return map[string]string{
		v1alpha1.LabelClass:     m.Spec.Class,
		v1alpha1.LabelOwnership: m.Status.Ownership,
		v1alpha1.LabelOSFamily:  family,
		v1alpha1.LabelPhase:     strings.ToLower(m.Status.Phase),
		v1alpha1.LabelSource:    m.Spec.SourceRef.Name,
	}
}

// putMachine writes a Machine: its labels first, then its status. Nothing is sent for
// a part that did not change.
func (r *Reconciler) putMachine(ctx context.Context, m *machine, isNew bool) error {
	labels := labelsOf(m.Machine)
	if isNew {
		m.Labels = labels
		obj, err := v1alpha1.MachineToUnstructured(m.Machine)
		if err != nil {
			return err
		}
		unstructured.RemoveNestedField(obj.Object, "status")
		if err := r.Client.Create(ctx, obj); err != nil {
			return fmt.Errorf("create Machine %s/%s: %w", m.Namespace, m.Name, err)
		}
		m.obj, m.ResourceVersion = obj, obj.GetResourceVersion()
	} else if !reflect.DeepEqual(m.obj.GetLabels(), labels) {
		m.obj.SetLabels(labels)
		if err := r.Client.Update(ctx, m.obj); err != nil {
			return fmt.Errorf("update the labels of Machine %s/%s: %w", m.Namespace, m.Name, err)
		}
		m.ResourceVersion = m.obj.GetResourceVersion()
	}
	m.Labels = labels
	obj, err := v1alpha1.MachineToUnstructured(m.Machine)
	if err != nil {
		return err
	}
	if !isNew && reflect.DeepEqual(obj.Object["status"], m.obj.Object["status"]) {
		return nil
	}
	if err := r.Client.Status().Update(ctx, obj); err != nil {
		return fmt.Errorf("update the status of Machine %s/%s: %w", m.Namespace, m.Name, err)
	}
	m.obj = obj
	return nil
}

func (r *Reconciler) deleteMachine(ctx context.Context, m *machine) error {
	if err := r.Client.Delete(ctx, m.obj, client.Preconditions{UID: &m.UID}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete Machine %s/%s: %w", m.Namespace, m.Name, err)
	}
	return nil
}

// writeSource sets the conditions of a MachineSource and writes its status. complete
// is given only after an enumeration; without it, Complete and StaleHold are left as they are.
func (r *Reconciler) writeSource(ctx context.Context, ms *v1alpha1.MachineSource, ready bool, reason, message string, complete ...bool) error {
	set := func(kind string, ok bool, why, text string) {
		status := metav1.ConditionFalse
		if ok {
			status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{Type: kind, Status: status, Reason: why, Message: text, ObservedGeneration: ms.Generation})
	}
	set(v1alpha1.ConditionReady, ready, reason, message)
	if len(complete) == 1 {
		if complete[0] {
			set(v1alpha1.ConditionComplete, true, "AllScopesRead", "the last enumeration read every scope")
		} else {
			set(v1alpha1.ConditionComplete, false, "Incomplete", "the last enumeration did not read every scope; nothing was marked stale")
		}
		if h := ms.Status.Held; h != nil {
			set(v1alpha1.ConditionStaleHold, true, "TooManyMissing", fmt.Sprintf("%d of %d active machines are missing; set spec.stale.releaseHoldFor to %s to mark them stale", h.WouldMarkStale, h.Live, h.EnumerationID))
		} else {
			set(v1alpha1.ConditionStaleHold, false, "NotHeld", "no sweep is held")
		}
	}
	obj, err := v1alpha1.MachineSourceToUnstructured(ms)
	if err != nil {
		return err
	}
	if err := r.Client.Status().Update(ctx, obj); err != nil {
		return fmt.Errorf("update the status of MachineSource %s/%s: %w", ms.Namespace, ms.Name, err)
	}
	return nil
}

func lastEnumeration(e source.Enumeration, created, marked int32) *v1alpha1.LastEnumeration {
	out := &v1alpha1.LastEnumeration{
		ID: e.ID, StartedAt: metav1.NewTime(e.StartedAt), FinishedAt: metav1.NewTime(e.FinishedAt),
		Complete: e.Complete, Sightings: int32(len(e.Sightings)), Created: created, MarkedStale: marked, // #nosec G115 -- a count of machines one source lists
	}
	for _, s := range e.Scopes {
		out.Scopes = append(out.Scopes, v1alpha1.ScopeStatus{Name: s.Name, Readable: s.Readable, Error: s.Error, Count: int32(s.Count)}) // #nosec G115 -- a count of machines one scope lists
	}
	return out
}

func newObject(gvk schema.GroupVersionKind) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u
}

// NewULID returns a ULID for the time t: 48 bits of milliseconds and 80 random bits,
// in the Crockford base-32 alphabet, 26 characters.
func NewULID(t time.Time) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(t.UnixMilli())<<16) // #nosec G115 -- times after 1970
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}
