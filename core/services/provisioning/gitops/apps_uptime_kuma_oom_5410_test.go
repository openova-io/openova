package gitops

import (
	"strings"
	"testing"
)

// #5410 — uptime-kuma must not regress to a memory ceiling that OOMKills it.
//
// It shipped at 128Mi and died permanently: live on hw290 Org theta-corp,
// 49 restarts with lastState.terminated.reason=OOMKilled, at exactly the
// declared value. The app installed, reported provisioned, and never served
// a single request — a customer-visible dead application.
//
// The value is load-bearing in a way that is easy to miss: qosResources()
// returns the declared request as the LIMIT too on every paid plan, so the
// pod is Guaranteed QoS (which the per-Org LimitRange's maxLimitRequestRatio
// {cpu:1,memory:1} requires in order to admit it). There is therefore no
// burst headroom — whatever is declared here is a hard ceiling.
//
// This guard is deliberately NOT a blanket floor across the catalog. A
// floor set high enough to catch uptime-kuma would also flag vaultwarden,
// which runs happily in 128Mi (Rust, genuinely lightweight) and has no
// evidence against it. Inventing a rule that fails a working app to catch a
// broken one trades a real defect for a false one. So this pins the specific
// app that has a proven OOM, and the accompanying issue records that the
// durable answer is runtime feedback — an OOMKill should surface as a
// provisioning signal — rather than a static threshold guess.
func TestUptimeKuma_MemoryAboveProvenOOMCeiling_5410(t *testing.T) {
	spec, ok := KnownApps["uptime-kuma"]
	if !ok {
		t.Fatal("uptime-kuma missing from KnownApps — if it was renamed, move this guard with it")
	}

	const provenOOMCeiling = 128 // Mi — the value that OOMKilled 49 times

	mem := strings.TrimSpace(spec.RAMMI)
	mi, err := memMi(mem)
	if err != nil {
		t.Fatalf("uptime-kuma RAMMI %q is unparseable: %v", mem, err)
	}
	if mi <= provenOOMCeiling {
		t.Errorf("uptime-kuma RAMMI is %q (%dMi) — at or below the %dMi ceiling that was PROVEN to OOMKill it 49 times on hw290. "+
			"This value is also the hard limit (Guaranteed QoS), so there is no burst headroom to absorb it.",
			mem, mi, provenOOMCeiling)
	}
}

// memMi parses a Kubernetes memory quantity limited to the Mi/Gi forms this
// map actually uses, and returns it in Mi.
func memMi(q string) (int, error) {
	mult := 1
	num := q
	switch {
	case strings.HasSuffix(q, "Gi"):
		mult, num = 1024, strings.TrimSuffix(q, "Gi")
	case strings.HasSuffix(q, "Mi"):
		num = strings.TrimSuffix(q, "Mi")
	default:
		return 0, errUnsupportedQuantity
	}
	n := 0
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0, errUnsupportedQuantity
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return 0, errUnsupportedQuantity
	}
	return n * mult, nil
}

var errUnsupportedQuantity = &quantityError{}

type quantityError struct{}

func (*quantityError) Error() string { return "unsupported memory quantity (want NNNMi or NNNGi)" }
