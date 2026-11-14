package api

// Test C1, document conformance, for the operations this package serves.
//
// For each served operation: a valid request, itself checked against the document, is accepted
// and its answer validates against the document; a request without a token (401), with a token
// that lacks the operation's scope (403) and with an invalid body or identifier (400) each get
// the documented answer, and each answer validates against the document. For each of the 25
// not-offered operations the answer is the profile's notImplementedStatus. The counter line
// "C1: <served>/<total>" counts the operations the profile serves that this package implements.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// documentCheck validates requests and answers against one document of the profile.
type documentCheck struct {
	t   *testing.T
	doc *openapi3.T
}

func loadDocument(t *testing.T, file string) *documentCheck {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(path.Join(profileDir, file))
	if err != nil {
		t.Fatal(err)
	}
	return &documentCheck{t: t, doc: doc}
}

// exchange sends req to the server and validates the request (when validRequest) and the
// answer against operation method docPath of the document.
func (c *documentCheck) exchange(f *fixture, method, docPath string, params map[string]string, req *http.Request, validRequest bool) *httptest.ResponseRecorder {
	c.t.Helper()
	item := c.doc.Paths.Value(docPath)
	if item == nil || item.GetOperation(method) == nil {
		c.t.Fatalf("the document has no operation %s %s", method, docPath)
	}
	route := &routers.Route{Spec: c.doc, Path: docPath, PathItem: item, Method: method, Operation: item.GetOperation(method)}
	in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if validRequest {
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			c.t.Fatalf("the test request is not valid for the document: %v", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	out := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: w.Code, Header: w.Header(),
		Body: io.NopCloser(bytes.NewReader(w.Body.Bytes())), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		c.t.Fatalf("%s %s: the answer %d does not validate against the document: %v\n%s", method, docPath, w.Code, err, w.Body.String())
	}
	return w
}

func (f *fixture) request(t *testing.T, method, p, jws string, body string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "https://"+host+p, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if jws != "" {
		req.Header.Set("Authorization", "Bearer "+jws)
	}
	req.Header.Set(InteractionHeader, "93bac548-d2de-4546-b106-880a5018460d")
	req.TLS = f.tlsState()
	return req
}

const validConsent = `{"Data":{"Permissions":["ReadAccountsBasic","ReadBalances"],"ExpirationDateTime":"2027-01-01T00:00:00+00:00"},"Risk":{}}`

func TestDocumentConformance(t *testing.T) {
	f := newFixture(t)
	doc := loadDocument(t, "account-info-openapi.yaml")
	base := "/open-banking/v4.0/aisp"
	good := mint(t, *f.now, nil)
	noScope := mint(t, *f.now, map[string]any{"scope": "openid"})

	// POST /account-access-consents
	w := doc.exchange(f, "POST", "/account-access-consents", nil, f.request(t, "POST", base+"/account-access-consents", good, validConsent), true)
	sameStatus(t, w, http.StatusCreated)
	id := consentID(t, w)
	for status, req := range map[int]*http.Request{
		http.StatusUnauthorized: f.request(t, "POST", base+"/account-access-consents", "", validConsent),
		http.StatusForbidden:    f.request(t, "POST", base+"/account-access-consents", noScope, validConsent),
		http.StatusBadRequest:   f.request(t, "POST", base+"/account-access-consents", good, `{"Data":{"Permissions":[]},"Risk":{}}`),
	} {
		sameStatus(t, doc.exchange(f, "POST", "/account-access-consents", nil, req, false), status)
	}

	// GET /account-access-consents/{ConsentId}
	params := map[string]string{"ConsentId": id}
	sameStatus(t, doc.exchange(f, "GET", "/account-access-consents/{ConsentId}", params, f.request(t, "GET", base+"/account-access-consents/"+id, good, ""), true), http.StatusOK)
	for status, req := range map[int]*http.Request{
		http.StatusUnauthorized: f.request(t, "GET", base+"/account-access-consents/"+id, "", ""),
		http.StatusForbidden:    f.request(t, "GET", base+"/account-access-consents/"+id, noScope, ""),
		http.StatusBadRequest:   f.request(t, "GET", base+"/account-access-consents/aac-unknown", good, ""),
	} {
		sameStatus(t, doc.exchange(f, "GET", "/account-access-consents/{ConsentId}", params, req, false), status)
	}

	// DELETE /account-access-consents/{ConsentId}: only an authorised consent is deleted.
	for status, req := range map[int]*http.Request{
		http.StatusUnauthorized: f.request(t, "DELETE", base+"/account-access-consents/"+id, "", ""),
		http.StatusForbidden:    f.request(t, "DELETE", base+"/account-access-consents/"+id, noScope, ""),
		http.StatusBadRequest:   f.request(t, "DELETE", base+"/account-access-consents/"+id, good, ""),
	} {
		sameStatus(t, doc.exchange(f, "DELETE", "/account-access-consents/{ConsentId}", params, req, false), status)
	}
	if _, err := f.store.Authorise(context.Background(), id, "cust-1", []string{"acc-1"}); err != nil {
		t.Fatal(err)
	}
	sameStatus(t, doc.exchange(f, "DELETE", "/account-access-consents/{ConsentId}", params, f.request(t, "DELETE", base+"/account-access-consents/"+id, good, ""), true), http.StatusNoContent)

	// Every not-offered operation answers notImplementedStatus to a call with a valid token.
	all := mint(t, *f.now, map[string]any{"scope": "openid accounts payments fundsconfirmations"})
	notOffered := 0
	for _, op := range f.srv.Operations() {
		if !op.NotOffered {
			continue
		}
		notOffered++
		sameStatus(t, f.call(t, op.Method, concrete(op), all, nil), f.profile.NotImplementedStatus)
	}
	if notOffered != 25 {
		t.Fatalf("%d operations are not offered, want 25", notOffered)
	}

	// The counter.
	total, implemented := 0, 0
	for _, d := range f.profile.Documents {
		total += d.Served
	}
	for _, op := range f.srv.Operations() {
		if op.Served {
			implemented++
		}
	}
	pending, _ := parseUnimplemented(unimplementedList)
	if implemented != total-len(pending) || implemented != 29 {
		t.Fatalf("%d operations are served, the profile serves %d and unimplemented.txt lists %d", implemented, total, len(pending))
	}
	t.Logf("C1: %d/%d", implemented, total)
}
