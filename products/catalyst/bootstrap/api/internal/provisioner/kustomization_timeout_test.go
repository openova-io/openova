// kustomization_timeout_test.go — locks the bootstrap Flux Kustomization
// timeouts at ≤ 5 minutes so iterative Phase-8a fixes are picked up
// from main on the next GitRepository poll instead of being held off
// for the duration of a failed health-check wait (issue #492).
//
// Phase-8a bug #17 (otech8 deployment 1bfc46347564467b, 2026-05-01):
// kustomize-controller's `wait: true` semantics hold the revision lock
// for the full `spec.timeout` while it polls the HelmReleases inside
// the Kustomization for `Ready=True`. With cilium broken (issue #491),
// the wait would never finish, so even though the GitRepository fetched
// the fix `66ea39f0` from main within 1 minute, bootstrap-kit's
// `lastAppliedRevision` stayed empty and `lastAttemptedRevision`
// stayed pinned to the old SHA `0765e89a` for the full 30 minutes.
// Operator was forced to wipe + reprovision instead of letting the
// in-flight cluster self-heal from the next poll.
//
// Canonical fix (Option A): reduce timeout from 30m to 5m on
// bootstrap-kit, sovereign-tls, and infrastructure-config — one timeout
// for all three so semantics are consistent. 5m matches the
// GitRepository poll interval; failed reconciles release the lock
// within ~6 minutes worst case and a fresh GitRepository revision
// gets applied on the next tick. We KEEP `wait: true` to preserve the
// "Kustomization Ready=True ⇒ every HR Ready" contract that downstream
// `dependsOn: bootstrap-kit` declarations rely on.
//
// THIS test asserts every Kustomization in the cloud-init template
// declares a timeout of exactly 5m. A future commit that bumps any of
// them back to 30m (or removes the timeout entirely, falling back to
// the kustomize-controller default of 5m which would be fine — but a
// missing timeout is a code smell here) lands as a test failure, NOT
// as a 30-minute deadlock on the next customer's first Sovereign.
package provisioner

import (
	"strings"
	"testing"
)

// TestKustomizationTimeout_AllAtFiveMinutes locks the bootstrap Flux
// Kustomization timeouts. All three of bootstrap-kit, sovereign-tls,
// and infrastructure-config must declare `timeout: 5m`.
//
// Implementation note: we don't bother locating each Kustomization by
// name and inspecting the next `timeout:` line beneath it. The
// cloud-init template is small and the only `timeout:` lines outside
// commentary are the three Kustomization timeouts. We assert presence
// of three `timeout: 5m` lines AND absence of any `timeout: 30m` lines
// (the pre-issue-492 form).
func TestKustomizationTimeout_AllAtFiveMinutes(t *testing.T) {
	tpl := readCloudInit(t)

	// Walk lines and bucket the operative `timeout:` declarations.
	// "Operative" means the line, after trimming whitespace, starts
	// with `timeout:` (no leading `#`). Comments that quote the
	// previous form for explanatory purposes are allowed.
	var operativeTimeouts []string
	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // comment — allowed to reference old/new form
		}
		if strings.HasPrefix(trimmed, "timeout:") {
			operativeTimeouts = append(operativeTimeouts, trimmed)
			t.Logf("line %d: %s", i+1, trimmed)
		}
	}

	// Expect exactly THREE operative timeout lines (bootstrap-kit +
	// sovereign-tls + infrastructure-config). A higher count means a
	// new Kustomization was added without updating this test —
	// promote the new one into the spec by extending the wanted
	// fixture; do not loosen the test.
	const wantCount = 3
	if got := len(operativeTimeouts); got != wantCount {
		t.Fatalf("expected exactly %d operative `timeout:` lines in cloud-init template (got %d): %v\n"+
			"If a new Kustomization was added, extend this test to assert its timeout matches the others.",
			wantCount, got, operativeTimeouts)
	}

	// Each must be exactly `timeout: 5m`. We deliberately match the
	// canonical form not just `<= 5m` because:
	//   - Anything shorter (e.g. 1m) risks tripping the timeout on
	//     legitimately slow CRD installs.
	//   - Anything longer (e.g. 10m) re-introduces the iteration-
	//     stall pathology issue #492 documents.
	const want = "timeout: 5m"
	for _, line := range operativeTimeouts {
		if line != want {
			t.Errorf("Kustomization timeout must be %q (issue #492 — locks revision lock release at GitRepository-poll cadence). Got: %q", want, line)
		}
	}
}

