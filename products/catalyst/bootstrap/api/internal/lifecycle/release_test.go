package lifecycle

import (
	"bytes"
	"context"
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

// testCredentialJSON is the field credential of a delivery request in these
// tests: testCredential (release_credential_test.go) as JSON.
var testCredentialJSON = func() string {
	b, err := json.Marshal(testCredential)
	if err != nil {
		panic(err)
	}
	return `"credential":` + string(b)
}()

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
	}
	if followers {
		job, _ := yaml.Marshal(followerPivotJob("http://gitea-http.gitea.svc.cluster.local:3000"))
		objs = append(objs,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secondaryKubeconfigs, Namespace: testNS}, Data: map[string][]byte{"region-b.yaml": []byte("kind: Config\n")}},
			cm(followerPivotConfigMap, map[string]string{"secondary-pivot.sh": "#!/bin/sh\n", "secondary-pivot-job.yaml": string(job)}))
	}
	return objs
}

// route is the route on cs. The registry takes every credential, unless env
// "test:registry" says it answers 403 or cannot be reached.
func route(cs kubernetes.Interface, env map[string]string) *ReleaseRoute {
	return &ReleaseRoute{
		clients: func() (kubernetes.Interface, error) { return cs, nil },
		now:     func() time.Time { return time.Unix(1763283600, 0) },
		getenv:  func(k string) string { return env[k] },
		checkCredential: func(_ context.Context, c registryCredential) error {
			switch env["test:registry"] {
			case "403":
				return &errCredentialRefused{status: http.StatusForbidden}
			case "unreachable":
				return fmt.Errorf("GET https://%s/v2/: dial tcp: lookup %s: no such host", c.Registry, c.Registry)
			}
			return nil
		},
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
	followersOnly := append(installed(false, false),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secondaryKubeconfigs, Namespace: testNS}, Data: map[string][]byte{"region-b.yaml": []byte("x")}})
	// SOV-521: what an earlier request recorded, of this candidate or another.
	primaryOnly := append(installed(true, true), promotedState(testCandidate, "primary"))
	otherCandidate := append(installed(true, true), promotedState("20261116T080000Z-0123456", "region-b"))
	for _, c := range []struct {
		name, body string
		objs       []runtime.Object
		env        map[string]string
		code       int
		want       string
	}{
		{"an unknown field", `{"candidate":"` + testCandidate + `","force":true}`, installed(false, false), nil, 400, "bad-body"},
		{"not a candidate id", `{"candidate":"main"}`, installed(false, false), nil, 400, "bad-candidate"},
		// The removed field: a Secret made by hand is no input any more.
		{"the removed field pullSecret", `{"candidate":"` + testCandidate + `","pullSecret":"release-pull"}`, installed(false, false), nil, 400, "pullSecret is removed: the route makes the delivery's pull Secret itself"},
		{"the removed field pullSecret beside a credential", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `,"pullSecret":"x"}`, installed(false, false), nil, 400, "pull-secret-removed"},
		// A dry run copies nothing from the registry: a credential would
		// travel for nothing.
		{"a credential in a dry run", `{"candidate":"` + testCandidate + `","dryRun":true,` + testCredentialJSON + `}`, installed(false, false), nil, 400, "dry-run-credential"},
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
		// The credential of a delivery: missing, incomplete, for another
		// registry, refused by the registry, or the registry unreachable; each
		// named before anything is written (TestReleaseCredentialRefusalsWriteNothing).
		{"a delivery without a credential", `{"candidate":"` + testCandidate + `"}`, installed(false, false), nil, 422, "registry-credential-missing"},
		{"a credential without a password", `{"candidate":"` + testCandidate + `","credential":{"registry":"ghcr.io","username":"openova-bot"}}`, installed(false, false), nil, 422, "has no password"},
		{"a credential for another registry", `{"candidate":"` + testCandidate + `","credential":{"registry":"docker.io","username":"u","password":"p"}}`, installed(false, false), nil, 422, "registry-credential-registry"},
		{"a credential the registry refuses", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, installed(false, false), map[string]string{"test:registry": "403"}, 422, "ghcr.io refused the credential (HTTP 403)"},
		{"a registry that cannot be reached", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, installed(false, false), map[string]string{"test:registry": "unreachable"}, 502, "registry-unreachable"},
		// SOV-521: region names primary or a follower; a rollback names a
		// region the state records a promote of this candidate for, and
		// carries no dry run and no pull credential.
		{"a region that is not one of the Sovereign", `{"candidate":"` + testCandidate + `","dryRun":true,"region":"region-z"}`, installed(true, true), nil, 400, "bad-region"},
		{"a follower name on a Sovereign without followers", `{"candidate":"` + testCandidate + `","dryRun":true,"region":"region-b"}`, installed(false, false), nil, 400, "bad-region"},
		{"a rollback without a region", `{"candidate":"` + testCandidate + `","rollback":true}`, installed(true, true), nil, 400, "rollback-without-region"},
		{"a rollback with a dry run", `{"candidate":"` + testCandidate + `","rollback":true,"region":"region-b","dryRun":true}`, installed(true, true), nil, 400, "rollback-dry-run"},
		{"a rollback with a credential", `{"candidate":"` + testCandidate + `","rollback":true,"region":"region-b",` + testCredentialJSON + `}`, installed(true, true), nil, 400, "rollback-credential"},
		{"a rollback of a region this candidate never promoted", `{"candidate":"` + testCandidate + `","rollback":true,"region":"region-b"}`, primaryOnly, nil, 409, "not-promoted"},
		{"a rollback when the state is of another candidate", `{"candidate":"` + testCandidate + `","rollback":true,"region":"region-b"}`, otherCandidate, nil, 409, "not-promoted"},
		{"a rollback with no state", `{"candidate":"` + testCandidate + `","rollback":true,"region":"primary"}`, installed(true, true), nil, 409, "not-promoted"},
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
			w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`)
			if w.Code != http.StatusAccepted {
				t.Fatalf("code %d: %s", w.Code, w.Body)
			}
			job := onlyJob(t, cs)
			pod := job.Spec.Template.Spec
			if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "stage" || len(pod.Containers) != 1 || pod.Containers[0].Name != "deliver" {
				t.Fatalf("want init [stage] and [deliver]; have %v and %v", pod.InitContainers, pod.Containers)
			}
			if *job.Spec.ActiveDeadlineSeconds != releaseGitSeconds+releaseDeliverySeconds || pod.RestartPolicy != corev1.RestartPolicyNever {
				t.Fatalf("deadline %d, restart %s", *job.Spec.ActiveDeadlineSeconds, pod.RestartPolicy)
			}
			assertRetriesOneEviction(t, "the bootstrap Job", job.Spec)
			for i, c := range []corev1.Container{pod.InitContainers[0], pod.Containers[0]} {
				want := []string{"/bin/sh /release/release-run.sh stage", "/bin/sh /release/release-run.sh deliver"}[i]
				if strings.Join(c.Command, " ") != want || c.Image != cutoverImage {
					t.Fatalf("%s runs %v in %s, want %q in step 01's image", c.Name, c.Command, c.Image, want)
				}
				for k, v := range map[string]string{"PULL_SECRET": releasePullSecret, "CUTOVER_HELMRELEASE": defaultCutoverHelmRelease,
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
		w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`)
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
		assertRetriesOneEviction(t, "the follower Job", j.Spec)
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
	for _, body := range []string{`{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, `{"candidate":"` + testCandidate + `","dryRun":true}`,
		`{"candidate":"` + testCandidate + `",` + testCredentialJSON + `,"region":"region-b"}`, `{"candidate":"` + testCandidate + `","rollback":true,"region":"region-b"}`} {
		cs := fake.NewSimpleClientset(append(installed(true, true), promotedState(testCandidate, "region-b"))...)
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
			if d, ok := a.(k8stesting.DeleteAction); ok {
				name = d.GetName()
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

// The commits a promote recorded in the tests' state ConfigMap.
const (
	recordedMain     = "1111111111111111111111111111111111111111"
	recordedPromoted = "2222222222222222222222222222222222222222"
)

// promotedState is the state ConfigMap after candidate's promote of regions.
func promotedState(candidate string, regions ...string) *corev1.ConfigMap {
	data := map[string]string{"candidate": candidate, "phase": "delivered", "job": "sovereign-release-1", "primary.stage": "region=primary phase=staged"}
	for _, r := range regions {
		data[r+".promotedFrom"], data[r+".promotedTo"], data[r+".phase"] = recordedMain, recordedPromoted, "promoted"
	}
	return cm(ReleaseStatusConfigMap, data)
}

func statusOf(t *testing.T, cs kubernetes.Interface) map[string]string {
	t.Helper()
	st, err := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), ReleaseStatusConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st.Data
}

// SOV-521: a request that names a region gives it to every container of the
// bootstrap, which hands it on to the candidate's mechanism; a whole request
// names none, as before.
func TestReleaseRegionRequestNamesTheRegionToTheBootstrap(t *testing.T) {
	for _, c := range []struct{ name, body, region string }{
		{"region b", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `,"region":"region-b"}`, "region-b"},
		{"the primary, dry run", `{"candidate":"` + testCandidate + `","dryRun":true,"region":"primary"}`, "primary"},
		{"every region", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(installed(true, true)...)
			w := put(t, route(cs, nil), admin, c.body)
			if w.Code != http.StatusAccepted {
				t.Fatalf("code %d: %s", w.Code, w.Body)
			}
			pod := onlyJob(t, cs).Spec.Template.Spec
			for _, ctr := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
				e := env(ctr, "RELEASE_REGION")
				switch {
				case c.region == "" && e != nil:
					t.Fatalf("a whole request sets RELEASE_REGION=%q in %s", e.Value, ctr.Name)
				case c.region != "" && (e == nil || e.Value != c.region):
					t.Fatalf("%s sets RELEASE_REGION %v, want %q", ctr.Name, e, c.region)
				}
				for _, n := range []string{"PROMOTED_FROM", "PROMOTED_TO"} {
					if env(ctr, n) != nil {
						t.Fatalf("a delivery sets %s in %s", n, ctr.Name)
					}
				}
			}
			st := statusOf(t, cs)
			if st["region"] != c.region || st["rollback"] != "" {
				t.Fatalf("state region %q rollback %q, want region %q", st["region"], st["rollback"], c.region)
			}
		})
	}
}

// SOV-521: a rollback is the rollback container alone, with the commits the
// promote of this candidate recorded for the region; no stage, no prewarm, no
// credential.
func TestReleaseRollbackRequestIsTheRollbackContainerAlone(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed(true, true), promotedState(testCandidate, "primary", "region-b"))...)
	w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","rollback":true,"region":"region-b"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	job := onlyJob(t, cs)
	pod := job.Spec.Template.Spec
	if len(pod.InitContainers) != 0 || len(pod.Containers) != 1 || pod.Containers[0].Name != "rollback" {
		t.Fatalf("a rollback runs the rollback container alone; init %v, containers %v", pod.InitContainers, pod.Containers)
	}
	c := pod.Containers[0]
	if strings.Join(c.Command, " ") != "/bin/sh /release/release-run.sh rollback" || c.Image != cutoverImage || pod.ServiceAccountName != runnerSA {
		t.Fatalf("rollback: command %v, image %s, SA %s", c.Command, c.Image, pod.ServiceAccountName)
	}
	for k, v := range map[string]string{"RELEASE_REGION": "region-b", "PROMOTED_FROM": recordedMain, "PROMOTED_TO": recordedPromoted, "CANDIDATE": testCandidate} {
		if e := env(c, k); e == nil || e.Value != v {
			t.Fatalf("rollback sets %s=%v, want %q", k, e, v)
		}
	}
	if env(c, "PULL_SECRET") != nil || *job.Spec.ActiveDeadlineSeconds != releaseGitSeconds {
		t.Fatalf("a rollback names a pull credential (%v) or has the deadline of a delivery (%d)", env(c, "PULL_SECRET"), *job.Spec.ActiveDeadlineSeconds)
	}
	scripts, _ := cs.CoreV1().ConfigMaps(testNS).Get(context.Background(), releaseScriptsConfigMap, metav1.GetOptions{})
	if scripts.Data["follower-job.yaml"] == "" {
		t.Fatal("the scripts of a rollback in a follower region lack the follower Job")
	}
	st := statusOf(t, cs)
	if st["phase"] != "requested" || st["rollback"] != "true" || st["region"] != "region-b" || st["region-b.promotedTo"] != recordedPromoted || st["primary.stage"] != "" {
		t.Fatalf("state %v", st)
	}
}

// SOV-521: across the requests of one candidate the state keeps what each
// region's promote recorded, and GET answers it; a request of another
// candidate starts clean.
func TestReleaseStateKeepsWhatEachRegionPromotedAcrossTheRequestsOfOneCandidate(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installed(true, true), promotedState(testCandidate, "primary", "region-b"))...)
	if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","dryRun":true,"region":"region-b"}`); w.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	st := statusOf(t, cs)
	for _, r := range []string{"primary", "region-b"} {
		if st[r+".promotedFrom"] != recordedMain || st[r+".promotedTo"] != recordedPromoted || st[r+".phase"] != "promoted" {
			t.Fatalf("a second request of the candidate lost what %s recorded: %v", r, st)
		}
	}
	if st["phase"] != "requested" || st["primary.stage"] != "" {
		t.Fatalf("the rest of the state is the new request's: %v", st)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/release", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, admin))
	w := httptest.NewRecorder()
	route(cs, nil).Status(w, req)
	var v struct {
		Promoted map[string]map[string]string `json:"promoted"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil ||
		v.Promoted["region-b"]["promotedFrom"] != recordedMain || v.Promoted["region-b"]["promotedTo"] != recordedPromoted || v.Promoted["primary"]["phase"] != "promoted" {
		t.Fatalf("GET: code %d, %s", w.Code, w.Body)
	}

	jobs, _ := cs.BatchV1().Jobs(testNS).List(context.Background(), metav1.ListOptions{})
	for _, j := range jobs.Items {
		if err := cs.BatchV1().Jobs(testNS).Delete(context.Background(), j.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if w := put(t, route(cs, nil), admin, `{"candidate":"20261116T100000Z-fedcba9","dryRun":true}`); w.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	for k := range statusOf(t, cs) {
		if strings.HasSuffix(k, ".promotedTo") || strings.HasSuffix(k, ".promotedFrom") {
			t.Fatalf("a request of another candidate kept %s", k)
		}
	}
}
