// Package reboot is a region's reboot agent (kured) as a topology transition
// sees it, and the hold that keeps it from starting a drain while a
// transition of the region runs (a move and a drain take turns).
//
// It uses the mechanism kured already has and adds no component: kured takes
// ONE lock before it cordons and drains a node — a JSON value in an
// annotation of its own DaemonSet (the --lock-annotation flag, default
// weave.works/kured-node-lock; kured 1.23 pkg/daemonsetlock). A lock that is
// live (its TTL not passed) and names something other than the node makes
// kured log "Lock already held" and try again at its next period: that is
// kured's own manual hold, and bp-kured's install hold is one (nodeID
// catalyst-install-hold, TTL 0).
//
// The region's Continuum takes that lock while a transition of its region is
// in progress, under the nodeID continuum-<region>, with a TTL (default 5
// minutes) it renews while the transition runs, and removes it when the
// transition ends. If the controller is gone, nothing renews it: kured's own
// expiry test (created + TTL passed) frees it and the next node takes it.
//
// It never overwrites another writer's lock: it writes only when the lock is
// absent, expired, or its own, and every write is a merge patch carrying the
// resourceVersion it read, so the API server refuses it (409) when kured or
// anyone else changed the DaemonSet in between. kured's own writes are full
// updates with the resourceVersion it read, refused the same way.
//
// The only write is that annotation (RBAC: get + patch on the DaemonSet named
// kured in the agent's namespace; scripts/check-continuum-no-workload-mutation.sh
// allows exactly this call). The read also lists nodes, to tell a node kured
// drains (its lock names the node and the node is cordoned) from a hold.
package reboot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
)

// Defaults: bp-kured installs DaemonSet kube-system/kured (its values pin the
// name with fullnameOverride) and leaves kured's default lock annotation once
// the install hold is released.
const (
	DefaultNamespace      = "kube-system"
	DefaultDaemonSet      = "kured"
	DefaultLockAnnotation = "weave.works/kured-node-lock"
	DefaultTTL            = 5 * time.Minute
)

