package suite

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// baoAnswerSpec is how the test server answers one path: an HTTP code, a
// Location for a redirect and a body. The answers of a path are given in
// order; the last one repeats.
type baoAnswerSpec struct {
	code     int
	location string
	body     string
}

// baoServer is one TLS test server that answers for bao.sov.test. A path that
// is not in answers gets 404. It counts the requests per path.
type baoServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	answers map[string][]baoAnswerSpec
	asked   map[string]int
}

func newBaoServer(t *testing.T, answers map[string][]baoAnswerSpec) *baoServer {
	t.Helper()
	b := &baoServer{answers: answers, asked: map[string]int{}}
	b.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		n := b.asked[r.URL.Path]
		b.asked[r.URL.Path] = n + 1
		specs := b.answers[r.URL.Path]
		b.mu.Unlock()
		a := baoAnswerSpec{code: http.StatusNotFound}
		if len(specs) > 0 {
			a = specs[min(n, len(specs)-1)]
		}
		if a.location != "" {
			w.Header().Set("Location", a.location)
		}
		w.WriteHeader(a.code)
		_, _ = io.WriteString(w, a.body)
	}))
	b.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes of the untrusted case
	b.srv.StartTLS()
	t.Cleanup(b.srv.Close)
	return b
}

// transport dials the test server for every host. trusted: it trusts the
// certificate of the test server, which is issued for example.com.
func (b *baoServer) transport(trusted bool) http.RoundTripper {
	addr := b.srv.Listener.Addr().String()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if trusted {
		roots := x509.NewCertPool()
		roots.AddCert(b.srv.Certificate())
		cfg.RootCAs, cfg.ServerName = roots, "example.com"
	}
	return &http.Transport{TLSClientConfig: cfg, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

// baoClosed are the answers of a Sovereign whose route closes the system
// paths: OpenBao answers the seal status, the gateway redirects the rest.
func baoClosed() map[string][]baoAnswerSpec {
	out := map[string][]baoAnswerSpec{
		"/v1/sys/seal-status": {{code: 200, body: `{"type":"shamir","initialized":true,"sealed":false}`}},
	}
	for _, p := range baoRoutePaths() {
		out[p] = []baoAnswerSpec{{code: 302, location: "https://bao.sov.test/ui/"}}
	}
	return out
}

func TestBaoRouteCheck(t *testing.T) {
	if _, ok := registry[baoRouteID]; !ok || baoRouteID != "SEC-G04" {
		t.Fatalf("check %s is not registered", baoRouteID)
	}
	with := func(path string, answers ...baoAnswerSpec) map[string][]baoAnswerSpec {
		out := baoClosed()
		out[path] = answers
		return out
	}
	openBao := baoAnswerSpec{code: 200, body: `{"started":false,"progress":0,"required":1}`}
	for name, c := range map[string]struct {
		answers map[string][]baoAnswerSpec
		trusted bool
		want    string // "" = PASS, else a part of the reason of the FAIL
	}{
		"every system path redirects":     {baoClosed(), true, ""},
		"the Location names port 443":     {with("/v1/sys/unseal", baoAnswerSpec{code: 302, location: "https://bao.sov.test:443/ui/"}), true, ""},
		"root generation reaches OpenBao": {with("/v1/sys/generate-root/attempt", openBao), true, "sample 1 of https://bao.sov.test/v1/sys/generate-root/attempt: HTTP 200; it must answer 302 to https://bao.sov.test/ui/"},
		"one prefix is not closed":        {with("/v1/sys/replication", baoAnswerSpec{code: 404}), true, "sample 1 of https://bao.sov.test/v1/sys/replication: HTTP 404"},
		"one sample of four is open":      {with("/v1/sys/raw", baoAnswerSpec{code: 302, location: "https://bao.sov.test/ui/"}, baoAnswerSpec{code: 302, location: "https://bao.sov.test/ui/"}, baoAnswerSpec{code: 302, location: "https://bao.sov.test/ui/"}, baoAnswerSpec{code: 403}), true, "sample 4 of https://bao.sov.test/v1/sys/raw: HTTP 403"},
		"a redirect to the landing page":  {with("/v1/sys/seal", baoAnswerSpec{code: 302, location: "https://bao.sov.test/sso/landing"}), true, `redirects to "https://bao.sov.test/sso/landing"; it must redirect to https://bao.sov.test/ui/`},
		"a redirect to another host":      {with("/v1/sys/init", baoAnswerSpec{code: 302, location: "https://auth.sov.test/ui/"}), true, `redirects to "https://auth.sov.test/ui/"`},
		"a redirect over plain HTTP":      {with("/v1/sys/rekey", baoAnswerSpec{code: 302, location: "http://bao.sov.test/ui/"}), true, `redirects to "http://bao.sov.test/ui/"`},
		"a permanent redirect":            {with("/v1/sys/audit", baoAnswerSpec{code: 301, location: "https://bao.sov.test/ui/"}), true, "HTTP 301; it must answer 302"},
		"the host redirects everything":   {with("/v1/sys/seal-status", baoAnswerSpec{code: 302, location: "https://bao.sov.test/ui/"}), true, "sample 1 of https://bao.sov.test/v1/sys/seal-status: HTTP 302; it must answer 200 from OpenBao"},
		"the control is not OpenBao":      {with("/v1/sys/seal-status", baoAnswerSpec{code: 200, body: "<html></html>"}), true, "the body is not the seal status of OpenBao"},
		"the certificate is not trusted":  {baoClosed(), false, "tls: failed to verify certificate"},
	} {
		b := newBaoServer(t, c.answers)
		r := runCheck(t, "none", baoRouteCheck{transport: b.transport(c.trusted)}, &CheckContext{}, discovered()...)
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		n := 0
		for _, s := range r.run.Steps {
			if s.Journey == baoRouteID {
				n++
			}
		}
		if n != 1+len(baoRoutePaths()) {
			t.Errorf("%s: %d steps, want %d", name, n, 1+len(baoRoutePaths()))
		}
		// Every path is asked four times, and a redirect is not followed. With
		// an untrusted certificate no request reaches the server.
		if !c.trusted {
			continue
		}
		for _, p := range append(baoRoutePaths(), baoRouteControlPath) {
			if n := b.asked[p]; n != baoRouteSamples {
				t.Errorf("%s: path %s was asked %d times", name, p, n)
			}
		}
		for _, p := range []string{"/ui/", "/sso/landing"} {
			if n := b.asked[p]; n != 0 {
				t.Errorf("%s: a redirect to %s was followed %d times", name, p, n)
			}
		}
	}
	// The step of a path holds every sample with its code and its Location.
	b := newBaoServer(t, with("/v1/sys/generate-root", baoAnswerSpec{code: 302, location: "https://bao.sov.test/ui/"}, openBao))
	r := runCheck(t, "none", baoRouteCheck{transport: b.transport(true)}, &CheckContext{}, discovered()...)
	var step *Step
	for _, s := range r.run.Steps {
		if s.ID == "SEC-G04.2" {
			step = s
		}
	}
	if step == nil {
		t.Fatal("no step SEC-G04.2")
	}
	samples, _ := step.Evidence["samples"].([]Evidence)
	if step.Evidence["path"] != "/v1/sys/generate-root" || len(samples) != 4 || samples[0]["code"] != 302 ||
		samples[0]["location"] != "https://bao.sov.test/ui/" || samples[1]["code"] != 200 || step.Status != Fail {
		t.Errorf("the step of /v1/sys/generate-root does not hold the samples: %+v", step)
	}
	// Without the name of the Sovereign the check fails; it asks nothing.
	b = newBaoServer(t, baoClosed())
	r = runCheck(t, "none", baoRouteCheck{transport: b.transport(true)}, &CheckContext{}, priorStep("D0", "D0.1", Pass, ""))
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "step D0.1 recorded no sovereignFQDN") {
		t.Errorf("without the name: want FAIL, got %+v", got)
	}
	if n := len(b.asked); n != 0 {
		t.Errorf("without the name the check asked %d paths", n)
	}
}

// The paths of the registered check: the 15 prefixes that the route of the
// bp-openbao chart closes, in its order, and one path below the first.
func TestBaoRouteList(t *testing.T) {
	want := "/v1/sys/generate-root /v1/sys/generate-recovery-token /v1/sys/unseal /v1/sys/seal /v1/sys/init " +
		"/v1/sys/rekey /v1/sys/rekey-recovery-key /v1/sys/step-down /v1/sys/raw /v1/sys/storage /v1/sys/rotate " +
		"/v1/sys/pprof /v1/sys/audit /v1/sys/config /v1/sys/replication"
	if got := strings.Join(baoClosedPrefixes, " "); got != want {
		t.Errorf("the closed prefixes are %s", got)
	}
	if got := baoRoutePaths(); len(got) != 16 || got[15] != "/v1/sys/generate-root/attempt" {
		t.Errorf("the redirected paths are %v", got)
	}
	if c, ok := registry[baoRouteID].(baoRouteCheck); !ok || c.transport != nil {
		t.Errorf("the registered check must ask over the default transport: %+v", registry[baoRouteID])
	}
	if baoRouteSamples != 4 || baoRouteControlPath != "/v1/sys/seal-status" {
		t.Errorf("the samples or the control changed: %d %s", baoRouteSamples, baoRouteControlPath)
	}
	for _, c := range []struct {
		location string
		ok       bool
	}{
		{"https://bao.sov.test/ui/", true}, {"https://bao.sov.test:443/ui/", true},
		{"https://bao.sov.test:8443/ui/", false}, {"https://bao.sov.test/ui", false},
		{"https://bao.sov.test/ui/?x=1", false}, {"/ui/", false}, {"https://u@bao.sov.test/ui/", false},
		{"https://bao.sov.test.evil/ui/", false}, {"", false},
	} {
		if got := baoUIRedirect(c.location, "bao.sov.test"); got != c.ok {
			t.Errorf("baoUIRedirect(%q) = %v, want %v", c.location, got, c.ok)
		}
	}
}
