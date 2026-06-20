// sandbox_db_hot_standby_test.go — D31 active-hot-standby regression
// tests for the sandbox.db plane.
//
// When the Sovereign opts in via SOVEREIGN_ENABLE_HOT_STANDBY=true AND
// both SOVEREIGN_PRIMARY_REGION + SOVEREIGN_REPLICA_REGION are non-empty
// AND distinct, sandbox.db.provision materialises a primary + replica
// Cluster.postgresql.cnpg.io pair (one each in the two regions) instead
// of a single Cluster. The rendered shape mirrors bp-wordpress-tenant
// chart 0.2.0+ + bp-cnpg-pair so failover / observability / WAL
// streaming behave identically across the marketplace tenant path and
// the sandbox.db plane.
//
// Defence-in-depth: if the operator enables the toggle but the region
// pair is degenerate (empty primary, empty replica, primary==replica),
// hotStandbyActive() returns false and the legacy single-Cluster shape
// is rendered instead. Same rule as the Organization-tenant gitops writer's
// renderOrgTenantOverlay defence — better to degrade silently to non-
// HA than to render unschedulable resources.

package tools

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func d31SandboxEnv(toggle bool, primary, replica string) *Env {
	return &Env{
		SandboxID:        "emrah",
		SandboxNamespace: "sandbox-uid-1",
		EnableHotStandby: toggle,
		PrimaryRegion:    primary,
		ReplicaRegion:    replica,
	}
}

