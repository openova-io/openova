package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const consents = "/open-banking/v4.0/aisp/account-access-consents"

func (f *fixture) tlsState() *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: clientCert}}}
}

type consentView struct {
	Data struct {
		ConsentID            string `json:"ConsentId"`
		Status               string
		Permissions          []string
		CreationDateTime     time.Time
		StatusUpdateDateTime time.Time
	}
	Links struct{ Self string }
}

func consentID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return view(t, w).Data.ConsentID
}

func view(t *testing.T, w *httptest.ResponseRecorder) consentView {
	t.Helper()
	var v consentView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Data.ConsentID == "" {
		t.Fatalf("status %d: not a consent: %q", w.Code, w.Body.String())
	}
	return v
}

func create(t *testing.T, f *fixture, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.call(t, http.MethodPost, consents, mint(t, *f.now, nil), []byte(body))
}

func TestConsentLifeCycle(t *testing.T) {
	f := newFixture(t)
	jws := mint(t, *f.now, nil)
	w := create(t, f, validConsent)
	sameStatus(t, w, http.StatusCreated)
	v := view(t, w)
	if v.Data.Status != "AWAU" || strings.Join(v.Data.Permissions, ",") != "ReadAccountsBasic,ReadBalances" ||
		!v.Data.CreationDateTime.Equal(*f.now) || v.Links.Self != "https://"+host+consents+"/"+v.Data.ConsentID ||
		!strings.HasPrefix(v.Data.ConsentID, "aac-") {
		t.Fatalf("created: %+v", v)
	}
	id := v.Data.ConsentID
	stored, ok, err := f.store.Get(context.Background(), id)
	if err != nil || !ok || stored.ClientID != clientID {
		t.Fatalf("stored: %+v %v %v", stored, ok, err)
	}
	if got := view(t, f.call(t, http.MethodGet, consents+"/"+id, jws, nil)); got.Data.Status != "AWAU" {
		t.Fatalf("read: %s", got.Data.Status)
	}
	*f.now = f.now.Add(time.Minute)
	if _, err := f.store.Authorise(context.Background(), id, "cust-1", []string{"acc-1"}); err != nil {
		t.Fatal(err)
	}
	jws = mint(t, *f.now, nil)
	got := view(t, f.call(t, http.MethodGet, consents+"/"+id, jws, nil))
	if got.Data.Status != "AUTH" || !got.Data.StatusUpdateDateTime.Equal(*f.now) {
		t.Fatalf("after the customer authorised: %+v", got.Data)
	}
	sameStatus(t, f.call(t, http.MethodDelete, consents+"/"+id, jws, nil), http.StatusNoContent)
	if got := view(t, f.call(t, http.MethodGet, consents+"/"+id, jws, nil)); got.Data.Status != "CANC" {
		t.Fatalf("after the delete: %s", got.Data.Status)
	}
	w = f.call(t, http.MethodDelete, consents+"/"+id, jws, nil)
	sameStatus(t, w, http.StatusBadRequest)
	if msg := errorMessage(t, w); !strings.Contains(msg, "only an authorised consent can be deleted") {
		t.Fatalf("second delete: %q", msg)
	}
}

func TestAnExpiredConsentReadsExpired(t *testing.T) {
	f := newFixture(t)
	id := consentID(t, create(t, f, validConsent))
	if _, err := f.store.Authorise(context.Background(), id, "cust-1", nil); err != nil {
		t.Fatal(err)
	}
	*f.now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := view(t, f.call(t, http.MethodGet, consents+"/"+id, mint(t, *f.now, nil), nil)); got.Data.Status != "EXPD" {
		t.Fatalf("status %s, want EXPD", got.Data.Status)
	}
}

