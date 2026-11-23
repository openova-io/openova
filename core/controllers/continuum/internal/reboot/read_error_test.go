package reboot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// A read that could not be made is Unknown, never "no agent": only a
// NotFound DaemonSet says the region has no reboot agent.
func TestReadError_IsUnknownNotNoAgent(t *testing.T) {
	dyn := newDyn(daemonSet(DefaultLockAnnotation, ""), node("w1", false))
	dyn.PrependReactor("get", "daemonsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "daemonsets"}, "kured", fmt.Errorf("RBAC: access denied"))
	})
	a := &Agent{Dyn: dyn, HolderID: "continuum-b"}
	s := a.Read(context.Background(), t0)
	if !s.Unknown || s.Agent || s.Held || !strings.Contains(s.Message, "forbidden") {
		t.Fatalf("a refused read must be Unknown and say why: %+v", s)
	}
	if err := a.Sync(context.Background(), Hold, t0); err == nil || len(patches(dyn)) != 0 {
		t.Fatalf("a hold over a refused read writes nothing and reports the error: %v %d", err, len(patches(dyn)))
	}

	missing := &Agent{Dyn: newDyn(node("w1", false)), HolderID: "continuum-b"}
	if s := missing.Read(context.Background(), t0); s.Unknown || s.Agent {
		t.Fatalf("no DaemonSet is known: no agent, got %+v", s)
	}
}

// Without the nodes a node kured drains cannot be told from a hold.
func TestNodesListError_IsUnknown(t *testing.T) {
	dyn := newDyn(daemonSet(DefaultLockAnnotation, lockJSON("w1", t0, time.Hour)), node("w1", true))
	dyn.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the server is currently unable to handle the request")
	})
	a := &Agent{Dyn: dyn, HolderID: "continuum-b"}
	s := a.Read(context.Background(), t0)
	if !s.Unknown || !s.Agent || len(s.Draining) > 0 || !strings.Contains(s.Message, "list nodes") {
		t.Fatalf("a failed node list must be Unknown and say why: %+v", s)
	}
}
