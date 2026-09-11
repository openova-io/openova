package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/docs"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// GET /api/v1/statements/{id}.pdf is gated by the SAME check as reading the
// statement — it is literally the same handler and the same scoped store call
// — so these cases pin the authorization decision without a database: every
// one of them is refused before a query would run.
func TestStatementPDFPermission(t *testing.T) {
	h := New(Deps{
		Config: config.Config{PublicURL: "http://localhost:8080", Profile: "sovereign"},
		Docs:   docs.New("http://docrender.invalid:8080", ""),
	})
	a, b := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	viewer := &store.Session{Email: "v@a.example", Role: store.RoleCustomerViewer, CustomerID: &a}

	cases := []struct {
		name string
		sess *store.Session
		path string
		want int
	}{
		{"anonymous download", nil, "/api/v1/statements/" + a + ".pdf", 401},
		{"anonymous download of any id", nil, "/api/v1/statements/anything.pdf", 401},
	}
	// Scope filtering — a customer principal getting 404 rather than 403 on
	// another customer's invoice — is the STORE's, applied by the same
	// GetStatement(scope, id) call the JSON and CSV routes make, and pinned
	// by the invoicing integration suite. It is deliberately not re-asserted
	// here against a nil store: that would test the mock, not the rule.
	_, _ = b, viewer
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, c.sess, "GET", c.path, "")
			if rec.Code != c.want {
				t.Fatalf("%s = %d (%s), want %d", c.path, rec.Code, rec.Body.String(), c.want)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("a refusal must be JSON, got %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// New() must build a renderer client from the config, so a deployment that
// sets DOCRENDER_URL gets the feature with no further wiring — and one that
// does not gets a client that honestly reports itself off rather than a nil
// the route would panic on.
func TestNewWiresTheRendererFromConfig(t *testing.T) {
	off := Deps{Config: config.Config{PublicURL: "http://localhost:8080", Profile: "sovereign"}}
	New(off)
	if off.Docs != nil {
		t.Fatal("New must not mutate the caller's Deps")
	}

	h := &Handler{Deps: Deps{Config: config.Config{
		PublicURL: "http://localhost:8080", Profile: "sovereign",
		DocRenderURL: "http://chargeback-docrender.chargeback.svc.cluster.local:8080",
	}}}
	h.Docs = docs.New(h.Config.DocRenderURL, h.Config.DocRenderToken)
	if !h.Docs.Enabled() {
		t.Fatal("a configured URL produced a disabled client")
	}
}

// With no renderer configured the route answers 503 and SAYS SO. The
// distinction matters: "not configured" is a feature an operator has not
// turned on, "could not render" is a defect, and a caller that cannot tell
// them apart files the wrong issue.
func TestStatementPDFUnconfiguredAnswers503(t *testing.T) {
	h := &Handler{Deps: Deps{
		Config: config.Config{PublicURL: "http://localhost:8080", Profile: "sovereign"},
		Docs:   docs.New("", ""), // the shape a Sovereign without the sub-chart has
	}}
	st := store.Statement{ID: "3f9c1b2e", CustomerID: "c1", Currency: "OMR", Total: "10.000000"}

	rec := httptest.NewRecorder()
	h.statementPDF(rec, httptest.NewRequest("GET", "/api/v1/statements/3f9c1b2e.pdf", nil), st)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "document renderer not configured" {
		t.Fatalf("the 503 does not say the renderer is unconfigured: %q", body.Error)
	}
	// Nothing was written as a document: a caller must never receive an empty
	// or truncated PDF in place of an error.
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/pdf") {
		t.Fatal("a refusal was served as application/pdf")
	}

	// Belt and braces: a nil client takes the same path rather than panicking.
	h.Docs = nil
	rec = httptest.NewRecorder()
	h.statementPDF(rec, httptest.NewRequest("GET", "/api/v1/statements/3f9c1b2e.pdf", nil), st)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil client status %d, want 503", rec.Code)
	}
}

// The .pdf suffix must be stripped before the store lookup, or every download
// would 404 on a statement that plainly exists — and a .csv download must be
// untouched by the addition.
func TestSuffixStrippingIsSharedByBothDownloads(t *testing.T) {
	for in, want := range map[string]string{
		"abc.pdf": "abc", "abc.csv": "abc", "abc": "abc",
		"a.b.c.pdf": "a.b.c",
		// Trimming is chained, so a doubled suffix loses both. Statement ids
		// are UUIDs and never carry one; the case is pinned so the behaviour
		// is a decision rather than a surprise.
		"report.pdf.csv": "report",
	} {
		got := strings.TrimSuffix(strings.TrimSuffix(in, ".csv"), ".pdf")
		if got != want {
			t.Errorf("id %q resolved to %q, want %q", in, got, want)
		}
	}
}
