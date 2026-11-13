package suite

import (
	"strings"
	"testing"
)

func TestEncryptionCheck(t *testing.T) {
	if _, ok := registry[encryptionID]; !ok || encryptionID != "FND-G09" {
		t.Fatalf("check %s is not registered", encryptionID)
	}
	for name, c := range map[string]struct {
		answer stubAnswer
		want   string // "" = PASS, else a part of the reason of the FAIL
	}{
		"the fleet matches the declared posture": {stubAnswer{"── verdict ──\nPASS: 8 agent(s) match the declared posture", 0}, ""},
		"a worker opted out":                     {stubAnswer{"── verdict ──\nFAIL: region-b/cilium-x on worker-2 reports OptedOut", 1}, "exited 1: FAIL: region-b/cilium-x on worker-2 reports OptedOut"},
		"a region could not be read":             {stubAnswer{"❌ a region could not be read — a partial sweep is not a pass.", 2}, "exited 2: ❌ a region could not be read"},
		"the script stops without output":        {stubAnswer{"", 3}, "exited 3: (no output)"},
	} {
		root := t.TempDir()
		// The stub answers by its last argument: the kubeconfig of the last region.
		path := stubScript(t, root, encryptionScript, map[string]stubAnswer{"b": c.answer})
		r := runCheck(t, "none", encryptionCheck{}, &CheckContext{RepoRoot: root})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		// The script is run once, with the kubeconfig of every region.
		if args := stubArgs(t, path); strings.Join(args, "|") != "--kubeconfig a --kubeconfig b" {
			t.Errorf("%s: the script was run with %q", name, args)
		}
		if len(r.run.Steps) != 1 || r.run.Steps[0].ID != "FND-G09.1" || r.run.Steps[0].Evidence["exit_code"] != c.answer.code {
			t.Errorf("%s: want one step FND-G09.1 with the exit code, got %+v", name, r.run.Steps)
		}
	}
	// A script that is missing from the checkout fails the check.
	r := runCheck(t, "none", encryptionCheck{}, &CheckContext{RepoRoot: t.TempDir()})
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "exited 127") {
		t.Errorf("a missing script: want FAIL with exit code 127, got %+v", got)
	}
}
