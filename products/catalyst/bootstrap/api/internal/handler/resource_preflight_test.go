package handler

// Unit tests for the #3889 pre-fire resource preflight gate. They lock in
// the two load-bearing behaviours:
//   - the gate REFUSES on positive pressure evidence (DiskPressure /
//     MemoryPressure / measured usage >= high-water), and
//   - it FAILS OPEN on every uncertainty (no nodes, list error, no CP
//     label on a multi-node cluster, missing in-cluster client).
//
// The node-proxy /stats/summary path is not served by the fake clientset,
// so the percent-threshold branch is covered directly against
// evaluateResourcePreflight via the condition branches plus the fsStats
// usedPct() math; the wire decode is covered by TestNodeStatsSummaryDecode.

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
)

func cpNode(name string, conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
		},
		Status: corev1.NodeStatus{Conditions: conditions},
	}
}

func workerNode(name string, conditions ...corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Conditions: conditions},
	}
}

func cond(t corev1.NodeConditionType, s corev1.ConditionStatus) corev1.NodeCondition {
	return corev1.NodeCondition{Type: t, Status: s}
}

func TestEvaluateResourcePreflight(t *testing.T) {
	const hw = 85
	tests := []struct {
		name      string
		nodes     []*corev1.Node
		wantOK    bool
		reasonSub string // substring expected in reason when wantOK=false
	}{
		{
			name:   "no nodes fails open",
			nodes:  nil,
			wantOK: true,
		},
		{
			name:   "healthy control-plane node allows",
			nodes:  []*corev1.Node{cpNode("mothership", cond(corev1.NodeReady, corev1.ConditionTrue), cond(corev1.NodeDiskPressure, corev1.ConditionFalse), cond(corev1.NodeMemoryPressure, corev1.ConditionFalse))},
			wantOK: true,
		},
		{
			name:      "DiskPressure on CP node refuses",
			nodes:     []*corev1.Node{cpNode("mothership", cond(corev1.NodeDiskPressure, corev1.ConditionTrue))},
			wantOK:    false,
			reasonSub: "DiskPressure",
		},
		{
			name:      "MemoryPressure on CP node refuses",
			nodes:     []*corev1.Node{cpNode("mothership", cond(corev1.NodeMemoryPressure, corev1.ConditionTrue))},
			wantOK:    false,
			reasonSub: "MemoryPressure",
		},
		{
			name: "DiskPressure wins over MemoryPressure when both set",
			nodes: []*corev1.Node{cpNode("mothership",
				cond(corev1.NodeDiskPressure, corev1.ConditionTrue),
				cond(corev1.NodeMemoryPressure, corev1.ConditionTrue))},
			wantOK:    false,
			reasonSub: "DiskPressure",
		},
		{
			name: "pressure on a worker is ignored — only CP node is guarded",
			nodes: []*corev1.Node{
				cpNode("mothership", cond(corev1.NodeDiskPressure, corev1.ConditionFalse)),
				workerNode("worker-1", cond(corev1.NodeDiskPressure, corev1.ConditionTrue)),
			},
			wantOK: true,
		},
		{
			name: "single unlabeled node is treated as the mothership",
			nodes: []*corev1.Node{
				workerNode("only-node", cond(corev1.NodeDiskPressure, corev1.ConditionTrue)),
			},
			wantOK:    false,
			reasonSub: "DiskPressure",
		},
		{
			name: "multi-node cluster with no CP label fails open (can't pick mothership)",
			nodes: []*corev1.Node{
				workerNode("a", cond(corev1.NodeDiskPressure, corev1.ConditionTrue)),
				workerNode("b", cond(corev1.NodeDiskPressure, corev1.ConditionTrue)),
			},
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tc.nodes))
			for _, n := range tc.nodes {
				objs = append(objs, n)
			}
			core := kfake.NewSimpleClientset(objs...)
			res := evaluateResourcePreflight(context.Background(), core, hw)
			if res.ok != tc.wantOK {
				t.Fatalf("ok=%v want %v (reason=%q)", res.ok, tc.wantOK, res.reason)
			}
			if !tc.wantOK && tc.reasonSub != "" {
				if !containsSub(res.reason, tc.reasonSub) {
					t.Fatalf("reason %q does not contain %q", res.reason, tc.reasonSub)
				}
			}
			if !tc.wantOK && res.reason == "" {
				t.Fatalf("refuse result must carry a non-empty reason")
			}
		})
	}
}

func TestEvaluateResourcePreflightNilClientFailsOpen(t *testing.T) {
	res := evaluateResourcePreflight(context.Background(), nil, 85)
	if !res.ok {
		t.Fatalf("nil client must fail open, got refuse: %q", res.reason)
	}
}

