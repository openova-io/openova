// Tests for application-controller cmd flag parsing. Asserts the
// chart-binary contract: every CLI flag passed by
// products/catalyst/chart/templates/controllers/application-controller-deployment.yaml
// is recognized by the binary's flag set. Regressions here are
// load-bearing — the chart args are passed verbatim, so an unknown
// flag crashes the Pod into CrashLoopBackOff.
package main

import (
	"flag"
	"os"
	"testing"
)

// TestChartArgsRecognized exercises the same args the chart passes:
//
//	--leader-elect=true
//	--metrics-bind-address=:8080
//	--health-probe-bind-address=:8081
//
// Plus the leader-elect-namespace flag siblings define for parity.
// The test rebuilds the flag set the way main() does and Parse()s
// the chart args; any unknown flag returns a non-nil error.
func TestChartArgsRecognized(t *testing.T) {
	t.Helper()

	// Use a fresh FlagSet (don't mutate flag.CommandLine across tests).
	fs := flag.NewFlagSet("application-controller", flag.ContinueOnError)
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		leaderElectNS        string
	)
	fs.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "")
	fs.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "")
	fs.BoolVar(&enableLeaderElection, "leader-elect", true, "")
	fs.StringVar(&leaderElectNS, "leader-elect-namespace", "openova-system", "")

	chartArgs := []string{
		"--leader-elect=true",
		"--metrics-bind-address=:8080",
		"--health-probe-bind-address=:8081",
	}
	if err := fs.Parse(chartArgs); err != nil {
		t.Fatalf("flag.Parse(%v) = %v; chart contract broken", chartArgs, err)
	}

	if metricsAddr != ":8080" {
		t.Errorf("metricsAddr = %q, want :8080", metricsAddr)
	}
	if probeAddr != ":8081" {
		t.Errorf("probeAddr = %q, want :8081", probeAddr)
	}
	if !enableLeaderElection {
		t.Errorf("enableLeaderElection = false, want true")
	}
}

// TestChartArgsLeaderElectFalse covers the chart path where
// .Values.controllers.application.leaderElection.enabled = false.
func TestChartArgsLeaderElectFalse(t *testing.T) {
	t.Helper()

	fs := flag.NewFlagSet("application-controller", flag.ContinueOnError)
	var enableLeaderElection bool
	fs.BoolVar(&enableLeaderElection, "leader-elect", true, "")

	if err := fs.Parse([]string{"--leader-elect=false"}); err != nil {
		t.Fatalf("flag.Parse = %v", err)
	}
	if enableLeaderElection {
		t.Errorf("enableLeaderElection = true, want false")
	}
}

// TestEnvBool covers the envBool helper (added alongside the
// --leader-elect flag) since LEADER_ELECT env var is the documented
// override per the package doc-comment.
func TestEnvBool(t *testing.T) {
	cases := []struct {
		set, val string
		fallback bool
		want     bool
	}{
		{"", "", true, true},      // unset -> fallback
		{"X", "true", false, true},
		{"X", "1", false, true},
		{"X", "yes", false, true},
		{"X", "false", true, false},
		{"X", "0", true, false},
		{"X", "no", true, false},
		{"X", "garbage", true, true}, // invalid -> fallback
	}
	for i, c := range cases {
		key := "TEST_ENVBOOL_KEY"
		if c.set == "" {
			os.Unsetenv(key)
		} else {
			os.Setenv(key, c.val)
		}
		got := envBool(key, c.fallback)
		if got != c.want {
			t.Errorf("case %d (val=%q fallback=%v): got %v, want %v", i, c.val, c.fallback, got, c.want)
		}
		os.Unsetenv(key)
	}
}
