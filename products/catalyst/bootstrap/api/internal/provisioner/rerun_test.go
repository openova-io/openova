// Tests for rerun.go: a Phase-0 run of a request rebuilt from its stored
// record re-applies the variables file of the deployment's working directory,
// and a Phase-0 result merges onto the record's result.
package provisioner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProvision_RecordRequestReappliesItsVariablesFile(t *testing.T) {
	t.Setenv(EnvEgressCIDRs, cpsEgress)
	t.Setenv(EnvDefaultAdminCIDRs, "")
	root := t.TempDir()
	module := filepath.Join(root, "providers", "hetzner")
	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatalf("mkdir module: %v", err)
	}
	if err := os.WriteFile(filepath.Join(module, "main.tf"), []byte("# module\n"), 0o600); err != nil {
		t.Fatalf("write module: %v", err)
	}
	p := &Provisioner{WorkDir: filepath.Join(root, "tofu"), ModulePath: module}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // each run stops at `tofu init`, before any cloud call
	run := func(req Request) error {
		_, err := p.Provision(ctx, req, make(chan Event, 64))
		return err
	}

	req := cpsRequest()
	req.DeploymentID, req.FromRecord = "rec-1", true
	file := filepath.Clean(filepath.Join(p.WorkDir, "rec-1", "tofu.auto.tfvars.json"))
	applied := []byte(`{"deployment_id":"rec-1","kubeconfig_bearer_token":"the applied one"}`)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.WriteFile(file, applied, 0o600); err != nil {
		t.Fatalf("write the applied file: %v", err)
	}
	if err := run(req); err == nil {
		t.Fatalf("Provision with a cancelled context returned no error")
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, applied) {
		t.Fatalf("a request rebuilt from the record rewrote the variables file the deployment was applied with:\n%s", got)
	}

	// The full request writes its own file, as the first run does.
	req.FromRecord = false
	_ = run(req)
	if got, _ := os.ReadFile(file); bytes.Equal(got, applied) || !bytes.Contains(got, []byte(`"deployment_id": "rec-1"`)) {
		t.Fatalf("the full request did not write its own variables file:\n%s", got)
	}

	// A rebuilt request with nothing applied stops before OpenTofu.
	req.DeploymentID, req.FromRecord = "rec-2", true
	if err := run(req); err == nil || !strings.Contains(err.Error(), "start a new deployment") {
		t.Fatalf("want a refusal naming the missing variables file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.WorkDir, "rec-2", "tofu.auto.tfvars.json")); !os.IsNotExist(err) {
		t.Fatalf("a variables file was written from the record alone (stat: %v)", err)
	}
}

func TestMergePhase0_KeepsTheFieldsOtherWritersOwn(t *testing.T) {
	at := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	prev := &Result{SovereignFQDN: "hw307.omani.works", KubeconfigPath: "/var/lib/catalyst/kubeconfigs/d.yaml", Phase1Substate: "watching", HandoverFiredAt: &at}
	fresh := &Result{SovereignFQDN: "hw307.omani.works", ControlPlaneIP: "192.0.2.10", ConsoleURL: "https://console.hw307.omani.works"}
	want := *fresh
	want.KubeconfigPath, want.Phase1Substate, want.HandoverFiredAt = prev.KubeconfigPath, prev.Phase1Substate, prev.HandoverFiredAt
	if got := MergePhase0(prev, fresh); !reflect.DeepEqual(*got, want) {
		t.Fatalf("MergePhase0:\n got %+v\nwant %+v", *got, want)
	}
	if prev.ControlPlaneIP != "" || fresh.KubeconfigPath != "" {
		t.Fatalf("MergePhase0 changed its inputs")
	}
	if MergePhase0(nil, fresh) != fresh || MergePhase0(prev, nil) != nil {
		t.Fatalf("a nil side must return fresh")
	}
	if !fresh.HasPhase0Outputs() || (&Result{KubeconfigPath: "/k"}).HasPhase0Outputs() || (*Result)(nil).HasPhase0Outputs() {
		t.Fatalf("HasPhase0Outputs must tell a Provision result from an upload's minimal one")
	}
}
