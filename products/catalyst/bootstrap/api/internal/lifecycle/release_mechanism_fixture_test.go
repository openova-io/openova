package lifecycle

import (
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// The cutover chart a fixture release carries (#7938): the parts of
// platform/self-sovereign-cutover/chart that the bootstrap's contract names
// (README.md, "Release mechanism"), with this checkout's release contract,
// release scripts, kit rewrite and pin extractor, and a step 03 of the test's
// choosing. The bootstrap renders it with the values of the
// installed cutover HelmRelease (cutoverHelmReleaseJSON), so the step podSpecs
// take the fixture's Gitea and upstream from those values.

const (
	cutoverChartPath = "platform/self-sovereign-cutover/chart"
	cutoverSlotFile  = kitDir + "/06a-bp-self-sovereign-cutover.yaml"
	installedCutover = "0.1.800"
	candidateCutover = "0.1.900"
)

// step03Marker is a step 03 that names its chart version and leaves a file the
// test reads; nothing else.
func step03Marker(version string) string {
	return "#!/bin/sh\nset -eu\necho \"[harbor-prewarm] step 03 of bp-self-sovereign-cutover " + version + " ran\"\ntouch \"${STUB_DIR}/step03-ran-" + version + "\"\n"
}

const cutoverFixtureSteps = `apiVersion: v1
kind: ConfigMap
metadata:
  name: cutover-step-01-gitea-mirror
  labels: {app.kubernetes.io/component: cutover-step}
data:
  podSpec: |
    serviceAccountName: bp-self-sovereign-cutover-runner
    restartPolicy: Never
    containers:
      - name: gitea-mirror
        image: alpine/k8s:1.31.4
        command: ["/bin/sh", "-c", "echo mirror"]
        env:
          - {name: UPSTREAM_REPO_URL, value: {{ .Values.upstreamRepo | quote }}}
          - {name: UPSTREAM_BRANCH, value: main}
          - {name: GITEA_INTERNAL_URL, value: {{ .Values.giteaURL | quote }}}
          - {name: GITEA_ORG, value: openova}
          - {name: GITEA_REPO, value: openova}
          - {name: GITEA_USERNAME, value: gitea_admin}
          - {name: PAT_SOURCE_NAMESPACE, value: catalyst-system}
          - {name: PAT_SOURCE_SECRET_NAME, value: catalyst-gitea-token}
          - {name: PAT_SOURCE_KEY, value: token}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cutover-step-03-harbor-prewarm
  labels: {app.kubernetes.io/component: cutover-step}
data:
  run.sh: |
{{ .Files.Get "files/step03.sh" | indent 4 }}
  podSpec: |
    serviceAccountName: bp-self-sovereign-cutover-runner
    restartPolicy: Never
    activeDeadlineSeconds: 9600
    containers:
      - name: harbor-prewarm
        image: alpine/k8s:1.31.4
        command: ["/bin/sh", "/step-script/run.sh"]
        env:
          - {name: UPSTREAM_BRANCH, value: main}
          - {name: UPSTREAM_PREFIX, value: {{ .Values.upstreamPrefix | quote }}}
          - name: GHCR_DOCKERCONFIG
            valueFrom: {secretKeyRef: {name: ghcr-pull, key: .dockerconfigjson}}
          - {name: GHCR_DOCKERCONFIG_NAMESPACE, value: flux-system}
          - {name: SECONDARY_HARBOR_NAMESPACE, value: harbor}
          - {name: SECONDARY_GHCR_SOURCE_NAMESPACE, value: flux-system}
          - {name: SECONDARY_GHCR_SOURCE_SECRET, value: ghcr-pull}
          - {name: SECONDARY_GHCR_DEST_SECRET, value: cutover-secondary-ghcr}
          - {name: SKC_DIR, value: /secondary-kubeconfigs}
        volumeMounts:
          - {name: step-script, mountPath: /step-script}
          - {name: secondary-kubeconfigs, mountPath: /secondary-kubeconfigs, readOnly: true}
    volumes:
      - name: step-script
        configMap: {name: cutover-step-03-harbor-prewarm, items: [{key: run.sh, path: run.sh}]}
      - name: secondary-kubeconfigs
        secret: {secretName: cutover-secondary-kubeconfigs, optional: true}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cutover-step-06-helmrepository-patches
  labels: {app.kubernetes.io/component: cutover-step}
data:
  kit-rewrite.sh: |
{{ .Files.Get "files/kit-rewrite.sh" | indent 4 }}
  podSpec: |
    containers:
      - name: helmrepository-patches
        image: alpine/k8s:1.31.4
        env:
          - {name: UPSTREAM_PREFIX, value: {{ .Values.upstreamPrefix | quote }}}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cutover-bootstrap-kit-pins
data:
  extract-pins.sh: |
{{ .Files.Get "files/extract-pins.sh" | indent 4 }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cutover-secondary-pivot-script
data:
  secondary-pivot-job.yaml: |
{{ tpl (.Files.Get "files/secondary-pivot-job.yaml") . | indent 4 }}
`

// writeCutoverChart writes the fixture cutover chart at version into the work
// tree w, with step03 as its step 03, and the kit slot that pins it.
func (f *fixture) writeCutoverChart(w, version, step03 string) {
	f.t.Helper()
	// The follower pivot Job of step 06, its Gitea a value: the follower's
	// Gitea exists only once the run fixture made it.
	job, err := yaml.Marshal(followerPivotJob("{{ .Values.followerGiteaURL }}"))
	if err != nil {
		f.t.Fatal(err)
	}
	c := w + "/" + cutoverChartPath
	f.write(c, "Chart.yaml", "apiVersion: v2\nname: bp-self-sovereign-cutover\nversion: "+version+"\n")
	f.write(c, "values.yaml", "upstreamRepo: \"\"\ngiteaURL: \"\"\nfollowerGiteaURL: \"\"\nupstreamPrefix: "+upstreamPrefix+"\n")
	f.write(c, "templates/steps.yaml", cutoverFixtureSteps)
	f.write(c, "files/step03.sh", step03)
	f.write(c, "files/kit-rewrite.sh", chartScript(f.t, "06-helmrepository-patches-job.yaml", "kit-rewrite.sh"))
	f.write(c, "files/extract-pins.sh", chartScript(f.t, "03b-bootstrap-kit-pins.yaml", "extract-pins.sh"))
	f.write(c, "files/secondary-pivot-job.yaml", string(job))
	contract, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(f.t), cutoverChartPath, "files/release/contract")))
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(c, "files/release/contract", string(contract))
	for _, n := range []string{"release-run.sh", "release-region.sh"} {
		b, err := releaseScripts.ReadFile("release/" + n)
		if err != nil {
			f.t.Fatal(err)
		}
		f.write(c, "files/release/"+n, string(b))
	}
	f.write(w, cutoverSlotFile, slot("bp-self-sovereign-cutover", version, upstreamPrefix))
}

// candidateCutoverRelease is laterRelease, plus a new cutover chart whose step
// 03 is step03: what the delivery must run.
func (f *fixture) candidateCutoverRelease(step03 string) func(w string) {
	return func(w string) {
		f.laterRelease(w)
		f.writeCutoverChart(w, candidateCutover, step03)
	}
}

// cutoverHelmReleaseJSON is the installed cutover HelmRelease the bootstrap
// renders the candidate's chart with.
func (r *runFixture) cutoverHelmReleaseJSON() string {
	return `{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
  "metadata": {"name": "bp-self-sovereign-cutover", "namespace": "flux-system"},
  "spec": {"releaseName": "self-sovereign-cutover", "targetNamespace": "catalyst",
    "values": {"upstreamRepo": "file://` + r.upstream + `", "giteaURL": "` + r.gitea + `", "followerGiteaURL": "` + r.followerURL + `"}}}`
}

// jobLog is the log of the delivery Job the stub's kubelet ran.
func (r *runFixture) jobLog(req releaseRequest) string {
	b, _ := os.ReadFile(filepath.Clean(r.stub + "/job-sovereign-release-delivery.log"))
	return string(b)
}
