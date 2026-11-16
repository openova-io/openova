package lifecycle

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The pull credential of a delivery (#7938). The first delivery to a two-region
// Sovereign stopped in its prewarm: the release pointed step 03's follower leg
// at the request's pull Secret, which exists in the primary region only, and
// that leg reads it through the FOLLOWER's kubeconfig. These tests run the
// stage, then the prewarm container as the release Job composes it, with the
// lines of step 03 that read the credential in the follower taken verbatim from
// the chart in this checkout, then the promote; and they check that no region
// holds the credential once the prewarm has ended, whatever its outcome.

const (
	releasePull  = "release-pull"
	harborNS     = "harbor"
	followerGHCR = "cutover-secondary-ghcr"
)

// followerCredentialBlock is the part of cutover step 03's follower leg that
// reads the upstream credential in a follower region and copies it into that
// region's harbor namespace ((2) of the #6764 leg of
// 03-harbor-prewarm-job.yaml), as Helm renders it.
func followerCredentialBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(t), "platform/self-sovereign-cutover/chart/templates/03-harbor-prewarm-job.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "      if ! kubectl --kubeconfig \"${skc}\" -n \"${SECONDARY_GHCR_SOURCE_NAMESPACE}\" get secret \"${SECONDARY_GHCR_SOURCE_SECRET}\"") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("03-harbor-prewarm-job.yaml no longer reads SECONDARY_GHCR_SOURCE_SECRET through the follower's kubeconfig; this test must follow it")
	}
	var block []string
	for _, l := range lines[start:] {
		block = append(block, strings.TrimPrefix(l, "    "))
		if l == "      fi" {
			break
		}
	}
	text := strings.Join(block, "\n") + "\n"
	if strings.Contains(text, "{{") || !strings.HasSuffix(text, "  fi\n") {
		t.Fatalf("the follower credential block of step 03 is not plain shell:\n%s", text)
	}
	return text
}

// step03Credential stands for the step 03 container of the cutover chart with
// its follower leg on (prewarmSecondaryHarbor): its command is the fake step
// 03 at fake, its credential variables the chart's defaults.
func step03Credential(fake string) *releaseInputs {
	return &releaseInputs{prewarm: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "harbor-prewarm", Image: cutoverImage, Command: []string{"/bin/sh", fake},
		Env: []corev1.EnvVar{
			plain("UPSTREAM_BRANCH", "main"), plain("UPSTREAM_PREFIX", upstreamPrefix),
			{Name: "GHCR_DOCKERCONFIG", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "ghcr-pull"}, Key: corev1.DockerConfigJsonKey}}},
			plain("GHCR_DOCKERCONFIG_NAMESPACE", "flux-system"),
			plain("SECONDARY_HARBOR_NAMESPACE", harborNS),
			plain("SECONDARY_GHCR_SOURCE_NAMESPACE", "flux-system"), plain("SECONDARY_GHCR_SOURCE_SECRET", "ghcr-pull"),
			plain("SECONDARY_GHCR_DEST_SECRET", followerGHCR),
		},
	}}}}
}

// plainEnv is the NAME=value list of a container's plain variables, with the
// Pod's paths mapped to the fixture's.
func (r *runFixture) plainEnv(c corev1.Container, names ...string) []string {
	paths := map[string]string{"/release": r.scripts, "/release-state": r.state, "/release-secondary-kubeconfigs": r.kubeconfigs, "/secondary-kubeconfigs": r.kubeconfigs}
	var out []string
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			continue
		}
		keep := len(names) == 0
		for _, n := range names {
			keep = keep || n == e.Name
		}
		if !keep {
			continue
		}
		v := e.Value
		if p, ok := paths[v]; ok {
			v = p
		}
		out = append(out, e.Name+"="+v)
	}
	return out
}

func (r *runFixture) secretPath(region, ns, name string) string {
	return filepath.Join(r.stub, "secrets", region, ns, name+".json")
}

