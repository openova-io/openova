package suite

import (
	"strings"
	"testing"
)

// fakeRelease is a fixture HelmRelease with its conditions.
func fakeRelease(namespace, name string, suspend bool, conds ...string) obj {
	return obj{"kind": "HelmRelease", "metadata": kubeMeta(namespace, name, "helm-controller"),
		"spec": obj{"suspend": suspend}, "status": obj{"conditions": kubeConditions(conds...)}}
}

// fakePod is a fixture pod with one container: its restart count and, when
// waiting is not empty, the reason it waits.
func fakePod(namespace, name string, restarts int, waiting string) obj {
	state := obj{"running": obj{}}
	if waiting != "" {
		state = obj{"waiting": obj{"reason": waiting}}
	}
	return obj{"metadata": kubeMeta(namespace, name), "spec": obj{"containers": []obj{{"name": "main", "image": "registry.example/app:1"}}},
		"status": obj{"phase": "Running", "containerStatuses": []obj{{"name": "main", "restartCount": restarts, "state": state,
			"imageID": "registry.example/app@sha256:" + strings.Repeat("a", 64)}}}}
}

func init() {
	organizations := kubeList(obj{"metadata": kubeMeta("", "acme")})
	healthy := kubeList(
		fakeRelease("flux-system", "bp-cilium", false, "Ready", "True"),
		fakeRelease("flux-system", "bp-velero", true, "Ready", "False"), // suspended: counted, not judged
		fakeRelease("acme", "shop", false, "Ready", "True"))
	pods := kubeList(
		fakePod("kube-system", "cilium-1", 0, ""),
		fakePod("catalyst-system", "catalyst-api-1", 3, ""), // three restarts pass
		fakePod("acme", "shop-1", 9, "CrashLoopBackOff"))    // an Organization namespace is not judged
	good := map[string]string{"* " + hrResource: healthy, "* namespaces": organizations, "* pods": pods}
	with := func(question, answer string) map[string]string {
		out := map[string]string{question: answer}
		for k, v := range good {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
		return out
	}
	fakeAnswers["releases-healthy"] = good
	fakeAnswers["releases-one-not-ready"] = with("b "+hrResource, kubeList(
		fakeRelease("flux-system", "bp-cilium", false, "Ready", "True"),
		fakeRelease("flux-system", "bp-harbor", false, "Ready", "False")))
	fakeAnswers["releases-one-unknown"] = with("b "+hrResource, kubeList(fakeRelease("flux-system", "bp-harbor", false, "Ready", "Unknown")))
	fakeAnswers["releases-one-stalled"] = with("b "+hrResource, kubeList(fakeRelease("flux-system", "bp-harbor", false, "Ready", "True", "Stalled", "True")))
	fakeAnswers["releases-no-condition"] = with("b "+hrResource, kubeList(fakeRelease("flux-system", "bp-harbor", false)))
	fakeAnswers["releases-none"] = with("b "+hrResource, kubeList())
	fakeAnswers["releases-crash-loop"] = with("b pods", kubeList(fakePod("gitea", "gitea-0", 2, "CrashLoopBackOff")))
	fakeAnswers["releases-four-restarts"] = with("b pods", kubeList(fakePod("gitea", "gitea-0", 4, "")))
	fakeAnswers["releases-no-pods"] = with("b pods", kubeList(fakePod("acme", "shop-1", 0, "")))
	fakeAnswers["releases-region-b-unreachable"] = map[string]string{"a " + hrResource: healthy, "a namespaces": organizations, "a pods": pods}
}

func TestReleasesCheck(t *testing.T) {
	if _, ok := registry[releasesID]; !ok || releasesID != "FND-G04" {
		t.Fatalf("check %s is not registered", releasesID)
	}
	for scenario, want := range map[string]string{
		"releases-healthy":              "",
		"releases-one-not-ready":        "1 of 2 HelmReleases are not Ready: flux-system/bp-harbor is Ready=False (Fixture)",
		"releases-one-unknown":          "flux-system/bp-harbor is Ready=Unknown",
		"releases-one-stalled":          "flux-system/bp-harbor is stalled",
		"releases-no-condition":         "flux-system/bp-harbor has no Ready condition",
		"releases-none":                 "no HelmRelease was found: nothing was measured",
		"releases-crash-loop":           "1 containers are in CrashLoopBackOff: gitea/gitea-0/main",
		"releases-four-restarts":        "1 containers have more than 3 restarts: gitea/gitea-0/main (4)",
		"releases-no-pods":              "no pod was found in the platform namespaces: nothing was measured",
		"releases-region-b-unreachable": "[region-b] kubectl get " + hrResource,
	} {
		r := runCheck(t, scenario, releasesCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", scenario, got)
		case want != "" && (got.Status != Fail || !strings.Contains(got.Reason, want)):
			t.Errorf("%s: want FAIL with %q, got %+v", scenario, want, got)
		}
		if len(r.run.Steps) != 6 {
			t.Errorf("%s: want three steps per region, got %d steps", scenario, len(r.run.Steps))
		}
	}
	// The evidence: the suspended release is counted, the restarts above 0 are listed.
	r := runCheck(t, "releases-healthy", releasesCheck{}, &CheckContext{})
	first, third := r.run.Steps[0].Evidence, r.run.Steps[2].Evidence
	if first["releases"] != 3 || first["suspended"] != 1 || first["ready"] != 2 {
		t.Errorf("the release step does not hold the evidence: %+v", first)
	}
	if restarts := third["restarts"].(map[string]int); len(restarts) != 1 || restarts["catalyst-system/catalyst-api-1/main"] != 3 || third["pods"] != 2 {
		t.Errorf("the restart step does not hold the evidence: %+v", third)
	}
	// A region that cannot be read fails every step of that region.
	if got := failedSteps(runCheck(t, "releases-region-b-unreachable", releasesCheck{}, &CheckContext{})); len(got) != 3 {
		t.Errorf("want 3 failed steps for the region that cannot be read, got %v", got)
	}
}
