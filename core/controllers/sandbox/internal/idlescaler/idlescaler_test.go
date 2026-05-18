// idlescaler_test.go — coverage for the Wave 10 IdleScaler.
//
// Strategy: drive the scaler with a fake controller-runtime client +
// a localhost httptest server that mimics pty-server's /idle endpoint.
// We assert four trajectories:
//
//	(1) Active pty-server with no idle time → no scale, no harm.
//	(2) Idle pty-server past timeout → spec.replicas patched to 0.
//	(3) activeSessions > 0 keeps the pod alive even past timeout.
//	(4) /idle probe failure with NO prior annotation → skip (next tick).
//	(5) Per-StatefulSet annotation overrides the controller default.
//	(6) StatefulSets outside `sandbox-*` namespace are ignored (defence
//	    in depth).
//	(7) StatefulSets already at replicas=0 are not re-patched.

package idlescaler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// helper to assemble a pty-server StatefulSet with the labels +
// annotations the renderer writes.
func ptyStatefulSet(namespace, name string, replicas int32, annotations map[string]string) *appsv1.StatefulSet {
	ann := map[string]string{}
	for k, v := range annotations {
		ann[k] = v
	}
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				LabelComponent: ComponentValue,
				LabelManagedBy: ManagedByValue,
			},
			Annotations: ann,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
		},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add clientgo scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

// helper — make a pty-server probe target. fn decides response per ns.
func newProbeServer(t *testing.T, fn func(ns string) (idleDTO, bool)) (*httptest.Server, func(ns string) string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/idle/", func(w http.ResponseWriter, r *http.Request) {
		ns := r.URL.Path[len("/idle/"):]
		dto, ok := fn(ns)
		if !ok {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dto)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	build := func(ns string) string { return srv.URL + "/idle/" + ns }
	return srv, build
}

// (1) Active pty-server, very recent activity → no scale.
func TestProcessOne_NotIdle_NoScale(t *testing.T) {
	t.Parallel()
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 2,
		map[string]string{AnnIdleTimeoutMinutes: "30"})

	c := newFakeClient(t, ss)
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{
			LastActivityAt: time.Now().UTC().Add(-1 * time.Minute),
			ActiveSessions: 0,
		}, true
	})

	now := time.Now().UTC()
	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 30,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Errorf("replicas: got %v want 2 (not idle yet, must not scale)", got.Spec.Replicas)
	}
	if _, ok := got.Annotations[AnnLastActivityAt]; !ok {
		t.Errorf("expected %s annotation to be stamped on success probe", AnnLastActivityAt)
	}
}

// (2) Idle past timeout, no active sessions → scale to zero.
func TestProcessOne_Idle_ScalesToZero(t *testing.T) {
	t.Parallel()
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 3,
		map[string]string{AnnIdleTimeoutMinutes: "30"})

	c := newFakeClient(t, ss)
	now := time.Now().UTC()
	stale := now.Add(-45 * time.Minute) // past the 30-min annotation timeout
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{LastActivityAt: stale, ActiveSessions: 0}, true
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 30,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 0 {
		t.Errorf("replicas: got %v want 0 (idle past timeout)", got.Spec.Replicas)
	}
	if got.Annotations[AnnLastActivityAt] == "" {
		t.Errorf("expected %s annotation to be stamped before scale", AnnLastActivityAt)
	}
}

// (3) activeSessions > 0 keeps the pod alive even past timeout.
func TestProcessOne_ActiveSessions_NeverScales(t *testing.T) {
	t.Parallel()
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 3,
		map[string]string{AnnIdleTimeoutMinutes: "5"})

	c := newFakeClient(t, ss)
	now := time.Now().UTC()
	stale := now.Add(-2 * time.Hour) // way past timeout
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{LastActivityAt: stale, ActiveSessions: 2}, true
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 5,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 3 {
		t.Errorf("replicas: got %v want 3 (activeSessions > 0 must keep pod alive)",
			got.Spec.Replicas)
	}
}

