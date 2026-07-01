package handler

import (
	"os"
	"sort"
	"testing"
)

// Test_cutoverAckTolerance covers the #4674 laggard-node tolerance knob.
func Test_cutoverAckTolerance(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "1": 1, "2": 2, "-1": 0, "junk": 0}
	for in, want := range cases {
		os.Setenv(envCutoverAckTolerance, in)
		if got := cutoverAckTolerance(); got != want {
			t.Errorf("cutoverAckTolerance(%q)=%d want %d", in, got, want)
		}
	}
	os.Unsetenv(envCutoverAckTolerance)
}

// Test_unackedRegistryPivotNodes proves the diagnostic lists exactly the nodes
// whose ack is not "v2" (the flaky ones), ignoring non-ack keys.
func Test_unackedRegistryPivotNodes(t *testing.T) {
	status := map[string]string{
		"node.a.registriesYaml": "v2",
		"node.b.registriesYaml": "v1",  // laggard
		"node.c.registriesYaml": "",    // laggard (unset)
		"node.d.registriesYaml": "v2",
		"progressPercent":       "27",  // ignored
		"currentStep":           "x",   // ignored
	}
	got := unackedRegistryPivotNodes(status)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("unackedRegistryPivotNodes=%v want [b c]", got)
	}
}
