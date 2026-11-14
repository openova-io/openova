package handler

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// #6156 — preflight-02 could not reach Fail on a 2-region Sovereign. The
// replica half lives in the peer region and this handler's client is scoped to
// region A, so the "replica not visible" branch was the permanent steady state
// and it reported Warn whether the standby was healthy or gone. classifyPreflight
// only blocks a mutating DR playback on Fail, so a real outage did not block it.
//
// These exercise continuumStandbyForPairs, the seam that decides it.

func contWithPair(pair string, standby *bool) unstructured.Unstructured {
	obj := map[string]interface{}{
		"spec": map[string]interface{}{
			"cnpgPair": map[string]interface{}{"name": pair},
		},
		"status": map[string]interface{}{},
	}
	if standby != nil {
		obj["status"].(map[string]interface{})["standbyAvailable"] = *standby
	}
	return unstructured.Unstructured{Object: obj}
}

func boolp(b bool) *bool { return &b }

// THE BUG: a standby outage on a pair whose replica is in the peer region must
// be reported unavailable, which is what lets preflight-02 reach Fail.
func TestPreflight02_StandbyOutage_IsReportedUnavailable_6156(t *testing.T) {
	conts := []unstructured.Unstructured{contWithPair("shared-pg", boolp(false))}
	avail, unavail := continuumStandbyForPairs(conts, []string{"shared-pg"})
	if len(avail) != 0 {
		t.Fatalf("a standby reported UNAVAILABLE must never land in available: %v", avail)
	}
	if len(unavail) != 1 || unavail[0] != "shared-pg" {
		t.Fatalf("standby outage must be reported unavailable so preflight-02 can Fail; got %v", unavail)
	}
}

// The healthy peer-region case still passes, on weaker provenance.
func TestPreflight02_HealthyPeerRegionStandby_IsAvailable_6156(t *testing.T) {
	conts := []unstructured.Unstructured{
		contWithPair("shared-pg", boolp(true)),
		contWithPair("shared-pg-b", boolp(true)),
	}
	avail, unavail := continuumStandbyForPairs(conts, []string{"shared-pg", "shared-pg-b"})
	if len(unavail) != 0 {
		t.Fatalf("healthy standbys must not be reported unavailable: %v", unavail)
	}
	if len(avail) != 2 {
		t.Fatalf("want both pairs available, got %v", avail)
	}
}

// VACUITY GUARD / honest-unknown: no Continuum naming the pair, or a CR that
// omits the key, must yield NEITHER verdict — so the caller keeps Warn and never
// fabricates a Pass. This is the condition #4901 was about.
func TestPreflight02_NoVerdict_StaysUnknown_6156(t *testing.T) {
	cases := map[string][]unstructured.Unstructured{
		"no continuum at all":     {},
		"continuum omits the key": {contWithPair("shared-pg", nil)},
		"continuum names another": {contWithPair("some-other-pair", boolp(true))},
	}
	for name, conts := range cases {
		avail, unavail := continuumStandbyForPairs(conts, []string{"shared-pg"})
		if len(avail) != 0 || len(unavail) != 0 {
			t.Fatalf("%s: absence of evidence must stay unknown, got avail=%v unavail=%v",
				name, avail, unavail)
		}
	}
}

// A false verdict from ANY producer wins over another's silence.
func TestPreflight02_FalseWinsOverSilence_6156(t *testing.T) {
	conts := []unstructured.Unstructured{
		contWithPair("shared-pg", boolp(true)),
		contWithPair("shared-pg", boolp(false)),
	}
	_, unavail := continuumStandbyForPairs(conts, []string{"shared-pg"})
	if len(unavail) != 1 {
		t.Fatalf("an outage reported by one producer must not be cancelled by another; got %v", unavail)
	}
}

// CONTROL — prove the helper discriminates rather than always answering the same
// way: the identical pair list yields opposite verdicts purely on the CR value.
func TestPreflight02_Control_SamePairOppositeVerdicts_6156(t *testing.T) {
	up, _ := continuumStandbyForPairs([]unstructured.Unstructured{contWithPair("p", boolp(true))}, []string{"p"})
	_, down := continuumStandbyForPairs([]unstructured.Unstructured{contWithPair("p", boolp(false))}, []string{"p"})
	if len(up) != 1 || len(down) != 1 {
		t.Fatalf("control failed — helper is not discriminating: up=%v down=%v", up, down)
	}
	if strings.Join(up, "") != "p" || strings.Join(down, "") != "p" {
		t.Fatalf("control failed on identity: up=%v down=%v", up, down)
	}
}
