package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestSandboxDBProvision_NameValidation covers the input-validation
// guard rails before any RBAC / API hop. The handler runs WITHOUT a
// live API server because it never gets past validation.
func TestSandboxDBProvision_NameValidation(t *testing.T) {
	cases := map[string]struct {
		args    string
		wantErr string
	}{
		"missing name":     {`{}`, "is required"},
		"too short":        {`{"name":"db"}`, "must match"},
		"uppercase":        {`{"name":"MyDB1"}`, "must match"},
		"leading digit":    {`{"name":"1db"}`, "must match"},
		"underscore":       {`{"name":"my_db"}`, "must match"},
		"too long":         {`{"name":"` + strings.Repeat("a", 41) + `"}`, "must match"},
		"unknown plan":     {`{"name":"mydb1","plan":"xxl"}`, "unknown plan"},
		"trailing hyphen":  {`{"name":"mydb-"}`, ""}, // legal per CNPG, our regex allows it
		"valid minimum":    {`{"name":"abcd"}`, ""},
		"valid with plan":  {`{"name":"mydb1","plan":"default"}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// No env / no real client — the validation runs first.
			// For valid names the call will progress to requireSandboxNS
			// (which fails because we passed an empty ctx); that's a
			// different error string, so we only assert the validation
			// error when expected.
			ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-uid-1"})
			_, err := sandboxDBProvision(ctx, json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			// Valid-name path: we expect the call to fall through to the
			// dynamicClient build (no kubeconfig + no SA → some build
			// error). The point is validation did NOT trip.
			if err == nil {
				return
			}
			// Validation errors carry "sandbox.db.provision:" but not
			// "must match" / "is required" / "unknown plan".
			low := err.Error()
			for _, bad := range []string{"must match", "is required", "unknown plan"} {
				if strings.Contains(low, bad) {
					t.Errorf("unexpected validation error for valid input: %v", err)
				}
			}
		})
	}
}

// TestBuildClusterCR_DefaultPlanShape asserts the rendered CR matches
// what the CNPG operator expects (postgresql.cnpg.io/v1 Cluster +
// bootstrap.initdb.database).
func TestBuildClusterCR_DefaultPlanShape(t *testing.T) {
	env := &Env{
		SandboxID:        "emrah",
		SandboxNamespace: "sandbox-uid-1",
	}
	cr := buildClusterCR(env, "mydb1", defaultCNPGPlan())
	if got := cr.GetAPIVersion(); got != "postgresql.cnpg.io/v1" {
		t.Errorf("apiVersion=%q want postgresql.cnpg.io/v1", got)
	}
	if got := cr.GetKind(); got != "Cluster" {
		t.Errorf("kind=%q want Cluster", got)
	}
	if got := cr.GetName(); got != "mydb1" {
		t.Errorf("name=%q want mydb1", got)
	}
	if got := cr.GetNamespace(); got != "sandbox-uid-1" {
		t.Errorf("namespace=%q want sandbox-uid-1", got)
	}
	labels := cr.GetLabels()
	if labels[cnpgManagedByLabel] != cnpgManagedByValue {
		t.Errorf("managed-by label=%q want %q", labels[cnpgManagedByLabel], cnpgManagedByValue)
	}
	if labels[cnpgSandboxIDLabel] != "emrah" {
		t.Errorf("sandbox-id label=%q want emrah", labels[cnpgSandboxIDLabel])
	}
	spec, ok := cr.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec type %T", cr.Object["spec"])
	}
	if spec["instances"].(int64) != 1 {
		t.Errorf("instances=%v want 1", spec["instances"])
	}
	if !strings.Contains(spec["imageName"].(string), "postgresql:16") {
		t.Errorf("imageName=%v want postgres 16", spec["imageName"])
	}
	storage := spec["storage"].(map[string]any)
	if storage["size"] != "5Gi" {
		t.Errorf("storage.size=%v want 5Gi", storage["size"])
	}
	bootstrap := spec["bootstrap"].(map[string]any)
	initdb := bootstrap["initdb"].(map[string]any)
	if initdb["database"] != "app" || initdb["owner"] != "app" {
		t.Errorf("initdb=%+v want database+owner=app", initdb)
	}
	if spec["enableSuperuserAccess"] != false {
		t.Errorf("enableSuperuserAccess=%v want false", spec["enableSuperuserAccess"])
	}
}

// TestConnectionFor_CNPGDefaults asserts the canonical envelope agents
// receive — host = `<name>-rw.<ns>.svc.cluster.local`, port 5432, secret
// = `<name>-app`.
func TestConnectionFor_CNPGDefaults(t *testing.T) {
	conn := connectionFor("mydb1", "sandbox-uid-1", "app")
	want := connectionEnvelope{
		Host:       "mydb1-rw.sandbox-uid-1.svc.cluster.local",
		Port:       5432,
		Database:   "app",
		User:       "app",
		SecretName: "mydb1-app",
		SecretKey:  "password",
		Cluster:    "mydb1",
		Namespace:  "sandbox-uid-1",
	}
	if conn != want {
		t.Errorf("conn=%+v want %+v", conn, want)
	}
}

// TestBuildBackupCR_LabelsAndRef asserts the on-demand Backup CR carries
// the cluster ref + the managed-by label so it sweeps cleanly when the
// agent later drops the Cluster.
func TestBuildBackupCR_LabelsAndRef(t *testing.T) {
	cr := buildBackupCR("sandbox-uid-1", "mydb1")
	if got := cr.GetKind(); got != "Backup" {
		t.Errorf("kind=%q want Backup", got)
	}
	if got := cr.GetNamespace(); got != "sandbox-uid-1" {
		t.Errorf("ns=%q want sandbox-uid-1", got)
	}
	if !strings.HasPrefix(cr.GetName(), "mydb1-dump-") {
		t.Errorf("name=%q want prefix mydb1-dump-", cr.GetName())
	}
	if cr.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		t.Errorf("managed-by label missing")
	}
	spec := cr.Object["spec"].(map[string]any)
	cluster := spec["cluster"].(map[string]any)
	if cluster["name"] != "mydb1" {
		t.Errorf("spec.cluster.name=%v want mydb1", cluster["name"])
	}
}

// TestRequireSandboxNS_GuardsEmpty makes sure no sandbox.db.* handler
// can race past namespace-resolution into a default-namespace call.
func TestRequireSandboxNS_GuardsEmpty(t *testing.T) {
	t.Run("no env on ctx", func(t *testing.T) {
		_, _, err := requireSandboxNS(context.Background())
		if err == nil || !strings.Contains(err.Error(), "server env missing") {
			t.Errorf("err = %v, want missing env", err)
		}
	})
	t.Run("env with empty namespace", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{OrgID: "acme"})
		_, _, err := requireSandboxNS(ctx)
		if err == nil || !strings.Contains(err.Error(), "no SANDBOX_NAMESPACE") {
			t.Errorf("err = %v, want missing namespace", err)
		}
	})
	t.Run("env populated", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-uid-1"})
		env, ns, err := requireSandboxNS(ctx)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if ns != "sandbox-uid-1" || env == nil {
			t.Errorf("got env=%v ns=%q", env, ns)
		}
	})
}

// TestSandboxDBCapabilityGate confirms the registry rejects sandbox.db.*
// calls whose claims lack the `sandbox.db` capability. Uses a registry
// in production mode (JWTSecret set) so the cap check fires.
func TestSandboxDBCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-uid-1",
	})
	for _, name := range []string{
		"sandbox.db.provision", "sandbox.db.list", "sandbox.db.get",
		"sandbox.db.drop", "sandbox.db.dump",
	} {
		t.Run(name, func(t *testing.T) {
			// Caller missing sandbox.db cap → 403.
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.session"}}
			args := json.RawMessage(`{"name":"mydb1"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err = %v, want forbidden", err)
			}
		})
	}
}

