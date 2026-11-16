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
// request's pull Secret in the primary region. The candidate's cutover chart
// carries a step 03 made of step 03's own lines that read the credential in the
// follower and copy it into the follower's harbor namespace; the delivery runs
// that step 03 (#7938). Unless the stage is to fail, the follower still holds
// the copy an older step 03 made in its harbor namespace.
func credentialFixture(t *testing.T, conflict, prewarmFails bool) (*runFixture, releaseRequest) {
	f := newFixture(t)
	fail := ""
	if prewarmFails {
		fail = `echo "[harbor-prewarm] FATAL: #6764 secondary region ${region} Harbor-prewarm Job cutover-harbor-prewarm-secondary-${region} did not succeed within 5400s" >&2
exit 1
`
	}
	fake := "set -eu\nfor skc in \"${SKC_DIR}\"/*.yaml; do\n  [ -e \"${skc}\" ] || continue\n  region=$(basename \"${skc}\" .yaml)\n" + followerCredentialBlock(t) +
		"  echo \"[harbor-prewarm] #6764 secondary region ${region}: the credential is in ${SECONDARY_HARBOR_NAMESPACE}/${SECONDARY_GHCR_DEST_SECRET}\"\n" + fail +
		"done\ntouch \"${STUB_DIR}/step03-ran-" + candidateCutover + "\"\n"
	id, _ := f.candidate(1, f.candidateCutoverRelease(fake))
	req := releaseRequest{Candidate: id, PullSecret: releasePull}
	r := newRunFixtureFrom(t, f, req, conflict)
	// The bootstrap's containers name the credential as the route composes them.
	for _, e := range credentialEnv(req) {
		r.env = append(r.env, e.Name+"="+e.Value)
	}
	r.putSecret("primary", testNS, releasePull, "openova-bot")
	if !conflict {
		r.putSecret("region-b", harborNS, followerGHCR, "an-older-credential")
	}
	return r, req
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
	r, req := credentialFixture(t, false, false)
	source := r.secretData("primary", testNS, releasePull)

	out, code := r.run("stage", req)
	if st := r.status(); code != 0 || st["phase"] != "staged" {
		t.Fatalf("stage: exit %d, phase %q\n%s", code, st["phase"], out)
	}
	copied := r.secretData("region-b", testNS, releasePull)
	if copied == nil || copied[corev1.DockerConfigJsonKey] != source[corev1.DockerConfigJsonKey] {
		t.Fatalf("after the stage, follower region-b holds no copy of the request's pull Secret in %s, where step 03's follower leg reads it; the stage said:\n%s", testNS, out)
	}

	out, code = r.run("deliver", req)
	logs := r.jobLog(req)
	if code != 0 {
		t.Fatalf("deliver: exit %d: the candidate's step 03 could not read the credential in the follower\n%s\n%s", code, out, logs)
	}
	if !strings.Contains(logs, "the credential is in harbor/"+followerGHCR) {
		t.Fatalf("the delivery's prewarm log is not the candidate's step 03:\n%s", logs)
	}
	if st := r.status(); st["phase"] != "delivered" || st["failedStep"] != "" {
		t.Fatalf("the delivery: phase %q failedStep %q\n%s", st["phase"], st["failedStep"], logs)
	}
	r.noCredentialLeft("after the delivery")
}

func TestReleasePrewarmFailureIsTheStateWithItsCauseAndLeavesNoCredential(t *testing.T) {
	r, req := credentialFixture(t, false, true)
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("stage: exit %d\n%s", code, out)
	}
	out, code := r.run("deliver", req)
	st := r.status()
	if code == 0 || st["phase"] != "failed" || st["failedStep"] != "prewarm" {
		t.Fatalf("a failed prewarm: exit %d, phase %q, failedStep %q; want a failure, phase failed, failedStep prewarm\n%s\n%s", code, st["phase"], st["failedStep"], out, r.jobLog(req))
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
	r, req := credentialFixture(t, true, false)
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
	if env := credentialEnv(req); len(env) != 0 {
		t.Fatalf("a dry run names the credential: %v", env)
	}
	r.putSecret("primary", testNS, releasePull, "openova-bot")
	if out, code := r.run("stage", req); code != 0 {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if r.secretData("primary", testNS, releasePull) == nil || r.secretData("region-b", testNS, releasePull) != nil {
		t.Fatal("a dry run copied or deleted the pull credential")
	}
}
