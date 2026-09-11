package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/docrender"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
)

func newServer(t *testing.T, mutate func(*Options)) http.Handler {
	t.Helper()
	opt := Options{Assets: docrender.Assets(), Version: "test", Now: func() time.Time {
		return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	}}
	if mutate != nil {
		mutate(&opt)
	}
	s, err := New(opt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s.Handler()
}

func post(t *testing.T, h http.Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/render", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The whole contract in one test: every document kind POSTed as JSON comes
// back as a PDF with a sensible filename.
func TestRenderReturnsAPDFForEveryTemplate(t *testing.T) {
	h := newServer(t, nil)
	for _, kind := range document.Templates {
		t.Run(kind, func(t *testing.T) {
			raw, err := fixture.Raw(kind)
			if err != nil {
				t.Fatal(err)
			}
			rec := post(t, h, raw, nil)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
				t.Fatalf("Content-Type = %q", ct)
			}
			if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
				t.Fatal("body is not a PDF")
			}
			if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".pdf") {
				t.Fatalf("Content-Disposition = %q", cd)
			}
			if got, want := rec.Header().Get("Content-Length"), fmt.Sprint(rec.Body.Len()); got != want {
				t.Errorf("Content-Length %s, body %s", got, want)
			}
		})
	}
}

// The same endpoint serves the HTML rendition when asked, so the console can
// show a document inline without a second service.
func TestRenderServesHTMLOnRequest(t *testing.T) {
	h := newServer(t, nil)
	raw, _ := fixture.Raw("invoice")

	for _, probe := range []struct {
		name    string
		headers map[string]string
		query   string
		wantCT  string
	}{
		{"default is pdf", nil, "", "application/pdf"},
		{"accept html", map[string]string{"Accept": "text/html"}, "", "text/html; charset=utf-8"},
		{"accept pdf wins", map[string]string{"Accept": "text/html,application/pdf"}, "", "application/pdf"},
		{"format query overrides accept", map[string]string{"Accept": "text/html"}, "?format=pdf", "application/pdf"},
		{"format=html", nil, "?format=html", "text/html; charset=utf-8"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/render"+probe.query, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range probe.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != probe.wantCT {
				t.Fatalf("Content-Type = %q, want %q", got, probe.wantCT)
			}
		})
	}
}

// A document that cannot be rendered comes back as 400 NAMING every problem,
// so the BSS can log what was wrong rather than "render failed".
func TestInvalidDocumentIsRefusedWithItsProblems(t *testing.T) {
	h := newServer(t, nil)
	rec := post(t, h, []byte(`{"template":"receipt","currency":"OMR","document":{"number":"","issued_at":"nope","seller":{"name":"x"},"buyer":{"name":"y"},"waterfall":{"net_subtotal":"1","total":"1"}}}`), nil)
	if rec.Code != 400 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error    string   `json:"error"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Problems) < 3 {
		t.Fatalf("only %d problems reported: %v", len(body.Problems), body.Problems)
	}
	joined := strings.Join(body.Problems, "\n")
	for _, want := range []string{"template:", "document.number:", "document.issued_at:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no problem for %s: %v", want, body.Problems)
		}
	}
}

func TestBadRequests(t *testing.T) {
	h := newServer(t, nil)
	raw, _ := fixture.Raw("invoice")

	cases := []struct {
		name string
		do   func() *httptest.ResponseRecorder
		want int
	}{
		{"not JSON", func() *httptest.ResponseRecorder { return post(t, h, []byte("{"), nil) }, 400},
		{"unknown field", func() *httptest.ResponseRecorder {
			return post(t, h, []byte(`{"template":"invoice","currency":"OMR","surprise":1,"document":{}}`), nil)
		}, 400},
		{"unknown locale", func() *httptest.ResponseRecorder {
			return post(t, h, bytes.Replace(raw, []byte(`"locale": "en"`), []byte(`"locale": "xx"`), 1), nil)
		}, 400},
		{"wrong content type", func() *httptest.ResponseRecorder {
			return post(t, h, raw, map[string]string{"Content-Type": "text/plain"})
		}, 415},
		{"GET on the render path", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/render", nil))
			return rec
		}, 405},
		{"unknown path", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/nope", nil))
			return rec
		}, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := c.do(); rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// The body cap is enforced by the SERVER, not by a hope that callers behave:
// a document past it is refused with 413 before anything is decoded.
func TestRequestSizeCap(t *testing.T) {
	const cap = 1024
	h := newServer(t, func(o *Options) { o.MaxBodyBytes = cap })
	raw, _ := fixture.Raw("invoice")
	if len(raw) <= cap {
		t.Fatalf("the fixture is %d bytes — it does not exceed the %d-byte cap, so this test would pass vacuously", len(raw), cap)
	}
	if rec := post(t, h, raw, nil); rec.Code != 413 {
		t.Fatalf("a %d-byte body against a %d-byte cap gave %d", len(raw), cap, rec.Code)
	}

	// The default cap admits a large but realistic document.
	def := newServer(t, nil)
	req, _ := fixture.Load("invoice")
	line := req.Document.Lines[0]
	for len(req.Document.Lines) < 1500 {
		req.Document.Lines = append(req.Document.Lines, line)
	}
	big, _ := json.Marshal(req)
	if len(big) < 300_000 {
		t.Fatalf("the large-document probe is only %d bytes — it does not exercise the cap", len(big))
	}
	if rec := post(t, def, big, nil); rec.Code != 200 {
		t.Fatalf("a %d-byte document was refused under the 2 MiB default: %d %s", len(big), rec.Code, rec.Body.String())
	}
}

// A render that outruns its deadline answers 504 rather than holding the
// caller's connection open — the BSS has its own request to answer.
func TestRenderTimeout(t *testing.T) {
	h := newServer(t, func(o *Options) { o.RenderTimeout = time.Nanosecond })
	raw, _ := fixture.Raw("invoice")
	rec := post(t, h, raw, nil)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "did not render within") {
		t.Errorf("the 504 does not say why: %s", rec.Body.String())
	}
}

// The optional shared secret gates the render endpoint and ONLY it: the
// kubelet's probes and Prometheus must keep working without the token.
func TestSharedSecretHeader(t *testing.T) {
	const token = "s3cret-render-token"
	h := newServer(t, func(o *Options) { o.Token = token })
	raw, _ := fixture.Raw("invoice")

	for _, c := range []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no header", nil, 401},
		{"wrong token", map[string]string{"X-Render-Token": "nope"}, 401},
		{"right length wrong value", map[string]string{"X-Render-Token": strings.Repeat("x", len(token))}, 401},
		{"correct token", map[string]string{"X-Render-Token": token}, 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			if rec := post(t, h, raw, c.hdr); rec.Code != c.want {
				t.Fatalf("status %d, want %d", rec.Code, c.want)
			}
		})
	}

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s returned %d with the token set — the kubelet does not send one", path, rec.Code)
		}
	}
}

// Readiness is a PROVEN capability: it renders the sample document through
// the real path at start-up. A tree whose templates do not parse must fail to
// start rather than serve 500s.
func TestHealthAndReadiness(t *testing.T) {
	h := newServer(t, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}
	var health struct {
		Status    string   `json:"status"`
		Version   string   `json:"version"`
		Templates []string `json:"templates"`
		Locales   []string `json:"locales"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.Version != "test" {
		t.Fatalf("healthz = %+v", health)
	}
	if len(health.Templates) != 4 || len(health.Locales) == 0 {
		t.Fatalf("healthz does not report what it can render: %+v", health)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("readyz %d: %s", rec.Code, rec.Body.String())
	}
}

