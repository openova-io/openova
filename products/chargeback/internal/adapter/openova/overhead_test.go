package openova

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ns(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// The defect this fixes: on a namespace-isolated Sovereign every Application
// runs in a platform namespace carrying no Organization label, so the
// collector dropped all of them and the ledger read empty while the cluster
// was plainly busy (#6850, measured on hw307).
func TestUnlabelledNamespaceFeedsTheOverheadLine(t *testing.T) {
	c := &PlatformCollector{}
	c.SetOverheadOrg("hw307-omani-works")
	c.ObserveNamespace(ns("gitea", nil))

	c.mu.Lock()
	defer c.mu.Unlock()
	if got := c.nsOrg["gitea"]; got != "hw307-omani-works" {
		t.Fatalf("unlabelled platform namespace mapped to %q, want the overhead org — its usage is dropped", got)
	}
	if !c.isOverheadNs("gitea") {
		t.Fatal("namespace not marked as overhead — its rows would bill as tenant consumption")
	}
}

// A tenant Organization's own namespace must stay a tenant row. Attributing it
// to overhead would under-bill the tenant and inflate the Sovereign's cost.
func TestLabelledNamespaceStaysTenantNotOverhead(t *testing.T) {
	c := &PlatformCollector{}
	c.SetOverheadOrg("hw307-omani-works")
	c.ObserveNamespace(ns("acme-prod", map[string]string{orgLabel: "acme"}))

	c.mu.Lock()
	defer c.mu.Unlock()
	if got := c.nsOrg["acme-prod"]; got != "acme" {
		t.Fatalf("tenant namespace mapped to %q, want \"acme\"", got)
	}
	if c.isOverheadNs("acme-prod") {
		t.Fatal("tenant namespace marked as overhead — the tenant would be under-billed")
	}
}

// Before the internal Organization is known, attributing unlabelled namespaces
// to *anything* would be a guess. They must be ignored, exactly as before.
func TestNoOverheadOrgMeansNoAttribution(t *testing.T) {
	c := &PlatformCollector{}
	c.ObserveNamespace(ns("gitea", nil))

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.nsOrg["gitea"]; ok {
		t.Fatal("attributed an unlabelled namespace with no overhead org known — that is a guess, not a measurement")
	}
}

// A namespace that GAINS an Organization label must stop being overhead, or
// the same consumption is counted on both lines and the split stops
// reconciling to the cloud total.
func TestNamespaceRelabelMovesOffOverhead(t *testing.T) {
	c := &PlatformCollector{}
	c.SetOverheadOrg("sov")
	c.ObserveNamespace(ns("shared", nil))
	c.ObserveNamespace(ns("shared", map[string]string{orgLabel: "acme"}))

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isOverheadNs("shared") {
		t.Fatal("namespace still marked overhead after gaining an org label — usage double-counts")
	}
	if got := c.nsOrg["shared"]; got != "acme" {
		t.Fatalf("relabelled namespace mapped to %q, want \"acme\"", got)
	}
}

// The Sovereign's own Organization is identified by spec.kind = internal.
func TestReadOrgMarksInternal(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want bool
	}{{"internal", true}, {"tenant", false}, {"", false}} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"slug": "s", "kind": tc.kind},
		}}
		f, err := readOrg(u)
		if err != nil {
			t.Fatalf("kind=%q: %v", tc.kind, err)
		}
		if f.Internal != tc.want {
			t.Fatalf("spec.kind=%q → Internal=%v, want %v", tc.kind, f.Internal, tc.want)
		}
	}
}

// The emitted row must carry the tier marker, or the allocation view cannot
// separate overhead from tenant rows and the ADR-0014 split is unbuildable.
func TestOverheadLabelIsSerialisable(t *testing.T) {
	lb := map[string]any{"namespace": "gitea", "tier": overheadTier}
	b, err := json.Marshal(lb)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["tier"] != "platform-overhead" {
		t.Fatalf("tier round-tripped as %v", back["tier"])
	}
}
