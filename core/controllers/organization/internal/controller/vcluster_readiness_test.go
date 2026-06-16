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
		objs      []runtime.Object
		wantPhase string
		wantReady bool
	}{
		{
			name:      "no HR, no namespace → Pending (orphaned bytes, NOT Ready)",
			objs:      nil,
			wantPhase: "Pending",
			wantReady: false,
		},
		{
			name:      "HR present but Ready=False → Provisioning",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "False")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "HR present, no Ready condition yet → Provisioning",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "HR Ready=True but namespace missing → Provisioning",
			objs:      []runtime.Object{newHR(slug, "True")},
			wantPhase: "Provisioning",
			wantReady: false,
		},
		{
			name:      "HR Ready=True AND namespace Active → Ready",
			objs:      []runtime.Object{newNS(slug), newHR(slug, "True")},
			wantPhase: "Ready",
			wantReady: true,
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

			phase, ready, msg := r.vclusterReadiness(context.Background(), slug)
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
