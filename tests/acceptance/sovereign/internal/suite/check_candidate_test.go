package suite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	candidateCommit = "4be7a11c0d4be7a11c0d4be7a11c0d4be7a11c0d"
	digestOne       = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestTwo       = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// candidatePins is a pin file as the bootstrap kit holds them. It also holds
// what the reader must pass over: a HelmRepository, comments, a quoted value,
// keys named chart and version at other paths, and a block of text.
const candidatePins = `# slot 01
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: HelmRepository
metadata:
  name: bp-cilium
  namespace: flux-system
spec:
  url: oci://ghcr.io/openova-io
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-cilium
  namespace: flux-system
  labels:
    version: "9.9.9"
spec:
  interval: 15m
  dependsOn:
    - name: bp-gateway-api
      version: 8.8.8
  # version: 7.7.7
  chart:
    spec:
      chart: bp-cilium
      version: 1.4.20 # the pin
      sourceRef:
        kind: HelmRepository
        name: bp-cilium
  postRenderers:
    - kustomize:
        patches:
          - patch: |
              kind: Service
              spec:
                chart:
                  spec:
                    version: 6.6.6
  values:
    chart:
      spec:
        version: 5.5.5
    cilium:
      version: 4.4.4
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-postgres-shared
  namespace: flux-system
spec:
  suspend: ${HR_SUSPEND:=false}
  chart:
    spec:
      chart: bp-postgres
      version: "0.2.33"
`

// fakeDeployed is a fixture HelmRelease whose history says which chart versions
// were tried: pairs of release status and chart version, newest first.
func fakeDeployed(namespace, name, chart string, suspend bool, history ...string) obj {
	snapshots := []obj{}
	for i := 0; i+1 < len(history); i += 2 {
		snapshots = append(snapshots, obj{"status": history[i], "chartName": chart, "chartVersion": history[i+1]})
	}
	return obj{"kind": "HelmRelease", "metadata": kubeMeta(namespace, name, "helm-controller"),
		"spec":   obj{"suspend": suspend, "chart": obj{"spec": obj{"chart": chart}}},
		"status": obj{"history": snapshots}}
}

// fakeContainer is a fixture pod with one init container and one container.
// An empty digest is a container that has not started.
func fakeContainer(namespace, name, ref, digest string) obj {
	imageID := ""
	if digest != "" {
		imageID = "registry.example/mirror/app@" + digest
	}
	return obj{"metadata": kubeMeta(namespace, name),
		"spec": obj{"initContainers": []obj{{"name": "init", "image": "registry.example/init:1"}},
			"containers": []obj{{"name": "main", "image": ref}}},
		"status": obj{"initContainerStatuses": []obj{{"name": "init", "imageID": "registry.example/init@" + digestOne}},
			"containerStatuses": []obj{{"name": "main", "imageID": imageID}}}}
}

