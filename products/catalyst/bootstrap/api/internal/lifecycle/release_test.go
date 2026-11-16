package lifecycle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const (
	testNS        = "catalyst"
	testCandidate = "20261116T090000Z-abcdef1"
	cutoverImage  = "harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4"
	runnerSA      = "bp-self-sovereign-cutover-runner"
)

const mirrorPodSpec = `serviceAccountName: bp-self-sovereign-cutover-runner
restartPolicy: Never
activeDeadlineSeconds: 1200
containers:
  - name: gitea-mirror
    image: harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4
    env:
      - {name: UPSTREAM_REPO_URL, value: "https://github.com/openova-io/openova"}
      - {name: UPSTREAM_BRANCH, value: main}
      - {name: GITEA_INTERNAL_URL, value: "http://gitea-http.gitea.svc.cluster.local:3000"}
      - {name: GITEA_ORG, value: openova}
      - {name: GITEA_REPO, value: openova}
      - {name: GITEA_USERNAME, value: gitea_admin}
      - {name: PAT_SOURCE_NAMESPACE, value: catalyst-system}
      - {name: PAT_SOURCE_SECRET_NAME, value: catalyst-gitea-token}
      - {name: PAT_SOURCE_KEY, value: token}
    command: ["/bin/sh", "-c"]
    args: ["echo mirror"]
    securityContext: {runAsNonRoot: true, runAsUser: 1000}
`

const prewarmPodSpec = `serviceAccountName: bp-self-sovereign-cutover-runner
restartPolicy: Never
activeDeadlineSeconds: 9600
containers:
  - name: harbor-prewarm
    image: harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4
    command: ["/bin/sh", "/step-script/run.sh"]
    env:
      - {name: UPSTREAM_BRANCH, value: main}
      - {name: UPSTREAM_PREFIX, value: "oci://ghcr.io/openova-io"}
      - name: GHCR_DOCKERCONFIG
        valueFrom: {secretKeyRef: {name: ghcr-pull, key: .dockerconfigjson}}
      - {name: GHCR_DOCKERCONFIG_NAMESPACE, value: flux-system}
      - {name: SECONDARY_GHCR_SOURCE_NAMESPACE, value: flux-system}
      - {name: SECONDARY_GHCR_SOURCE_SECRET, value: ghcr-pull}
    volumeMounts:
      - {name: step-script, mountPath: /step-script}
      - {name: tmp, mountPath: /tmp}
volumes:
  - name: step-script
    configMap: {name: cutover-step-03-harbor-prewarm, items: [{key: run.sh, path: run.sh}]}
  - name: tmp
    emptyDir: {}
`

const pivotPodSpec = `containers:
  - name: helmrepository-patches
    image: harbor.openova.io/proxy-dockerhub/alpine/k8s:1.31.4
    env:
      - {name: UPSTREAM_PREFIX, value: "oci://ghcr.io/openova-io"}
`

// brokenStep03 is an installed step 03 a delivery must not depend on: its
// script fails and its podSpec does not parse.
const brokenStep03 = "containers: [ {name: harbor-prewarm, command: [\"/bin/sh\", \"-c\", \"exit 1\"]"

func cm(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}, Data: data}
}

func pullSecret(name string, hosts ...string) *corev1.Secret {
	auths := map[string]map[string]string{}
	for _, h := range hosts {
		auths[h] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("u:p"))}
	}
	b, _ := json.Marshal(map[string]any{"auths": auths})
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS}, Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: b}}
}

