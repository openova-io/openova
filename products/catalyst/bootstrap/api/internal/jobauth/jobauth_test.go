package jobauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// token builds a JWT-shaped string with the given payload. Its signature is
// not real: in these tests the fake TokenReview is the judge.
func token(t *testing.T, payload map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256", "kid": "k"}) + "." + enc(payload) + ".c2ln"
}

// jobToken is shaped like a projected ServiceAccount token.
func jobToken(t *testing.T) string {
	return token(t, map[string]any{
		"iss":           "https://kubernetes.default.svc.cluster.local",
		"sub":           Account,
		"kubernetes.io": map[string]any{"namespace": "catalyst-system", "serviceaccount": map[string]string{"name": "catalyst-conformance"}},
	})
}

// cluster answers every TokenReview with the given user; "" means not
// authenticated. It counts the reviews.
type cluster struct {
	user    string
	fail    error
	reviews int
}

func (c *cluster) clients() (kubernetes.Interface, error) {
	cs := fakek8s.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		c.reviews++
		if c.fail != nil {
			return true, nil, c.fail
		}
		tr := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenReview).DeepCopy()
		tr.Status = authnv1.TokenReviewStatus{Authenticated: c.user != "", User: authnv1.UserInfo{Username: c.user}}
		return true, tr, nil
	})
	return cs, nil
}

// served records what reached the session middleware and the handler.
type served struct {
	session, handler bool
	claims           *auth.Claims
	req              *http.Request
	body             string
}

func serve(t *testing.T, clients Clients, req *http.Request) (*httptest.ResponseRecorder, *served) {
	t.Helper()
	s := &served{}
	session := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.session = true
			w.WriteHeader(http.StatusTeapot)
		})
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handler = true
		s.claims = auth.ClaimsFromContext(r.Context())
		s.req = r
		b, _ := io.ReadAll(r.Body)
		s.body = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := httptest.NewRecorder()
	RequireSessionOrJob(clients, session, log)(next).ServeHTTP(rec, req)
	return rec, s
}

func request(method, target, body, bearerToken string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body %q: %v", rec.Body.String(), err)
	}
	return body["error"]
}

const (
	appPath = "/api/v1/sovereigns/dep1/applications/accept-1"
	create  = `{"blueprint":"bp-wordpress","org":"conformance","environment":"conformance-dev","name":"accept-1"}`
	move    = `{"placement":{"targets":[{"region":"a","role":"primary"}],"ownedDependencies":[]}}`
)

// TestJobMayCreateMoveAndDeleteInConformance: the three requests of the
// table reach the handler in Organization conformance, with the claims of
// the account and without the token, the cookie or a host.
func TestJobMayCreateMoveAndDeleteInConformance(t *testing.T) {
	for _, c := range []struct{ name, method, target, body string }{
		{"create", http.MethodPost, "/catalyst/v1/apps/instances", create},
		{"move", http.MethodPut, appPath + "?namespace=conformance", move},
		{"delete", http.MethodDelete, appPath + "?namespace=conformance", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := &cluster{user: Account}
			req := request(c.method, c.target, c.body, jobToken(t))
			req.Header.Set("Cookie", "catalyst_session=x")
			req.Header.Set("X-Forwarded-Host", "console.acme.omani.homes")
			req.Header.Set("X-User-Email", "someone@acme.example")
			rec, s := serve(t, cl.clients, req)
			if rec.Code != http.StatusNoContent || !s.handler || s.session {
				t.Fatalf("got %d handler=%v session=%v body=%s; want 204 from the handler alone", rec.Code, s.handler, s.session, rec.Body.String())
			}
			if cl.reviews != 1 {
				t.Errorf("token reviews = %d, want 1", cl.reviews)
			}
			if s.claims == nil || s.claims.Sub != Account || s.claims.Org != Organization || s.claims.Tier != "admin" {
				t.Errorf("claims = %+v, want subject %s, Organization %s, tier admin", s.claims, Account, Organization)
			}
			for _, h := range []string{"Authorization", "Cookie", "X-Forwarded-Host", "X-User-Email"} {
				if v := s.req.Header.Get(h); v != "" {
					t.Errorf("header %s reached the handler: %q", h, v)
				}
			}
			if s.req.Host != "" || s.req.Header.Get("X-User-Sub") != Account {
				t.Errorf("host %q, X-User-Sub %q; want no host and the account", s.req.Host, s.req.Header.Get("X-User-Sub"))
			}
			if s.body != c.body {
				t.Errorf("the handler read body %q, want %q", s.body, c.body)
			}
		})
	}
}

