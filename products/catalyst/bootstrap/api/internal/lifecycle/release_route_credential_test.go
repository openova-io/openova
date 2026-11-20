package lifecycle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// The route makes the delivery's pull Secret from the credential of the
// request; nothing is made by hand on the Sovereign (release_credential.go).

// assertRetriesOneEviction fails unless a Job of the delivery replaces one Pod
// the cluster took away and fails at once on any exit of its own scripts.
func assertRetriesOneEviction(t *testing.T, what string, spec batchv1.JobSpec) {
	t.Helper()
	p := spec.PodFailurePolicy
	if spec.BackoffLimit == nil || *spec.BackoffLimit != 1 || p == nil || len(p.Rules) != 2 ||
		p.Rules[0].Action != batchv1.PodFailurePolicyActionCount || len(p.Rules[0].OnPodConditions) != 1 ||
		p.Rules[0].OnPodConditions[0].Type != corev1.DisruptionTarget ||
		p.Rules[1].Action != batchv1.PodFailurePolicyActionFailJob || p.Rules[1].OnExitCodes == nil ||
		p.Rules[1].OnExitCodes.Operator != batchv1.PodFailurePolicyOnExitCodesOpNotIn || fmt.Sprint(p.Rules[1].OnExitCodes.Values) != "[0]" {
		b, _ := json.Marshal(spec.PodFailurePolicy)
		t.Fatalf("%s does not replace one evicted Pod (backoffLimit 1, a DisruptionTarget counted) and fail on its scripts' exit: backoffLimit %v, podFailurePolicy %s", what, spec.BackoffLimit, b)
	}
}

// credentialIn names the objects of the fake cluster (ConfigMaps, Jobs,
// Secrets, Events) other than the pull Secret that hold the credential.
func credentialIn(t *testing.T, cs kubernetes.Interface) []string {
	t.Helper()
	ctx := context.Background()
	needles := []string{testCredential.Password, base64.StdEncoding.EncodeToString([]byte(testCredential.Username + ":" + testCredential.Password))}
	var found []string
	check := func(kind, name string, obj any) {
		b, _ := json.Marshal(obj)
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) || bytes.Contains(b, []byte(base64.StdEncoding.EncodeToString([]byte(n)))) {
				found = append(found, kind+" "+name)
				return
			}
		}
	}
	cms, _ := cs.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	for _, o := range cms.Items {
		check("ConfigMap", o.Name, o)
	}
	jobs, _ := cs.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	for _, o := range jobs.Items {
		check("Job", o.Name, o)
	}
	evs, _ := cs.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	for _, o := range evs.Items {
		check("Event", o.Name, o)
	}
	secs, _ := cs.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	for _, o := range secs.Items {
		if o.Name != releasePullSecret {
			check("Secret", o.Name, o)
		}
	}
	return found
}

