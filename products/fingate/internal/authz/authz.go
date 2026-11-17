// Package authz is the internal authorisation API of the open-banking product: the calls with
// which the consent step of the authorisation server shows a consent to the customer.
//
//	GET  /internal/v1/authorisations/{consentId}?client_id=<id>   the consent, as the customer will see it
//	POST /internal/v1/authorisations/{consentId}/customer         the customer of an identity, and the customer's accounts
//
// Both calls read: neither writes to the store by itself. The read of the store expires an
// authorised consent whose expiry has passed, as every read of the store does. The call that
// records the customer's decision is not served yet.
//
// Every call, whatever it addresses, is checked first. Rules T1 to T5 of package token run with
// the audience Audience, so a token made for the resource server is refused here. Then the
// subject must be Subject and the token must end at most MaxLifetime plus token.Skew from now.
// A refused token is answered with 401 and the header WWW-Authenticate.
//
// Then the call is routed. Only the exact method and the exact path of a call are a route: a
// HEAD request, and a path that is not clean, are answered with 404 like any other request.
//
// An answer other than 200 has the body {"error": "<code>"}:
//
//	400 bad-request          the body is not the JSON the call takes, or a value is missing
//	401 invalid_token        the token was refused
//	403 client-mismatch      client_id is not the client that created the consent
//	404 consent-not-found    no consent has this identifier
//	404 customer-unknown     the ledger knows no customer of this identity
//	404 no-route             the method and the path are none of the calls
//	409 not-awaiting         the consent does not await a decision
//	500 internal             the store failed, the token could not be checked, or the consent has no summary
//	502 ledger-unavailable   the ledger could not answer
//
// The package issues no code and no token and holds no customer data: the customer and the
// accounts come from the ledger port on every call.
package authz

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/profile"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

// Audience is the audience of the token a caller presents.
const Audience = "fingate-authz"

// Subject is the subject of the token a caller presents: the consent step of the authorisation server.
const Subject = "fapi-consent"

// MaxLifetime is the longest lifetime of a caller's token. A token that ends later than
// MaxLifetime plus token.Skew from now is refused.
const MaxLifetime = 60 * time.Second

// Config configures a Server.
type Config struct {
	Profile *profile.Profile              // its signing algorithms are read (rule T1)
	Issuer  string                        // the realm's issuer, an https address (rule T3)
	Keys    token.KeySource               // the realm's key set (rule T2)
	Store   store.Store                   // the consents
	Ledger  *ledgerclient.Client          // the ledger port
	Now     func() time.Time              // nil: time.Now
	Logf    func(format string, a ...any) // receives one line per refused token and per failure; nil: nothing is logged
}

// Server is the internal authorisation API. It is an http.Handler.
type Server struct {
	cfg     Config
	checker *token.Checker
	mux     *http.ServeMux
}

// route is one call of the API: the one method it takes, its path, and what serves it.
type route struct {
	method, path string
	serve        http.HandlerFunc
}

// ServeHTTP serves the call.
func (rt route) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.serve(w, r) }

// New checks the configuration and builds the routes.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil || cfg.Ledger == nil {
		return nil, errors.New("authz: the configuration needs Profile, Issuer, Keys, Store and Ledger")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	checker, err := token.New(token.Config{Profile: cfg.Profile, Issuer: cfg.Issuer, Audience: Audience, Keys: cfg.Keys, Now: cfg.Now})
	if err != nil {
		return nil, fmt.Errorf("authz: %w", err)
	}
	s := &Server{cfg: cfg, checker: checker, mux: http.NewServeMux()}
	for _, rt := range []route{
		{http.MethodGet, "/internal/v1/authorisations/{consentId}", s.read},
		{http.MethodPost, "/internal/v1/authorisations/{consentId}/customer", s.customer},
	} {
		s.mux.Handle(rt.method+" "+rt.path, rt)
	}
	return s, nil
}

// ServeHTTP checks the token of the call and then routes it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	jws := ""
	if scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		jws = strings.TrimSpace(value)
	}
	claims, err := s.checker.Verify(r.Context(), jws)
	if err == nil && claims.Subject != Subject {
		err = &token.Error{Rule: "subject", Status: token.StatusUnauthorized, Reason: fmt.Sprintf("sub %q is not %q", claims.Subject, Subject)}
	}
	if err == nil && claims.ExpiresAt.After(s.cfg.Now().Add(MaxLifetime+token.Skew)) {
		err = &token.Error{Rule: "lifetime", Status: token.StatusUnauthorized, Reason: fmt.Sprintf("the token ends more than %s from now", MaxLifetime+token.Skew)}
	}
	var refused *token.Error
	switch {
	case errors.As(err, &refused):
		s.cfg.Logf("authz: %s %q: %v", r.Method, r.URL.Path, err)
		w.Header().Set("WWW-Authenticate", token.Challenge)
		fail(w, http.StatusUnauthorized, "invalid_token")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	// The mux alone would serve HEAD as GET and would redirect a path that is not clean.
	// Neither is a call: only a route found with its own method is served.
	h, _ := s.mux.Handler(r)
	if rt, ok := h.(route); !ok || rt.method != r.Method {
		fail(w, http.StatusNotFound, "no-route")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // no call takes a larger body
	s.mux.ServeHTTP(w, r)
}

// write writes v as the JSON body of an answer.
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes an answer other than 200.
func fail(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}

// internal logs err and answers 500.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.cfg.Logf("authz: %s %q: %v", r.Method, r.URL.Path, err)
	fail(w, http.StatusInternalServerError, "internal")
}

