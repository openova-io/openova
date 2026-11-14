package suite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// kubeProxyPod is a fixture Cilium agent pod on a node.
func kubeProxyPod(name, node, phase string) obj {
	return obj{"metadata": obj{"namespace": "kube-system", "name": name}, "spec": obj{"nodeName": node}, "status": obj{"phase": phase}}
}

func kubeProxyNodeFixture(name string) obj { return obj{"metadata": obj{"name": name}} }

func init() {
	nodesA := kubeList(kubeProxyNodeFixture("a-cp1"), kubeProxyNodeFixture("a-w1"))
	podsA := kubeList(kubeProxyPod("cilium-a1", "a-cp1", "Running"), kubeProxyPod("cilium-a2", "a-w1", "Running"))
	nodesB := kubeList(kubeProxyNodeFixture("b-cp1"))
	podsB := kubeList(kubeProxyPod("cilium-b1", "b-cp1", "Running"))
	fakeAnswers["kube-proxy"] = map[string]string{
		"a nodes": nodesA, "a pods kube-system": podsA, "b nodes": nodesB, "b pods kube-system": podsB,
	}
	fakeAnswers["kube-proxy-agent-pending"] = map[string]string{
		"a nodes": nodesA, "b nodes": nodesB, "b pods kube-system": podsB,
		"a pods kube-system": kubeList(kubeProxyPod("cilium-a1", "a-cp1", "Running"), kubeProxyPod("cilium-a2", "a-w1", "Pending")),
	}
	fakeAnswers["kube-proxy-no-node"] = map[string]string{
		"a nodes": nodesA, "a pods kube-system": podsA, "b nodes": kubeList(), "b pods kube-system": kubeList(),
	}
	fakeAnswers["kube-proxy-region-b-unreachable"] = map[string]string{"a nodes": nodesA, "a pods kube-system": podsA}
}

const (
	kubeProxyStatusTrue  = "KVStore:                 Disabled\nKubernetes:              Ok   1.31 (v1.31.4+k3s1) [linux/amd64]\nKubeProxyReplacement:    True   [eth0   10.20.0.5 (Direct Routing)]\nHost firewall:           Disabled\n"
	kubeProxyStatusFalse = "KVStore:                 Disabled\nKubeProxyReplacement:    False\n"
	kubeProxyNatClean    = "#listed iptables-legacy\n-P PREROUTING ACCEPT\n-N CILIUM_POST_nat\n-A POSTROUTING -m comment --comment \"cilium-feeder: CILIUM_POST_nat\" -j CILIUM_POST_nat\n#listed iptables-nft\n-P PREROUTING ACCEPT\n#listed iptables\n-P PREROUTING ACCEPT\n"
	kubeProxyNatLegacy   = "#listed iptables-legacy\n-N KUBE-SERVICES\n-N KUBE-SVC-NPX46M4PTMTKRN6Y\n-A KUBE-SERVICES -j KUBE-SVC-NPX46M4PTMTKRN6Y\n#listed iptables-nft\n-P PREROUTING ACCEPT\n"
	kubeProxyNatNft      = "#listed iptables-legacy\n-P PREROUTING ACCEPT\n#listed iptables-nft\n-N KUBE-SVC-NPX46M4PTMTKRN6Y\n"
)

// fakeKubeProxyExec answers `cilium-dbg status` with status and the nat
// listing with nat, except for the pods named in override, whose answers are
// "<status>|<nat>". An override "error" makes the exec fail with no output;
// an override "exit 1" gives the usual answers together with an error, as a
// command that prints and then exits 1; an override "nat error" answers the
// status and makes the nat listing fail with no output.
func fakeKubeProxyExec(t *testing.T, override map[string]string) *[]string {
	t.Helper()
	asked := []string{}
	saved := kubeProxyExec
	t.Cleanup(func() { kubeProxyExec = saved })
	kubeProxyExec = func(_ context.Context, region kube.Region, pod string, cmd ...string) ([]byte, error) {
		asked = append(asked, region.ID+"/"+pod+"/"+cmd[0])
		status, nat := kubeProxyStatusTrue, kubeProxyNatClean
		var err error
		switch o, ok := override[pod]; {
		case ok && o == "error":
			return nil, errors.New("exec: container not found")
		case ok && o == "exit 1":
			err = errors.New("command terminated with exit code 1")
		case ok && o == "nat error":
			if cmd[0] == "sh" {
				return nil, errors.New("exec: sh: not found")
			}
		case ok:
			status, nat, _ = strings.Cut(o, "|")
		}
		if cmd[0] == "cilium-dbg" {
			return []byte(status), err
		}
		if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" || cmd[2] != kubeProxyNatScript {
			t.Errorf("unexpected command %q", cmd)
		}
		return []byte(nat), err
	}
	return &asked
}

