package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/core/cmd/k8s-ws-proxy/internal/auth"
)

const aliasLabel = "app.kubernetes.io/name"

func pod(name, node string, phase corev1.PodPhase, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "catalyst-system", Labels: labels},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestLabelPodResolver_LiteralPodWins(t *testing.T) {
	cs := fake.NewSimpleClientset(
		pod("k8s-ws-proxy-abc", "node-a", corev1.PodRunning, map[string]string{aliasLabel: "k8s-ws-proxy"}),
	)
	r := NewLabelPodResolver(cs, aliasLabel, "node-a")
	got, err := r.Resolve(context.Background(), "catalyst-system", "k8s-ws-proxy-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "k8s-ws-proxy-abc" {
		t.Fatalf("got %q, want the literal name back untouched", got)
	}
}

func TestLabelPodResolver_AliasResolvesToNodeLocalRunningPod(t *testing.T) {
	cs := fake.NewSimpleClientset(
		pod("k8s-ws-proxy-remote", "node-b", corev1.PodRunning, map[string]string{aliasLabel: "k8s-ws-proxy"}),
		pod("k8s-ws-proxy-local", "node-a", corev1.PodRunning, map[string]string{aliasLabel: "k8s-ws-proxy"}),
	)
	r := NewLabelPodResolver(cs, aliasLabel, "node-a")
	got, err := r.Resolve(context.Background(), "catalyst-system", "k8s-ws-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if got != "k8s-ws-proxy-local" {
		t.Fatalf("got %q, want the node-local pod (the Service in front of this DaemonSet is internalTrafficPolicy: Local)", got)
	}
}

// TestLabelPodResolver_NoMatchIsAHardFailure — the fallback must not be
// open. A workload name nothing carries has to fail, not pick a pod that
// happens to be nearby.
func TestLabelPodResolver_NoMatchIsAHardFailure(t *testing.T) {
	cs := fake.NewSimpleClientset(
		pod("unrelated", "node-a", corev1.PodRunning, map[string]string{aliasLabel: "something-else"}),
	)
	r := NewLabelPodResolver(cs, aliasLabel, "node-a")
	if got, err := r.Resolve(context.Background(), "catalyst-system", "k8s-ws-proxy"); err == nil {
		t.Fatalf("resolved %q for a workload name nothing carries — the fallback is open", got)
	}
}

// TestLabelPodResolver_SkipsNonRunning — a Pending or Succeeded Pod
// cannot serve an exec stream; selecting one would produce a connection
// that renders and then fails on click, which is the whole defect
// class #5991 is about.
func TestLabelPodResolver_SkipsNonRunning(t *testing.T) {
	cs := fake.NewSimpleClientset(
		pod("proxy-pending", "node-a", corev1.PodPending, map[string]string{aliasLabel: "k8s-ws-proxy"}),
		pod("proxy-running", "node-b", corev1.PodRunning, map[string]string{aliasLabel: "k8s-ws-proxy"}),
	)
	r := NewLabelPodResolver(cs, aliasLabel, "node-a")
	got, err := r.Resolve(context.Background(), "catalyst-system", "k8s-ws-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if got != "proxy-running" {
		t.Fatalf("got %q, want proxy-running — a non-Running pod was selected on node-locality alone", got)
	}
}

func TestNewLabelPodResolver_NilWhenLabelUnset(t *testing.T) {
	if r := NewLabelPodResolver(fake.NewSimpleClientset(), "", "node-a"); r != nil {
		t.Fatal("resolver built with an empty alias label — the default must be literal pod names with NO apiserver read")
	}
}

// ── call-site pins ────────────────────────────────────────────────────

// recordingResolver reports what the handler asked for and what it was
// told, so the tests below can assert the handler CONSULTS the resolver
// and DIALS the resolved name — not merely that the resolver works.
type recordingResolver struct {
	sawNamespace string
	sawSegment   string
	give         string
	err          error
}

func (r *recordingResolver) Resolve(_ context.Context, ns, segment string) (string, error) {
	r.sawNamespace, r.sawSegment = ns, segment
	return r.give, r.err
}

func signedRequest(t *testing.T, url, path string, secret []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(now, 10))
	req.Header.Set(auth.HeaderHMAC, auth.ComputeHex(secret, now, path))
	return req
}