// installed is a cut-over Sovereign with the cutover chart's parts in place.
func installed(followers, marker bool) []runtime.Object {
	run := "echo prewarm\n"
	if marker {
		run += `echo "[harbor-prewarm] #7922 secondary region ${region}: also mirrors the pins of ${UPSTREAM_BRANCH}"` + "\n"
	}
	objs := []runtime.Object{
		cm("self-sovereign-cutover-status", map[string]string{"cutoverComplete": "true"}),
		cm(mirrorStepConfigMap, map[string]string{"stepName": "gitea-mirror", "podSpec": mirrorPodSpec}),
		// The installed step 03: the route never reads it (#7938).
		cm("cutover-step-03-harbor-prewarm", map[string]string{"stepName": "harbor-prewarm", "podSpec": prewarmPodSpec, "run.sh": run}),
		cm(pivotStepConfigMap, map[string]string{"kit-rewrite.sh": "rewrite_kit_tree() { :; }\n", "podSpec": pivotPodSpec}),
		cm(pinsConfigMap, map[string]string{"extract-pins.sh": "bootstrapkit_pins() { :; }\n"}),
		pullSecret("release-pull", "ghcr.io"),
	}
	if followers {
		job, _ := yaml.Marshal(followerPivotJob("http://gitea-http.gitea.svc.cluster.local:3000"))
		objs = append(objs,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secondaryKubeconfigs, Namespace: testNS}, Data: map[string][]byte{"region-b.yaml": []byte("kind: Config\n")}},
			cm(followerPivotConfigMap, map[string]string{"secondary-pivot.sh": "#!/bin/sh\n", "secondary-pivot-job.yaml": string(job)}))
	}
	return objs
}

func route(cs kubernetes.Interface, env map[string]string) *ReleaseRoute {
	return &ReleaseRoute{
		clients: func() (kubernetes.Interface, error) { return cs, nil },
		now:     func() time.Time { return time.Unix(1763283600, 0) },
		getenv:  func(k string) string { return env[k] },
	}
}

var admin = &auth.Claims{Email: "admin@example.test", Tier: "owner"}

func put(t *testing.T, rt *ReleaseRoute, claims *auth.Claims, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereign/release", bytes.NewBufferString(body))
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	w := httptest.NewRecorder()
	rt.Request(w, req)
	return w
}

func created(cs *fake.Clientset, resource string) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

func onlyJob(t *testing.T, cs kubernetes.Interface) *batchv1.Job {
	t.Helper()
	jobs, err := cs.BatchV1().Jobs(testNS).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("want one Job, have %d (%v)", len(jobs.Items), err)
	}
	return &jobs.Items[0]
}