// load returns the consent the path names. When there is none it answers and returns false.
func (s *Server) load(w http.ResponseWriter, r *http.Request) (store.Consent, bool) {
	c, found, err := s.cfg.Store.Get(r.Context(), r.PathValue("consentId"))
	switch {
	case err != nil:
		s.internal(w, r, err)
	case !found:
		fail(w, http.StatusNotFound, "consent-not-found")
	}
	return c, err == nil && found
}

// decode reads the one JSON object of a request body into v. A member v does not have, a
// second value and a body that is not JSON are refused.
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("the body holds more than one value")
	}
	return nil
}

// summary is what the customer is asked to agree to. Only account-access consents exist; the
// package that adds another consent family adds its summary here.
func summary(c store.Consent) (map[string]any, error) {
	if c.Family != consent.AccountAccess {
		return nil, fmt.Errorf("consent %s: no summary for the family %q", c.ID, c.Family)
	}
	var req accountinfo.OBReadConsent1
	if err := json.Unmarshal(c.Request, &req); err != nil {
		return nil, fmt.Errorf("stored consent %s: %w", c.ID, err)
	}
	permissions := make([]string, 0, len(req.Data.Permissions))
	for _, p := range req.Data.Permissions {
		permissions = append(permissions, string(p))
	}
	return map[string]any{"permissions": permissions}, nil
}

// read answers GET /internal/v1/authorisations/{consentId}?client_id=<id>.
func (s *Server) read(w http.ResponseWriter, r *http.Request) {
	c, ok := s.load(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("client_id") != c.ClientID {
		fail(w, http.StatusForbidden, "client-mismatch")
		return
	}
	if c.State != consent.Awaiting {
		fail(w, http.StatusConflict, "not-awaiting")
		return
	}
	sum, err := summary(c)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	expires := ""
	if !c.ExpiresAt.IsZero() {
		expires = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	write(w, http.StatusOK, map[string]any{"consentId": c.ID, "family": c.Family, "clientId": c.ClientID,
		"state": c.State, "expiresAt": expires, "summary": sum})
}

// account is one account the customer may choose.
type account struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`  // the nickname, or the sub-type when the account has no nickname
	MaskedNumber string `json:"maskedNumber"` // four asterisks and the last four characters of the identification
	Currency     string `json:"currency"`
}

// mask hides all but the last four characters of an account identification. An identification
// of four characters or fewer is not shown at all.
func mask(identification string) string {
	r := []rune(identification)
	if len(r) <= 4 {
		return ""
	}
	return "****" + string(r[len(r)-4:])
}

// customer answers POST /internal/v1/authorisations/{consentId}/customer.
func (s *Server) customer(w http.ResponseWriter, r *http.Request) {
	c, ok := s.load(w, r)
	if !ok {
		return
	}
	var body struct {
		Identity *struct {
			Scheme     string            `json:"scheme"`
			Subject    string            `json:"subject"`
			Attributes map[string]string `json:"attributes"`
		} `json:"identity"`
	}
	if err := decode(r, &body); err != nil || body.Identity == nil || body.Identity.Scheme == "" || body.Identity.Subject == "" {
		fail(w, http.StatusBadRequest, "bad-request")
		return
	}
	if c.State != consent.Awaiting {
		fail(w, http.StatusConflict, "not-awaiting")
		return
	}
	cust, err := s.cfg.Ledger.ResolveCustomer(r.Context(), body.Identity.Scheme, body.Identity.Subject, body.Identity.Attributes)
	if errors.Is(err, ledgerclient.ErrNotFound) {
		fail(w, http.StatusNotFound, "customer-unknown")
		return
	}
	if err == nil && cust.GetCustomerId() == "" {
		err = errors.New("the ledger resolved a customer without an identifier")
	}
	var accounts []account
	if err == nil {
		list, lerr := s.cfg.Ledger.Accounts(r.Context(), cust.GetCustomerId())
		err = lerr
		accounts = make([]account, 0, len(list))
		for _, a := range list {
			name := a.GetNickname()
			if name == "" {
				name = a.GetSubType()
			}
			accounts = append(accounts, account{ID: a.GetAccountId(), DisplayName: name, MaskedNumber: mask(a.GetIdentification()), Currency: a.GetCurrency()})
		}
	}
	if err != nil {
		s.cfg.Logf("authz: consent %s: %v", c.ID, err)
		fail(w, http.StatusBadGateway, "ledger-unavailable")
		return
	}
	write(w, http.StatusOK, map[string]any{"customerId": cust.GetCustomerId(), "accounts": accounts})
}