func TestReleaseRouteMakesThePullSecretFromTheRequestsCredential(t *testing.T) {
	cs := fake.NewSimpleClientset(installed(true, true)...)
	var logs bytes.Buffer
	rt := route(cs, nil)
	rt.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var asked []registryCredential
	rt.checkCredential = func(_ context.Context, c registryCredential) error { asked = append(asked, c); return nil }
	w := put(t, rt, admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if len(asked) != 1 || asked[0] != testCredential {
		t.Fatalf("the registry was asked about %v, want the request's credential once", asked)
	}
	job := onlyJob(t, cs)
	sec, err := cs.CoreV1().Secrets(testNS).Get(context.Background(), releasePullSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the route made no pull Secret %s/%s: %v", testNS, releasePullSecret, err)
	}
	var cfg struct {
		Auths map[string]struct{ Username, Password, Auth string } `json:"auths"`
	}
	if sec.Type != corev1.SecretTypeDockerConfigJson || json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg) != nil || len(cfg.Auths) != 1 ||
		cfg.Auths["ghcr.io"].Auth != base64.StdEncoding.EncodeToString([]byte(testCredential.Username+":"+testCredential.Password)) {
		t.Fatalf("the pull Secret is not a docker configuration with the one auth entry of ghcr.io the cutover's step 03 reads: type %s, %s", sec.Type, sec.Data[corev1.DockerConfigJsonKey])
	}
	if sec.Labels[releaseJobLabel] != job.Name || sec.Labels["app.kubernetes.io/component"] != releaseComponent || sec.Labels[releaseCandidateLabel] != testCandidate {
		t.Fatalf("the pull Secret is not marked as the delivery's: labels %v", sec.Labels)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Kind != "Job" || sec.OwnerReferences[0].Name != job.Name {
		t.Fatalf("the pull Secret is not owned by the delivery's Job %s: %v", job.Name, sec.OwnerReferences)
	}
	for _, a := range cs.Actions() {
		if c, ok := a.(k8stesting.CreateActionImpl); ok && a.GetResource().Resource == "secrets" && c.GetCreateOptions().FieldManager != releaseFieldManager {
			t.Fatalf("the pull Secret is created with the field manager %q, want %q", c.GetCreateOptions().FieldManager, releaseFieldManager)
		}
	}
	for _, c := range append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...) {
		if e := env(c, "PULL_SECRET"); e == nil || e.Value != releasePullSecret {
			t.Fatalf("%s names the pull Secret %v, want %s", c.Name, e, releasePullSecret)
		}
	}
	st := statusOf(t, cs)
	if _, old := st["pullSecret"]; old || !strings.Contains(st["credential"], "held in the Secret "+testNS+"/"+releasePullSecret) {
		t.Fatalf("the state: credential %q, pullSecret present %v", st["credential"], old)
	}
	if found := credentialIn(t, cs); len(found) > 0 {
		t.Fatalf("the credential is in %v, which are not the pull Secret", found)
	}
	for where, text := range map[string]string{"the log": logs.String(), "the answer": w.Body.String()} {
		if strings.Contains(text, testCredential.Password) || !strings.Contains(logs.String(), "release requested") {
			t.Fatalf("the credential is in %s, or nothing was logged:\n%s", where, text)
		}
	}
}

// Every refusal of a credential names its cause and comes before anything is
// written: no pull Secret, no state, no scripts, no Job; the answer and the
// log carry no part of the credential.
func TestReleaseCredentialRefusalsWriteNothing(t *testing.T) {
	for _, c := range []struct {
		name, body, registry, want string
		code                       int
	}{
		{"no credential", `{"candidate":"` + testCandidate + `"}`, "", "registry-credential-missing", 422},
		{"no username", `{"candidate":"` + testCandidate + `","credential":{"registry":"ghcr.io","password":"` + testCredential.Password + `"}}`, "", "has no username", 422},
		{"another registry", `{"candidate":"` + testCandidate + `","credential":{"registry":"quay.io","username":"u","password":"` + testCredential.Password + `"}}`, "", "a delivery copies from ghcr.io", 422},
		{"refused by the registry", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, "403", "ghcr.io refused the credential (HTTP 403)", 422},
		{"the registry unreachable", `{"candidate":"` + testCandidate + `",` + testCredentialJSON + `}`, "unreachable", "cannot ask ghcr.io whether it takes the credential", 502},
		{"the removed field", `{"candidate":"` + testCandidate + `","pullSecret":"release-pull"}`, "", "Send the upstream registry's credential in the request instead", 400},
	} {
		t.Run(c.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset(installed(true, true)...)
			var logs bytes.Buffer
			rt := route(cs, map[string]string{"test:registry": c.registry})
			rt.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			w := put(t, rt, admin, c.body)
			if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("code %d, want %d with %q: %s", w.Code, c.code, c.want, w.Body)
			}
			if c.code != 400 && !strings.Contains(w.Body.String(), "nothing was staged") {
				t.Fatalf("the answer does not say nothing was staged: %s", w.Body)
			}
			for _, r := range []string{"secrets", "configmaps", "jobs"} {
				if n := created(cs, r); n != 0 {
					t.Fatalf("a refused request created %d %s", n, r)
				}
			}
			for _, a := range cs.Actions() {
				if a.GetVerb() == "update" || a.GetVerb() == "patch" {
					t.Fatalf("a refused request wrote: %s %s", a.GetVerb(), a.GetResource().Resource)
				}
			}
			if strings.Contains(w.Body.String()+logs.String(), testCredential.Password) {
				t.Fatalf("the credential is in the answer or the log:\n%s\n%s", w.Body, logs.String())
			}
		})
	}
}