func env(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func TestReleaseRequestIsForASovereignAdminOnly(t *testing.T) {
	for name, c := range map[string]*auth.Claims{
		"no session":                   nil,
		"an Organization-scoped admin": {Email: "o@example.test", Tier: auth.OrgScopedTier},
		"a viewer":                     {Email: "v@example.test", Tier: "viewer"},
		"an application-admin":         {Email: "a@example.test", RealmAccess: auth.RealmAccess{Roles: []string{"application-admin"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(installed(false, false)...)
			w := put(t, route(cs, nil), c, `{"candidate":"`+testCandidate+`","dryRun":true}`)
			if w.Code != http.StatusForbidden || created(cs, "jobs") != 0 {
				t.Fatalf("code %d, Jobs created %d: %s", w.Code, created(cs, "jobs"), w.Body)
			}
		})
	}
	for name, c := range map[string]*auth.Claims{
		"tier owner":           admin,
		"tier admin":           {Email: "x@example.test", Tier: "admin"},
		"realm catalyst-owner": {Email: "y@example.test", RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}}},
	} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(installed(false, false)...)
			if w := put(t, route(cs, nil), c, `{"candidate":"`+testCandidate+`","dryRun":true}`); w.Code != http.StatusAccepted {
				t.Fatalf("code %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestReleaseRequestRefusals(t *testing.T) {
	without := func(name string) []runtime.Object {
		var out []runtime.Object
		for _, o := range installed(false, false) {
			if m, ok := o.(metav1.Object); !ok || m.GetName() != name {
				out = append(out, o)
			}
		}
		return out
	}
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "sovereign-release-1", Namespace: testNS,
		Labels: map[string]string{"app.kubernetes.io/component": releaseComponent}}}
	done := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "sovereign-release-0", Namespace: testNS,
		Labels: map[string]string{"app.kubernetes.io/component": releaseComponent}},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}
	noPrefix := installed(false, false)
	noPrefix[3] = cm(pivotStepConfigMap, map[string]string{"kit-rewrite.sh": "rewrite_kit_tree() { :; }\n", "podSpec": strings.Replace(pivotPodSpec, "UPSTREAM_PREFIX", "SOMETHING", 1)})
	managed := func(name string, mutate func(*corev1.Secret)) *corev1.Secret {
		s := pullSecret(name, "ghcr.io")
		mutate(s)
		return s
	}
	followersOnly := append(installed(false, false),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secondaryKubeconfigs, Namespace: testNS}, Data: map[string][]byte{"region-b.yaml": []byte("x")}})
	for _, c := range []struct {
		name, body string
		objs       []runtime.Object
		env        map[string]string
		code       int
		want       string
	}{
		{"an unknown field", `{"candidate":"` + testCandidate + `","force":true}`, installed(false, false), nil, 400, "bad-body"},
		{"not a candidate id", `{"candidate":"main"}`, installed(false, false), nil, 400, "bad-candidate"},
		{"a pull secret that is not a name", `{"candidate":"` + testCandidate + `","pullSecret":"A B"}`, installed(false, false), nil, 400, "bad-pull-secret"},
		{"a signature is required", `{"candidate":"` + testCandidate + `","dryRun":true}`, installed(false, false), map[string]string{envReleaseSignature: "required"}, 412, "cut unsigned"},
		{"an unknown signature policy fails closed", `{"candidate":"` + testCandidate + `","dryRun":true}`, installed(false, false), map[string]string{envReleaseSignature: "cosign"}, 412, "signature-required"},
		{"no cutover status", `{"candidate":"` + testCandidate + `","dryRun":true}`, without("self-sovereign-cutover-status"), nil, 409, "not-cut-over"},
		{"cutover not complete", `{"candidate":"` + testCandidate + `","dryRun":true}`, append(without("self-sovereign-cutover-status"), cm("self-sovereign-cutover-status", map[string]string{"cutoverComplete": "false"})), nil, 409, "not-cut-over"},
		{"a release is running", `{"candidate":"` + testCandidate + `","dryRun":true}`, append(installed(false, false), running), nil, 409, "sovereign-release-1"},
		{"no step 01", `{"candidate":"` + testCandidate + `","dryRun":true}`, without(mirrorStepConfigMap), nil, 424, mirrorStepConfigMap},
		{"no kit rewrite", `{"candidate":"` + testCandidate + `","dryRun":true}`, without(pivotStepConfigMap), nil, 424, pivotStepConfigMap},
		{"no pin extractor", `{"candidate":"` + testCandidate + `","dryRun":true}`, without(pinsConfigMap), nil, 424, pinsConfigMap},
		{"a step 06 that names no upstream prefix", `{"candidate":"` + testCandidate + `","dryRun":true}`, noPrefix, nil, 424, "UPSTREAM_PREFIX"},
		{"followers without the follower pivot", `{"candidate":"` + testCandidate + `","dryRun":true}`, followersOnly, nil, 424, followerPivotConfigMap},
		{"a delivery without a pull secret", `{"candidate":"` + testCandidate + `"}`, installed(false, false), nil, 422, "no credential"},
		{"a pull secret without ghcr.io", `{"candidate":"` + testCandidate + `","pullSecret":"other"}`, append(installed(false, false), pullSecret("other", "docker.io")), nil, 422, "no auth entry for ghcr.io"},
		{"an absent pull secret", `{"candidate":"` + testCandidate + `","pullSecret":"absent"}`, installed(false, false), nil, 422, "absent"},
		// The delivery deletes its pull Secret: one another controller manages is refused.
		{"a reflected pull secret", `{"candidate":"` + testCandidate + `","pullSecret":"ghcr-pull"}`, append(installed(false, false), managed("ghcr-pull", func(s *corev1.Secret) {
			s.Annotations = map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "flux-system/ghcr-pull"}
		})), nil, 422, "managed by the reflector"},
		{"a Helm-managed pull secret", `{"candidate":"` + testCandidate + `","pullSecret":"chart-pull"}`, append(installed(false, false), managed("chart-pull", func(s *corev1.Secret) {
			s.Annotations = map[string]string{"meta.helm.sh/release-name": "bp-harbor"}
		})), nil, 422, "managed by the Helm release bp-harbor"},
		{"an owned pull secret", `{"candidate":"` + testCandidate + `","pullSecret":"owned-pull"}`, append(installed(false, false), managed("owned-pull", func(s *corev1.Secret) {
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ServiceAccount", Name: "x", UID: "u"}}
		})), nil, 422, "managed by its owner ServiceAccount/x"},
		{"a Flux-applied pull secret", `{"candidate":"` + testCandidate + `","pullSecret":"flux-pull"}`, append(installed(false, false), managed("flux-pull", func(s *corev1.Secret) {
			s.Labels = map[string]string{"kustomize.toolkit.fluxcd.io/name": "bootstrap-kit"}
		})), nil, 422, "managed by Flux"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(c.objs...)
			w := put(t, route(cs, c.env), admin, c.body)
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("code %d, want %d with %q: %s", w.Code, c.code, c.want, w.Body)
			}
			if created(cs, "jobs") != 0 {
				t.Fatal("a refused request created a Job")
			}
		})
	}
	t.Run("an ended release does not block the next", func(t *testing.T) {
		cs := fake.NewSimpleClientset(append(installed(false, false), done)...)
		if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","dryRun":true}`); w.Code != http.StatusAccepted {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
	})
}