func init() {
	releases := kubeList(
		fakeDeployed("flux-system", "bp-cilium", "bp-cilium", false, "deployed", "1.4.20", "superseded", "1.4.19"),
		fakeDeployed("flux-system", "bp-postgres-shared", "bp-postgres", true, "deployed", "0.2.30"), // suspended: listed, not compared
		fakeDeployed("acme", "shop", "bp-shop", false, "deployed", "3.0.0"))                          // no pin: listed, not compared
	pods := kubeList(
		fakeContainer("kube-system", "cilium-1", "registry.example/cilium:1.19", digestOne),
		fakeContainer("kube-system", "cilium-2", "registry.example/cilium:1.19", digestOne),
		fakeContainer("acme", "shop-1", "registry.example/shop:3", "")) // an Organization namespace is not read
	good := map[string]string{"* " + hrResource: releases, "* namespaces": kubeList(obj{"metadata": kubeMeta("", "acme")}), "* pods": pods}
	with := func(question, answer string) map[string]string {
		out := map[string]string{question: answer}
		for k, v := range good {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
		return out
	}
	fakeAnswers["candidate-as-pinned"] = good
	fakeAnswers["candidate-older-version"] = with("b "+hrResource, kubeList(
		fakeDeployed("flux-system", "bp-cilium", "bp-cilium", false, "failed", "1.4.20", "deployed", "1.4.19")))
	fakeAnswers["candidate-nothing-deployed"] = with("b "+hrResource, kubeList(
		fakeDeployed("flux-system", "bp-cilium", "bp-cilium", false)))
	fakeAnswers["candidate-two-digests-in-a-region"] = with("b pods", kubeList(
		fakeContainer("kube-system", "cilium-1", "registry.example/cilium:1.19", digestOne),
		fakeContainer("kube-system", "cilium-2", "registry.example/cilium:1.19", digestTwo)))
	fakeAnswers["candidate-two-digests-across-regions"] = with("b pods", kubeList(
		fakeContainer("kube-system", "cilium-1", "registry.example/cilium:1.19", digestTwo)))
	fakeAnswers["candidate-no-digest"] = with("b pods", kubeList(
		fakeContainer("kube-system", "cilium-1", "registry.example/cilium:1.19", "")))
	fakeAnswers["candidate-no-containers"] = with("b pods", kubeList())
	fakeAnswers["candidate-no-releases"] = with("b "+hrResource, kubeList())
	fakeAnswers["candidate-region-b-unreachable"] = map[string]string{"a " + hrResource: releases,
		"a namespaces": kubeList(obj{"metadata": kubeMeta("", "acme")}), "a pods": pods}
}

// candidateRun runs the check with the suite built from built and a checkout
// that holds the pin file.
func candidateRun(t *testing.T, scenario, built string) *runner {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(pinsDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-cilium.yaml"), []byte(candidatePins), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_SHA", built)
	return runCheck(t, scenario, candidateCheck{}, &CheckContext{RepoRoot: root, Candidate: Candidate{Commit: candidateCommit}})
}

func TestCandidateCheck(t *testing.T) {
	if _, ok := registry[candidateID]; !ok || candidateID != "FND-G01" {
		t.Fatalf("check %s is not registered", candidateID)
	}
	r := candidateRun(t, "candidate-as-pinned", candidateCommit)
	if got := r.run.Checks[0]; got.Status != Pass || len(r.run.Steps) != 7 {
		t.Fatalf("what runs is what the candidate pins: want PASS in 7 steps, got %+v in %d steps: %v", got, len(r.run.Steps), failedSteps(r))
	}
	if ev := r.run.Steps[2].Evidence; ev["releases"] != 3 || ev["compared"] != 1 {
		t.Errorf("only the pinned release that is not suspended is compared: %+v", ev)
	}
	// The artifacts move from the evidence of the last step into the record.
	last := r.run.Steps[len(r.run.Steps)-1]
	if _, held := last.Evidence[ArtifactsKey].(*Artifacts); !held || r.run.Artifacts != nil {
		t.Fatalf("the last step must hold the artifacts until the record is finalized: %+v", last.Evidence)
	}
	r.run.Finalize(nil)
	if _, still := last.Evidence[ArtifactsKey]; still || r.run.Artifacts == nil {
		t.Fatal("Finalize must move the artifacts from the step into the record")
	}
	b, err := json.Marshal(r.run.Artifacts)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"charts":[` +
		`{"region":"region-a","namespace":"acme","release":"shop","chart":"bp-shop","version":"3.0.0","pinned":""},` +
		`{"region":"region-a","namespace":"flux-system","release":"bp-cilium","chart":"bp-cilium","version":"1.4.20","pinned":"1.4.20"},` +
		`{"region":"region-a","namespace":"flux-system","release":"bp-postgres-shared","chart":"bp-postgres","version":"0.2.30","pinned":"0.2.33"},` +
		`{"region":"region-b","namespace":"acme","release":"shop","chart":"bp-shop","version":"3.0.0","pinned":""},` +
		`{"region":"region-b","namespace":"flux-system","release":"bp-cilium","chart":"bp-cilium","version":"1.4.20","pinned":"1.4.20"},` +
		`{"region":"region-b","namespace":"flux-system","release":"bp-postgres-shared","chart":"bp-postgres","version":"0.2.30","pinned":"0.2.33"}],` +
		`"images":[` +
		`{"region":"region-a","ref":"registry.example/cilium:1.19","digest":"` + digestOne + `","containers":2},` +
		`{"region":"region-a","ref":"registry.example/init:1","digest":"` + digestOne + `","containers":2},` +
		`{"region":"region-b","ref":"registry.example/cilium:1.19","digest":"` + digestOne + `","containers":2},` +
		`{"region":"region-b","ref":"registry.example/init:1","digest":"` + digestOne + `","containers":2}]}`
	if string(b) != want {
		t.Errorf("artifacts:\n got %s\nwant %s", b, want)
	}

	for name, c := range map[string]struct {
		scenario, built, want string
		artifacts             bool // the record still gets artifacts
	}{
		"the suite was built from another commit":      {"candidate-as-pinned", strings.Repeat("d", 40), "the suite was built from dddddddddddddddddddddddddddddddddddddddd and the candidate commit is " + candidateCommit, true},
		"a release runs an older version than its pin": {"candidate-older-version", candidateCommit, "1 of 1 HelmReleases do not run their pinned version: flux-system/bp-cilium runs 1.4.19 and the pin is 1.4.20", true},
		"a pinned release has nothing deployed":        {"candidate-nothing-deployed", candidateCommit, "flux-system/bp-cilium runs no deployed version and the pin is 1.4.20", true},
		"one reference, two digests, in one region":    {"candidate-two-digests-in-a-region", candidateCommit, "1 image references have more than one digest: registry.example/cilium:1.19 has " + digestOne + " and " + digestTwo, true},
		"one reference, two digests, across regions":   {"candidate-two-digests-across-regions", candidateCommit, "1 image references have more than one digest: registry.example/cilium:1.19 has " + digestOne + " and " + digestTwo, true},
		"a container without a digest":                 {"candidate-no-digest", candidateCommit, "1 containers have no image digest: kube-system/cilium-1/main", true},
		"no container in a region":                     {"candidate-no-containers", candidateCommit, "no container was found in the platform namespaces: nothing was measured", false},
		"no HelmRelease in a region":                   {"candidate-no-releases", candidateCommit, "no HelmRelease was found: nothing was measured", false},
		"a region that cannot be reached":              {"candidate-region-b-unreachable", candidateCommit, "[region-b] kubectl get " + hrResource, false},
	} {
		r := candidateRun(t, c.scenario, c.built)
		got := r.run.Checks[0]
		if got.Status != Fail || !strings.Contains(got.Reason, c.want) {
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		if r.run.Finalize(nil); (r.run.Artifacts != nil) != c.artifacts {
			t.Errorf("%s: artifacts in the record: got %v, want %v", name, r.run.Artifacts != nil, c.artifacts)
		}
	}

	// Without a candidate commit, and without a checkout, the check fails.
	t.Setenv("GITHUB_SHA", candidateCommit)
	r = runCheck(t, "candidate-as-pinned", candidateCheck{}, &CheckContext{RepoRoot: t.TempDir()})
	if got := failedSteps(r); len(got) < 2 || !strings.Contains(got[0], "the run names no candidate commit") || !strings.Contains(got[1], "no HelmRelease with a chart version was found") {
		t.Errorf("without a candidate: got %v", got)
	}
	if r.run.Finalize(nil); r.run.Artifacts != nil {
		t.Error("without pins the record must get no artifacts")
	}
}

// TestCandidateSampleRecord writes one version 2 record that holds the
// artifacts of check FND-G01. When ACCEPT_SAMPLE_RECORD_DIR is set the record
// stays there, so that `scripts/acceptance-gate.py validate-record` can prove
// that the types of the artifacts and the schema of that tool agree.
func TestCandidateSampleRecord(t *testing.T) {
	dir := os.Getenv("ACCEPT_SAMPLE_RECORD_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	now := time.Date(2026, 11, 20, 8, 15, 0, 0, time.UTC)
	run := candidateRun(t, "candidate-as-pinned", candidateCommit).run
	run.Schema, run.RunID, run.Env, run.Console = SchemaVersion, NewRunID(now), "candidate", "https://console.sample.omani.works"
	run.RunIn, run.Capabilities, run.SuiteGitSHA = string(RunInLocal), []string{}, candidateCommit
	run.StartedAt, run.FinishedAt = now, now.Add(time.Minute)
	run.Candidate, run.Producer, run.Subject = Candidate{Commit: candidateCommit}, Producer{Kind: "manual"}, Evidence{}
	run.Finalize(nil)
	if run.Result != Pass || run.Artifacts == nil || len(run.Artifacts.Charts) != 6 || len(run.Artifacts.Images) != 4 {
		t.Fatalf("the record does not hold the artifacts of both regions: result %s, artifacts %+v", run.Result, run.Artifacts)
	}
	if _, err := run.Write(dir); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateReadPins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(pinsDir))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-cilium.yaml"), []byte(candidatePins), 0o600); err != nil {
		t.Fatal(err)
	}
	pins, err := readPins(root)
	if err != nil || len(pins) != 2 || pins["flux-system/bp-cilium"] != (chartPin{"bp-cilium", "1.4.20"}) ||
		pins["flux-system/bp-postgres-shared"] != (chartPin{"bp-postgres", "0.2.33"}) {
		t.Fatalf("the pins were not read: %v %v", pins, err)
	}
	// A HelmRelease without a version is an error, never a pin that is passed over.
	bad := strings.Replace(candidatePins, `      version: "0.2.33"`, "", 1)
	if err := os.WriteFile(filepath.Join(dir, "02-bad.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPins(root); err == nil || !strings.Contains(err.Error(), "02-bad.yaml") {
		t.Fatalf("a HelmRelease without a version must be an error that names the file, got %v", err)
	}
	if _, err := readPins(""); err == nil {
		t.Fatal("an empty root must be an error")
	}
}

// TestCandidatePinsOfThisRepository reads the pin files of this repository.
// The reader finds as many pins as the files hold HelmRelease documents, and
// every pin has a chart and a version.
func TestCandidatePinsOfThisRepository(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pinsDir), "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no pin file under %s: %v", pinsDir, err)
	}
	documents := 0
	for _, f := range files {
		text, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatal(err)
		}
		documents += strings.Count("\n"+string(text), "\nkind: HelmRelease")
	}
	pins, err := readPins(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != documents || documents == 0 {
		t.Fatalf("the files hold %d HelmRelease documents and the reader found %d pins", documents, len(pins))
	}
	for key, pin := range pins {
		if !strings.HasPrefix(key, "flux-system/") || pin.Chart == "" || pin.Version == "" || strings.ContainsAny(pin.Version, " \"'#") {
			t.Errorf("pin %s is not complete: %+v", key, pin)
		}
	}
}