func (r *runFixture) putSecret(region, ns, name, user string) {
	r.t.Helper()
	cfg, _ := json.Marshal(map[string]any{"auths": map[string]any{"ghcr.io": map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":not-a-real-token"))}}})
	doc, _ := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Secret", "type": string(corev1.SecretTypeDockerConfigJson),
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": "u-" + region, "resourceVersion": "42", "creationTimestamp": "2026-11-16T02:29:25Z"},
		"data":     map[string]string{corev1.DockerConfigJsonKey: base64.StdEncoding.EncodeToString(cfg)},
	})
	r.write(filepath.Dir(r.secretPath(region, ns, name)), name+".json", string(doc))
}

// secretData is the data of a Secret the stub holds, or nil.
func (r *runFixture) secretData(region, ns, name string) map[string]string {
	b, err := os.ReadFile(r.secretPath(region, ns, name))
	if err != nil {
		return nil
	}
	var s struct {
		Data     map[string]string `json:"data"`
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if json.Unmarshal(b, &s) != nil {
		r.t.Fatalf("%s/%s/%s is not JSON", region, ns, name)
	}
	if s.Data == nil {
		s.Data = map[string]string{}
	}
	return s.Data
}

// credentialFixture is a two-region Sovereign whose sovereign-admin made the
// request's pull Secret in the primary region, and whose follower still holds
// the copy step 03 made of an older credential in its harbor namespace.
func credentialFixture(t *testing.T, conflict, prewarmFails bool) (*runFixture, releaseRequest, corev1.Container) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id, PullSecret: releasePull}
	r := newRunFixtureFrom(t, f, req, conflict)
	fail := ""
	if prewarmFails {
		fail = `echo "[harbor-prewarm] FATAL: #6764 secondary region ${region} Harbor-prewarm Job cutover-harbor-prewarm-secondary-${region} did not succeed within 5400s" >&2
exit 1
`
	}
	// The fake step 03: for each follower, step 03's own lines, then a result.
	fake := "set -eu\nfor skc in " + r.kubeconfigs + "/*.yaml; do\n  region=$(basename \"${skc}\" .yaml)\n" + followerCredentialBlock(t) +
		"  echo \"[harbor-prewarm] #6764 secondary region ${region}: the credential is in ${SECONDARY_HARBOR_NAMESPACE}/${SECONDARY_GHCR_DEST_SECRET}\"\n" + fail + "done\n"
	f.write(f.dir, "step03.sh", fake)
	in := step03Credential(f.dir + "/step03.sh")
	in.mirror = corev1.Container{Env: []corev1.EnvVar{plain("UPSTREAM_REPO_URL", "file://"+f.upstream)}}
	pw, err := in.prewarmContainer(testNS, req)
	if err != nil {
		t.Fatal(err)
	}
	// The stage and promote containers carry the same credential variables.
	r.env = r.plainEnv(in.runContainer("stage", "stage", testNS, req), "PULL_SECRET", "SECONDARY_HARBOR_NAMESPACE", "SECONDARY_GHCR_DEST_SECRET")
	r.putSecret("primary", testNS, releasePull, "openova-bot")
	r.putSecret("region-b", harborNS, followerGHCR, "an-older-credential")
	return r, req, pw
}

func (r *runFixture) prewarm(pw corev1.Container, req releaseRequest) (string, int) {
	r.t.Helper()
	if strings.Join(pw.Command, " ") != "/bin/sh /release/release-run.sh prewarm" {
		r.t.Fatalf("the prewarm container does not run step 03 under release-run.sh prewarm: %v", pw.Command)
	}
	return r.runArgs(append([]string{"prewarm"}, pw.Args...), req, r.plainEnv(pw)...)
}

// noCredentialLeft fails when any region still holds the pull credential.
func (r *runFixture) noCredentialLeft(when string) {
	r.t.Helper()
	for _, s := range [][3]string{{"primary", testNS, releasePull}, {"region-b", testNS, releasePull}, {"region-b", harborNS, followerGHCR}} {
		if r.secretData(s[0], s[1], s[2]) != nil {
			r.t.Errorf("%s: region %s still holds %s/%s", when, s[0], s[1], s[2])
		}
	}
	if st := r.status(); !strings.HasPrefix(st["credential"], "deleted in every region") {
		r.t.Errorf("%s: the state does not say the credential is deleted: credential=%q", when, st["credential"])
	}
}

