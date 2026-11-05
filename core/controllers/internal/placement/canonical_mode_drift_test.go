package placement

import (
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// The posture fold exists twice on purpose — here (the fan-out resolver's
// vocabulary) and in pkg/apis/blueprint/v1alpha1 (the stored-shape codec that
// catalyst-api also links). This package is `internal`, so the codec cannot
// import it; the two are pinned together here instead, over every spelling
// either side has ever accepted plus the pass-through cases.
func TestCanonicalMode_AgreesWithCanonicalize(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"singleton", "single-region", "single_region",
		"active-active", "active_active",
		"active-hot-standby", "active-hotstandby", "active_hot_standby",
		"active-passive", "active_passive", "  Active-Passive ",
		"", "bogus-mode", "multi-region",
	}
	for _, in := range inputs {
		if got, want := bpv1alpha1.CanonicalMode(in), Canonicalize(in); got != want {
			t.Errorf("CanonicalMode(%q) = %q but placement.Canonicalize(%q) = %q — the two folds drifted", in, got, in, want)
		}
	}
	for _, m := range CanonicalModes() {
		if bpv1alpha1.CanonicalMode(m) != m {
			t.Errorf("canonical token %q does not fold to itself", m)
		}
	}
}