// leftover is the pull Secret an earlier delivery's killed Job left.
func leftover() *corev1.Secret {
	s, _ := pullSecretFor(testNS, "sovereign-release-1", testCandidate, registryCredential{Registry: "ghcr.io", Username: "old", Password: "an-older-credential"})
	return s
}

func releaseJob(name string, ended bool) *batchv1.Job {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, Labels: map[string]string{"app.kubernetes.io/component": releaseComponent}}}
	if ended {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	}
	return j
}

func pullSecretOf(t *testing.T, cs kubernetes.Interface) *corev1.Secret {
	t.Helper()
	s, err := cs.CoreV1().Secrets(testNS).Get(context.Background(), releasePullSecret, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return s
}

// A pull Secret a killed release Job left is removed when the next request
// arrives, whatever it asks, and on a read of the state once no release Job
// runs; a running Job's is kept.
func TestReleasePullSecretLeftoverIsRemoved(t *testing.T) {
	get := func(cs kubernetes.Interface) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/release", nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, admin))
		w := httptest.NewRecorder()
		route(cs, nil).Status(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET: code %d: %s", w.Code, w.Body)
		}
	}
	t.Run("a dry run removes it and makes none", func(t *testing.T) {
		cs := fake.NewSimpleClientset(append(installed(false, false), releaseJob("sovereign-release-1", true), leftover())...)
		if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","dryRun":true}`); w.Code != http.StatusAccepted {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		if s := pullSecretOf(t, cs); s != nil {
			t.Fatalf("the leftover pull Secret is still there: labels %v", s.Labels)
		}
	})
	t.Run("a delivery replaces it with its own", func(t *testing.T) {
		cs := fake.NewSimpleClientset(append(installed(false, false), releaseJob("sovereign-release-1", true), leftover())...)
		if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`); w.Code != http.StatusAccepted {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		s := pullSecretOf(t, cs)
		if s == nil || s.Labels[releaseJobLabel] == "sovereign-release-1" || bytes.Contains(s.Data[corev1.DockerConfigJsonKey], []byte("an-older-credential")) {
			t.Fatalf("the delivery did not replace the leftover with its own pull Secret: %v", s)
		}
	})
	t.Run("a rollback removes it", func(t *testing.T) {
		cs := fake.NewSimpleClientset(append(installed(true, true), promotedState(testCandidate, "region-b"), leftover())...)
		if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`","rollback":true,"region":"region-b"}`); w.Code != http.StatusAccepted {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		if pullSecretOf(t, cs) != nil {
			t.Fatal("the leftover pull Secret is still there after a rollback request")
		}
	})
	t.Run("a read of the state removes it once no Job runs", func(t *testing.T) {
		cs := fake.NewSimpleClientset(releaseJob("sovereign-release-1", true), leftover())
		get(cs)
		if pullSecretOf(t, cs) != nil {
			t.Fatal("a read of the state kept the pull Secret of a Job that ended")
		}
	})
	t.Run("a read of the state keeps a running Job's", func(t *testing.T) {
		cs := fake.NewSimpleClientset(releaseJob("sovereign-release-1", false), leftover())
		get(cs)
		if pullSecretOf(t, cs) == nil {
			t.Fatal("a read of the state removed the pull Secret of a Job that runs")
		}
	})
}

