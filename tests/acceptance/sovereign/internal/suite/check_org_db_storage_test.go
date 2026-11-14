package suite

import (
	"strings"
	"testing"
)

// Fixtures of check CLD-G09.

func orgDBRelease(namespace, name, target, component string, side bool) obj {
	meta := kubeMeta(namespace, name)
	labels := obj{orgDBComponentLabel: component}
	if side {
		labels[orgDBSideLabel] = "primary"
	}
	meta["labels"] = labels
	return obj{"metadata": meta, "spec": obj{"targetNamespace": target}}
}

func orgDBVolume(namespace, name, phase, class string, cnpg bool) obj {
	meta := kubeMeta(namespace, name)
	if cnpg {
		meta["labels"] = obj{orgDBClusterLabel: "cnpg-pair-primary"}
	}
	return obj{"metadata": meta, "spec": obj{"storageClassName": class}, "status": obj{"phase": phase}}
}

func orgDBVariables(class string) string {
	data := obj{"PROVIDER": "local"}
	if class != "" {
		data[orgDBClassKey] = class
	}
	return kubeList(obj{"metadata": kubeMeta("flux-system", "sovereign-variables"), "data": data})
}

func init() {
	good := map[string]string{
		// Region a holds both releases of Organization acme and the platform's
		// own slot-16b release, which has no side label and is not judged.
		"a " + hrResource: kubeList(
			orgDBRelease("acme", "bp-cnpg-pair-primary", "acme", orgDBComponent, true),
			orgDBRelease("flux-system", "bp-cnpg-pair-replica", "acme", orgDBComponent, true),
			orgDBRelease("flux-system", "bp-cnpg-pair", "cnpg", orgDBComponent, false)),
		// Another component with a side label is not a database release.
		"b " + hrResource:          kubeList(orgDBRelease("beta", "bp-other", "beta", "other", true)),
		"a configmaps flux-system": orgDBVariables("durable-ssd"),
		"b configmaps flux-system": orgDBVariables("durable-ssd"),
		"a " + orgDBVolumes + " acme": kubeList(
			orgDBVolume("acme", "cnpg-pair-primary-1", "Bound", "durable-ssd", true),
			orgDBVolume("acme", "cnpg-pair-primary-2", "Bound", "durable-ssd", true),
			// Not a database volume: no cnpg.io/cluster label, not judged.
			orgDBVolume("acme", "uploads", "Pending", "other", false)),
		"b " + orgDBVolumes + " acme": kubeList(
			orgDBVolume("acme", "cnpg-pair-replica-1", "Bound", "durable-ssd", true)),
	}
	with := func(changes map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range good {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
			if v == "" {
				delete(out, k) // nothing answers this question
			}
		}
		return out
	}
	fakeAnswers["org-db-good"] = good
	fakeAnswers["org-db-wrong-class"] = with(map[string]string{
		"b " + orgDBVolumes + " acme": kubeList(orgDBVolume("acme", "cnpg-pair-replica-1", "Bound", "hdd-cold", true))})
	fakeAnswers["org-db-pending"] = with(map[string]string{
		"a " + orgDBVolumes + " acme": kubeList(orgDBVolume("acme", "cnpg-pair-primary-1", "Pending", "durable-ssd", true))})
	fakeAnswers["org-db-no-class-field"] = with(map[string]string{
		"a " + orgDBVolumes + " acme": kubeList(orgDBVolume("acme", "cnpg-pair-primary-1", "Bound", "", true))})
	fakeAnswers["org-db-no-release"] = with(map[string]string{
		"a " + hrResource: kubeList(orgDBRelease("flux-system", "bp-cnpg-pair", "cnpg", orgDBComponent, false))})
	fakeAnswers["org-db-unreadable-releases"] = with(map[string]string{"b " + hrResource: ""})
	fakeAnswers["org-db-no-variable"] = with(map[string]string{"b configmaps flux-system": orgDBVariables("")})
	fakeAnswers["org-db-no-volume"] = with(map[string]string{
		"a " + orgDBVolumes + " acme": kubeList(orgDBVolume("acme", "uploads", "Bound", "durable-ssd", false)),
		"b " + orgDBVolumes + " acme": kubeList()})
}

func TestOrgDBStorageCheck(t *testing.T) {
	r := runCheck(t, "org-db-good", orgDBStorageCheck{}, &CheckContext{RunIn: RunInCloud})
	if got := failedSteps(r); len(got) != 0 {
		t.Fatalf("a Sovereign whose database volumes are Bound with its class must pass, failed: %v", got)
	}
	ids := []string{}
	for _, s := range r.run.Steps {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, " "); got != "CLD-G09.1 CLD-G09.2 CLD-G09.3 CLD-G09.4" {
		t.Fatalf("steps %s, want CLD-G09.1 to CLD-G09.4", got)
	}
	if got := r.run.Steps[0].Evidence["namespaces"]; strings.Join(got.([]string), " ") != "acme" {
		t.Errorf("the slot-16b release must not be judged: namespaces %v, want [acme]", got)
	}
	if got := r.run.Steps[1].Evidence["volumes"].([]string); len(got) != 2 {
		t.Errorf("region a: a PVC without the label %s must not be judged: %v", orgDBClusterLabel, got)
	}
	if got := r.run.Steps[3].Evidence["judged"]; got != 3 {
		t.Errorf("judged %v volumes, want 3", got)
	}
}

func TestOrgDBStorageCheckFails(t *testing.T) {
	for scenario, want := range map[string]string{
		"org-db-wrong-class":         "acme/cnpg-pair-replica-1 Bound hdd-cold",
		"org-db-pending":             "acme/cnpg-pair-primary-1 Pending durable-ssd",
		"org-db-no-class-field":      "acme/cnpg-pair-primary-1 Bound ",
		"org-db-no-release":          "no per-Organization database exists, so nothing was measured",
		"org-db-unreadable-releases": "cannot reach",
		"org-db-no-variable":         "have no value for SOVEREIGN_CNPG_STORAGE_CLASS",
		"org-db-no-volume":           "hold no PVC with the label cnpg.io/cluster in any region: nothing was measured",
	} {
		r := runCheck(t, scenario, orgDBStorageCheck{}, &CheckContext{RunIn: RunInCloud})
		got := failedSteps(r)
		if len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s: want exactly one failed step that says %q, got %q", scenario, want, got)
		}
		if res := r.run.Checks[0]; res.Status != Fail {
			t.Errorf("%s: the check is %s, want FAIL", scenario, res.Status)
		}
	}
}
