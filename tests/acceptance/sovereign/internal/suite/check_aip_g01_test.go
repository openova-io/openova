package suite

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// The image of the metering sidecar, as bp-newapi rendered it.
const meterSidecarImage = "ghcr.io/openova-io/openova/services-metering-sidecar:41e02c8"

// meterPod is a fixture Pod. Each pair of arguments is the name of a list of
// the Pod spec and the image of one container in it.
func meterPod(namespace, name string, listAndImage ...string) obj {
	spec := obj{}
	for i := 0; i+1 < len(listAndImage); i += 2 {
		list, _ := spec[listAndImage[i]].([]obj)
		spec[listAndImage[i]] = append(list, obj{"name": "c" + strconv.Itoa(i/2), "image": listAndImage[i+1]})
	}
	return obj{"metadata": kubeMeta(namespace, name), "spec": spec}
}

// meterPodIn is meterPod with the phase of the Pod's status, and a reason when
// one is given.
func meterPodIn(phase, reason, namespace, name string, listAndImage ...string) obj {
	pod := meterPod(namespace, name, listAndImage...)
	status := obj{"phase": phase}
	if reason != "" {
		status["reason"] = reason
	}
	pod["status"] = status
	return pod
}

func init() {
	clean := kubeList(
		meterPod("newapi", "bp-newapi-1", "initContainers", "busybox:1.36",
			"containers", "ghcr.io/openova-io/openova/newapi-mirror:v0.9.0"),
		meterPod("kube-system", "cilium-x", "containers", "quay.io/cilium/cilium:v1.19.3"),
	)
	fakeAnswers["meter-clean"] = map[string]string{"* pods": clean}
	fakeAnswers["meter-sidecar-in-b"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPod("acme", "bp-newapi-2", "containers", "ghcr.io/openova-io/openova/newapi-mirror:v0.9.0",
			"containers", meterSidecarImage)),
	}
	fakeAnswers["meter-other-registry"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPod("acme", "bp-newapi-3", "containers", "harbor.acme.omani.works/openova/services-metering-sidecar@sha256:259420d4")),
	}
	fakeAnswers["meter-init"] = map[string]string{
		"a pods": kubeList(meterPod("acme", "bp-newapi-4", "initContainers", meterSidecarImage, "containers", "busybox:1.36")),
		"b pods": clean,
	}
	fakeAnswers["meter-ephemeral"] = map[string]string{
		"a pods": kubeList(meterPod("acme", "bp-newapi-5", "containers", "busybox:1.36", "ephemeralContainers", meterSidecarImage)),
		"b pods": clean,
	}
	fakeAnswers["meter-no-container"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPod("acme", "empty")),
	}
	fakeAnswers["meter-no-pod"] = map[string]string{"a pods": clean, "b pods": kubeList()}
	// The measured case: a Pod evicted days before stays in phase Failed with
	// the sidecar in its spec; the Pod of the Deployment runs without it.
	fakeAnswers["meter-ended"] = map[string]string{
		"a pods": kubeList(
			meterPodIn("Failed", "Evicted", "newapi", "bp-newapi-old", "containers", "ghcr.io/openova-io/openova/newapi-mirror:v0.9.0",
				"containers", meterSidecarImage),
			meterPodIn("Succeeded", "", "acme", "bp-newapi-job", "initContainers", meterSidecarImage, "containers", "busybox:1.36"),
			meterPodIn("Running", "", "newapi", "bp-newapi-new", "containers", "ghcr.io/openova-io/openova/newapi-mirror:v0.9.0"),
		),
		"b pods": clean,
	}
	fakeAnswers["meter-running"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPodIn("Running", "", "acme", "bp-newapi-6", "containers", meterSidecarImage)),
	}
	fakeAnswers["meter-pending"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPodIn("Pending", "", "acme", "bp-newapi-7", "containers", meterSidecarImage)),
	}
	fakeAnswers["meter-unknown"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPodIn("Unknown", "NodeLost", "acme", "bp-newapi-8", "containers", meterSidecarImage)),
	}
	fakeAnswers["meter-only-ended"] = map[string]string{
		"a pods": clean,
		"b pods": kubeList(meterPodIn("Failed", "Evicted", "acme", "bp-newapi-9", "containers", "busybox:1.36")),
	}
	fakeAnswers["meter-b-unreachable"] = map[string]string{"a pods": clean}
}