// TestSandboxDBProvision_PlanRejection asserts unknown plan names error
// before any cluster call. Runs against the registry so the auth gate +
// the validation gate both fire.
func TestSandboxDBProvision_PlanRejection(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-uid-1",
	})
	cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
	args := json.RawMessage(`{"name":"mydb1","plan":"xxl"}`)
	_, err := r.Call(context.Background(), "sandbox.db.provision", args, CallOpts{Claims: cl})
	if err == nil || !strings.Contains(err.Error(), "unknown plan") {
		t.Errorf("err = %v, want unknown plan", err)
	}
}

// TestReadCNPGStatusSummary_PendingFallback covers the empty-status path
// (newly created Cluster before the operator's first reconcile).
func TestReadCNPGStatusSummary_PendingFallback(t *testing.T) {
	out := readCNPGStatusSummary(map[string]any{})
	if out["phase"] != "Pending" {
		t.Errorf("empty status: phase=%v want Pending", out["phase"])
	}
}

// TestReadCNPGStatusSummary_ReadyCondition asserts the helper surfaces
// the operator's terminal Ready condition without echoing the full
// status sub-tree.
func TestReadCNPGStatusSummary_ReadyCondition(t *testing.T) {
	in := map[string]any{
		"status": map[string]any{
			"phase":          "Cluster in healthy state",
			"readyInstances": int64(1),
			"instances":      int64(1),
			"currentPrimary": "mydb1-1",
			"writeService":   "mydb1-rw",
			"conditions": []any{
				map[string]any{
					"type":   "Ready",
					"status": "True",
					"reason": "ClusterIsReady",
				},
				map[string]any{
					"type":   "ContinuousArchiving",
					"status": "False",
				},
			},
		},
	}
	out := readCNPGStatusSummary(in)
	if out["phase"] != "Cluster in healthy state" {
		t.Errorf("phase=%v", out["phase"])
	}
	ready, ok := out["ready"].(map[string]any)
	if !ok || ready["status"] != "True" || ready["type"] != "Ready" {
		t.Errorf("ready cond not surfaced: %+v", out)
	}
}