func TestReleaseDeliveryGivesEachFollowerThePullCredentialAndLeavesNone(t *testing.T) {
	r, req, pw := credentialFixture(t, false, false)
	source := r.secretData("primary", testNS, releasePull)

	out, code := r.run("stage", req)
	if st := r.status(); code != 0 || st["phase"] != "staged" {
		t.Fatalf("stage: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	copied := r.secretData("region-b", testNS, releasePull)
	if copied == nil || copied[corev1.DockerConfigJsonKey] != source[corev1.DockerConfigJsonKey] {
		t.Fatalf("after the stage, follower region-b holds no copy of the request's pull Secret in %s, where step 03's follower leg reads it; the stage said:\n%s", testNS, out)
	}

	out, code = r.prewarm(pw, req)
	if code != 0 {
		t.Fatalf("prewarm: exit %d: step 03's follower leg could not read the credential in the follower\n%s", code, out)
	}
	if !strings.Contains(out, "the credential is in harbor/"+followerGHCR) {
		t.Fatalf("the prewarm's log is not step 03's:\n%s", out)
	}
	if st := r.status(); st["phase"] != "staged" || st["failedStep"] != "" {
		t.Fatalf("a prewarm that succeeded changed the state: phase %q failedStep %q", st["phase"], st["failedStep"])
	}
	r.noCredentialLeft("after the prewarm")

	out, code = r.run("promote", req)
	if st := r.status(); code != 0 || st["phase"] != "delivered" {
		t.Fatalf("promote: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	r.noCredentialLeft("after the promote")
}

func TestReleasePrewarmFailureIsTheStateWithItsCauseAndLeavesNoCredential(t *testing.T) {
	r, req, pw := credentialFixture(t, false, true)
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	out, code := r.prewarm(pw, req)
	st := r.status()
	if code == 0 || st["phase"] != "failed" || st["failedStep"] != "prewarm" {
		t.Fatalf("a failed prewarm: exit %d, phase %q, failedStep %q; want a failure, phase failed, failedStep prewarm\n%s", code, st["phase"], st["failedStep"], out)
	}
	if !strings.Contains(st["message"], "Harbor-prewarm Job cutover-harbor-prewarm-secondary-region-b did not succeed") {
		t.Fatalf("the state does not name the cause step 03 printed: %q", st["message"])
	}
	r.noCredentialLeft("after a failed prewarm")
	if r.remote("main") != r.main || r.followerRef("main") != r.followerMain {
		t.Fatal("a failed prewarm moved a main")
	}
}

func TestReleaseStageFailureLeavesNoCredential(t *testing.T) {
	r, req, _ := credentialFixture(t, true, false)
	out, code := r.run("stage", req)
	st := r.status()
	if code == 0 || st["phase"] != "conflict" || st["failedStep"] != "stage" {
		t.Fatalf("stage: exit %d, phase %q, failedStep %q; want a conflict in the stage\n%s", code, st["phase"], st["failedStep"], out)
	}
	r.noCredentialLeft("after a failed stage")
}

func TestReleaseDryRunTouchesNoCredential(t *testing.T) {
	f := newFixture(t)
	id, _ := f.candidate(1, f.laterRelease)
	req := releaseRequest{Candidate: id, DryRun: true, PullSecret: releasePull}
	r := newRunFixtureFrom(t, f, req, false)
	in := step03Credential("/bin/true")
	r.env = r.plainEnv(in.runContainer("stage", "stage", testNS, req), "PULL_SECRET", "SECONDARY_HARBOR_NAMESPACE", "SECONDARY_GHCR_DEST_SECRET")
	if len(r.env) != 0 {
		t.Fatalf("a dry run names the credential: %v", r.env)
	}
	r.putSecret("primary", testNS, releasePull, "openova-bot")
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if r.secretData("primary", testNS, releasePull) == nil || r.secretData("region-b", testNS, releasePull) != nil {
		t.Fatal("a dry run copied or deleted the pull credential")
	}
}
