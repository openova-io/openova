package suite

import (
	"strings"
	"testing"
)

// fakeCrossplaneClaim is a fixture CloudAdoption claim. composite is the value
// of spec.resourceRef.name; "" leaves spec.resourceRef out.
func fakeCrossplaneClaim(name, composite string, conds ...string) obj {
	spec := obj{"parameters": obj{"resourceKind": "server", "resourceId": "id-" + name}}
	if composite != "" {
		spec["resourceRef"] = obj{"apiVersion": "compose.openova.io/v1alpha1", "kind": "XCloudAdoption", "name": composite}
	}
	return obj{"kind": "CloudAdoption", "metadata": obj{"namespace": "crossplane-system", "name": name},
		"spec": spec, "status": obj{"conditions": kubeConditions(conds...)}}
}

// fakeCrossplaneWorkspace is a fixture Workspace. composite is the value of
// the label crossplane.io/composite; "" gives a Workspace without labels.
func fakeCrossplaneWorkspace(name, composite string, conds ...string) obj {
	meta := obj{"name": name}
	if composite != "" {
		meta["labels"] = obj{crossplaneCompositeLabel: composite, "crossplane.io/claim-name": "not-read"}
	}
	return obj{"kind": "Workspace", "metadata": meta, "status": obj{"conditions": kubeConditions(conds...)}}
}

// fakeCrossplanePackage is a fixture provider or function with its package reference.
func fakeCrossplanePackage(name, pkg string) obj {
	return obj{"metadata": obj{"name": name}, "spec": obj{"package": pkg},
		"status": obj{"conditions": kubeConditions("Installed", "True", "Healthy", "True")}}
}