// When the Job cannot be created the route makes no pull Secret; when the
// pull Secret cannot be made the route deletes the Job it just created.
func TestReleasePullSecretAndJobLiveTogether(t *testing.T) {
	t.Run("the Job cannot be created", func(t *testing.T) {
		cs := fake.NewSimpleClientset(installed(false, false)...)
		cs.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("admission webhook refused it")
		})
		if w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`); w.Code != http.StatusBadGateway {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		if pullSecretOf(t, cs) != nil {
			t.Fatal("a request whose Job was not created left a pull Secret")
		}
	})
	t.Run("the pull Secret cannot be made", func(t *testing.T) {
		cs := fake.NewSimpleClientset(installed(false, false)...)
		cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("quota exceeded")
		})
		w := put(t, route(cs, nil), admin, `{"candidate":"`+testCandidate+`",`+testCredentialJSON+`}`)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "credential-write-failed") {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		if jobs, _ := cs.BatchV1().Jobs(testNS).List(context.Background(), metav1.ListOptions{}); len(jobs.Items) != 0 {
			t.Fatalf("a Job without its pull Secret is left: %s", jobs.Items[0].Name)
		}
		if st := statusOf(t, cs); st["phase"] != "failed" || !strings.Contains(st["message"], "nothing was staged") {
			t.Fatalf("the state: %v", st)
		}
	})
}

// The registry check speaks the distribution API's sign-in, over TLS only.
func TestRegistryProbe(t *testing.T) {
	user, pass := testCredential.Username, testCredential.Password
	var tokenAsked string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		switch r.URL.Path {
		case "/v2/":
			if r.Header.Get("X-Test") == "basic" {
				if u == user && p == pass {
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="https://%s/token",service="ghcr.io",scope="repository:user/image:pull"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			tokenAsked = r.URL.RawQuery
			if u != user || p != pass {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"token":"t"}`))
		}
	}))
	defer srv.Close()
	probe := registryProbe{client: srv.Client(), base: srv.URL}
	ok := registryCredential{Registry: "ghcr.io", Username: user, Password: pass}
	bad := registryCredential{Registry: "ghcr.io", Username: user, Password: "wrong"}
	if err := probe.check(context.Background(), ok); err != nil || !strings.Contains(tokenAsked, "service=ghcr.io") {
		t.Fatalf("a credential the token realm takes: %v (asked %q)", err, tokenAsked)
	}
	var refused *errCredentialRefused
	if err := probe.check(context.Background(), bad); !errors.As(err, &refused) || refused.status != http.StatusForbidden || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("a credential the token realm refuses: %v", err)
	}
	basic := registryProbe{client: &http.Client{Transport: headerTransport{srv.Client().Transport}}, base: srv.URL}
	if err := basic.check(context.Background(), ok); err != nil {
		t.Fatalf("a registry that takes the credential as basic auth: %v", err)
	}
	if err := basic.check(context.Background(), bad); !errors.As(err, &refused) || refused.status != http.StatusUnauthorized {
		t.Fatalf("a registry that refuses the credential as basic auth: %v", err)
	}
	srv.Close()
	if err := probe.check(context.Background(), ok); err == nil || errors.As(err, &refused) || strings.Contains(err.Error(), pass) {
		t.Fatalf("a registry that cannot be reached is not a refusal, and the error names no credential: %v", err)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="http://example.test/token",service="ghcr.io"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer plain.Close()
	if err := (registryProbe{client: plain.Client(), base: plain.URL}).check(context.Background(), ok); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("a token realm over plain http is sent no credential: %v", err)
	}
}

// headerTransport marks requests for the test registry's basic-auth answer.
type headerTransport struct{ next http.RoundTripper }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Test", "basic")
	return h.next.RoundTrip(r)
}