func TestReleaseDryRunIsTheStageAlone(t *testing.T) {
	cs := fake.NewSimpleClientset(installed(false, false)...)
	w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","dryRun":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	job := onlyJob(t, cs)
	pod := job.Spec.Template.Spec
	if len(pod.InitContainers) != 0 || len(pod.Containers) != 1 || pod.Containers[0].Name != "stage" {
		t.Fatalf("a dry run runs the stage container alone; init %d, containers %v", len(pod.InitContainers), pod.Containers)
	}
	c := pod.Containers[0]
	if c.Image != cutoverImage || pod.ServiceAccountName != runnerSA || strings.Join(c.Command, " ") != "/bin/sh /release/release-run.sh stage" {
		t.Fatalf("stage: image %s, SA %s, command %v", c.Image, pod.ServiceAccountName, c.Command)
	}
	if env(c, "PULL_SECRET") != nil {
		t.Fatalf("a dry run names a pull credential to deliver and delete: %v", env(c, "PULL_SECRET"))
	}
	if env(c, "DRY_RUN").Value != "true" || env(c, "CANDIDATE").Value != testCandidate || env(c, "GITEA_ORG").Value != "openova" || env(c, "UPSTREAM_PREFIX").Value != "oci://ghcr.io/openova-io" {
		t.Fatalf("stage env: %v", c.Env)
	}
	scripts, err := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), releaseScriptsConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"release-run.sh", "release-region.sh"} {
		b, _ := releaseScripts.ReadFile("release/" + n)
		if scripts.Data[n] != string(b) {
			t.Fatalf("the scripts ConfigMap does not carry %s as built", n)
		}
	}
	st, err := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ReleaseStatusConfigMap, metav1.GetOptions{})
	if err != nil || st.Data["phase"] != "requested" || st.Data["job"] != job.Name || st.Data["requestedBy"] != admin.Email ||
		!strings.HasPrefix(st.Data["signature"], "unverified:") || st.Data["dryRun"] != "true" {
		t.Fatalf("status %v (%v)", st.Data, err)
	}
}