// hotStandbyActive embodies the defence-in-depth rule: toggle ON,
// distinct non-empty regions ⇒ true; any degenerate case ⇒ false.
func TestHotStandbyActive_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		env     *Env
		want    bool
	}{
		{"toggle-off", d31SandboxEnv(false, "hz-fsn-rtz-prod", "hz-hel-rtz-prod"), false},
		{"toggle-on-empty-primary", d31SandboxEnv(true, "", "hz-hel-rtz-prod"), false},
		{"toggle-on-empty-replica", d31SandboxEnv(true, "hz-fsn-rtz-prod", ""), false},
		{"toggle-on-both-empty", d31SandboxEnv(true, "", ""), false},
		{"toggle-on-same-region", d31SandboxEnv(true, "hz-fsn-rtz-prod", "hz-fsn-rtz-prod"), false},
		{"toggle-on-distinct", d31SandboxEnv(true, "hz-fsn-rtz-prod", "hz-hel-rtz-prod"), true},
		{"nil-env", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hotStandbyActive(tc.env); got != tc.want {
				t.Errorf("hotStandbyActive=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestReplicaClusterName_Suffix asserts the canonical `<primary>-replica`
// suffix + 53-char ceiling so the CNPG operator's
// `<name>-replication-XX` Secret names stay inside the 63-char RFC-1123
// label limit.
func TestReplicaClusterName_Suffix(t *testing.T) {
	if got := replicaClusterName("mydb"); got != "mydb-replica" {
		t.Errorf("replicaClusterName(mydb)=%q want mydb-replica", got)
	}
	long := "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghij" // 50 chars
	got := replicaClusterName(long)
	if len(got) > 53 {
		t.Errorf("len(replicaClusterName) = %d, want <= 53; got=%q", len(got), got)
	}
}

// TestReplicationServiceName_Suffix asserts the canonical `<primary>-mesh`
// suffix used by the primary Cluster's spec.managed.services.additional
// entry — the ClusterMesh-global Service the replica's externalCluster
// resolves through.
func TestReplicationServiceName_Suffix(t *testing.T) {
	if got := replicationServiceName("mydb"); got != "mydb-mesh" {
		t.Errorf("replicationServiceName(mydb)=%q want mydb-mesh", got)
	}
}

// TestBuildClusterCRPair_PrimaryShape asserts the primary Cluster CR
// carries the canonical D31 shape: openova.io/region nodeAffinity
// pinned to primaryRegion, primaryUpdateMethod=switchover, and a
// ClusterMesh-global Service via spec.managed.services.additional[].
func TestBuildClusterCRPair_PrimaryShape(t *testing.T) {
	env := d31SandboxEnv(true, "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	pair := buildClusterCRPair(env, "mydb1", defaultCNPGPlan())
	if len(pair) != 2 {
		t.Fatalf("buildClusterCRPair returned %d CRs, want 2", len(pair))
	}
	primary := pair[0]
	if got := primary.GetName(); got != "mydb1" {
		t.Errorf("primary.name=%q want mydb1", got)
	}
	primaryLabels := primary.GetLabels()
	if primaryLabels["openova.io/cnpg-role"] != "primary" {
		t.Errorf("primary cnpg-role=%q want primary", primaryLabels["openova.io/cnpg-role"])
	}
	if primaryLabels["openova.io/region"] != "hz-fsn-rtz-prod" {
		t.Errorf("primary region label=%q want hz-fsn-rtz-prod", primaryLabels["openova.io/region"])
	}
	if primaryLabels["catalyst.openova.io/cnpg-pair"] != "mydb1" {
		t.Errorf("primary cnpg-pair label=%q want mydb1", primaryLabels["catalyst.openova.io/cnpg-pair"])
	}

	spec := primary.Object["spec"].(map[string]any)
	if got, ok := spec["primaryUpdateMethod"].(string); !ok || got != "switchover" {
		t.Errorf("primaryUpdateMethod=%v want switchover", spec["primaryUpdateMethod"])
	}
	// nodeAffinity → openova.io/region IN [hz-fsn-rtz-prod]
	regionVal := primaryRegionAffinityValue(t, primary)
	if regionVal != "hz-fsn-rtz-prod" {
		t.Errorf("primary nodeAffinity region=%q want hz-fsn-rtz-prod", regionVal)
	}
	// managed.services.additional[0] → name=mydb1-mesh, global annotation
	managed := spec["managed"].(map[string]any)
	services := managed["services"].(map[string]any)
	additional := services["additional"].([]any)
	if len(additional) != 1 {
		t.Fatalf("primary managed.services.additional len=%d want 1", len(additional))
	}
	svc := additional[0].(map[string]any)
	svcTpl := svc["serviceTemplate"].(map[string]any)
	svcMeta := svcTpl["metadata"].(map[string]any)
	if got := svcMeta["name"]; got != "mydb1-mesh" {
		t.Errorf("replication svc name=%v want mydb1-mesh", got)
	}
	svcAnn := svcMeta["annotations"].(map[string]any)
	if svcAnn["service.cilium.io/global"] != "true" {
		t.Errorf("missing service.cilium.io/global=true annotation; got=%v", svcAnn)
	}
}

// TestBuildClusterCRPair_ReplicaShape asserts the replica Cluster CR
// carries the canonical D31 shape: openova.io/region nodeAffinity
// pinned to replicaRegion, replica.enabled=true, externalClusters[]
// resolves to the primary via the ClusterMesh-global Service.
func TestBuildClusterCRPair_ReplicaShape(t *testing.T) {
	env := d31SandboxEnv(true, "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	pair := buildClusterCRPair(env, "mydb1", defaultCNPGPlan())
	replica := pair[1]
	if got := replica.GetName(); got != "mydb1-replica" {
		t.Errorf("replica.name=%q want mydb1-replica", got)
	}
	replicaLabels := replica.GetLabels()
	if replicaLabels["openova.io/cnpg-role"] != "replica" {
		t.Errorf("replica cnpg-role=%q want replica", replicaLabels["openova.io/cnpg-role"])
	}
	if replicaLabels["openova.io/region"] != "hz-hel-rtz-prod" {
		t.Errorf("replica region label=%q want hz-hel-rtz-prod", replicaLabels["openova.io/region"])
	}
	if replicaLabels["catalyst.openova.io/cnpg-pair"] != "mydb1" {
		t.Errorf("replica cnpg-pair label=%q want mydb1", replicaLabels["catalyst.openova.io/cnpg-pair"])
	}

	spec := replica.Object["spec"].(map[string]any)
	regionVal := primaryRegionAffinityValue(t, replica)
	if regionVal != "hz-hel-rtz-prod" {
		t.Errorf("replica nodeAffinity region=%q want hz-hel-rtz-prod", regionVal)
	}
	rep := spec["replica"].(map[string]any)
	if rep["enabled"] != true {
		t.Errorf("replica.enabled=%v want true", rep["enabled"])
	}
	if rep["source"] != "mydb1" {
		t.Errorf("replica.source=%v want mydb1", rep["source"])
	}
	ext := spec["externalClusters"].([]any)
	if len(ext) != 1 {
		t.Fatalf("externalClusters len=%d want 1", len(ext))
	}
	extC := ext[0].(map[string]any)
	if extC["name"] != "mydb1" {
		t.Errorf("externalClusters[0].name=%v want mydb1", extC["name"])
	}
	connParams := extC["connectionParameters"].(map[string]any)
	if connParams["host"] != "mydb1-mesh" {
		t.Errorf("externalClusters host=%v want mydb1-mesh", connParams["host"])
	}
	if connParams["user"] != "streaming_replica" {
		t.Errorf("externalClusters user=%v want streaming_replica", connParams["user"])
	}
}

// primaryRegionAffinityValue is a small helper that dives through the
// nested affinity/nodeAffinity/required... structure to extract the
// single openova.io/region match value. Fails the test if the shape
// doesn't match (Cluster CRs without it are unschedulable on a multi-
// region Sovereign so this is the right place to be strict).
func primaryRegionAffinityValue(t *testing.T, cr *unstructured.Unstructured) string {
	t.Helper()
	spec := cr.Object["spec"].(map[string]any)
	aff := spec["affinity"].(map[string]any)
	na := aff["nodeAffinity"].(map[string]any)
	req := na["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
	terms := req["nodeSelectorTerms"].([]any)
	if len(terms) == 0 {
		t.Fatalf("no nodeSelectorTerms on %s", cr.GetName())
	}
	term := terms[0].(map[string]any)
	matches := term["matchExpressions"].([]any)
	for _, m := range matches {
		mm := m.(map[string]any)
		if mm["key"] == "openova.io/region" {
			vals := mm["values"].([]any)
			if len(vals) == 0 {
				t.Fatalf("openova.io/region match has no values on %s", cr.GetName())
			}
			return vals[0].(string)
		}
	}
	t.Fatalf("no openova.io/region matchExpression on %s", cr.GetName())
	return ""
}
