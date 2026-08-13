// jobs_cutover_step_order_6093_test.go — pins the ORDER the `/jobs`
// read path projects the eleven self-sovereign cutover steps in, and
// therefore the dependency edges the execution tree renders (issue
// #6093).
//
// # What went wrong
//
// chrootSeedCutoverActivity preferred listStepNamesFromStatus() — which
// reads the durable `self-sovereign-cutover-status` ConfigMap and ends
// in sort.Strings, because a ConfigMap's data map carries no sequence —
// and only fell back to the order-bearing listCutoverSteps() when that
// returned EMPTY. The chart pre-seeds all eleven `step.<name>.result`
// keys at install (09-cutover-status-configmap.yaml), so the empty case
// never happens on a Sovereign where the chart installed: the
// correctly-ordered branch was unreachable and the alphabetical one was
// permanently in charge.
//
// Measured on hw293.omantel.biz (dep a0077ba47e3720e5, 2026-08-11), with
// the chart Ready at 0.1.179 and no cutover ever run, the projected rows
// chained catalyst-api-env-patch -> crossplane-provider-pivot ->
// egress-block-test -> ... — pure lexicographic order, with the step-08
// deny-egress sovereignty proof rendered third. All eleven positions
// were wrong.
//
// # What these tests hold
//
// The assertions are on the ORDERED SLUG VALUES, never on the count: a
// length check passes on any permutation, which is exactly the mistake
// that let the scramble ship. Each test names the sequence it demands.
package handler

import (
	"strings"
	"testing"
)

// theElevenStepsInChartOrder is the declared execution order carried by
// the `bp.openova.io/cutover-order` label on the cutover-step-NN-<slug>
// ConfigMaps.
//
// UPDATED for #6214 (2026-08-13). The labels were renumbered so the
// deny-egress hold runs strictly LAST: gitea-token-mint 9->8,
// vcluster-registry-pivot 10->9, crossplane-provider-pivot 11->10,
// egress-block-test 8->11. Step-08's pre-hold lints assert that every
// Flux source, workload image and Crossplane Provider package already
// resolves Sovereign-local — two of the steps that PRODUCE that state
// used to run after it, so cutoverComplete was structurally unreachable
// on any Sovereign carrying an external Provider.
//
// Note the FILENAMES are deliberately unchanged (08-egress-block-test-job.yaml
// still carries order 11); the label is authoritative, so read the label,
// never the filename prefix. Verified against
// platform/self-sovereign-cutover/chart/templates/*.yaml at chart 0.1.182,
// and pinned there by cutover-contract.sh Case 91 (egress strictly last)
// and Case 92 (the labels are a contiguous 1..11 permutation).
var theElevenStepsInChartOrder = []string{
	"gitea-mirror",              // label 1
	"harbor-projects",           // label 2
	"harbor-prewarm",            // label 3
	"registry-pivot",            // label 4
	"flux-gitrepository-patch",  // label 5
	"helmrepository-patches",    // label 6
	"catalyst-api-env-patch",    // label 7
	"gitea-token-mint",          // label 8  (file 09-)
	"vcluster-registry-pivot",   // label 9  (file 10-)
	"crossplane-provider-pivot", // label 10 (file 11-)
	"egress-block-test",         // label 11 (file 08-) — the deny-egress proof, LAST
}

// statusKeysFor builds the durable status map the chart pre-seeds at
// install: one empty `step.<name>.result` per declared step, and nothing
// that encodes order. Returned as a Go map precisely because a map has
// no iteration order — the same property the real ConfigMap has.
func statusKeysFor(names []string) map[string]string {
	out := map[string]string{
		"cutoverComplete": "false",
		"progressPercent": "0",
		"totalSteps":      "11",
	}
	for _, n := range names {
		out["step."+n+".result"] = ""
		out["step."+n+".startedAt"] = ""
		out["step."+n+".finishedAt"] = ""
		out["step."+n+".jobName"] = ""
	}
	return out
}

// TestMergeCutoverStepOrder_ChartOrderWins is the headline: when the
// order-bearing step ConfigMaps are readable, the projection order is
// the CHART's, not the alphabet's.
func TestMergeCutoverStepOrder_ChartOrderWins(t *testing.T) {
	status := listStepNamesFromStatus(statusKeysFor(theElevenStepsInChartOrder))

	// Control on the INPUT, so this test cannot pass by accident: the
	// status-derived list must genuinely be the scrambled one. If
	// listStepNamesFromStatus ever started preserving order, the
	// assertion below would be vacuous and we want to know.
	if equalSlugs(status, theElevenStepsInChartOrder) {
		t.Fatalf("control failed: the status-derived order is ALREADY the chart order, so this test proves nothing.\n got %v", status)
	}
	if got, want := strings.Join(status, ","), "catalyst-api-env-patch,crossplane-provider-pivot,egress-block-test,flux-gitrepository-patch,gitea-mirror,gitea-token-mint,harbor-prewarm,harbor-projects,helmrepository-patches,registry-pivot,vcluster-registry-pivot"; got != want {
		t.Fatalf("control failed: expected listStepNamesFromStatus to return the alphabetical order.\n got  %s\n want %s", got, want)
	}

	got := mergeCutoverStepOrder(theElevenStepsInChartOrder, status)
	if !equalSlugs(got, theElevenStepsInChartOrder) {
		t.Errorf("projection order is not the chart's execution order.\n got  %v\n want %v", got, theElevenStepsInChartOrder)
	}

	// Name the single most misleading consequence explicitly, so a
	// regression reads as what it is rather than as a diff of two lists.
	//
	// Asserted as a RELATION (last), not the literal 8 it used to be. That
	// literal was written before #6214 renumbered the labels, and it had
	// become a trap pointing the wrong way: anyone correcting the fixture
	// above to the real chart order would have been failed by this line and
	// pushed to revert the correction — the guard arguing against the fix it
	// is supposed to protect. Renumbering stays free; moving the deny-egress
	// hold off the end does not. Mirrors cutover-contract.sh Case 91.
	if idx, last := indexOfSlug(got, "egress-block-test"), len(theElevenStepsInChartOrder)-1; idx != last {
		t.Errorf("egress-block-test is the deny-egress sovereignty proof and must project LAST (it proves the end state, so nothing may run after it — #6214); projected at position %d of %d, want position %d", idx+1, len(got), last+1)
	}
}