func TestFsStatsUsedPct(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	tests := []struct {
		name string
		fs   *fsStats
		want int
	}{
		{"nil fs is unknown", nil, -1},
		{"nil capacity is unknown", &fsStats{UsedBytes: u(10)}, -1},
		{"nil used is unknown", &fsStats{CapacityBytes: u(100)}, -1},
		{"zero capacity is unknown", &fsStats{CapacityBytes: u(0), UsedBytes: u(10)}, -1},
		{"half used", &fsStats{CapacityBytes: u(100), UsedBytes: u(50)}, 50},
		{"at high water", &fsStats{CapacityBytes: u(100), UsedBytes: u(85)}, 85},
		{"near full", &fsStats{CapacityBytes: u(100), UsedBytes: u(93)}, 93},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fs.usedPct(); got != tc.want {
				t.Fatalf("usedPct()=%d want %d", got, tc.want)
			}
		})
	}
}

func TestPreflightHighWaterPct(t *testing.T) {
	tests := []struct {
		name string
		env  string
		set  bool
		want int
	}{
		{"unset uses default", "", false, preflightDiskHighWaterPct},
		{"empty uses default", "", true, preflightDiskHighWaterPct},
		{"valid override", "70", true, 70},
		{"non-numeric uses default", "abc", true, preflightDiskHighWaterPct},
		{"zero out of range uses default", "0", true, preflightDiskHighWaterPct},
		{"100 out of range uses default", "100", true, preflightDiskHighWaterPct},
		{"negative uses default", "-5", true, preflightDiskHighWaterPct},
		{"lower bound 1", "1", true, 1},
		{"upper bound 99", "99", true, 99},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("CATALYST_PREFLIGHT_DISK_HIGH_WATER_PCT", tc.env)
			} else {
				// ensure unset
				t.Setenv("CATALYST_PREFLIGHT_DISK_HIGH_WATER_PCT", "")
			}
			if got := preflightHighWaterPct(); got != tc.want {
				t.Fatalf("preflightHighWaterPct()=%d want %d", got, tc.want)
			}
		})
	}
}

func TestNodeStatsSummaryDecode(t *testing.T) {
	// The kubelet /stats/summary shape — confirm our partial struct picks
	// the higher of node.fs and node.runtime.imageFs usage.
	raw := []byte(`{
		"node": {
			"fs": {"capacityBytes": 100, "usedBytes": 60},
			"runtime": {"imageFs": {"capacityBytes": 100, "usedBytes": 91}}
		}
	}`)
	var s nodeStatsSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := s.Node.Fs.usedPct(); got != 60 {
		t.Fatalf("fs usedPct=%d want 60", got)
	}
	if s.Node.Runtime == nil || s.Node.Runtime.ImageFs == nil {
		t.Fatalf("imageFs not decoded")
	}
	if got := s.Node.Runtime.ImageFs.usedPct(); got != 91 {
		t.Fatalf("imageFs usedPct=%d want 91", got)
	}
}

func TestCheckResourcePreflightDisableBreakGlass(t *testing.T) {
	t.Setenv("CATALYST_PREFLIGHT_DISABLE", "1")
	h := &Handler{}
	// With the break-glass set, the gate must return nil even though no
	// sovereignDepsFactory is wired (it must short-circuit before the
	// client build).
	if err := h.checkResourcePreflight(context.Background()); err != nil {
		t.Fatalf("break-glass disable must allow, got %v", err)
	}
}

func TestCheckResourcePreflightRefusesViaFakeDeps(t *testing.T) {
	h := &Handler{}
	core := kfake.NewSimpleClientset(cpNode("mothership", cond(corev1.NodeDiskPressure, corev1.ConditionTrue)))
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core}, nil
	})
	err := h.checkResourcePreflight(context.Background())
	if err == nil {
		t.Fatalf("expected refuse on DiskPressure, got nil")
	}
	if !containsSub(err.Error(), "DiskPressure") {
		t.Fatalf("error %q missing DiskPressure", err.Error())
	}
}

func TestCheckResourcePreflightAllowsHealthyViaFakeDeps(t *testing.T) {
	h := &Handler{}
	core := kfake.NewSimpleClientset(cpNode("mothership", cond(corev1.NodeDiskPressure, corev1.ConditionFalse), cond(corev1.NodeMemoryPressure, corev1.ConditionFalse)))
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core}, nil
	})
	if err := h.checkResourcePreflight(context.Background()); err != nil {
		t.Fatalf("healthy node must allow, got %v", err)
	}
}

// containsSub is a tiny substring helper kept local to avoid importing
// strings into the test for a single use (the package already imports a
// great deal; this keeps the diff minimal).
func containsSub(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