// (4) Probe failure with no prior annotation → skip (no scale, no
// annotation written).
func TestProcessOne_ProbeFailNoPriorAnnotation_Skips(t *testing.T) {
	t.Parallel()
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 2,
		map[string]string{AnnIdleTimeoutMinutes: "5"})

	c := newFakeClient(t, ss)
	now := time.Now().UTC()
	// probe returns 404
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{}, false
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 5,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 {
		t.Errorf("replicas: got %v want 2 (probe failed, no decision)",
			got.Spec.Replicas)
	}
	if _, ok := got.Annotations[AnnLastActivityAt]; ok {
		t.Errorf("annotation: got %v, expected NO annotation when probe fails",
			got.Annotations[AnnLastActivityAt])
	}
}

// (5) Per-StatefulSet annotation override the controller default.
func TestProcessOne_AnnotationOverridesDefault(t *testing.T) {
	t.Parallel()
	// SS says timeout is 5 minutes; controller default is 60 (would
	// not have scaled at 10min idle).
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 1,
		map[string]string{AnnIdleTimeoutMinutes: "5"})

	c := newFakeClient(t, ss)
	now := time.Now().UTC()
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{
			LastActivityAt: now.Add(-10 * time.Minute),
			ActiveSessions: 0,
		}, true
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 60, // would NOT scale at 10min
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 0 {
		t.Errorf("replicas: got %v want 0 (annotation says 5min, 10min idle)",
			got.Spec.Replicas)
	}
}

// (6) StatefulSets outside `sandbox-*` are ignored.
func TestRunOnce_IgnoresNonSandboxNamespace(t *testing.T) {
	t.Parallel()
	rogue := ptyStatefulSet("kube-system", "pty-server", 1,
		map[string]string{AnnIdleTimeoutMinutes: "5"})

	c := newFakeClient(t, rogue)
	now := time.Now().UTC()
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{
			LastActivityAt: now.Add(-2 * time.Hour),
			ActiveSessions: 0,
		}, true
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 5,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "kube-system", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 1 {
		t.Errorf("replicas: got %v want 1 (must NOT touch kube-system)",
			got.Spec.Replicas)
	}
}

// (7) StatefulSets already at replicas=0 are not re-patched (idempotent).
func TestProcessOne_AlreadyZero_NoOp(t *testing.T) {
	t.Parallel()
	ss := ptyStatefulSet("sandbox-emrah", "pty-server", 0,
		map[string]string{AnnIdleTimeoutMinutes: "5"})

	c := newFakeClient(t, ss)
	now := time.Now().UTC()
	_, probe := newProbeServer(t, func(ns string) (idleDTO, bool) {
		return idleDTO{
			LastActivityAt: now.Add(-2 * time.Hour),
			ActiveSessions: 0,
		}, true
	})

	s := New(c, logr.Discard(), Options{
		DefaultIdleTimeoutMinutes: 5,
		ProbeURL:                  probe,
		Now:                       func() time.Time { return now },
	})

	// A double pass — both passes should leave replicas==0 and not error.
	for i := 0; i < 2; i++ {
		if err := s.runOnce(context.Background()); err != nil {
			t.Fatalf("runOnce pass %d: %v", i, err)
		}
	}
	var got appsv1.StatefulSet
	if err := c.Get(context.Background(),
		client.ObjectKey{Namespace: "sandbox-emrah", Name: "pty-server"}, &got); err != nil {
		t.Fatalf("get post-pass: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 0 {
		t.Errorf("replicas: got %v want 0 (already zero, no-op)", got.Spec.Replicas)
	}
}

// (8) Default URL builder produces the cluster-DNS form.
func TestDefaultProbeURL(t *testing.T) {
	t.Parallel()
	s := New(nil, logr.Discard(), Options{})
	got := s.probeURL("sandbox-ceo-at-acme-com")
	want := fmt.Sprintf("http://pty-server.sandbox-ceo-at-acme-com.svc.cluster.local:%d%s",
		PtyServicePort, IdlePath)
	if got != want {
		t.Errorf("default probeURL:\n  got  %q\n  want %q", got, want)
	}
}

// (9) NeedLeaderElection — singleton across HA replicas.
func TestNeedLeaderElection_True(t *testing.T) {
	t.Parallel()
	s := New(nil, logr.Discard(), Options{})
	if !s.NeedLeaderElection() {
		t.Errorf("NeedLeaderElection: got false, want true (must be singleton)")
	}
}