func TestAnotherClientCannotSeeTheConsent(t *testing.T) {
	f := newFixture(t)
	id := consentID(t, create(t, f, validConsent))
	other := mint(t, *f.now, map[string]any{"client_id": "other.app"})
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		w := f.call(t, m, consents+"/"+id, other, nil)
		sameStatus(t, w, http.StatusBadRequest)
		if msg := errorMessage(t, w); !strings.Contains(msg, "does not exist") {
			t.Fatalf("%s by another client: %q", m, msg)
		}
	}
}

func TestInvalidConsentRequestsAreRefused(t *testing.T) {
	f := newFixture(t)
	cases := map[string]string{
		`{"Data":{"Permissions":[]},"Risk":{}}`:                                                           "Data.Permissions is empty",
		`{"Data":{"Permissions":["ReadEverything"]},"Risk":{}}`:                                           "is not a permission",
		`{"Data":{"Permissions":["ReadBalances","ReadBalances"]},"Risk":{}}`:                              "is repeated",
		`{"Data":{"Permissions":["ReadBalances"]}}`:                                                       "Risk is missing",
		`{"Data":{"Permissions":["ReadBalances"],"ExpirationDateTime":"2026-10-01T00:00:00Z"},"Risk":{}}`: "not in the future",
		`{"Data":{"Permissions":["ReadBalances"],"TransactionFromDateTime":"2026-10-02T00:00:00Z","TransactionToDateTime":"2026-10-01T00:00:00Z"},"Risk":{}}`: "is after",
		`{"Data":`: "",
	}
	for body, want := range cases {
		w := create(t, f, body)
		sameStatus(t, w, http.StatusBadRequest)
		if msg := errorMessage(t, w); !strings.Contains(msg, want) {
			t.Errorf("%s: message %q, want %q", body, msg, want)
		}
	}
	req := f.request(t, http.MethodPost, consents, mint(t, *f.now, nil), validConsent)
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	sameStatus(t, w, http.StatusUnsupportedMediaType)
	for i := 1; i <= 9; i++ {
		if _, ok, _ := f.store.Get(context.Background(), fmt.Sprintf("aac-%032d", i)); ok {
			t.Fatal("a refused request stored a consent")
		}
	}
}

func TestTheCharsetFormOfJSONIsAccepted(t *testing.T) {
	f := newFixture(t)
	req := f.request(t, http.MethodPost, consents, mint(t, *f.now, nil), validConsent)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, req)
	sameStatus(t, w, http.StatusCreated)
}

func TestTokenRulesOnTheConsentRoutes(t *testing.T) {
	f := newFixture(t)
	// T7: a suspended partner.
	f.setDefs(t, strings.Replace(defs("products: [accounts]"), "state: approved", "state: suspended", 1))
	w := create(t, f, validConsent)
	sameStatus(t, w, http.StatusForbidden)
	if msg := errorMessage(t, w); !strings.HasPrefix(msg, "rule T7:") {
		t.Fatalf("suspended partner: %q", msg)
	}
	// T10: the application is not granted the family.
	f.setDefs(t, strings.Replace(defs("products: [accounts]"), "        products: [accounts]\n  - id: other", "        products: []\n  - id: other", 1))
	w = create(t, f, validConsent)
	sameStatus(t, w, http.StatusForbidden)
	if msg := errorMessage(t, w); !strings.HasPrefix(msg, "rule T10:") {
		t.Fatalf("family not granted: %q", msg)
	}
	// The installation does not serve the family: the operation is not implemented.
	f.setDefs(t, defs("products: []"))
	sameStatus(t, create(t, f, validConsent), f.profile.NotImplementedStatus)
	// T5 before routing: an expired token.
	f.setDefs(t, defs("products: [accounts]"))
	expired := mint(t, f.now.Add(-time.Hour), nil)
	sameStatus(t, f.call(t, http.MethodPost, consents, expired, []byte(validConsent)), http.StatusUnauthorized)
}

func TestNoDefinitionsYet(t *testing.T) {
	f := newFixture(t)
	f.holder.Set(nil)
	sameStatus(t, create(t, f, validConsent), http.StatusServiceUnavailable)
}
