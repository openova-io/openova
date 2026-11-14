// capabilities_test.go — coverage for the plan→MCP-capabilities map +
// ResolveCapabilities. The tenant orchestrator carries an exact
// duplicate of CapabilitiesForPlan so the assertions here pin the
// shape both call-sites must agree on.

package sandboxapi

import (
	"sort"
	"testing"
)

// containsAll returns true when every entry in `want` appears in `have`.
func containsAll(have, want []string) bool {
	idx := make(map[string]struct{}, len(have))
	for _, h := range have {
		idx[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := idx[w]; !ok {
			return false
		}
	}
	return true
}

func TestCapabilitiesForPlan_Shape(t *testing.T) {
	free := CapabilitiesForPlan(PlanSandboxFree)
	pro := CapabilitiesForPlan(PlanSandboxPro)
	ent := CapabilitiesForPlan(PlanSandboxEnt)

	// Every plan carries the read-only Free baseline (the agent can
	// always discover what it sees).
	freeBaseline := []string{
		"gitea.repo.list", "gitea.repo.get", "gitea.pr.list", "gitea.pr.get",
		"k8s.read.get", "k8s.read.list",
		"sandbox.session.*",
		"rag.search", "skills.list", "skills.get",
	}
	for _, plan := range [][]string{free, pro, ent} {
		if !containsAll(plan, freeBaseline) {
			t.Errorf("plan missing Free baseline: %v", plan)
		}
	}

	// Free MUST NOT carry any write or per-Sandbox provisioning grant.
	forbiddenOnFree := []string{
		"sandbox.db.*", "sandbox.storage.*", "sandbox.preview.*",
		"sandbox.deploy.staging", "sandbox.deploy.production",
		"sandbox.stripe.*", "marketplace.*",
		"flux.reconcile", "flux.suspend", "flux.resume",
		"gitea.pr.create", "gitea.pr.merge", "gitea.issue.*",
	}
	freeSet := make(map[string]struct{}, len(free))
	for _, c := range free {
		freeSet[c] = struct{}{}
	}
	for _, bad := range forbiddenOnFree {
		if _, ok := freeSet[bad]; ok {
			t.Errorf("Free plan unexpectedly grants %q", bad)
		}
	}

	// Pro extends Free with the per-Sandbox provisioning surface.
	proExtras := []string{
		"sandbox.db.*", "sandbox.storage.*", "sandbox.preview.*",
		"sandbox.auth.*", "sandbox.secrets.*", "marketplace.*",
		"flux.status",
	}
	if !containsAll(pro, proExtras) {
		t.Errorf("Pro plan missing extras %v: %v", proExtras, pro)
	}
	// Pro MUST NOT carry Ent-only deploy/stripe/flux-mutation grants.
	forbiddenOnPro := []string{
		"sandbox.deploy.staging", "sandbox.deploy.production",
		"sandbox.stripe.*",
		"flux.reconcile", "flux.suspend", "flux.resume",
		"gitea.pr.create", "gitea.pr.merge", "gitea.issue.*",
	}
	proSet := make(map[string]struct{}, len(pro))
	for _, c := range pro {
		proSet[c] = struct{}{}
	}
	for _, bad := range forbiddenOnPro {
		if _, ok := proSet[bad]; ok {
			t.Errorf("Pro plan unexpectedly grants %q", bad)
		}
	}

	// Ent extends Pro with the deploy / stripe / flux-mutation / gitea-write surface.
	entExtras := []string{
		"sandbox.deploy.staging", "sandbox.deploy.production",
		"sandbox.deploy.status", "sandbox.deploy.rollback",
		"sandbox.stripe.*",
		"flux.reconcile", "flux.suspend", "flux.resume",
		"gitea.pr.create", "gitea.pr.merge", "gitea.issue.*",
	}
	if !containsAll(ent, entExtras) {
		t.Errorf("Ent plan missing extras %v: %v", entExtras, ent)
	}

	// Unknown plan slug falls through to Free.
	unknown := CapabilitiesForPlan("sandbox-xxl")
	sort.Strings(unknown)
	sortedFree := append([]string(nil), free...)
	sort.Strings(sortedFree)
	if len(unknown) != len(sortedFree) {
		t.Errorf("unknown plan length=%d want %d", len(unknown), len(sortedFree))
	}
	for i := range unknown {
		if unknown[i] != sortedFree[i] {
			t.Errorf("unknown[%d]=%q want %q", i, unknown[i], sortedFree[i])
		}
	}

	// Empty slug behaves identically.
	if got := CapabilitiesForPlan(""); !containsAll(got, freeBaseline) {
		t.Errorf("empty plan missing free baseline: %v", got)
	}
}

func TestCapabilitiesForPlan_ReturnsFreshSlice(t *testing.T) {
	// Mutating the returned slice MUST NOT poison the package-level
	// template — callers (controller, orchestrator) trim or append in
	// place.
	first := CapabilitiesForPlan(PlanSandboxPro)
	first[0] = "MUTATED"
	second := CapabilitiesForPlan(PlanSandboxPro)
	if second[0] == "MUTATED" {
		t.Fatalf("CapabilitiesForPlan leaks a shared backing array: second=%v", second)
	}
}

func TestResolveCapabilities_PrefersSpecOverride(t *testing.T) {
	spec := &SandboxSpec{
		PlanID:       PlanSandboxFree,
		Capabilities: []string{"sandbox.db.*", "k8s.read.list", " ", ""},
	}
	got := ResolveCapabilities(spec)
	// Whitespace + empty entries are stripped.
	for _, c := range got {
		if c == "" || c == " " {
			t.Errorf("ResolveCapabilities leaked empty entry: %v", got)
		}
	}
	// Spec override wins over the plan default.
	if len(got) != 2 || got[0] != "sandbox.db.*" || got[1] != "k8s.read.list" {
		t.Errorf("ResolveCapabilities did not honour spec override: %v", got)
	}
}

func TestResolveCapabilities_FallsBackToPlan(t *testing.T) {
	spec := &SandboxSpec{PlanID: PlanSandboxPro}
	got := ResolveCapabilities(spec)
	if !containsAll(got, []string{"sandbox.db.*", "sandbox.storage.*"}) {
		t.Errorf("fallback to plan default missing Pro grants: %v", got)
	}
}

func TestResolveCapabilities_NilSpec(t *testing.T) {
	got := ResolveCapabilities(nil)
	if !containsAll(got, []string{"gitea.repo.list", "rag.search"}) {
		t.Errorf("nil spec did not return Free baseline: %v", got)
	}
}

func TestResolveCapabilities_EmptySpecCapsFallsBack(t *testing.T) {
	// Empty spec.Capabilities (e.g. only whitespace) MUST NOT lock out
	// the bearer — fall through to plan default.
	spec := &SandboxSpec{PlanID: PlanSandboxEnt, Capabilities: []string{"  ", ""}}
	got := ResolveCapabilities(spec)
	if !containsAll(got, []string{"sandbox.deploy.production"}) {
		t.Errorf("whitespace-only override did not fall back to plan default: %v", got)
	}
}