var (
	kuredDaemonSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	nodeGVR           = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

// Mode is what Sync does with the hold.
type Mode int

const (
	// Leave changes nothing (the state of a transition could not be read).
	Leave Mode = iota
	// Hold takes or renews the lock.
	Hold
	// Release removes the lock when it is this controller's.
	Release
)

// Lock is kured's single lock value (kured pkg/daemonsetlock
// LockAnnotationValue; the metadata kured adds is not needed here).
type Lock struct {
	NodeID  string        `json:"nodeID"`
	Created time.Time     `json:"created"`
	TTL     time.Duration `json:"TTL"`
}

// Expired is kured's own test (pkg/daemonsetlock ttlExpired): a lock with a
// TTL is free once created + TTL has passed; a lock without one never is.
func (l Lock) Expired(now time.Time) bool {
	return l.TTL > 0 && now.Sub(l.Created) >= l.TTL
}

type multiLock struct {
	Locks []Lock `json:"locks"`
}

// Agent is one region's reboot agent.
type Agent struct {
	Dyn dynamic.Interface
	// Namespace and DaemonSet of kured ("" = the defaults).
	Namespace string
	DaemonSet string
	// HolderID is the nodeID this controller locks with (continuum-<region>).
	HolderID string
	// TTL of the hold (0 = DefaultTTL).
	TTL time.Duration
}

func (a *Agent) ns() string {
	if a.Namespace != "" {
		return a.Namespace
	}
	return DefaultNamespace
}

func (a *Agent) name() string {
	if a.DaemonSet != "" {
		return a.DaemonSet
	}
	return DefaultDaemonSet
}

func (a *Agent) ttl() time.Duration {
	if a.TTL > 0 {
		return a.TTL
	}
	return DefaultTTL
}

// state is one read of kured's DaemonSet.
type state struct {
	found   bool
	rv      string
	key     string
	value   string
	present bool
	single  bool
	locks   []Lock // the live locks
	parseOK bool
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func (a *Agent) readDaemonSet(ctx context.Context, now time.Time) (state, error) {
	st := state{key: DefaultLockAnnotation, single: true}
	ds, err := a.Dyn.Resource(kuredDaemonSetGVR).Namespace(a.ns()).Get(ctx, a.name(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.found, st.rv = true, ds.GetResourceVersion()
	var args []string
	containers, _, _ := unstructured.NestedSlice(ds.Object, "spec", "template", "spec", "containers")
	for _, c := range containers {
		cm, _ := c.(map[string]interface{})
		for _, field := range []string{"command", "args"} {
			list, _ := cm[field].([]interface{})
			for _, x := range list {
				if s, ok := x.(string); ok {
					args = append(args, s)
				}
			}
		}
	}
	if v, ok := flagValue(args, "--lock-annotation"); ok && v != "" {
		st.key = v
	}
	if v, ok := flagValue(args, "--concurrency"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			st.single = false
		}
	}
	st.value, st.present = ds.GetAnnotations()[st.key]
	st.parseOK = true
	if !st.present {
		return st, nil
	}
	var all []Lock
	if st.single {
		var l Lock
		if err := json.Unmarshal([]byte(st.value), &l); err != nil {
			st.parseOK = false
			return st, nil
		}
		all = []Lock{l}
	} else {
		var ml multiLock
		if err := json.Unmarshal([]byte(st.value), &ml); err != nil {
			st.parseOK = false
			return st, nil
		}
		all = ml.Locks
	}
	for _, l := range all {
		if !l.Expired(now) {
			st.locks = append(st.locks, l)
		}
	}
	return st, nil
}

// Read reports the region's reboot agent. It only reads. Only a NotFound
// DaemonSet is "no agent"; any other failed read is Unknown, never a guess.
func (a *Agent) Read(ctx context.Context, now time.Time) transition.RebootStatus {
	st, err := a.readDaemonSet(ctx, now)
	if err != nil {
		return transition.RebootStatus{Unknown: true, Message: fmt.Sprintf("read DaemonSet %s/%s: %v", a.ns(), a.name(), err)}
	}
	if !st.found {
		return transition.RebootStatus{Message: fmt.Sprintf("no DaemonSet %s/%s: no reboot agent in this region", a.ns(), a.name())}
	}
	out := transition.RebootStatus{Agent: true, Holdable: st.single && st.parseOK}
	if !st.single {
		out.Message = "kured takes more than one lock (--concurrency > 1): a transition cannot hold it; drains are still waited for"
	}
	if !st.parseOK {
		// kured refuses to lock over a value it cannot read, so nothing can
		// be drained; nor is it overwritten here.
		out.Held = true
		out.Message = fmt.Sprintf("annotation %s holds a value kured cannot read: no node can be drained", st.key)
		return out
	}
	nodes, err := a.Dyn.Resource(nodeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Without the nodes a drain cannot be told from a hold.
		out.Unknown, out.Message = true, "list nodes: "+err.Error()
		return out
	}
	cordoned := map[string]bool{}
	for _, n := range nodes.Items {
		u, _, _ := unstructured.NestedBool(n.Object, "spec", "unschedulable")
		cordoned[n.GetName()] = u
	}
	for _, l := range st.locks {
		u, isNode := cordoned[l.NodeID]
		switch {
		case !isNode:
			out.Held = true
		case u:
			out.Draining = append(out.Draining, l.NodeID)
		}
		if st.single {
			out.Holder = l.NodeID
		}
	}
	sort.Strings(out.Draining)
	return out
}

// Sync takes, renews or releases the hold. It never writes over a live lock
// that is not its own, and it writes only with the resourceVersion it read; a
// refused write (409) is not an error: the next tick reads again.
func (a *Agent) Sync(ctx context.Context, mode Mode, now time.Time) error {
	if mode == Leave {
		return nil
	}
	st, err := a.readDaemonSet(ctx, now)
	if err != nil || !st.found || !st.single || !st.parseOK {
		return err
	}
	var own *Lock
	if st.present {
		var l Lock
		_ = json.Unmarshal([]byte(st.value), &l)
		if l.NodeID == a.HolderID {
			own = &l
		}
	}
	live := len(st.locks) > 0
	var value interface{}
	switch mode {
	case Hold:
		if live && own == nil {
			return nil // another holder's live lock: a drain, or a hold
		}
		if own != nil && !own.Expired(now) && now.Sub(own.Created) < a.ttl()/3 {
			return nil // fresh enough
		}
		b, err := json.Marshal(Lock{NodeID: a.HolderID, Created: now.UTC(), TTL: a.ttl()})
		if err != nil {
			return err
		}
		value = string(b)
	case Release:
		if own == nil {
			return nil
		}
		value = nil // a merge patch removes the key
	}
	return a.patch(ctx, st, value)
}

func (a *Agent) patch(ctx context.Context, st state, value interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{
		"resourceVersion": st.rv,
		"annotations":     map[string]interface{}{st.key: value},
	}})
	if err != nil {
		return err
	}
	_, err = a.Dyn.Resource(kuredDaemonSetGVR).Namespace(a.ns()).Patch(ctx, a.name(), types.MergePatchType, body, metav1.PatchOptions{})
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}