// A delivery is the bootstrap of the candidate's own mechanism (#7938): the
// stage, then deliver, which runs the candidate's release-run.sh. Nothing of
// the installed step 03 is read or run, so a broken one cannot stop it: the
// second delivery to hw307 failed in the installed step 03, whose repair was in
// the candidate it was delivering.
func TestReleaseDeliveryIsTheBootstrapOfTheCandidatesMechanism(t *testing.T) {
	for name, step03 := range map[string]runtime.Object{
		"installed step 03 works":  nil,
		"installed step 03 broken": cm("cutover-step-03-harbor-prewarm", map[string]string{"podSpec": brokenStep03, "run.sh": "exit 1\n"}),
		"installed step 03 absent": nil,
	} {
		t.Run(name, func(t *testing.T) {
			objs := installed(false, false)
			switch name {
			case "installed step 03 broken":
				objs[2] = step03
			case "installed step 03 absent":
				objs = append(objs[:2], objs[3:]...)
			}
			cs := fake.NewSimpleClientset(objs...)
			w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","pullSecret":"release-pull"}`)
			if w.Code != http.StatusAccepted {
				t.Fatalf("code %d: %s", w.Code, w.Body)
			}
			job := onlyJob(t, cs)
			pod := job.Spec.Template.Spec
			if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "stage" || len(pod.Containers) != 1 || pod.Containers[0].Name != "deliver" {
				t.Fatalf("want init [stage] and [deliver]; have %v and %v", pod.InitContainers, pod.Containers)
			}
			if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != releaseGitSeconds+releaseDeliverySeconds || pod.RestartPolicy != corev1.RestartPolicyNever {
				t.Fatalf("backoff %d, deadline %d, restart %s", *job.Spec.BackoffLimit, *job.Spec.ActiveDeadlineSeconds, pod.RestartPolicy)
			}
			for i, c := range []corev1.Container{pod.InitContainers[0], pod.Containers[0]} {
				want := []string{"/bin/sh /release/release-run.sh stage", "/bin/sh /release/release-run.sh deliver"}[i]
				if strings.Join(c.Command, " ") != want || c.Image != cutoverImage {
					t.Fatalf("%s runs %v in %s, want %q in step 01's image", c.Name, c.Command, c.Image, want)
				}
				for k, v := range map[string]string{"PULL_SECRET": "release-pull", "CUTOVER_HELMRELEASE": defaultCutoverHelmRelease,
					"RELEASE_JOB": job.Name, "UPSTREAM_PREFIX": "oci://ghcr.io/openova-io", "DRY_RUN": "false", "CANDIDATE": testCandidate} {
					if e := env(c, k); e == nil || e.Value != v {
						t.Fatalf("%s sets %s=%v, want %q", c.Name, k, e, v)
					}
				}
				for _, m := range c.VolumeMounts {
					if m.MountPath == "/step-script" {
						t.Fatalf("%s mounts the installed step 03", c.Name)
					}
				}
			}
			var vols []string
			for _, v := range pod.Volumes {
				vols = append(vols, v.Name)
				if v.ConfigMap != nil && v.ConfigMap.Name == "cutover-step-03-harbor-prewarm" {
					t.Fatal("the bootstrap mounts the installed step 03")
				}
			}
			if strings.Join(vols, " ") != "release-scripts release-kit-rewrite release-pins release-state release-secondary-kubeconfigs" {
				t.Fatalf("the bootstrap's volumes: %v", vols)
			}
			if pod.ServiceAccountName != runnerSA {
				t.Fatalf("the bootstrap runs as %q, want step 01's %q", pod.ServiceAccountName, runnerSA)
			}
			for _, a := range cs.Actions() {
				if g, ok := a.(k8stesting.GetAction); ok && g.GetName() == "cutover-step-03-harbor-prewarm" {
					t.Fatal("the route read the installed step 03")
				}
			}
			st, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ReleaseStatusConfigMap, metav1.GetOptions{})
			if !strings.Contains(st.Data["mechanism"], "rc/"+testCandidate+" pins") {
				t.Fatalf("the state does not say which mechanism will run: %q", st.Data["mechanism"])
			}
		})
	}
}

