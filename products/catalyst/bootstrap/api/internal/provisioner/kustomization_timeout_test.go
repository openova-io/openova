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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// operativeTimeout is one non-comment `timeout:` line in the cloud-init
// template, tagged with the kind + metadata.name of the document that owns it.
// Ownership matters because the two controllers that read a `timeout:` field
// mean completely different things by it (see classifyTimeouts).
type operativeTimeout struct {
	line int
	text string // trimmed, e.g. "timeout: 5m"
	kind string // owning document kind, e.g. "Kustomization"
	name string // owning document metadata.name
}

// classifyTimeouts walks the cloud-init template and returns every operative
// `timeout:` declaration tagged with its owning document.
//
// Ownership tracking mirrors TestKustomizationTimeout_WaitTrueRetained: the
// document kind comes from a `kind:` line, and metadata.name is the `name:`
// line whose previous non-comment line is `metadata:` (so sourceRef.name /
// dependsOn[].name / substituteFrom[].name never shadow it).
func classifyTimeouts(tpl string) []operativeTimeout {
	var out []operativeTimeout
	kind, name, prevNonComment := "", "", ""
	for i, line := range strings.Split(tpl, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // comment — never operative
		}
		if strings.HasPrefix(trimmed, "kind:") {
			// A `kind:` under sourceRef (previous line is `sourceRef:`) names the
			// REFERENT, not this document. Only a top-of-document `kind:` re-tags.
			if prevNonComment != "sourceRef:" {
				kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "kind:"))
				name = ""
			}
		}
		if strings.HasPrefix(trimmed, "name:") && prevNonComment == "metadata:" {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "name:"))
		}
		if strings.HasPrefix(trimmed, "timeout:") {
			out = append(out, operativeTimeout{line: i + 1, text: trimmed, kind: kind, name: name})
		}
		prevNonComment = trimmed
	}
	return out
}

