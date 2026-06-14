// provisioner_shared_pg_test.go — #3188 / ADR-0010 coverage for the
// shared-Postgres opt-in seam.
//
// bootstrap-kit slot 16a (16a-bp-postgres-shared.yaml) gates the shared
// CNPG engine on the chart-side `${SOVEREIGN_ENABLE_SHARED_PG:=false}`
// envsubst. Before this seam NOTHING set the substitute var, so the
// fallback `false` always won and the reusable-shared-Postgres model was
// dormant + unreachable even on a fresh prov. These tests pin the wire:
// the boolean Request.EnableSharedPostgres field stringifies to the
// `enable_shared_pg` tofu var (which the cloud-init template interpolates
// verbatim into the Kustomization postBuild.substitute as
// SOVEREIGN_ENABLE_SHARED_PG).
//
//  1. opt-in set  → enable_shared_pg = "true"  (the model is reachable).
//  2. opt-in absent (default) → enable_shared_pg = "false" (safe default
//     preserved — single-region / non-sharing provs byte-identical).
package provisioner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTfvarsSharedPG is a small helper that validates + writes the tfvars
// for the given Request into a temp dir and returns the decoded map.
func writeTfvarsSharedPG(t *testing.T, req Request) map[string]any {
	t.Helper()
	dir, err := os.MkdirTemp("", "writeTfvars-sharedpg-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tofu.auto.tfvars.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	return out
}

// 1. The fire-body opt-in flips the substitution var to "true".
func TestWriteTfvars_EnableSharedPostgres_OptInResolvesTrue(t *testing.T) {
	req := tfvarsRequest(t)
	req.EnableSharedPostgres = true

	out := writeTfvarsSharedPG(t, req)

	if got := out["enable_shared_pg"]; got != "true" {
		t.Errorf("enable_shared_pg = %v, want %q (opt-in set → slot 16a renders the shared engine)", got, "true")
	}
}

// 2. Absent (the default) keeps the gate OFF — safe-by-default preserved.
func TestWriteTfvars_EnableSharedPostgres_DefaultResolvesFalse(t *testing.T) {
	req := tfvarsRequest(t)
	// EnableSharedPostgres left at its zero value (false) — this is now the
	// EXPLICIT opt-out shape (an operator who sets `"enableSharedPostgres":
	// false` in the body for the byte-identical dedicated-cluster path).
	// As of #3370 the CreateDeployment handler pre-seeds the field to TRUE
	// before json.Decode, so an OMITTED body provisions shared-pg by
	// default (covered by handler test TestCreateDeployment_
	// EnableSharedPostgresDefaultsTrue). This provisioner-layer test pins
	// that a zero-value Request still stringifies to "false" so the opt-out
	// path keeps slot 16a an empty-but-Ready release.

	out := writeTfvarsSharedPG(t, req)

	if got := out["enable_shared_pg"]; got != "false" {
		t.Errorf("enable_shared_pg = %v, want %q (explicit opt-out → slot 16a is an empty-but-Ready release)", got, "false")
	}
}