// The bootstrap's release scripts are the cutover chart's own (#7938): the
// chart carries the mechanism a delivery of it runs, the API the copy that
// bootstraps it. They must not drift.
func TestReleaseScriptsAreTheCutoverChartsOwn(t *testing.T) {
	for _, n := range []string{"release-run.sh", "release-region.sh"} {
		api, err := releaseScripts.ReadFile("release/" + n)
		if err != nil {
			t.Fatal(err)
		}
		chart, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(t), "platform/self-sovereign-cutover/chart/files/release", n)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(api, chart) {
			t.Fatalf("%s differs between products/catalyst/bootstrap/api/internal/lifecycle/release/ and platform/self-sovereign-cutover/chart/files/release/: copy one onto the other", n)
		}
	}
}

func TestReleaseFollowerJobIsTheCutoverFollowerPivotJob(t *testing.T) {
	for _, marker := range []bool{false, true} {
		cs := fake.NewSimpleClientset(installed(true, marker)...)
		w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","pullSecret":"release-pull"}`)
		if w.Code != http.StatusAccepted {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		scripts, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), releaseScriptsConfigMap, metav1.GetOptions{})
		var j batchv1.Job
		if err := yaml.Unmarshal([]byte(scripts.Data["follower-job.yaml"]), &j); err != nil {
			t.Fatalf("follower-job.yaml: %v", err)
		}
		c := j.Spec.Template.Spec.Containers[0]
		if j.Namespace != "gitea" || j.Name != "__JOB__" || j.Labels["app.kubernetes.io/managed-by"] != "flux" || strings.Join(c.Command, " ") != "/bin/sh /release/release-region.sh" {
			t.Fatalf("follower Job: ns %s name %s labels %v command %v", j.Namespace, j.Name, j.Labels, c.Command)
		}
		if env(c, "LOCAL_PREFIX") != nil || env(c, "TRUST_SECRET") != nil || env(c, "PHASE").Value != "__PHASE__" ||
			env(c, "CANDIDATE").Value != testCandidate || env(c, "GITEA_PAT").ValueFrom.SecretKeyRef.Name != "cutover-secondary-gitea-pat" ||
			env(c, "UPSTREAM_REPO_URL").Value != "https://github.com/openova-io/openova" || env(c, "GITEA_INTERNAL_URL") == nil {
			t.Fatalf("follower env: %v", c.Env)
		}
		if v := j.Spec.Template.Spec.Volumes; len(v) != 1 || v[0].ConfigMap == nil || v[0].ConfigMap.Name != releaseScriptsConfigMap {
			t.Fatalf("follower volumes: %v", v)
		}
		// The step 03 that runs is the candidate's: whatever the installed one
		// says, the route cannot know; the stage says, from the candidate's.
		st, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ReleaseStatusConfigMap, metav1.GetOptions{})
		if !strings.HasPrefix(st.Data["followerPrewarm"], "unknown until the stage renders the candidate's step 03") || st.Data["regions"] != "2" {
			t.Fatalf("marker %v: followerPrewarm %q, regions %s", marker, st.Data["followerPrewarm"], st.Data["regions"])
		}
	}
}