// Metrics are the operator's only view of a service with no UI: renders,
// duration, size and errors must all appear, with their Prometheus types.
func TestMetrics(t *testing.T) {
	h := newServer(t, nil)
	raw, _ := fixture.Raw("invoice")
	if rec := post(t, h, raw, nil); rec.Code != 200 {
		t.Fatalf("render %d", rec.Code)
	}
	post(t, h, []byte(`{"template":"invoice","currency":"XX","document":{}}`), nil) // an error to count

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("metrics %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`# TYPE docrender_renders_total counter`,
		`docrender_renders_total{format="pdf",template="invoice"} 1`,
		`# TYPE docrender_render_duration_seconds histogram`,
		`docrender_render_duration_seconds_count{format="pdf",template="invoice"} 1`,
		`# TYPE docrender_render_bytes histogram`,
		`docrender_render_bytes_count{format="pdf",template="invoice"} 1`,
		`# TYPE docrender_render_errors_total counter`,
		`docrender_render_errors_total{reason="invalid"} 1`,
		`# TYPE docrender_http_requests_total counter`,
		`docrender_http_requests_total{path="/v1/render",status="200"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n%s", want, body)
		}
	}
	// Cardinality stays bounded: an unknown path must not become a label.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/some/attacker/path", nil))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(rec.Body.String(), "/some/attacker/path") {
		t.Error("an arbitrary path became a metric label")
	}
}

// The renderer makes NO outbound call and keeps NO state, which is what lets
// the chart deny egress. Two renders of the same document are identical and
// neither depends on anything outside the process.
func TestStatelessAndRepeatable(t *testing.T) {
	h := newServer(t, nil)
	raw, _ := fixture.Raw("invoice")
	first := post(t, h, raw, nil)
	second := post(t, h, raw, nil)
	if first.Code != 200 || second.Code != 200 {
		t.Fatalf("%d / %d", first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		a, b := first.Body.Bytes(), second.Body.Bytes()
		n := 0
		for n < len(a) && n < len(b) && a[n] == b[n] {
			n++
		}
		lo := n - 60
		if lo < 0 {
			lo = 0
		}
		t.Fatalf("two renders of one document differ at byte %d of %d/%d\nA: %q\nB: %q", n, len(a), len(b), a[lo:min(len(a), n+60)], b[lo:min(len(b), n+60)])
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newServer(t, nil)
	raw, _ := fixture.Raw("invoice")
	rec := post(t, h, raw, nil)
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// The service logs every request; a test run should not.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
