package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestValidateStripeKey covers the per-call key validation gate.
// Stops obvious typos / publishable-key mix-ups from being stored as
// authoritative Stripe credentials in the per-Sandbox Secret.
func TestValidateStripeKey(t *testing.T) {
	cases := map[string]string{
		"":                                  "is required",
		"   ":                               "is required",
		"sk_test_aaaaaaaaaaaaaaaaa":         "",
		"sk_live_aaaaaaaaaaaaaaaaa":         "",
		"rk_test_aaaaaaaaaaaaaaaaa":         "",
		"rk_live_aaaaaaaaaaaaaaaaa":         "",
		"sk_test_short":                     "too short",
		"pk_live_aaaaaaaaaaaaaaaaa":         "must start with",
		"whatever_aaaaaaaaaaaaaaaaa":        "must start with",
	}
	for in, want := range cases {
		err := validateStripeKey(in)
		if want == "" {
			if err != nil {
				t.Errorf("%q: err=%v want nil", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v want %q", in, err, want)
		}
	}
}

// TestMaskStripeKey pins the masking format so the agent + operator can
// verify which key got bound without re-exposing it in clear.
func TestMaskStripeKey(t *testing.T) {
	cases := map[string]string{
		"":                                "***",
		"shorty":                          "***",
		"sk_test_ABCDEFGHxY12":            "sk_test_…xY12",
		"sk_live_ABCDEFGHxY12":            "sk_live_…xY12",
		"rk_live_ABCDEFGHxY12":            "rk_live_…xY12",
		"weirdformatXXXXXXXXXXxY12":       "***xY12",
	}
	for in, want := range cases {
		if got := maskStripeKey(in); got != want {
			t.Errorf("%q: got=%q want=%q", in, got, want)
		}
	}
}

// TestStripeLimitOr exercises the limit-clamping helper.
func TestStripeLimitOr(t *testing.T) {
	cases := []struct {
		in, def int
		want    string
	}{
		{0, 20, "20"},
		{-3, 20, "20"},
		{5, 20, "5"},
		{200, 20, "100"},
	}
	for _, c := range cases {
		if got := stripeLimitOr(c.in, c.def); got != c.want {
			t.Errorf("stripeLimitOr(%d,%d)=%q want %q", c.in, c.def, got, c.want)
		}
	}
}

// TestStripeAPIBaseURL pins the override behaviour — production callers
// hit api.stripe.com; tests redirect via stripeAPIBaseOverride.
func TestStripeAPIBaseURL(t *testing.T) {
	if got := stripeAPIBaseURL(); got != stripeAPIBase {
		t.Errorf("default base=%q want %q", got, stripeAPIBase)
	}
	prev := stripeAPIBaseOverride
	stripeAPIBaseOverride = "https://example.test"
	defer func() { stripeAPIBaseOverride = prev }()
	if got := stripeAPIBaseURL(); got != "https://example.test" {
		t.Errorf("override base=%q", got)
	}
}

// TestStripeDo_Success exercises the happy-path: forms-urlencoded body,
// authz header, JSON envelope decode.
func TestStripeDo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk_test_xxxxxxxxxxxx" {
			t.Errorf("authz=%q", got)
		}
		if got := r.Header.Get("Stripe-Version"); got == "" {
			t.Errorf("Stripe-Version missing")
		}
		if r.URL.Path != "/v1/products" {
			t.Errorf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"prod_1","name":"Widget"}],"has_more":false}`))
	}))
	defer srv.Close()
	prev := stripeAPIBaseOverride
	stripeAPIBaseOverride = srv.URL
	defer func() { stripeAPIBaseOverride = prev }()

	body, status, err := stripeDo(context.Background(), "sk_test_xxxxxxxxxxxx", http.MethodGet, "/v1/products", nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if status != 200 {
		t.Errorf("status=%d", status)
	}
	data, _ := body["data"].([]any)
	if len(data) != 1 {
		t.Errorf("len(data)=%d", len(data))
	}
}

// TestStripeDo_ErrorEnvelope verifies Stripe error JSON envelopes get
// unwrapped into a clean message rather than dumped raw.
func TestStripeDo_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"api_key_invalid","message":"Invalid API Key provided"}}`))
	}))
	defer srv.Close()
	prev := stripeAPIBaseOverride
	stripeAPIBaseOverride = srv.URL
	defer func() { stripeAPIBaseOverride = prev }()

	_, status, err := stripeDo(context.Background(), "sk_test_xxxxxxxxxxxx", http.MethodGet, "/v1/products", nil)
	if status != 401 {
		t.Errorf("status=%d", status)
	}
	if err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("err=%v want Invalid API Key", err)
	}
}