func TestKubeProxyCheck(t *testing.T) {
	if _, ok := registry[kubeProxyID]; !ok || kubeProxyID != "NET-G03" {
		t.Fatalf("check %s is not registered", kubeProxyID)
	}
	for name, c := range map[string]struct {
		scenario string
		override map[string]string
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"every node is clean":                  {"kube-proxy", nil, ""},
		"an agent prints and exits 1":          {"kube-proxy", map[string]string{"cilium-a2": "exit 1"}, ""},
		"replacement is False on one node":     {"kube-proxy", map[string]string{"cilium-b1": kubeProxyStatusFalse + "|" + kubeProxyNatClean}, `node b-cp1 (agent cilium-b1): KubeProxyReplacement is "False", want True`},
		"the status has no replacement line":   {"kube-proxy", map[string]string{"cilium-a2": "KVStore: Disabled\n|" + kubeProxyNatClean}, `KubeProxyReplacement is "", want True`},
		"a KUBE-SVC chain in the legacy table": {"kube-proxy", map[string]string{"cilium-a1": kubeProxyStatusTrue + "|" + kubeProxyNatLegacy}, "the nat table names a KUBE-SVC- chain in 2 line(s) (listed with iptables-legacy, iptables-nft)"},
		"a KUBE-SVC chain in the nft table":    {"kube-proxy", map[string]string{"cilium-b1": kubeProxyStatusTrue + "|" + kubeProxyNatNft}, "the nat table names a KUBE-SVC- chain in 1 line(s)"},
		"no program lists the nat table":       {"kube-proxy", map[string]string{"cilium-a1": kubeProxyStatusTrue + "|"}, "no iptables program could list the nat table"},
		"the nat table cannot be listed":       {"kube-proxy", map[string]string{"cilium-a1": "nat error"}, "listing the nat table failed: exec: sh: not found"},
		"the agent cannot be asked":            {"kube-proxy", map[string]string{"cilium-b1": "error"}, "cilium-dbg status failed: exec: container not found"},
		"a node has no Running agent":          {"kube-proxy-agent-pending", nil, "node a-w1 has no Running Cilium agent"},
		"a region has no node":                 {"kube-proxy-no-node", nil, "region region-b has no node"},
		"a region cannot be reached":           {"kube-proxy-region-b-unreachable", nil, "[region-b] kubectl get nodes"},
	} {
		fakeKubeProxyExec(t, c.override)
		r := runCheck(t, c.scenario, kubeProxyCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// One step per region, and every agent is asked both questions.
	asked := fakeKubeProxyExec(t, nil)
	r := runCheck(t, "kube-proxy", kubeProxyCheck{}, &CheckContext{})
	if len(r.run.Steps) != 2 || r.run.Steps[0].ID != "NET-G03.1" || r.run.Steps[1].ID != "NET-G03.2" {
		t.Fatalf("want the steps NET-G03.1 and NET-G03.2, got %+v", r.run.Steps)
	}
	want := "region-a/cilium-a1/cilium-dbg region-a/cilium-a1/sh region-a/cilium-a2/cilium-dbg region-a/cilium-a2/sh region-b/cilium-b1/cilium-dbg region-b/cilium-b1/sh"
	if got := strings.Join(*asked, " "); got != want {
		t.Errorf("asked %q, want %q", got, want)
	}
	if nodes, _ := r.run.Steps[0].Evidence["nodes"].([]string); strings.Join(nodes, ",") != "a-cp1,a-w1" {
		t.Errorf("the step of region-a does not hold its nodes: %+v", r.run.Steps[0].Evidence)
	}
}

func TestKubeProxyParsers(t *testing.T) {
	for status, want := range map[string]string{
		kubeProxyStatusTrue:       "True",
		kubeProxyStatusFalse:      "False",
		"":                        "",
		"KubeProxyReplacement:\n": "",
	} {
		if got := kubeProxyReplacement(status); got != want {
			t.Errorf("kubeProxyReplacement(%q) = %q, want %q", status, got, want)
		}
	}
	listed, chains := kubeProxyNat(kubeProxyNatClean)
	if strings.Join(listed, ",") != "iptables-legacy,iptables-nft,iptables" || chains != 0 {
		t.Errorf("clean table: listed %v, chains %d", listed, chains)
	}
	listed, chains = kubeProxyNat(kubeProxyNatLegacy)
	if strings.Join(listed, ",") != "iptables-legacy,iptables-nft" || chains != 2 {
		t.Errorf("legacy table: listed %v, chains %d", listed, chains)
	}
	if listed, chains = kubeProxyNat(""); len(listed) != 0 || chains != 0 {
		t.Errorf("empty output: listed %v, chains %d", listed, chains)
	}
}

// The fake iptables programs of TestKubeProxyNatScript.
//
// The test links each program name to the test binary, as fakeRegions names
// the test binary as kubectl, and puts what the program prints in the
// environment variable kubeProxyFakeVar(program). The init below then answers
// as the program would: it prints that text, or exits 1 when the text is
// empty, or exits 2 when it is not asked for the nat table. So the test
// writes no program file.
const kubeProxyFakeEnv = "SUITE_FAKE_"

// kubeProxyFakeVar is the environment variable of one fake iptables program.
func kubeProxyFakeVar(program string) string {
	return kubeProxyFakeEnv + strings.ToUpper(strings.ReplaceAll(program, "-", "_"))
}

func init() {
	program := filepath.Base(os.Args[0])
	if !strings.HasPrefix(program, "iptables") {
		return
	}
	out := os.Getenv(kubeProxyFakeVar(program))
	switch {
	case strings.Join(os.Args[1:], " ") != "-t nat -S":
		os.Exit(2)
	case out == "":
		os.Exit(1)
	}
	fmt.Println(out)
	os.Exit(0)
}

// TestKubeProxyNatScript runs the listing script with sh, with fake iptables
// programs as the only programs on the PATH.
func TestKubeProxyNatScript(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		programs map[string]string // program name -> its output; "" makes it exit 1
		listed   string
		chains   int
	}{
		"both back ends, one with a chain": {map[string]string{"iptables-legacy": "-N KUBE-SVC-X", "iptables-nft": "-P PREROUTING ACCEPT"}, "iptables-legacy,iptables-nft", 1},
		"a program that fails is skipped":  {map[string]string{"iptables-legacy": "", "iptables-nft": "-N KUBE-SVC-Y", "iptables": "-N KUBE-SVC-Y"}, "iptables-nft,iptables", 2},
		"no program at all":                {map[string]string{}, "", 0},
	} {
		dir := t.TempDir()
		env := []string{"PATH=" + dir}
		for program, out := range c.programs {
			if err := os.Symlink(self, filepath.Join(dir, program)); err != nil {
				t.Fatal(err)
			}
			env = append(env, kubeProxyFakeVar(program)+"="+out)
		}
		cmd := exec.Command("sh", "-c", kubeProxyNatScript)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%s: the script failed: %v", name, err)
			continue
		}
		listed, chains := kubeProxyNat(string(out))
		if strings.Join(listed, ",") != c.listed || chains != c.chains {
			t.Errorf("%s: listed %v and %d chain lines, want %s and %d; output %q", name, listed, chains, c.listed, c.chains, out)
		}
	}
}