// TestMergeCutoverStepOrder_DurableOnlyStepSurvives holds law 3: a step
// that exists only in the durable record — its ConfigMap reaped after a
// completed cutover, which is the whole reason the status-derived path
// exists (#3646) — must still project, and must land AFTER every
// order-bearing step rather than being interleaved into a position it
// cannot claim (law 4).
func TestMergeCutoverStepOrder_DurableOnlyStepSurvives(t *testing.T) {
	// The live ConfigMaps have lost two steps; the durable record still
	// names all eleven. "aaa-reaped" is deliberately alphabetically
	// FIRST so an accidental re-sort would move it to the front and be
	// caught here.
	ordered := []string{"gitea-mirror", "harbor-projects"}
	durable := listStepNamesFromStatus(statusKeysFor([]string{
		"gitea-mirror", "harbor-projects", "aaa-reaped", "zzz-reaped",
	}))

	got := mergeCutoverStepOrder(ordered, durable)
	want := []string{"gitea-mirror", "harbor-projects", "aaa-reaped", "zzz-reaped"}
	if !equalSlugs(got, want) {
		t.Fatalf("durable-only steps must survive and trail the order-bearing ones.\n got  %v\n want %v", got, want)
	}
}

// TestMergeCutoverStepOrder_NoOrderBearingSourceStillProjects holds the
// vacuity case: when listCutoverSteps yields nothing (step ConfigMaps
// unreadable, or none carries the order label), the projection must
// still surface every step the durable record names. Returning an empty
// slice here would silently erase the Cutover group from /jobs — a
// worse failure than the wrong order.
func TestMergeCutoverStepOrder_NoOrderBearingSourceStillProjects(t *testing.T) {
	durable := listStepNamesFromStatus(statusKeysFor(theElevenStepsInChartOrder))

	got := mergeCutoverStepOrder(nil, durable)
	if len(got) != len(theElevenStepsInChartOrder) {
		t.Fatalf("with no order-bearing source the projection must still surface every durable step.\n got  %v (%d)\n want %d steps", got, len(got), len(theElevenStepsInChartOrder))
	}
	if !equalSlugs(got, durable) {
		t.Errorf("with no order-bearing source the durable order must pass through unchanged.\n got  %v\n want %v", got, durable)
	}
}

// TestMergeCutoverStepOrder_NoDurableRecordStillProjects is the mirror:
// a chart installed but never run whose status ConfigMap has not been
// written yet must still project its steps, in chart order.
func TestMergeCutoverStepOrder_NoDurableRecordStillProjects(t *testing.T) {
	got := mergeCutoverStepOrder(theElevenStepsInChartOrder, nil)
	if !equalSlugs(got, theElevenStepsInChartOrder) {
		t.Errorf("with no durable record the chart order must pass through unchanged.\n got  %v\n want %v", got, theElevenStepsInChartOrder)
	}
}

// TestMergeCutoverStepOrder_NoDuplicatesWhenBothSourcesAgree is the
// idempotency floor: the union of two sources naming the same eleven
// steps is eleven rows, not twenty-two. A duplicated slug would mint a
// second leaf Job per step on every /jobs read.
func TestMergeCutoverStepOrder_NoDuplicatesWhenBothSourcesAgree(t *testing.T) {
	durable := listStepNamesFromStatus(statusKeysFor(theElevenStepsInChartOrder))
	got := mergeCutoverStepOrder(theElevenStepsInChartOrder, durable)

	seen := map[string]int{}
	for _, s := range got {
		seen[s]++
	}
	for slug, n := range seen {
		if n != 1 {
			t.Errorf("step %q projected %d times; each step must appear exactly once", slug, n)
		}
	}
	if len(got) != 11 {
		t.Errorf("union of two sources naming the same 11 steps must be 11 rows, got %d: %v", len(got), got)
	}
}

// TestMergeCutoverStepOrder_BlanksDropped defends the downstream
// contract: ActivityBridge skips an empty slug, so a blank must never
// occupy a projection slot and shift the chain.
func TestMergeCutoverStepOrder_BlanksDropped(t *testing.T) {
	got := mergeCutoverStepOrder([]string{"gitea-mirror", "  ", ""}, []string{"", "harbor-projects"})
	want := []string{"gitea-mirror", "harbor-projects"}
	if !equalSlugs(got, want) {
		t.Errorf("blank slugs must be dropped, not projected.\n got  %v\n want %v", got, want)
	}
}

func equalSlugs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexOfSlug(in []string, want string) int {
	for i, s := range in {
		if s == want {
			return i
		}
	}
	return -1
}