func TestMeterImageCheck(t *testing.T) {
	if _, ok := registry[meterImageID]; !ok || meterImageID != "AIP-G01" {
		t.Fatalf("check %s is not registered", meterImageID)
	}
	for name, c := range map[string]struct {
		scenario string
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"no region runs the sidecar":          {"meter-clean", ""},
		"a container runs the sidecar":        {"meter-sidecar-in-b", "1 containers in region region-b run an image whose name holds \"metering-sidecar\": acme/bp-newapi-2/c1 " + meterSidecarImage},
		"the sidecar comes from another host": {"meter-other-registry", "acme/bp-newapi-3/c0 harbor.acme.omani.works/openova/services-metering-sidecar@sha256:259420d4"},
		"an init container runs the sidecar":  {"meter-init", "1 containers in region region-a run an image"},
		"an ephemeral container runs it":      {"meter-ephemeral", "acme/bp-newapi-5/c1 " + meterSidecarImage},
		"a region has pods and no container":  {"meter-no-container", "no container was found in region region-b: nothing was measured"},
		"a region has no pod":                 {"meter-no-pod", "no container was found in region region-b: nothing was measured"},
		"a region cannot be reached":          {"meter-b-unreachable", "[region-b] kubectl get pods"},
		"only ended pods hold the sidecar":    {"meter-ended", ""},
		"a running pod runs the sidecar":      {"meter-running", "acme/bp-newapi-6/c0 " + meterSidecarImage},
		"a pending pod will run the sidecar":  {"meter-pending", "acme/bp-newapi-7/c0 " + meterSidecarImage},
		"a pod of an unknown phase holds it":  {"meter-unknown", "acme/bp-newapi-8/c0 " + meterSidecarImage},
		"a region has only ended pods":        {"meter-only-ended", "no container was found in region region-b: nothing was measured"},
	} {
		r := runCheck(t, c.scenario, meterImageCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// One step per region, and the evidence holds what was inspected and found.
	r := runCheck(t, "meter-sidecar-in-b", meterImageCheck{}, &CheckContext{})
	if len(r.run.Steps) != 2 {
		t.Fatalf("want one step per region, got %d", len(r.run.Steps))
	}
	a, b := r.run.Steps[0], r.run.Steps[1]
	if a.Journey != meterImageID || a.ID != "AIP-G01.1" || a.Status != Pass || a.Evidence["pods"] != 2 || a.Evidence["images"] != 3 {
		t.Errorf("the step of region-a does not hold the evidence: %+v", a)
	}
	if b.ID != "AIP-G01.2" || b.Status != Fail || b.Evidence["images"] != 2 ||
		strings.Join(b.Evidence["found"].([]string), ",") != "acme/bp-newapi-2/c1 "+meterSidecarImage {
		t.Errorf("the step of region-b does not hold the evidence: %+v", b)
	}
	// An ended Pod is listed and counted as ended; its containers are not judged.
	r = runCheck(t, "meter-ended", meterImageCheck{}, &CheckContext{})
	if a := r.run.Steps[0]; a.Status != Pass || a.Evidence["pods"] != 3 || a.Evidence["ended"] != 2 || a.Evidence["images"] != 1 ||
		len(a.Evidence["found"].([]string)) != 0 {
		t.Errorf("the step of region-a does not hold the evidence of the ended pods: %+v", a)
	}
	// Without a region the check fails; it never passes on nothing.
	r = runCheck(t, "meter-clean", meterImageCheck{}, &CheckContext{})
	r.run.Steps, r.run.Checks = nil, nil
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(meterImageID, StateRequired)}}, []Check{meterImageCheck{}}, &CheckContext{}, true)
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "the run has no region") {
		t.Errorf("without a region: want FAIL, got %+v", got)
	}
}
