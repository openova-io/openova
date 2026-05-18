// sandbox_mapper_test.go — coverage for the Sandbox CR + sandbox-Pod
// mapper introduced 2026-05-18. Mirrors the HR-mapper coverage matrix
// (every status transition + family/region fallbacks + parent-org
// contains edge).
package test

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/informer"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func parseSandbox(t *testing.T, raw string) *unstructured.Unstructured {
	t.Helper()
	js, err := yaml.YAMLToJSON([]byte(strings.TrimSpace(raw)))
	if err != nil {
		t.Fatalf("yaml->json: %v", err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(js); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return u
}

func TestSandbox_Phase_Ready(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: emrah-at-acme-io
  namespace: acme
spec:
  owner:
    email: emrah@acme.io
    orgRef:
      slug: acme
  quota:
    cpu: "4"
    memory: 8Gi
    storage: 50Gi
    concurrentSessions: 3
status:
  phase: Ready
  sessions: 2
  storageUsed: 8.2Gi
`)
	res, ok := informer.BuildFromSandbox(sb, "fsn1")
	if !ok {
		t.Fatal("BuildFromSandbox returned not-ok")
	}
	if res.Node.Status != "succeeded" {
		t.Fatalf("status: %s", res.Node.Status)
	}
	if res.Node.ID != "fsn1:sandbox:emrah-at-acme-io" {
		t.Fatalf("id: %s", res.Node.ID)
	}
	if res.Node.Label != "emrah@acme.io" {
		t.Fatalf("label: %s", res.Node.Label)
	}
	if res.Node.Family == nil || *res.Node.Family != "sandbox" {
		t.Fatalf("family: %+v", res.Node.Family)
	}
	if res.Node.Region == nil || *res.Node.Region != "fsn1" {
		t.Fatalf("region: %+v", res.Node.Region)
	}
	if kind, _ := res.Node.Meta["kind"].(string); kind != "Sandbox" {
		t.Fatalf("meta.kind: %+v", res.Node.Meta["kind"])
	}
	if owner, _ := res.Node.Meta["ownerEmail"].(string); owner != "emrah@acme.io" {
		t.Fatalf("meta.ownerEmail: %+v", res.Node.Meta["ownerEmail"])
	}
	if slug, _ := res.Node.Meta["orgSlug"].(string); slug != "acme" {
		t.Fatalf("meta.orgSlug: %+v", res.Node.Meta["orgSlug"])
	}
	// One contains edge pointing at the tenant-org node.
	if len(res.Relationships) != 1 {
		t.Fatalf("rels=%d want 1: %+v", len(res.Relationships), res.Relationships)
	}
	r := res.Relationships[0]
	if r.Type != "contains" || r.FromID != "fsn1:sandbox:emrah-at-acme-io" || r.ToID != "fsn1:org:acme" {
		t.Fatalf("unexpected rel: %+v", r)
	}
}

func TestSandbox_Phase_Provisioning(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: alice-at-zeta-io
spec:
  owner:
    email: alice@zeta.io
    orgRef:
      slug: zeta
  quota:
    cpu: "2"
    memory: 4Gi
    storage: 20Gi
    concurrentSessions: 1
status:
  phase: Provisioning
`)
	res, _ := informer.BuildFromSandbox(sb, "hel1")
	if res.Node.Status != "running" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestSandbox_Phase_Failed(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: x
spec:
  owner:
    email: x@x.io
    orgRef:
      slug: x
  quota:
    cpu: "1"
    memory: 1Gi
    storage: 1Gi
    concurrentSessions: 1
status:
  phase: Failed
`)
	res, _ := informer.BuildFromSandbox(sb, "fsn1")
	if res.Node.Status != "failed" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestSandbox_Phase_Pending_When_Missing(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: pending
spec:
  owner:
    email: y@y.io
    orgRef:
      slug: y
  quota:
    cpu: "1"
    memory: 1Gi
    storage: 1Gi
    concurrentSessions: 1
`)
	res, _ := informer.BuildFromSandbox(sb, "fsn1")
	if res.Node.Status != "pending" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestSandbox_FamilyLabelOverride(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: sb-with-label
  labels:
    catalyst.openova.io/family: dev-experience
spec:
  owner:
    email: a@a.io
    orgRef:
      slug: a
  quota:
    cpu: "1"
    memory: 1Gi
    storage: 1Gi
    concurrentSessions: 1
status:
  phase: Ready
`)
	res, _ := informer.BuildFromSandbox(sb, "fsn1")
	if res.Node.Family == nil || *res.Node.Family != "dev-experience" {
		t.Fatalf("family: %+v", res.Node.Family)
	}
}

func TestSandbox_RegionFallback(t *testing.T) {
	sb := parseSandbox(t, `
apiVersion: sandbox.openova.io/v1
kind: Sandbox
metadata:
  name: no-region
spec:
  owner:
    email: z@z.io
    orgRef:
      slug: z
  quota:
    cpu: "1"
    memory: 1Gi
    storage: 1Gi
    concurrentSessions: 1
`)
	res, _ := informer.BuildFromSandbox(sb, "")
	if res.Node.ID != "default:sandbox:no-region" {
		t.Fatalf("id: %s", res.Node.ID)
	}
	if res.Node.Region == nil || *res.Node.Region != "default" {
		t.Fatalf("region: %+v", res.Node.Region)
	}
	// contains edge still points at tenant-org node in `default` region.
	if len(res.Relationships) != 1 || res.Relationships[0].ToID != "default:org:z" {
		t.Fatalf("rels: %+v", res.Relationships)
	}
}

func TestSandbox_NoOrgSlug_NoContainsEdge(t *testing.T) {
	// Hand-crafted unstructured (the CRD requires orgRef but a
	// malformed CR shouldn't crash the mapper).
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "sandbox.openova.io/v1",
		"kind":       "Sandbox",
		"metadata":   map[string]interface{}{"name": "orphan"},
		"spec": map[string]interface{}{
			"owner": map[string]interface{}{"email": "orphan@x.io"},
		},
		"status": map[string]interface{}{"phase": "Ready"},
	}}
	res, ok := informer.BuildFromSandbox(u, "fsn1")
	if !ok {
		t.Fatal("not-ok")
	}
	if len(res.Relationships) != 0 {
		t.Fatalf("rels=%d want 0: %+v", len(res.Relationships), res.Relationships)
	}
	if _, hasParent := res.Node.Meta["parent"]; hasParent {
		t.Fatalf("meta.parent should be absent when org slug missing: %+v", res.Node.Meta)
	}
}

func TestSandboxPod_PtyServer_Running_All_Ready(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pty-server-abc123-xyz",
			Namespace: "sandbox-emrah-at-acme-io",
			Labels: map[string]string{
				informer.SandboxPodComponentLabel: "pty-server",
				informer.LabelSandboxName:         "emrah-at-acme-io",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
			},
		},
	}
	res, ok := informer.BuildFromSandboxPod(pod, "fsn1")
	if !ok {
		t.Fatal("not-ok")
	}
	if res.Node.Status != "succeeded" {
		t.Fatalf("status: %s", res.Node.Status)
	}
	if res.Node.ID != "fsn1:sandbox-pod:sandbox-emrah-at-acme-io/pty-server-abc123-xyz" {
		t.Fatalf("id: %s", res.Node.ID)
	}
	if kind, _ := res.Node.Meta["kind"].(string); kind != "SandboxPod" {
		t.Fatalf("meta.kind: %+v", res.Node.Meta["kind"])
	}
	if comp, _ := res.Node.Meta["component"].(string); comp != "pty-server" {
		t.Fatalf("meta.component: %+v", res.Node.Meta["component"])
	}
	if len(res.Relationships) != 1 || res.Relationships[0].Type != "contains" {
		t.Fatalf("expected one contains edge: %+v", res.Relationships)
	}
	if res.Relationships[0].ToID != "fsn1:sandbox:emrah-at-acme-io" {
		t.Fatalf("parent: %s", res.Relationships[0].ToID)
	}
}

func TestSandboxPod_McpServer_NotReady_Yet(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "openova-sandbox-mcp-7d4f8-q9k2",
			Namespace: "sandbox-alice",
			Labels: map[string]string{
				informer.SandboxPodComponentLabel: "openova-sandbox-mcp",
				informer.LabelSandboxName:         "alice",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: false},
			},
		},
	}
	res, _ := informer.BuildFromSandboxPod(pod, "fsn1")
	if res.Node.Status != "running" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestSandboxPod_CrashLoopBackOff_IsFailed(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pty-server-broken",
			Namespace: "sandbox-bob",
			Labels: map[string]string{
				informer.SandboxPodComponentLabel: "pty-server",
				informer.LabelSandboxName:         "bob",
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		},
	}
	res, _ := informer.BuildFromSandboxPod(pod, "fsn1")
	if res.Node.Status != "failed" {
		t.Fatalf("status: %s", res.Node.Status)
	}
}

func TestSandboxPod_NonSandboxComponent_IsSkipped(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-abc",
			Namespace: "default",
			Labels: map[string]string{
				informer.SandboxPodComponentLabel: "nginx",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	_, ok := informer.BuildFromSandboxPod(pod, "fsn1")
	if ok {
		t.Fatalf("non-sandbox pod should not be mapped")
	}
}

func TestSandboxPod_FallbackParent_FromNamespaceStem(t *testing.T) {
	// No explicit LabelSandboxName — mapper should derive parent from
	// the `sandbox-<name>` namespace.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pty-server-no-label",
			Namespace: "sandbox-derived-name",
			Labels: map[string]string{
				informer.SandboxPodComponentLabel: "pty-server",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	res, ok := informer.BuildFromSandboxPod(pod, "fsn1")
	if !ok {
		t.Fatal("not-ok")
	}
	if len(res.Relationships) != 1 {
		t.Fatalf("rels=%d want 1: %+v", len(res.Relationships), res.Relationships)
	}
	if res.Relationships[0].ToID != "fsn1:sandbox:derived-name" {
		t.Fatalf("parent: %s", res.Relationships[0].ToID)
	}
}