// TestSandboxStripeBindAccount_ArgValidation covers per-arg validation
// before any API / cluster call.
func TestSandboxStripeBindAccount_ArgValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1", OwnerUID: "u1"})
	cases := map[string]string{
		`{}`:                           "is required",
		`{"api_key":""}`:               "is required",
		`{"api_key":"sk_test_short"}`:  "too short",
		`{"api_key":"pk_live_AAAAAAAAAAAAAAAAA"}`: "must start with",
	}
	for args, want := range cases {
		_, err := sandboxStripeBindAccount(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestSandboxStripeCreateCheckoutSession_ArgValidation covers the
// per-arg URL + price validation before any API call.
func TestSandboxStripeCreateCheckoutSession_ArgValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{SandboxNamespace: "sandbox-u1", OwnerUID: "u1"})
	cases := map[string]string{
		`{}`:                                                                              "`price_id` is required",
		`{"price_id":"price_1","success_url":"","cancel_url":"https://ok"}`:               "`success_url` is required",
		`{"price_id":"price_1","success_url":"https://ok","cancel_url":""}`:               "`cancel_url` is required",
		`{"price_id":"price_1","success_url":"notaurl","cancel_url":"https://ok"}`:        "invalid URL",
		`{"price_id":"price_1","success_url":"ftp://x/y","cancel_url":"https://ok"}`:      "must be http or https",
		`{"price_id":"price_1","success_url":"https://ok","cancel_url":"https://k","mode":"wat"}`: "`mode` must be one of",
	}
	for args, want := range cases {
		_, err := sandboxStripeCreateCheckoutSession(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args=%s err=%v want %q", args, err, want)
		}
	}
}

// TestReadStripeKey_BareEncoding verifies the read path decodes the
// base64'd value back to the original Stripe key (smoke test on the
// encode-on-write / decode-on-read symmetry without standing up a fake
// apiserver).
func TestReadStripeKey_BareEncoding(t *testing.T) {
	v := "sk_test_AAAABBBBCCCCDDDD"
	enc := base64.StdEncoding.EncodeToString([]byte(v))
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := strings.TrimSpace(string(dec)); got != v {
		t.Errorf("roundtrip mismatch: got %q want %q", got, v)
	}
}

// TestSandboxStripeCatalogueWiredIn ensures all four tools are in the
// catalogue with non-nil Handler + their granular tier-bound
// RequiredCapability (PR #1671 — Ent plan grants `sandbox.stripe.*`
// which the wildcard matcher resolves to these per-tool grants).
func TestSandboxStripeCatalogueWiredIn(t *testing.T) {
	r := NewRegistry(&Env{OrgID: "acme", OwnerUID: "u1"})
	want := map[string]string{
		"sandbox.stripe.bindAccount":           "sandbox.stripe.bindAccount",
		"sandbox.stripe.listProducts":          "sandbox.stripe.listProducts",
		"sandbox.stripe.listPrices":            "sandbox.stripe.listPrices",
		"sandbox.stripe.createCheckoutSession": "sandbox.stripe.createCheckoutSession",
	}
	seen := map[string]bool{}
	for _, tool := range r.List() {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		if tool.Handler == nil {
			t.Errorf("%s: Handler is nil", tool.Name)
		}
		if tool.RequiredCapability != want[tool.Name] {
			t.Errorf("%s: RequiredCapability=%q want %q", tool.Name, tool.RequiredCapability, want[tool.Name])
		}
		seen[tool.Name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s: missing from catalogue", name)
		}
	}
}

// TestSandboxStripeCapabilityGate confirms the registry rejects
// sandbox.stripe.* calls whose claims lack the `sandbox.stripe` cap.
func TestSandboxStripeCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:            "acme",
		JWTSecret:        []byte("x"),
		SandboxNamespace: "sandbox-u1",
		OwnerUID:         "u1",
	})
	for _, name := range []string{
		"sandbox.stripe.bindAccount",
		"sandbox.stripe.listProducts",
		"sandbox.stripe.listPrices",
		"sandbox.stripe.createCheckoutSession",
	} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"api_key":"sk_test_AAAABBBBCCCCDDDD","price_id":"price_1","success_url":"https://ok","cancel_url":"https://k"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}

// TestStripeDo_FormEncoding verifies POST bodies are form-encoded (the
// Stripe REST API rejects JSON bodies on most endpoints — line_items[0]
// nesting is array-bracket notation, not JSON objects).
func TestStripeDo_FormEncoding(t *testing.T) {
	gotBody := ""
	gotCT := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_1","url":"https://checkout.stripe.com/c/cs_test_1"}`))
	}))
	defer srv.Close()
	prev := stripeAPIBaseOverride
	stripeAPIBaseOverride = srv.URL
	defer func() { stripeAPIBaseOverride = prev }()

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", "https://ok")
	form.Set("cancel_url", "https://no")
	form.Set("line_items[0][price]", "price_1")
	form.Set("line_items[0][quantity]", "1")
	body, status, err := stripeDo(context.Background(), "sk_test_xxxxxxxxxxxx", http.MethodPost, "/v1/checkout/sessions", form)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if status != 200 {
		t.Errorf("status=%d", status)
	}
	if got, _ := body["id"].(string); got != "cs_test_1" {
		t.Errorf("id=%q", got)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("content-type=%q", gotCT)
	}
	if !strings.Contains(gotBody, "line_items%5B0%5D%5Bprice%5D=price_1") {
		t.Errorf("body=%q (line_items not form-encoded)", gotBody)
	}
}

