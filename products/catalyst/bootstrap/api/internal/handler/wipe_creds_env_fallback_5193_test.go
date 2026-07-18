package handler

import (
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// #5193 — an env the platform created must stay wipeable from the
// huawei-operator-creds projected env (CATALYST_HUAWEI_*) even in the worst case
// a partial destroy hit: the per-deployment tfvars are gone, the pod rolled (no
// in-memory dep.Request creds), and the wipe body carries no creds. Without this
// fallback buildWipeCredsRaw yields an empty bag → the wipe 400s "huawei
// credentials are required" and the record strands at status=wiping, blocking the
// one-environment-at-a-time preflight gate for the next fire.
func TestBuildWipeCredsRaw_HuaweiOperatorCredsEnvFallback_5193(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "AK-FROM-SECRET")
	t.Setenv("CATALYST_HUAWEI_SECRET_KEY", "SK-FROM-SECRET")
	t.Setenv("CATALYST_HUAWEI_PROJECT_ID", "PID-FROM-SECRET")
	t.Setenv("CATALYST_HUAWEI_REGION", "me-east-215")

	// Worst case: no body creds, no in-memory dep.Request creds.
	got := buildWipeCredsRaw("huawei", wipeRequest{}, provisioner.Request{})

	for k, want := range map[string]string{
		"access_key": "AK-FROM-SECRET",
		"secret_key": "SK-FROM-SECRET",
		"project_id": "PID-FROM-SECRET",
		"region":     "me-east-215",
	} {
		if got[k] != want {
			t.Fatalf("%s: want env fallback %q, got %q", k, want, got[k])
		}
	}
}

// Body-supplied creds still WIN over the env fallback (operator override intact).
func TestBuildWipeCredsRaw_BodyWinsOverEnv_5193(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_ACCESS_KEY", "AK-FROM-SECRET")
	got := buildWipeCredsRaw("huawei", wipeRequest{HuaweiAccessKey: "AK-FROM-BODY"}, provisioner.Request{})
	if got["access_key"] != "AK-FROM-BODY" {
		t.Fatalf("access_key: body must win over env, got %q", got["access_key"])
	}
}

// Region defaults to me-east-215 ONLY when AK/SK/PID resolved (matches janitor.go);
// the truly-empty case stays empty so the EVS/orphan backstop reads "no creds".
func TestBuildWipeCredsRaw_RegionDefaultOnlyWithCreds_5193(t *testing.T) {
	// No env, no body, no depReq → everything empty, region NOT defaulted.
	empty := buildWipeCredsRaw("huawei", wipeRequest{}, provisioner.Request{})
	if empty["region"] != "" {
		t.Fatalf("region: empty-everything case must stay empty, got %q", empty["region"])
	}
	// Creds present but region absent → default fills in.
	got := buildWipeCredsRaw("huawei", wipeRequest{
		HuaweiAccessKey: "ak", HuaweiSecretKey: "sk", HuaweiProjectID: "pid",
	}, provisioner.Request{})
	if got["region"] != "me-east-215" {
		t.Fatalf("region: want default me-east-215 when creds present, got %q", got["region"])
	}
}
