// Package api is the resource server of the open-banking product: the router over the
// generated server interfaces of the profile's documents, the token check on every route,
// and the operations it serves.
//
// Every operation of every served document is a route. A call to a resource path is checked
// in two places. Before routing, rules T1 to T5 of package token verify the token; a call
// without a valid token ends with 401 whatever it addresses. On the route itself the full
// rules T1 to T10 run, with the scopes, the grant and the family the document gives the
// operation. An operation under a notOffered prefix of the profile, an operation of an API
// family the installation does not serve, and an operation listed in unimplemented.txt
// answer the profile's notImplementedStatus after the token is verified.
//
// unimplemented.txt names, one per line as "<api> <METHOD> <path>", the operations the
// profile serves and this package does not implement yet. It only shrinks: the package that
// implements an operation removes its line.
package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/fingate/internal/definitions"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/gen/confirmationfunds"
	"github.com/openova-io/openova/products/fingate/internal/gen/events"
	"github.com/openova-io/openova/products/fingate/internal/gen/paymentinitiation"
	"github.com/openova-io/openova/products/fingate/internal/gen/vrp"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/profile"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

//go:embed unimplemented.txt
var unimplementedList string

// InteractionHeader is the correlation header of every request and answer.
const InteractionHeader = "x-fapi-interaction-id"

// ErrorCode is the code of every error entry this server writes: the reason is in the message.
const ErrorCode = "NARR"

// families maps the api name of a served document to its API family (the product of rule
// T10) and to the consent family of its customer operations (rule T9).
var families = map[string]struct{ product, consent string }{
	"account-info":       {"accounts", "account-access"},
	"payment-initiation": {"payments", "payment"},
	"vrp":                {"vrp", "vrp"},
	"confirmation-funds": {"funds-confirmation", "funds-confirmation"},
	"events":             {"events", ""},
}

// Config configures a Server.
type Config struct {
	Profile     *profile.Profile    // loaded with profile.Load
	Documents   fs.FS               // the profile folder; its documents are read for servers and security
	Definitions *definitions.Holder // the installation's definitions; read on every call
	Checker     *token.Checker      // built with Definitions as its Partners and Consents{Store} as its Consents
	Store       store.Store
	Ledger      *ledgerclient.Client // the ledger port, the only way to core banking
	Now         func() time.Time     // nil: time.Now
	NewID       func() string        // nil: 32 random hexadecimal characters
}

// Operation is one route of the server.
type Operation struct {
	Key        string // "<api> <METHOD> <path>", the form of unimplemented.txt
	API        string
	Method     string
	Path       string // the path as the document writes it
	Pattern    string // the route: "<METHOD> <server path><path>"
	Product    string
	Consent    string // the consent family of a customer operation
	Grant      token.Grant
	Scopes     []string
	NotOffered bool
	Served     bool // implemented here: neither under a notOffered prefix nor listed in unimplemented.txt
}

// The documents of which this package implements no operation yet. Every one of their
// operations is listed in unimplemented.txt, so its route answers before it reaches them.
type (
	paymentsPending struct {
		paymentinitiation.ServerInterface
	}
	vrpPending   struct{ vrp.ServerInterface }
	fundsPending struct {
		confirmationfunds.ServerInterface
	}
	eventsPending struct{ events.ServerInterface }
)

// Server is the resource server. It is an http.Handler for every path under the profile's basePath.
type Server struct {
	cfg   Config
	mux   *http.ServeMux
	ops   map[string]*Operation // by Pattern
	bases map[string]string     // the server path of each served document, by api
	impl  *accountInfo
}

type contextKey int

const (
	claimsKey contextKey = 1
	urlKey    contextKey = 2 // the URL of the call, for the links and the page of an answer
)

// recorder registers every route on the mux and remembers it.
type recorder struct {
	*http.ServeMux
	patterns []string
}

func (r *recorder) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.ServeMux.HandleFunc(pattern, h)
}

