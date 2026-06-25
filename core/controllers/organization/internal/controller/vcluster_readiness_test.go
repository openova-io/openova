// vcluster_readiness_test.go — #3687 (fold #3669). Locks the contract
// that the Organization's vCluster phase + Ready condition derive from a
// LIVE readback of the vCluster HelmRelease (+ the per-Org Namespace),
// never from "I PUT the manifest to Gitea." The old code reported
// Phase=Provisioning frozen + Ready=True the instant the Gitea PutFile
// returned, painting a green Org over orphaned bytes.
package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// toClientObjects adapts a runtime.Object slice into the client.Object
// slice the fake builder's WithObjects expects.
func toClientObjects(objs []runtime.Object) []client.Object {
	out := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		if co, ok := o.(client.Object); ok {
			out = append(out, co)
		}
	}
	return out
}

// hrGVK is the Flux v2 HelmRelease GVK the readback Gets.
var hrGVK = schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}

// readbackScheme registers the core types + the unstructured HelmRelease
// (and its List kind) so the controller-runtime fake client can serve a
// typed Namespace Get and an unstructured HR Get.
func readbackScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	s.AddKnownTypeWithName(hrGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(hrGVK.GroupVersion().WithKind("HelmReleaseList"), &unstructured.UnstructuredList{})
	return s
}

func newHR(slug, readyStatus string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(hrGVK)
	hr.SetNamespace(slug)
	hr.SetName("vcluster")
	if readyStatus != "" {
		_ = unstructured.SetNestedSlice(hr.Object, []any{
			map[string]any{"type": "Ready", "status": readyStatus},
		}, "status", "conditions")
	}
	return hr
}

func newNS(slug string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: slug}}
}

func TestVClusterReadiness_PhaseLadder(t *testing.T) {
	const slug = "acme"

	cases := []struct {
		name      string
		planSlug  string
		objs      []runtime.Object
		wantPhase string
		wantReady bool
	}{
		// ── vcluster tier (m/l/xl/flexi): readiness keys off the vcluster HR. ──
		{
			name:      "vcluster tier: no HR, no namespace → Pending (orphaned bytes, NOT Ready)",
			planSlug:  "m",
			objs:      nil,
			wantPhase: "Pending",
			wantReady: false,
		},
		{
			name:      "vcluster tier: HR present but Ready=False → Provisioning",
			planSlug:  "m",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "False")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "vcluster tier: HR present, no Ready condition yet → Provisioning",
			planSlug:  "m",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "vcluster tier: HR Ready=True but namespace missing → Provisioning",
			planSlug:  "m",
			objs:      []runtime.Object{newHR(slug, "True")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "vcluster tier: HR Ready=True AND namespace Active → Ready",
			planSlug:  "m",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "True")},
			wantPhase: "Ready",
			wantReady: true,
		},
		// ── host tier (""/s/free): NO vcluster HR is ever authored, so
		//    readiness keys SOLELY off the host namespace existing (#4339). ──
		{
			name:      "host tier (s): namespace Active, NO HR → Ready (do NOT wait on a vcluster HR)",
			planSlug:  "s",
			objs:      []runtime.Object{newNS(slug)},
			wantPhase: "Ready",
			wantReady: true,
		},
		{
			name:      "host tier (free): namespace Active, NO HR → Ready",
			planSlug:  "free",
			objs:      []runtime.Object{newNS(slug)},
			wantPhase: "Ready",
			wantReady: true,
		},
		{
			name:      "host tier (empty plan): namespace Active, NO HR → Ready",
			planSlug:  "",
			objs:      []runtime.Object{newNS(slug)},
			wantPhase: "Ready",
			wantReady: true,
		},
		{
			name:      "host tier (s): namespace missing → Pending (host ns is the boundary)",
			planSlug:  "s",
			objs:      nil,
			wantPhase: "Pending",
			wantReady: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().
				WithScheme(readbackScheme(t)).
				WithObjects(toClientObjects(tc.objs)...).
				Build()
			r := &Reconciler{Log: logr.Discard()}
			r.Client = cl

			phase, ready, msg := r.vclusterReadiness(context.Background(), slug, tc.planSlug)
			if phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q (msg=%q)", phase, tc.wantPhase, msg)
			}
			if ready != tc.wantReady {
				t.Errorf("ready = %v, want %v (phase=%q msg=%q)", ready, tc.wantReady, phase, msg)
			}
			// A not-Ready result MUST carry an explanatory message so the
			// operator console can surface why the Org is not green.
			if !ready && msg == "" {
				t.Errorf("not-Ready result must carry a non-empty message")
			}
		})
	}
}

// TestVClusterReadiness_HostTierDoesNotWaitOnVclusterHR locks the #4339
// contract directly: a host-tier (S/free/"") Org reaches Ready as soon as its
// host namespace is Active, even though NO vcluster HR exists — whereas the
// identical-shaped vcluster-tier (M) Org with the same single namespace stays
// not-Ready because it is still waiting on its (absent) vcluster HR. This is
// the regression that wedged host-tier funnel Orgs at
// Ready=False:VClusterProvisioning forever so the apps-install phase was never
// reached.
func TestVClusterReadiness_HostTierDoesNotWaitOnVclusterHR(t *testing.T) {
	const slug = "g5funnel"

	// Only the host namespace exists — no vcluster HR (correctly never authored
	// for host tier; never yet reconciled for the vcluster tier).
	cl := fake.NewClientBuilder().
		WithScheme(readbackScheme(t)).
		WithObjects(toClientObjects([]runtime.Object{newNS(slug)})...).
		Build()
	r := &Reconciler{Log: logr.Discard()}
	r.Client = cl

	for _, plan := range []string{"", "s", "free"} {
		_, ready, msg := r.vclusterReadiness(context.Background(), slug, plan)
		if !ready {
			t.Errorf("host-tier plan %q with an Active namespace must be Ready (no vcluster HR to wait on); got not-Ready (msg=%q)", plan, msg)
		}
	}

	// The vcluster tier with the SAME single namespace must NOT be Ready — it is
	// still waiting on the vcluster HR that has not reconciled yet.
	if _, ready, _ := r.vclusterReadiness(context.Background(), slug, "m"); ready {
		t.Errorf("vcluster-tier plan \"m\" must wait on the vcluster HR; got Ready with only the namespace present")
	}
}

func TestHelmReleaseReady(t *testing.T) {
	if helmReleaseReady(newHR("x", "True")) != true {
		t.Errorf("Ready=True HR must report ready")
	}
	if helmReleaseReady(newHR("x", "False")) != false {
		t.Errorf("Ready=False HR must report not-ready")
	}
	if helmReleaseReady(newHR("x", "")) != false {
		t.Errorf("HR with no conditions must report not-ready")
	}
}