// TestKustomizationTimeout_AllAtFiveMinutes locks the bootstrap Flux
// Kustomization timeouts. The two health-gating (`wait: true`)
// Kustomizations — bootstrap-kit and infrastructure-config — must each
// declare `timeout: 5m`.
//
// #3845/#3888 — sovereign-tls was deliberately re-homed onto the
// bootstrap-kit-crs idiom (`wait: false` + `retryInterval: 1m`, NO
// dependsOn, NO timeout: an async, self-retrying applier that must not be
// gated on the ~50 leaf-app HRs). A `wait: false` Kustomization does not
// hold the revision lock the way a `wait: true` one does, so the issue
// #492 deadlock pathology this test guards simply does not apply to it —
// it has no operative `timeout:` line and is excluded from the count.
//
// #6336 — the operative `timeout:` lines are no longer Kustomization-only.
// The bootstrap `GitRepository/openova` now declares one too, and it means the
// OPPOSITE thing: source-controller's clone deadline, on an object that holds
// no revision lock. Conflating the two is exactly how a well-meaning "make the
// timeouts consistent" edit would re-break a fresh prov, so this test buckets
// timeouts BY OWNING KIND (classifyTimeouts) and pins each bucket to its own
// canonical value. An operative `timeout:` owned by neither kind fails: a newly
// inlined resource must be promoted into this spec, not absorbed silently.
func TestKustomizationTimeout_AllAtFiveMinutes(t *testing.T) {
	tpl := readCloudInit(t)

	all := classifyTimeouts(tpl)
	var operativeTimeouts []string
	for _, ot := range all {
		t.Logf("line %d: %s (owner: %s/%s)", ot.line, ot.text, ot.kind, ot.name)
		switch ot.kind {
		case "Kustomization":
			operativeTimeouts = append(operativeTimeouts, ot.text)
		case "GitRepository":
			// Asserted by TestGitRepositoryCloneTimeout_HasHeadroom below.
		default:
			t.Errorf("line %d: operative `timeout:` on an unclassified document (kind=%q name=%q): %s\n"+
				"Every operative timeout must be promoted into this test's spec — a new inlined resource "+
				"carrying a timeout nobody pinned is how issue #492 and issue #6336 both happened.",
				ot.line, ot.kind, ot.name, ot.text)
		}
	}

	// Expect exactly ONE Kustomization-owned timeout line in cloud-init:
	// bootstrap-kit.
	// #4521 moved the `infrastructure-config` Kustomization OUT of the inline
	// cloud-init into a COMMITTED Flux Kustomization CR
	// (clusters/_template/infrastructure/providers/{,hetzner}/
	// infrastructure-config-kustomization.yaml) to keep it off the byte-capped
	// cloud-init render — so its `timeout: 5m` now lives in those repo files,
	// not here (asserted separately by configKustomizationTimeoutFiles below).
	// The inline `infrastructure-providers` LAYER-1 Kustomization carries NO
	// timeout (it is a wait:false self-retrying applier, like sovereign-tls +
	// bootstrap-kit-crs). A higher count means a new health-gating Kustomization
	// was inlined without updating this test — promote it into the spec by
	// extending the wanted fixture; do not loosen the test.
	const wantCount = 1
	if got := len(operativeTimeouts); got != wantCount {
		t.Fatalf("expected exactly %d operative `timeout:` lines in cloud-init template (got %d): %v\n"+
			"If a new health-gating Kustomization was added, extend this test to assert its timeout matches the others.",
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
// HelmReleases' Ready states; flipping bootstrap-kit / infrastructure-config
// to `wait: false` would break that contract and let infrastructure-config
// apply before its HRs are actually Ready.
//
// This test catches a future "drive-by simplification" that flips
// wait: false thinking it's harmless.
//
// EXCEPTIONS — these Kustomizations legitimately run `wait: false`:
//   - `bootstrap-kit-crs` (#3804 / Refs #3642): carries the raw
//     CRD-dependent CRs (HTTPRoute / ExternalSecret / CNPG `Cluster`)
//     extracted out of the now-in-vCluster Helm charts. Those CRs reconcile
//     to health ASYNCHRONOUSLY (CNPG cluster provisioning, ESO sync) and
//     MUST NOT gate the Kustomization — Flux only needs to confirm the apply
//     succeeded.
//   - `sovereign-tls` (#3845 / Refs #3888): re-homed onto the same async
//     idiom. The wildcard-TLS + Gateway datapath must NOT be gated on the
//     ~50 bootstrap-kit leaf-app HRs (a single non-foundational app failing
//     would otherwise wedge the whole Sovereign's :443). It reconciles in
//     parallel with a short retryInterval until the cert-manager.io /
//     gateway.networking.k8s.io CRDs register, then applies — with per-CR
//     health (cert issuance, envoy restart) reconciling async, so `wait:
//     false` is correct.
//   - `infrastructure-config` (#4212 / Refs #4002 #4018): the cross-cloud
//     Crossplane object-model layer (provider-opentofu + the Observe-first
//     CloudAdoption placeholder; on Hetzner the cloud-conditional `path`
//     also pulls in the native provider-hcloud overlay). The Crossplane
//     PROVIDER PACKAGE image pull is a day-2 surface and MUST NOT gate
//     Phase-1 convergence (the sacred-thin cloud-init mandate + the #4049
//     cold-image-pull lesson: a slow ghcr/upbound pull would otherwise
//     wedge the whole Sovereign for the 5m timeout each cycle). Nothing
//     health-gating dependsOn it; the placeholder CloudAdoption applies
//     immediately and the real-id claims are server-side-applied
//     post-handover (handler/post_handover_adoption_apply.go). So `wait:
//     false` is correct — the apply must succeed, the package health must
//     not block.
//
// Only bootstrap-kit remains health-gating with `wait: true` (it aggregates
// the ~50 leaf-app HRs' Ready states), so exactly 1 `wait: true` is the
// load-bearing assertion below.
func TestKustomizationTimeout_WaitTrueRetained(t *testing.T) {
	tpl := readCloudInit(t)

	// We expect at least 1 `wait: true` line (bootstrap-kit). Cheap
	// presence-count rather than per-name extraction; combined with the
	// timeout test above, a missing `wait: true` would mean the count
	// drops below 1. (infrastructure-config moved to wait:false in #4212 —
	// see the allowlist below — so the prior count of 2 is now 1.)
	const want = "wait: true"
	count := strings.Count(tpl, want)
	if count < 1 {
		t.Errorf("expected `wait: true` on the bootstrap-kit health-gating Kustomization (got %d occurrences). Issue #492 fix is Option A (timeout reduction); Option B (wait: false) would break the dependsOn chain.", count)
	}

	// NO operative `wait: false` EXCEPT on the async-applier tiers below.
	// Track the owning Kustomization via its metadata.name — recognised as
	// the `name:` line whose previous non-comment line is `metadata:` (so
	// the `sourceRef.name` / `dependsOn[].name` lines that also say "name:"
	// don't shadow it). The allowlist is name-scoped, not a blanket
	// relaxation. As with the 30m guard, comments are fine.
	allowWaitFalseFor := map[string]bool{
		"bootstrap-kit-crs":        true, // #3804 / Refs #3642 — async CR applier
		"sovereign-tls":            true, // #3845 / Refs #3888 — async wildcard-TLS applier
		"infrastructure-providers": true, // #4521 — Crossplane Provider-install LAYER 1 (package pull off the critical path)
		"infrastructure-config":    true, // #4212 / #4521 — Crossplane object-model config LAYER 2 (dependsOn infrastructure-providers)
	}
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
			if allowWaitFalseFor[currentName] {
				prevNonComment = trimmed
				continue // intentional async applier tier (#3804/#3642, #3845/#3888)
			}
			t.Errorf("line %d carries operative `wait: false` on Kustomization %q — issue #492 fix is Option A (timeout reduction), NOT Option B. Flipping wait breaks the `dependsOn: bootstrap-kit` Ready contract (only bootstrap-kit-crs + sovereign-tls may use wait:false):\n  %s",
				i+1, currentName, line)
		}
		prevNonComment = trimmed
	}
}

// TestKustomizationTimeout_CommittedInfrastructureConfig is the #4521 guard:
// the `infrastructure-config` LAYER-2 Flux Kustomization was evicted OUT of the
// byte-capped cloud-init into two COMMITTED Flux Kustomization CRs (the Huawei
// cloud-agnostic variant + the Hetzner overlay variant). Their `timeout: 5m`
// protection (issue #492 — release the revision lock at GitRepository-poll
// cadence) must travel WITH them; this test asserts both committed CRs still
// carry `timeout: 5m`, so the move did not silently drop the guard.
func TestKustomizationTimeout_CommittedInfrastructureConfig(t *testing.T) {
	// repoRoot resolves the same way modulePath() does (six dirs up from the
	// provisioner package), then down into clusters/_template.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", "..", "..", ".."))
	files := []string{
		filepath.Join(repoRoot, "clusters", "_template", "infrastructure", "providers", "infrastructure-config-kustomization.yaml"),
		filepath.Join(repoRoot, "clusters", "_template", "infrastructure", "providers", "hetzner", "infrastructure-config-kustomization.yaml"),
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("#4521: committed infrastructure-config Kustomization CR missing (%s): %v", f, err)
		}
		body := string(raw)
		if !strings.Contains(body, "timeout: 5m") {
			t.Errorf("#4521: committed infrastructure-config CR %s must carry `timeout: 5m` (issue #492 revision-lock release); the timeout guard was dropped when LAYER 2 moved off cloud-init", f)
		}
		// LAYER-2 must dependsOn LAYER-1 (the #4521 ordering fix) so the
		// ProviderConfig is dry-run only after the provider registers its CRD.
		if !strings.Contains(body, "- name: infrastructure-providers") {
			t.Errorf("#4521: committed infrastructure-config CR %s must `dependsOn: [infrastructure-providers]` (the atomic-dry-run ordering fix)", f)
		}
	}
}