// New reads the documents of the profile and builds the routes.
func New(cfg Config) (*Server, error) {
	if cfg.Profile == nil || cfg.Documents == nil || cfg.Definitions == nil || cfg.Checker == nil || cfg.Store == nil || cfg.Ledger == nil {
		return nil, errors.New("api: the configuration needs Profile, Documents, Definitions, Checker, Store and Ledger")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	s := &Server{cfg: cfg, ops: map[string]*Operation{}, bases: map[string]string{}}
	pending, err := parseUnimplemented(unimplementedList)
	if err != nil {
		return nil, err
	}
	rec := &recorder{ServeMux: http.NewServeMux()}
	s.impl = &accountInfo{s: s}
	mounted := map[string]bool{}
	for _, d := range cfg.Profile.Documents {
		f, ok := families[d.API]
		if !ok {
			if d.Served != 0 {
				return nil, fmt.Errorf("api: document %s serves %d operations and has no API family here", d.API, d.Served)
			}
			continue // a document whose operations the product calls, not serves
		}
		base, sec, err := readDocument(cfg.Documents, d.File)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(base, cfg.Profile.BasePath+"/") && base != cfg.Profile.BasePath {
			return nil, fmt.Errorf("api: document %s has the server %q outside the basePath %q", d.API, base, cfg.Profile.BasePath)
		}
		s.bases[d.API] = base
		for _, o := range d.Ops {
			op := &Operation{Key: d.API + " " + o.Method + " " + o.Path, API: d.API, Method: o.Method, Path: o.Path,
				Pattern: o.Method + " " + base + o.Path, Product: f.product, NotOffered: cfg.Profile.NotOfferedPath(o.Path)}
			req, ok := sec[o.Method+" "+o.Path]
			if !ok {
				return nil, fmt.Errorf("api: %s has no security requirement", op.Key)
			}
			op.Grant, op.Scopes = req.grant, req.scopes
			if op.Grant == token.GrantCustomer {
				op.Consent = f.consent
			}
			if pending[op.Key] && op.NotOffered {
				return nil, fmt.Errorf("api: unimplemented.txt lists %s, which is not offered", op.Key)
			}
			op.Served = !op.NotOffered && !pending[op.Key]
			delete(pending, op.Key)
			s.ops[op.Pattern] = op
		}
		switch d.API {
		case "account-info":
			strict := accountinfo.NewStrictHandlerWithOptions(s.impl, nil, accountinfo.StrictHTTPServerOptions{
				RequestErrorHandlerFunc: s.badRequest, ResponseErrorHandlerFunc: s.internalError})
			accountinfo.HandlerWithOptions(strict, accountinfo.StdHTTPServerOptions{BaseURL: base, BaseRouter: rec,
				Middlewares: []accountinfo.MiddlewareFunc{s.check}, ErrorHandlerFunc: s.badRequest})
		case "payment-initiation":
			paymentinitiation.HandlerWithOptions(paymentsPending{}, paymentinitiation.StdHTTPServerOptions{BaseURL: base, BaseRouter: rec,
				Middlewares: []paymentinitiation.MiddlewareFunc{s.check}, ErrorHandlerFunc: s.badRequest})
		case "vrp":
			vrp.HandlerWithOptions(vrpPending{}, vrp.StdHTTPServerOptions{BaseURL: base, BaseRouter: rec,
				Middlewares: []vrp.MiddlewareFunc{s.check}, ErrorHandlerFunc: s.badRequest})
		case "confirmation-funds":
			confirmationfunds.HandlerWithOptions(fundsPending{}, confirmationfunds.StdHTTPServerOptions{BaseURL: base, BaseRouter: rec,
				Middlewares: []confirmationfunds.MiddlewareFunc{s.check}, ErrorHandlerFunc: s.badRequest})
		case "events":
			events.HandlerWithOptions(eventsPending{}, events.StdHTTPServerOptions{BaseURL: base, BaseRouter: rec,
				Middlewares: []events.MiddlewareFunc{s.check}, ErrorHandlerFunc: s.badRequest})
		}
		mounted[d.API] = true
	}
	if len(pending) > 0 {
		keys := make([]string, 0, len(pending))
		for k := range pending {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("api: unimplemented.txt lists operations of no served document: %s", strings.Join(keys, "; "))
	}
	if len(rec.patterns) != len(s.ops) {
		return nil, fmt.Errorf("api: the generated code registers %d routes, the documents have %d operations", len(rec.patterns), len(s.ops))
	}
	for _, p := range rec.patterns {
		if s.ops[p] == nil {
			return nil, fmt.Errorf("api: the generated code registers %q, which no document operation has", p)
		}
	}
	for api := range families {
		if !mounted[api] {
			return nil, fmt.Errorf("api: the profile has no document %s", api)
		}
	}
	s.mux = rec.ServeMux
	return s, nil
}

// Operations returns every route, sorted by pattern.
func (s *Server) Operations() []Operation {
	out := make([]Operation, 0, len(s.ops))
	for _, op := range s.ops {
		out = append(out, *op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// ServeHTTP answers a call to a resource path.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	iid := r.Header.Get(InteractionHeader)
	if iid == "" {
		iid = s.cfg.NewID()
		r.Header.Set(InteractionHeader, iid)
	}
	w.Header().Set(InteractionHeader, iid)
	if _, err := s.cfg.Checker.Verify(r.Context(), bearer(r)); err != nil {
		s.refuse(w, err)
		return
	}
	// The generated code binds a body of "application/json; charset=utf-8" twice and fails on
	// the second read; that form means the same as "application/json".
	if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "application/json" &&
		len(params) == 1 && strings.EqualFold(params["charset"], "utf-8") {
		r.Header.Set("Content-Type", "application/json")
	}
	_, pattern := s.mux.Handler(r)
	op := s.ops[pattern]
	if op == nil {
		s.mux.ServeHTTP(w, r) // no route: the router answers 404 or 405
		return
	}
	defs := s.cfg.Definitions.Current()
	if defs == nil {
		s.writeError(w, http.StatusServiceUnavailable, "the definitions of this installation are not loaded yet")
		return
	}
	if !op.Served || !defs.Serves(op.Product) {
		w.WriteHeader(s.cfg.Profile.NotImplementedStatus)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// check applies rules T1 to T10 on a route. It runs after the generated code bound the parameters.
func (s *Server) check(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op := s.ops[r.Pattern]
		if op == nil || !op.Served {
			w.WriteHeader(s.cfg.Profile.NotImplementedStatus)
			return
		}
		desc := token.Operation{Grant: op.Grant, Scopes: op.Scopes, Product: op.Product}
		if op.Grant == token.GrantCustomer {
			desc.ConsentFamily, desc.AccountID, desc.ConsentID = op.Consent, r.PathValue("AccountId"), r.PathValue("ConsentId")
			// A family's implementation names the consent permissions an operation needs (rule
			// T9) with a method permissions(key string) []string; without one, none is needed.
			if p, ok := any(s.impl).(interface{ permissions(key string) []string }); ok {
				desc.Permissions = p.permissions(op.Key)
			}
		}
		claims, err := s.cfg.Checker.Check(r.Context(), token.Request{JWS: bearer(r), Thumbprint: thumbprint(r), Operation: desc})
		if err != nil {
			s.refuse(w, err)
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), claimsKey, claims), urlKey, *r.URL)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// claimsOf returns the verified claims that check stored for the call.
func claimsOf(ctx context.Context) token.Claims {
	c, _ := ctx.Value(claimsKey).(token.Claims)
	return c
}

// bearer returns the token of the Authorization header, or "" when it is not a bearer token.
func bearer(r *http.Request) string {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}

// thumbprint returns the thumbprint of the client certificate verified on this connection, or "".
func thumbprint(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return token.Thumbprint(r.TLS.PeerCertificates[0].Raw)
}

// refuse answers a failed token rule, or 500 when the check could not run.
func (s *Server) refuse(w http.ResponseWriter, err error) {
	var te *token.Error
	if !errors.As(err, &te) {
		s.writeError(w, http.StatusInternalServerError, "the token could not be checked")
		return
	}
	if te.Status == token.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", token.Challenge)
	}
	s.writeError(w, te.Status, "rule "+te.Rule+": "+te.Reason)
}

func (s *Server) badRequest(w http.ResponseWriter, _ *http.Request, err error) {
	s.writeError(w, http.StatusBadRequest, err.Error())
}

// internalError answers an error of an operation: an *answer with its status, and anything
// else with 500. A 404 has no body, as the documents declare it.
func (s *Server) internalError(w http.ResponseWriter, _ *http.Request, err error) {
	var a *answer
	switch {
	case !errors.As(err, &a):
		s.writeError(w, http.StatusInternalServerError, "the answer could not be written")
	case a.status == http.StatusNotFound:
		w.WriteHeader(http.StatusNotFound)
	default:
		s.writeError(w, a.status, a.message)
	}
}

// errorBody is the documents' error schema with one entry.
func errorBody(message string) accountinfo.OBErrorResponse1 {
	if len(message) > 500 {
		message = message[:500]
	}
	return accountinfo.OBErrorResponse1{Errors: []accountinfo.OBError1{{ErrorCode: ErrorCode, Message: &message}}}
}

// writeError writes the documents' error schema with the status.
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(errorBody(message))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// requirement is the security requirement of one operation.
type requirement struct {
	grant  token.Grant
	scopes []string
}

// readDocument returns the server path of a document and the security requirement of each
// operation, keyed "<METHOD> <path>".
func readDocument(fsys fs.FS, file string) (string, map[string]requirement, error) {
	raw, err := fs.ReadFile(fsys, file)
	if err != nil {
		return "", nil, fmt.Errorf("api: %w", err)
	}
	type secList = []map[string][]string
	var doc struct {
		Servers  []struct{ URL string } `yaml:"servers"`
		Security secList                `yaml:"security"`
		Paths    map[string]map[string]struct {
			Security *secList `yaml:"security"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("api: %s: %w", file, err)
	}
	if len(doc.Servers) != 1 || !strings.HasPrefix(doc.Servers[0].URL, "/") {
		return "", nil, fmt.Errorf("api: %s must name exactly one server, as a path", file)
	}
	out := map[string]requirement{}
	for path, item := range doc.Paths {
		for method, op := range item {
			m := strings.ToUpper(method)
			if !slices.Contains([]string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"}, m) {
				continue
			}
			list := doc.Security
			if op.Security != nil {
				list = *op.Security
			}
			if len(list) != 1 || len(list[0]) != 1 {
				return "", nil, fmt.Errorf("api: %s: %s %s must have exactly one security requirement", file, m, path)
			}
			for scheme, scopes := range list[0] {
				var g token.Grant
				switch scheme {
				case "TPPOAuth2Security":
					g = token.GrantClient
				case "PSUOAuth2Security":
					g = token.GrantCustomer
				default:
					return "", nil, fmt.Errorf("api: %s: %s %s uses the unknown scheme %s", file, m, path, scheme)
				}
				if len(scopes) == 0 {
					return "", nil, fmt.Errorf("api: %s: %s %s names no scope", file, m, path)
				}
				out[m+" "+path] = requirement{grant: g, scopes: scopes}
			}
		}
	}
	return strings.TrimSuffix(doc.Servers[0].URL, "/"), out, nil
}

// parseUnimplemented reads unimplemented.txt: one "<api> <METHOD> <path>" per line, no duplicates.
func parseUnimplemented(text string) (map[string]bool, error) {
	out := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) != 3 || strings.Join(f, " ") != line || out[line] {
			return nil, fmt.Errorf("api: unimplemented.txt line %d %q is not \"<api> <METHOD> <path>\" or is repeated", n, line)
		}
		out[line] = true
	}
	return out, nil
}