func TestReleaseStatus(t *testing.T) {
	get := func(cs kubernetes.Interface) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/release", nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, admin))
		w := httptest.NewRecorder()
		route(cs, nil).Status(w, req)
		var v map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		return v
	}
	if v := get(fake.NewSimpleClientset()); v["phase"] != "none" {
		t.Fatalf("no release: %v", v)
	}
	st := cm(ReleaseStatusConfigMap, map[string]string{"phase": "staged", "job": "sovereign-release-9", "candidate": testCandidate})
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "sovereign-release-9", Namespace: testNS}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: testNS, Labels: map[string]string{"job-name": "sovereign-release-9"}},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "stage", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: "prewarm", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if v := get(fake.NewSimpleClientset(st, job, pod)); v["step"] != "prewarm" || v["phase"] != "staged" || v["jobState"] != "running" {
		t.Fatalf("running: %v", v)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	pod.Status.InitContainerStatuses[1].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}
	if v := get(fake.NewSimpleClientset(st, job, pod)); v["failedStep"] != "prewarm" || v["phase"] != "failed" {
		t.Fatalf("failed: %v", v)
	}

	// #7938: the prewarm and the promote run in the delivery Job the
	// candidate's mechanism starts; its step is the one named.
	st2 := cm(ReleaseStatusConfigMap, map[string]string{"phase": "staged", "job": "sovereign-release-9", "deliveryJob": "sovereign-release-9-delivery",
		"mechanism": "bp-self-sovereign-cutover 0.1.900 (chart tree abc), the candidate's"})
	boot := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "sovereign-release-9", Namespace: testNS}}
	bootPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: testNS, Labels: map[string]string{"job-name": "sovereign-release-9"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "deliver", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	delPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: testNS, Labels: map[string]string{"job-name": "sovereign-release-9-delivery"}},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{Name: "prewarm", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	v := get(fake.NewSimpleClientset(st2, boot, bootPod, delPod))
	if v["step"] != "prewarm" || !strings.Contains(fmt.Sprint(v["status"]), "0.1.900") {
		t.Fatalf("during the delivery Job: %v", v)
	}
}

// TestReleaseRouteVerbsAreGranted replays a delivery to a two-region Sovereign
// and checks every call the route made against the ClusterRole the API server
// runs with. A route that needs a verb the role lacks answers 502 on a cluster.
func TestReleaseRouteVerbsAreGranted(t *testing.T) {
	path := filepath.Join(repoRoot(t), "products/catalyst/chart/templates/clusterrole-cutover-driver.yaml")
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(b, &role); err != nil || len(role.Rules) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
	for _, body := range []string{`{"candidate":"` + testCandidate + `","pullSecret":"release-pull"}`, `{"candidate":"` + testCandidate + `","dryRun":true}`} {
		cs := fake.NewSimpleClientset(installed(true, true)...)
		// A second request replaces the status and scripts it finds: update.
		for i := 0; i < 2; i++ {
			if i == 1 {
				jobs, _ := cs.BatchV1().Jobs(testNS).List(context.Background(), metav1.ListOptions{})
				for _, j := range jobs.Items {
					if err := cs.BatchV1().Jobs(testNS).Delete(context.Background(), j.Name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				cs.ClearActions()
			}
			if w := put(t, route(cs, nil), admin, body); w.Code != http.StatusAccepted {
				t.Fatalf("code %d: %s", w.Code, w.Body)
			}
		}
		get := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/release", nil)
		get = get.WithContext(context.WithValue(get.Context(), auth.ClaimsKey, admin))
		route(cs, nil).Status(httptest.NewRecorder(), get)
		seen := 0
		for _, a := range cs.Actions() {
			name := ""
			if g, ok := a.(k8stesting.GetAction); ok {
				name = g.GetName()
			}
			if !granted(role.Rules, a.GetVerb(), a.GetResource().Group, a.GetResource().Resource, name) {
				t.Errorf("%s %s/%s %q is not granted by %s", a.GetVerb(), a.GetResource().Group, a.GetResource().Resource, name, role.Name)
			}
			seen++
		}
		if seen < 8 {
			t.Fatalf("only %d calls were recorded; the replay did not run", seen)
		}
	}
}

func granted(rules []rbacv1.PolicyRule, verb, group, resource, name string) bool {
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v || x == "*" {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		if has(r.APIGroups, group) && has(r.Resources, resource) && has(r.Verbs, verb) &&
			(len(r.ResourceNames) == 0 || (name != "" && has(r.ResourceNames, name))) {
			return true
		}
	}
	return false
}