// TestServeHTTP_ConsultsPodResolver pins the CALL SITE. podresolve.go
// can be perfect and the feature still dead if ServeHTTP never calls it;
// this asserts the handler passed the URL's namespace + pod segment in,
// which is the only observable that distinguishes "wired" from "written".
func TestServeHTTP_ConsultsPodResolver(t *testing.T) {
	secret := []byte("k")
	v, err := auth.NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr := &recordingResolver{give: "k8s-ws-proxy-xyz"}
	h, err := New(HandlerOptions{
		Verifier:          v,
		RESTConfig:        &rest.Config{Host: "https://127.0.0.1:1"},
		PodResolver:       rr,
		AllowedNamespaces: []string{"nothing-matches"}, // stop before the apiserver dial
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	path := "/api/v1/namespaces/catalyst-system/pods/k8s-ws-proxy/exec"
	resp, err := http.DefaultClient.Do(signedRequest(t, srv.URL, path, secret))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The namespace gate runs BEFORE resolution, so a denied namespace
	// proves nothing about the resolver. Use an allowed namespace and
	// assert on what the resolver was asked.
	if rr.sawSegment != "" {
		t.Fatalf("resolver consulted despite the namespace gate firing first (saw %q) — ordering regressed", rr.sawSegment)
	}

	h2, err := New(HandlerOptions{
		Verifier:          v,
		RESTConfig:        &rest.Config{Host: "https://127.0.0.1:1"},
		PodResolver:       rr,
		AllowedNamespaces: []string{"catalyst-system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	resp2, err := http.DefaultClient.Do(signedRequest(t, srv2.URL, path, secret))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if rr.sawNamespace != "catalyst-system" || rr.sawSegment != "k8s-ws-proxy" {
		t.Fatalf("resolver saw (%q, %q), want (catalyst-system, k8s-ws-proxy) — ServeHTTP does not consult the resolver",
			rr.sawNamespace, rr.sawSegment)
	}
}

// TestServeHTTP_ResolverFailureIs404 — an unresolvable workload name
// must end the request, not fall through to a dial of the raw segment.
func TestServeHTTP_ResolverFailureIs404(t *testing.T) {
	secret := []byte("k")
	v, _ := auth.NewVerifier(secret, 0)
	h, err := New(HandlerOptions{
		Verifier:          v,
		RESTConfig:        &rest.Config{Host: "https://127.0.0.1:1"},
		PodResolver:       &recordingResolver{err: ErrNoPodForAlias},
		AllowedNamespaces: []string{"catalyst-system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	path := "/api/v1/namespaces/catalyst-system/pods/ghost/exec"
	resp, err := http.DefaultClient.Do(signedRequest(t, srv.URL, path, secret))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when the pod segment resolves to nothing", resp.StatusCode)
	}
}

// ── the apiserver-shaped path parser ──────────────────────────────────

func TestParseAPIServerExecPath(t *testing.T) {
	cases := []struct {
		path        string
		ns, pod     string
		ok          bool
		explanation string
	}{
		{"/api/v1/namespaces/guacamole/pods/guacd-1/exec", "guacamole", "guacd-1", true, "the shape guacd emits"},
		{"/api/v1/namespaces/ns/pods/p/attach", "", "", false, "attach is a different subresource; answering it with exec would lie about the session"},
		{"/api/v1/namespaces/ns/pods/p/exec/extra", "", "", false, "trailing segment"},
		{"/api/v1/namespaces//pods/p/exec", "", "", false, "empty namespace"},
		{"/api/v1/namespaces/ns/pods//exec", "", "", false, "empty pod"},
		{"/api/v1/namespaces/ns/services/p/exec", "", "", false, "not the pods resource"},
		{"/api/v1/nodes/n/proxy", "", "", false, "unrelated apiserver path"},
		{"/proxy/exec/ns/pod/c", "", "", false, "the canonical route is handled elsewhere"},
	}
	for _, c := range cases {
		ns, pod, ok := parseAPIServerExecPath(c.path)
		if ok != c.ok || ns != c.ns || pod != c.pod {
			t.Fatalf("parseAPIServerExecPath(%q) = (%q,%q,%v), want (%q,%q,%v) — %s",
				c.path, ns, pod, ok, c.ns, c.pod, c.ok, c.explanation)
		}
	}
}

// TestBuildExecURL_OmitsEmptyContainer — guacd omits the container
// parameter when the connection declares none. Forwarding container=
// (empty) asks the apiserver for a container literally named "", which
// 404s; omitting it selects the Pod's default container.
func TestBuildExecURL_OmitsEmptyContainer(t *testing.T) {
	u, err := buildExecURL("https://api", "ns", "pod", "", []string{"/bin/sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := u.Query()["container"]; present {
		t.Fatalf("query carries an empty container parameter: %s", u.RawQuery)
	}
	u2, err := buildExecURL("https://api", "ns", "pod", "app", []string{"/bin/sh"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if u2.Query().Get("container") != "app" {
		t.Fatalf("a declared container was dropped: %s", u2.RawQuery)
	}
}
