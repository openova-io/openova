package suite

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// frontDoorServer is one TLS test server that answers for every host of the
// test Sovereign sov.test. answers maps a host name (the first label) to the
// HTTP codes of its answers, in order; the last code repeats. A host that is
// not in answers gets 404. The server counts the requests per host.
type frontDoorServer struct {
	srv     *httptest.Server
	roots   *x509.CertPool
	issuer  string
	mu      sync.Mutex
	answers map[string][]int
	asked   map[string]int
}

func newFrontDoorServer(t *testing.T, answers map[string][]int) *frontDoorServer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Front Door Test CA", Organization: []string{"suite"}},
		DNSNames: []string{"*.sov.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	f := &frontDoorServer{roots: x509.NewCertPool(), issuer: cert.Issuer.String(), answers: answers, asked: map[string]int{}}
	f.roots.AddCert(cert)
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimSuffix(strings.Split(r.Host, ":")[0], ".sov.test")
		f.mu.Lock()
		n := f.asked[host+r.URL.Path]
		f.asked[host+r.URL.Path] = n + 1
		codes := f.answers[host+r.URL.Path]
		f.mu.Unlock()
		code := http.StatusNotFound
		if len(codes) > 0 {
			code = codes[min(n, len(codes)-1)]
		}
		if code >= 300 && code < 400 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(code)
	}))
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes of the untrusted case
	f.srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// transport dials the test server for every host. trusted: it trusts the
// test certificate; otherwise only the public roots of the machine.
func (f *frontDoorServer) transport(trusted bool) http.RoundTripper {
	addr := f.srv.Listener.Addr().String()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if trusted {
		cfg.RootCAs = f.roots
	}
	return &http.Transport{TLSClientConfig: cfg, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
}

// discovered are the steps of a run whose step D0.1 learned the name sov.test.
func discovered() []*Step {
	s := priorStep("D0", "D0.1", Pass, "")
	s.Evidence = Evidence{"sovereignFQDN": "sov.test", "tier": "owner"}
	return []*Step{s}
}

func TestFrontDoorsCheck(t *testing.T) {
	if _, ok := registry[frontDoorsID]; !ok || frontDoorsID != "FND-G06" {
		t.Fatalf("check %s is not registered", frontDoorsID)
	}
	good := map[string][]int{"console/": {200}, "auth/": {302}, "vault/": {401}}
	doors := []frontDoor{{Name: "console"}, {Name: "auth"}, {Name: "vault", Closed: true}}
	with := func(host string, codes ...int) map[string][]int {
		out := map[string][]int{}
		for k, v := range good {
			out[k] = v
		}
		out[host+"/"] = codes
		return out
	}
	for name, c := range map[string]struct {
		answers map[string][]int
		trusted bool
		want    string // "" = PASS, else a part of the reason of the FAIL
	}{
		"every host answers as it must":           {good, true, ""},
		"an open host answers 404":                {with("console", 404), true, "sample 1 of https://console.sov.test/: HTTP 404; it must answer 200 or a redirect"},
		"one sample of four fails":                {with("console", 200, 200, 200, 503), true, "sample 4 of https://console.sov.test/: HTTP 503"},
		"an open host refuses anonymous access":   {with("auth", 401), true, "sample 1 of https://auth.sov.test/: HTTP 401; it must answer 200 or a redirect"},
		"a closed host serves a page":             {with("vault", 200), true, "sample 1 of https://vault.sov.test/: HTTP 200; it must answer 401 or 403 (closed to anonymous access)"},
		"a closed host answers 403":               {with("vault", 403), true, ""},
		"the certificate is not publicly trusted": {good, false, "sample 1 of https://console.sov.test/: Get \"https://console.sov.test/\": tls: failed to verify certificate"},
	} {
		f := newFrontDoorServer(t, c.answers)
		check := frontDoorsCheck{doors: doors, transport: f.transport(c.trusted)}
		r := runCheck(t, "none", check, &CheckContext{}, discovered()...)
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		// Every host is asked four times, and a redirect is not followed. With
		// an untrusted certificate no request reaches the server.
		for _, d := range doors {
			if !c.trusted {
				break
			}
			if n := f.asked[d.Name+"/"]; n != frontDoorSamples {
				t.Errorf("%s: host %s was asked %d times", name, d.Name, n)
			}
		}
		if n := f.asked["auth/elsewhere"]; n != 0 {
			t.Errorf("%s: the redirect of auth was followed %d times", name, n)
		}
	}
	// The step of a host holds every sample with its code and the issuer.
	f := newFrontDoorServer(t, with("auth", 302, 404))
	r := runCheck(t, "none", frontDoorsCheck{doors: doors, transport: f.transport(true)}, &CheckContext{}, discovered()...)
	var step *Step
	for _, s := range r.run.Steps {
		if s.ID == "FND-G06.2" {
			step = s
		}
	}
	samples, _ := step.Evidence["samples"].([]Evidence)
	if step.Evidence["host"] != "auth.sov.test" || step.Evidence["closed"] != false || len(samples) != 4 ||
		samples[0]["code"] != 302 || samples[0]["issuer"] != f.issuer || samples[1]["code"] != 404 {
		t.Errorf("the step of auth does not hold the samples: %+v", step)
	}
	// Without the name of the Sovereign the check fails; it asks no host.
	r = runCheck(t, "none", frontDoorsCheck{doors: doors, transport: f.transport(true)}, &CheckContext{}, priorStep("D0", "D0.1", Pass, ""))
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "step D0.1 recorded no sovereignFQDN") {
		t.Errorf("without the name: want FAIL, got %+v", got)
	}
}

// The hosts of the registered check: the eight hosts of layer L5 of the shell
// verifier, none of them closed.
func TestFrontDoorsList(t *testing.T) {
	names := []string{}
	for _, d := range frontDoors {
		if d.Closed {
			t.Errorf("host %s is closed", d.Name)
		}
		names = append(names, d.Name)
	}
	if got := strings.Join(names, " "); got != "console api auth gitea harbor bao marketplace pdns" {
		t.Errorf("the hosts are %s", got)
	}
	if c, ok := registry[frontDoorsID].(frontDoorsCheck); !ok || c.doors != nil || c.transport != nil {
		t.Errorf("the registered check must ask frontDoors over the default transport: %+v", registry[frontDoorsID])
	}
	if frontDoorSamples != 4 || fmt.Sprint(openCodes) != "[200 301 302 303 307 308]" || fmt.Sprint(closedCodes) != "[401 403]" {
		t.Errorf("the samples or the codes changed: %d %v %v", frontDoorSamples, openCodes, closedCodes)
	}
}