// TestJobIsForbiddenOutsideConformance: another Organization, a request
// that does not say which Organization, or any other route is 403.
func TestJobIsForbiddenOutsideConformance(t *testing.T) {
	for _, c := range []struct{ name, method, target, body string }{
		{"create in another Organization", http.MethodPost, "/catalyst/v1/apps/instances", strings.Replace(create, `"conformance"`, `"acme"`, 1)},
		{"create without an Organization", http.MethodPost, "/catalyst/v1/apps/instances", `{"blueprint":"bp-wordpress","name":"accept-1"}`},
		{"create with a body that is not an object", http.MethodPost, "/catalyst/v1/apps/instances", `[]`},
		{"create with a body above the limit", http.MethodPost, "/catalyst/v1/apps/instances", `{"org":"conformance","pad":"` + strings.Repeat("x", maxBody) + `"}`},
		{"move in another namespace", http.MethodPut, appPath + "?namespace=acme", move},
		{"move without a namespace", http.MethodPut, appPath, move},
		{"move naming two namespaces", http.MethodPut, appPath + "?namespace=conformance&namespace=acme", move},
		{"move that also changes the parameters", http.MethodPut, appPath + "?namespace=conformance", `{"placement":{},"parameters":{"a":"b"}}`},
		{"update without a placement", http.MethodPut, appPath + "?namespace=conformance", `{"version":"1.2.3"}`},
		{"delete in another namespace", http.MethodDelete, appPath + "?namespace=acme", ""},
		{"delete without a namespace", http.MethodDelete, appPath, ""},
		{"install through the Organization route", http.MethodPost, "/api/v1/org/applications", create},
		{"install through the deployment route", http.MethodPost, "/api/v1/sovereigns/dep1/applications", create},
		{"read an Application", http.MethodGet, appPath + "?namespace=conformance", ""},
		{"delete a deployment", http.MethodDelete, "/api/v1/deployments/dep1", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := &cluster{user: Account}
			rec, s := serve(t, cl.clients, request(c.method, c.target, c.body, jobToken(t)))
			if rec.Code != http.StatusForbidden || s.handler || s.session {
				t.Fatalf("got %d handler=%v session=%v; want 403 and nothing served", rec.Code, s.handler, s.session)
			}
			if code := errorCode(t, rec); code != "job-token-forbidden" {
				t.Errorf("error = %q, want job-token-forbidden", code)
			}
		})
	}
}

// TestAnotherAccountIsUnauthenticated: a token of another account, and a
// token the cluster does not authenticate, are 401 on every route.
func TestAnotherAccountIsUnauthenticated(t *testing.T) {
	for _, user := range []string{"system:serviceaccount:default:other", "system:serviceaccount:catalyst-system:catalyst-api", ""} {
		cl := &cluster{user: user}
		rec, s := serve(t, cl.clients, request(http.MethodPost, "/catalyst/v1/apps/instances", create, jobToken(t)))
		if rec.Code != http.StatusUnauthorized || s.handler || s.session {
			t.Fatalf("user %q: got %d handler=%v session=%v; want 401 and nothing served", user, rec.Code, s.handler, s.session)
		}
		if code := errorCode(t, rec); code != "unauthenticated" {
			t.Errorf("user %q: error = %q, want unauthenticated", user, code)
		}
	}
}

// TestTokenReviewThatCannotRunIs502: no cluster client, or a failing
// review call, is 502 and serves nothing.
func TestTokenReviewThatCannotRunIs502(t *testing.T) {
	noClient := func() (kubernetes.Interface, error) { return nil, errors.New("not in a cluster") }
	failing := &cluster{fail: errors.New("apiserver down")}
	for name, clients := range map[string]Clients{"no client": noClient, "failing review": failing.clients} {
		rec, s := serve(t, clients, request(http.MethodPost, "/catalyst/v1/apps/instances", create, jobToken(t)))
		if rec.Code != http.StatusBadGateway || s.handler || s.session {
			t.Fatalf("%s: got %d handler=%v session=%v; want 502 and nothing served", name, rec.Code, s.handler, s.session)
		}
		if code := errorCode(t, rec); code != "token-review-failed" {
			t.Errorf("%s: error = %q, want token-review-failed", name, code)
		}
	}
}

// TestOtherRequestsGoToTheSession: no bearer, a session bearer, a bearer
// that is not a JWT and an OPTIONS request go to the session middleware,
// and no TokenReview runs.
func TestOtherRequestsGoToTheSession(t *testing.T) {
	session := token(t, map[string]any{"sub": "u1", "email": "u@acme.example", "tier": "owner"})
	for _, c := range []struct{ name, method, bearer string }{
		{"no bearer", http.MethodPost, ""},
		{"session bearer", http.MethodPost, session},
		{"not a JWT", http.MethodPost, "opaque-token"},
		{"payload not base64", http.MethodPost, "a.%%%.c"},
		{"OPTIONS with a job token", http.MethodOptions, jobToken(t)},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := &cluster{user: Account}
			rec, s := serve(t, cl.clients, request(c.method, "/catalyst/v1/apps/instances", create, c.bearer))
			if rec.Code != http.StatusTeapot || !s.session || s.handler {
				t.Fatalf("got %d session=%v handler=%v; want the session middleware alone", rec.Code, s.session, s.handler)
			}
			if cl.reviews != 0 {
				t.Errorf("token reviews = %d, want 0", cl.reviews)
			}
		})
	}
}