// The primary region is region a, the first of the run; region b is a
// follower. A follower holds no claim and serves no Workspace kind: the
// healthy scenario answers its claim list empty and leaves its Workspace
// question unanswered, so a check that asked it would fail.
func init() {
	ok := []string{"Synced", "True", "Ready", "True"}
	claims := kubeList(
		fakeCrossplaneClaim("adopt-lb", "adopt-lb-x7k2p", ok...),
		fakeCrossplaneClaim("adopt-server", "adopt-server-q9w4m", ok...))
	workspaces := kubeList(
		fakeCrossplaneWorkspace("adopt-id-adopt-lb", "adopt-lb-x7k2p", ok...),
		fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server-q9w4m", ok...),
		fakeCrossplaneWorkspace("base", "", "Synced", "False", "Ready", "False"),                 // composed by no claim: not judged
		fakeCrossplaneWorkspace("other", "xnodepool-abc12", "Synced", "False", "Ready", "False")) // another composite: not judged
	providers := kubeList(fakeCrossplanePackage("provider-opentofu", "xpkg.upbound.io/upbound/provider-opentofu:v1.1.3"))
	functions := kubeList(fakeCrossplanePackage("function-patch-and-transform", "harbor.example/crossplane/function-patch-and-transform:v0.8.2"))
	good := map[string]string{
		"a " + crossplaneClaimResource: claims, "a " + crossplaneWorkspaceResource: workspaces,
		"b " + crossplaneClaimResource:    kubeList(),
		"* " + crossplaneProviderResource: providers, "* " + crossplaneFunctionResource: functions,
	}
	with := func(question, answer string) map[string]string {
		out := map[string]string{question: answer}
		for k, v := range good {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
		return out
	}
	claimA := func(c obj) map[string]string {
		return with("a "+crossplaneClaimResource, kubeList(fakeCrossplaneClaim("adopt-lb", "adopt-lb-x7k2p", ok...), c))
	}
	workspacesA := func(list ...obj) map[string]string {
		return with("a "+crossplaneWorkspaceResource, kubeList(append([]obj{fakeCrossplaneWorkspace("adopt-id-adopt-lb", "adopt-lb-x7k2p", ok...)}, list...)...))
	}
	fakeAnswers["crossplane-healthy"] = good
	fakeAnswers["crossplane-claim-not-synced"] = claimA(fakeCrossplaneClaim("adopt-server", "adopt-server-q9w4m", "Synced", "False", "Ready", "True"))
	fakeAnswers["crossplane-claim-not-ready"] = claimA(fakeCrossplaneClaim("adopt-server", "adopt-server-q9w4m", "Synced", "True", "Ready", "False"))
	fakeAnswers["crossplane-claim-no-conditions"] = claimA(fakeCrossplaneClaim("adopt-server", "adopt-server-q9w4m"))
	fakeAnswers["crossplane-claim-no-composite"] = claimA(fakeCrossplaneClaim("adopt-server", "", ok...))
	fakeAnswers["crossplane-no-workspace"] = workspacesA()
	fakeAnswers["crossplane-two-workspaces"] = workspacesA(
		fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server-q9w4m", ok...),
		fakeCrossplaneWorkspace("adopt-id-adopt-server-2", "adopt-server-q9w4m", ok...))
	fakeAnswers["crossplane-workspace-by-claim-name"] = workspacesA(fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server", ok...))
	fakeAnswers["crossplane-workspace-not-synced"] = workspacesA(fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server-q9w4m", "Synced", "False", "Ready", "True"))
	fakeAnswers["crossplane-workspace-not-ready"] = workspacesA(fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server-q9w4m", "Synced", "True", "Ready", "False"))
	fakeAnswers["crossplane-workspace-no-conditions"] = workspacesA(fakeCrossplaneWorkspace("adopt-id-adopt-server", "adopt-server-q9w4m"))
	// The placeholder claim the template shipped until it was removed: its
	// composite never composes the Workspace.
	fakeAnswers["crossplane-placeholder-claim"] = claimA(fakeCrossplaneClaim("adoption-placeholder", "adoption-placeholder-njsdd", "Synced", "True", "Ready", "False"))
	fakeAnswers["crossplane-no-claim"] = with("a "+crossplaneClaimResource, kubeList())
	fakeAnswers["crossplane-claims-unread"] = map[string]string{
		"b " + crossplaneClaimResource: kubeList(), "a " + crossplaneWorkspaceResource: workspaces,
		"* " + crossplaneProviderResource: providers, "* " + crossplaneFunctionResource: functions,
	}
	fakeAnswers["crossplane-workspaces-unread"] = map[string]string{
		"a " + crossplaneClaimResource: claims, "b " + crossplaneClaimResource: kubeList(),
		"* " + crossplaneProviderResource: providers, "* " + crossplaneFunctionResource: functions,
	}
	fakeAnswers["crossplane-packages-unread"] = map[string]string{
		"a " + crossplaneClaimResource: claims, "a " + crossplaneWorkspaceResource: workspaces,
		"b " + crossplaneClaimResource: kubeList(),
	}
	// The follower region of the run of 15 November 2026: the claim kind is
	// served (bp-crossplane-claims runs in every region), no claim, and no
	// Workspace kind.
	fakeAnswers["crossplane-follower-no-workspace-kind"] = with("b "+crossplaneWorkspaceResource, fakeNotServed)
	// A follower that does not serve the claim kind holds no claim either.
	fakeAnswers["crossplane-follower-claim-kind-not-served"] = with("b "+crossplaneClaimResource, fakeNotServed)
	// A claim in a follower observes a second time what the primary observes.
	fakeAnswers["crossplane-follower-holds-claim"] = with("b "+crossplaneClaimResource, kubeList(fakeCrossplaneClaim("adopt-lb", "adopt-lb-x7k2p", ok...)))
	// A follower whose claims cannot be read is not measured.
	fakeAnswers["crossplane-follower-claims-unread"] = map[string]string{
		"a " + crossplaneClaimResource: claims, "a " + crossplaneWorkspaceResource: workspaces,
		"* " + crossplaneProviderResource: providers, "* " + crossplaneFunctionResource: functions,
	}
}

func TestCrossplaneCheck(t *testing.T) {
	if _, ok := registry[crossplaneID]; !ok || crossplaneID != "CLD-G06" {
		t.Fatalf("check %s is not registered", crossplaneID)
	}
	for scenario, tc := range map[string]struct {
		want     string // "" is PASS
		failStep int    // the index of the one step that fails
	}{
		"crossplane-healthy":                        {"", 0},
		"crossplane-packages-unread":                {"", 0},
		"crossplane-follower-no-workspace-kind":     {"", 0},
		"crossplane-follower-claim-kind-not-served": {"", 0},
		"crossplane-claim-not-synced":               {"claim crossplane-system/adopt-server is Synced=False (Fixture)", 0},
		"crossplane-claim-not-ready":                {"claim crossplane-system/adopt-server is Ready=False (Fixture)", 0},
		"crossplane-claim-no-conditions":            {"claim crossplane-system/adopt-server has no Synced condition and has no Ready condition", 0},
		"crossplane-claim-no-composite":             {"claim crossplane-system/adopt-server has no spec.resourceRef.name", 0},
		"crossplane-no-workspace":                   {"claim crossplane-system/adopt-server composes 0 Workspaces with crossplane.io/composite=adopt-server-q9w4m, want 1", 0},
		"crossplane-two-workspaces":                 {"claim crossplane-system/adopt-server composes 2 Workspaces with crossplane.io/composite=adopt-server-q9w4m, want 1", 0},
		"crossplane-workspace-by-claim-name":        {"claim crossplane-system/adopt-server composes 0 Workspaces", 0},
		"crossplane-workspace-not-synced":           {"Workspace adopt-id-adopt-server of claim crossplane-system/adopt-server is Synced=False (Fixture)", 0},
		"crossplane-workspace-not-ready":            {"Workspace adopt-id-adopt-server of claim crossplane-system/adopt-server is Ready=False (Fixture)", 0},
		"crossplane-workspace-no-conditions":        {"Workspace adopt-id-adopt-server of claim crossplane-system/adopt-server has no Synced condition and has no Ready condition", 0},
		"crossplane-placeholder-claim":              {"claim crossplane-system/adoption-placeholder composes 0 Workspaces with crossplane.io/composite=adoption-placeholder-njsdd, want 1", 0},
		"crossplane-no-claim":                       {"no CloudAdoption claim was found: nothing was measured", 0},
		"crossplane-claims-unread":                  {"[region-a] kubectl get " + crossplaneClaimResource, 0},
		"crossplane-workspaces-unread":              {"[region-a] kubectl get " + crossplaneWorkspaceResource, 0},
		"crossplane-follower-holds-claim":           {"follower region region-b holds 1 CloudAdoption claims (crossplane-system/adopt-lb): the adoption seam is the primary region's alone", 1},
		"crossplane-follower-claims-unread":         {"[region-b] kubectl get " + crossplaneClaimResource, 1},
	} {
		r := runCheck(t, scenario, crossplaneCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case tc.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", scenario, got)
		case tc.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, tc.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", scenario, tc.want, got)
		}
		if len(r.run.Steps) != 2 {
			t.Errorf("%s: want one step per region, got %d steps", scenario, len(r.run.Steps))
			continue
		}
		if tc.want != "" {
			if failed := failedSteps(r); len(failed) != 1 || r.run.Steps[tc.failStep].Status != Fail {
				t.Errorf("%s: want only step %d to fail, got %v", scenario, tc.failStep, failed)
			}
		}
	}
	// The evidence: the claims, the Workspaces, which Workspace each claim
	// composes, and the package references, which are not judged.
	r := runCheck(t, "crossplane-healthy", crossplaneCheck{}, &CheckContext{})
	ev := r.run.Steps[0].Evidence
	composed, _ := ev["composed"].(map[string]string)
	packages, _ := ev["packages"].(map[string]string)
	if ev["claims"] != 2 || ev["workspaces"] != 4 || len(composed) != 2 || composed["crossplane-system/adopt-lb"] != "adopt-id-adopt-lb" {
		t.Errorf("the step does not hold the claim evidence: %+v", ev)
	}
	if len(packages) != 2 || packages["provider/provider-opentofu"] != "xpkg.upbound.io/upbound/provider-opentofu:v1.1.3" ||
		packages["function/function-patch-and-transform"] != "harbor.example/crossplane/function-patch-and-transform:v0.8.2" {
		t.Errorf("the step does not hold the package references: %+v", ev["packages"])
	}
	if _, unread := ev["packages_unread"]; unread {
		t.Errorf("a healthy run records unread packages: %+v", ev)
	}
	// The follower's step records its claim count and its packages.
	if fev := r.run.Steps[1].Evidence; fev["claims"] != 0 || len(fev["packages"].(map[string]string)) != 2 {
		t.Errorf("the follower step does not hold its evidence: %+v", fev)
	}
	// Packages that cannot be read are recorded, and the step still passes.
	r = runCheck(t, "crossplane-packages-unread", crossplaneCheck{}, &CheckContext{})
	if unread, _ := r.run.Steps[1].Evidence["packages_unread"].([]string); len(unread) != 2 {
		t.Errorf("want the two unread package lists in the evidence, got %+v", r.run.Steps[1].Evidence)
	}
	// A follower that does not serve the claim kind says so.
	r = runCheck(t, "crossplane-follower-claim-kind-not-served", crossplaneCheck{}, &CheckContext{})
	if served, set := r.run.Steps[1].Evidence["claims_served"]; !set || served != false {
		t.Errorf("want claims_served=false for a follower that does not serve the kind, got %+v", r.run.Steps[1].Evidence)
	}
	// The steps are numbered per region.
	if r.run.Steps[0].ID != "CLD-G06.1" || r.run.Steps[1].ID != "CLD-G06.2" {
		t.Errorf("want the steps CLD-G06.1 and CLD-G06.2, got %s and %s", r.run.Steps[0].ID, r.run.Steps[1].ID)
	}
}