// TestKustomizationTimeout_NoThirtyMinuteRegression is the negative
// guard: NO operative `timeout: 30m` line appears anywhere in the
// cloud-init template. The pre-issue-492 form was `timeout: 30m`;
// anything that re-introduces it deadlocks Phase-8a iteration.
//
// Comments that reference the old form are explicitly allowed (the
// fix's commentary quotes "30m" for context). Operative YAML keys
// are not.
func TestKustomizationTimeout_NoThirtyMinuteRegression(t *testing.T) {
	tpl := readCloudInit(t)

	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Match `timeout: 30m` operatively. We don't match bare `30m`
		// because the cloud-init has unrelated 30m-flavoured strings
		// (e.g. systemd timers) that aren't kustomize-controller
		// timeouts. The `timeout:` prefix scopes the regression check
		// to the kustomize-controller knob.
		if strings.HasPrefix(trimmed, "timeout: 30m") {
			t.Errorf("line %d carries `timeout: 30m` outside a comment — issue #492 regression. The 30m wait deadlocks Phase-8a iteration when the first apply is unhealthy:\n  %s",
				i+1, line)
		}
	}
}

// TestKustomizationTimeout_WaitTrueRetained asserts every health-gating
// Flux Kustomization in the cloud-init template still declares
// `wait: true`. The issue #492 fix was Option A (reduce timeout) NOT
// Option B (set wait: false). Downstream `dependsOn: bootstrap-kit`
// relies on the Kustomization-level Ready=True signal aggregating its
// HelmReleases' Ready states; flipping bootstrap-kit / sovereign-tls /
// infrastructure-config to `wait: false` would break that contract and
// let infrastructure-config apply before its HRs are actually Ready.
//
// This test catches a future "drive-by simplification" that flips
// wait: false thinking it's harmless.
//
// EXCEPTION (#3804 / Refs #3642) — `bootstrap-kit-crs` is the ONE
// Kustomization that legitimately runs `wait: false`. It carries the
// raw CRD-dependent CRs (HTTPRoute / ExternalSecret / CNPG `Cluster`)
// that were extracted out of the now-in-vCluster Helm charts. Those CRs
// reconcile to health ASYNCHRONOUSLY (CNPG cluster provisioning, ESO
// sync) and MUST NOT gate the Kustomization — Flux only needs to confirm
// the apply succeeded (the CRDs are present, which `dependsOn:
// bootstrap-kit` already guarantees). It carries no `wait: true`, so the
// three load-bearing Kustomizations are still asserted below.
func TestKustomizationTimeout_WaitTrueRetained(t *testing.T) {
	tpl := readCloudInit(t)

	// We expect at least 3 `wait: true` lines (bootstrap-kit +
	// sovereign-tls + infrastructure-config). Cheap presence-count
	// rather than per-name extraction; combined with the timeout test
	// above, a missing `wait: true` would mean the count drops below 3.
	const want = "wait: true"
	count := strings.Count(tpl, want)
	if count < 3 {
		t.Errorf("expected `wait: true` on each of the 3 health-gating Kustomizations (got %d occurrences). Issue #492 fix is Option A (timeout reduction); Option B (wait: false) would break the dependsOn chain.", count)
	}

	// NO operative `wait: false` EXCEPT on bootstrap-kit-crs. Track the
	// owning Kustomization via its metadata.name — recognised as the
	// `name:` line whose previous non-comment line is `metadata:` (so the
	// `sourceRef.name` / `dependsOn[].name` lines that also say "name:"
	// don't shadow it). The allowlist is name-scoped, not a blanket
	// relaxation. As with the 30m guard, comments are fine.
	const allowWaitFalseFor = "bootstrap-kit-crs"
	currentName := ""
	prevNonComment := ""
	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "name:") && prevNonComment == "metadata:" {
			currentName = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		if strings.HasPrefix(trimmed, "wait: false") {
			if currentName == allowWaitFalseFor {
				prevNonComment = trimmed
				continue // intentional async CR-applier tier (#3804 / Refs #3642)
			}
			t.Errorf("line %d carries operative `wait: false` on Kustomization %q — issue #492 fix is Option A (timeout reduction), NOT Option B. Flipping wait breaks the `dependsOn: bootstrap-kit` Ready contract (only bootstrap-kit-crs may use wait:false):\n  %s",
				i+1, currentName, line)
		}
		prevNonComment = trimmed
	}
}