// gitRepositoryCloneTimeoutFloor / Ceiling — the same bounds
// scripts/check-gitrepository-clone-timeout.sh enforces, restated here so the
// Go suite fails on its own without the shell guard present.
//
// Floor: a depth-1 clone of this repo measured 651s (10m51s) on hw297 region-b
// on 2026-08-14 — `13:41:33Z building artifact` -> `13:52:24Z ready=True`.
// Ceiling: helmwatch's DefaultWatchTimeout (60m); a clone budget larger than
// the Phase-1 watch window turns an unreachable origin into one silent endless
// attempt instead of a failure the watch can classify.
const (
	gitRepositoryCloneTimeoutFloor   = 15 * time.Minute
	gitRepositoryCloneTimeoutCeiling = 60 * time.Minute
)

// TestGitRepositoryCloneTimeout_HasHeadroom is the issue #6336 guard: the
// bootstrap `GitRepository/openova` cloud-init writes MUST declare an operative
// `spec.timeout`, and it must sit inside [floor, ceiling].
//
// Absence is the defect, not a neutral default. With the field absent, the
// source-controller v1.4.1 CRD default of 60s applies; on hw297 region-b every
// reconcile then died with `failed to checkout and determine revision: unable
// to clone 'https://github.com/openova-io/openova': context deadline exceeded`,
// so bootstrap-kit / bootstrap-kit-crs / sovereign-tls all sat on `Source
// artifact not found`, `kubectl get helmrelease -A` returned nothing, and the
// prov froze at `phase1-watching` with 0 HelmReleases.
//
// There is no shallow-clone lever to reach for instead: source-controller
// v1.4.1 already sets `repository.CloneConfig{... ShallowClone: true}`
// unconditionally, and `source.toolkit.fluxcd.io/v1` exposes no depth /
// sparse-checkout field. `spec.ignore` filters at artifact-build time, AFTER
// checkout, so it never shrinks the transfer either. The deadline is the only
// knob, which is precisely why it must not drift back down.
func TestGitRepositoryCloneTimeout_HasHeadroom(t *testing.T) {
	tpl := readCloudInit(t)

	var found []operativeTimeout
	for _, ot := range classifyTimeouts(tpl) {
		if ot.kind == "GitRepository" {
			found = append(found, ot)
		}
	}

	if len(found) != 1 {
		t.Fatalf("#6336: expected exactly 1 operative GitRepository `timeout:` in the cloud-init template, got %d: %+v\n"+
			"Zero means the 60s CRD default applies and a fresh Sovereign converges to ZERO HelmReleases.",
			len(found), found)
	}
	got := found[0]

	if got.name != "openova" {
		t.Errorf("#6336: the timed GitRepository is named %q, want \"openova\" (the bootstrap source every Kustomization sourceRefs)", got.name)
	}

	raw := strings.TrimSpace(strings.TrimPrefix(got.text, "timeout:"))
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("#6336: GitRepository/%s timeout %q is not a parseable duration: %v", got.name, raw, err)
	}
	if d < gitRepositoryCloneTimeoutFloor {
		t.Errorf("#6336: GitRepository/%s timeout %v is below the %v floor. A depth-1 clone of this repo measured 10m51s on hw297; anything under the floor reproduces the 0-HelmRelease wedge.\n"+
			"NOTE: the 5m of issue #492 is the kustomize-controller REVISION-LOCK budget. A GitRepository holds no revision lock — do not harmonise this down to match it.",
			got.name, d, gitRepositoryCloneTimeoutFloor)
	}
	if d > gitRepositoryCloneTimeoutCeiling {
		t.Errorf("#6336: GitRepository/%s timeout %v exceeds the %v ceiling (helmwatch DefaultWatchTimeout) — an unreachable origin would never surface inside the Phase-1 watch window.",
			got.name, d, gitRepositoryCloneTimeoutCeiling)
	}
}
