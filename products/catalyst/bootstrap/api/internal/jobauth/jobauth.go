// Package jobauth lets one in-cluster Job call the console API with the
// ServiceAccount token that Kubernetes projects into its pod.
//
// The conformance Job of a Sovereign has no person and no session. It sends
// "Authorization: Bearer <projected token>". RequireSessionOrJob wraps the
// session middleware of the session route group. Its rules, in the order
// they apply:
//
//  1. A request without a bearer, a request whose bearer is not a projected
//     ServiceAccount token (its payload has no "kubernetes.io" claim), and
//     every OPTIONS request go to the session middleware, unchanged.
//  2. A projected token is judged by a TokenReview of the cluster the API
//     runs in, never by reading it. When the review cannot run: 502
//     token-review-failed.
//  3. A token the review does not authenticate, and a token of any account
//     but Account: 401 unauthenticated.
//  4. A request of Account passes only when it matches a row of allowed and
//     names the Organization Organization the way that row reads it.
//     Otherwise: 403 job-token-forbidden.
//  5. A request that passes goes to the next handler, past the session
//     middleware. It carries claims of tier admin for Organization
//     Organization whose subject is Account. Its Authorization, Cookie,
//     X-Forwarded-Host and X-User-Email headers and its Host are removed,
//     so no handler forwards the token or takes an Organization from the
//     host.
//
// An Application of an Organization lives in the namespace named after the
// Organization (internal/handler/install_refs.go), so the namespace
// Organization holds the Applications of Organization Organization.
package jobauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const (
	// Account is the TokenReview user name of the one ServiceAccount that
	// may call the API with its token.
	Account = "system:serviceaccount:catalyst-system:catalyst-conformance"
	// Organization is the one Organization whose Applications it may change.
	Organization = "conformance"
	// tier is the tier of the claims a request of Account carries. The
	// Application handlers ask for admin or higher.
	tier = "admin"
	// maxBody is the largest request body read: the limit of the API's
	// mutation handlers.
	maxBody = 1 << 16
)

// Clients returns the cluster client that the TokenReview runs with.
type Clients func() (kubernetes.Interface, error)

// route is one request Account may make.
type route struct {
	method string
	path   *regexp.Regexp
	// names returns nil when the request names Organization, and otherwise
	// says why not.
	names func(r *http.Request) error
}

var applicationPath = regexp.MustCompile(`^/api/v1/sovereigns/[^/]+/applications/[^/]+$`)

// allowed is every request Account may make: create, move and delete an
// Application. Nothing else.
var allowed = []route{
	{http.MethodPost, regexp.MustCompile(`^/catalyst/v1/apps/instances$`), orgInBody},
	{http.MethodPut, applicationPath, placementInNamespace},
	{http.MethodDelete, applicationPath, inNamespace},
}

// RequireSessionOrJob returns the middleware of the session route group:
// session for every request that does not carry a projected ServiceAccount
// token, and the rules of this package for every request that does.
func RequireSessionOrJob(clients Clients, session func(http.Handler) http.Handler, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		viaSession := session(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearer(r)
			if r.Method == http.MethodOptions || !projected(token) {
				viaSession.ServeHTTP(w, r)
				return
			}
			user, authenticated, err := review(r.Context(), clients, token)
			if err != nil {
				log.Warn("job token: the token review did not run", "err", err)
				write(w, http.StatusBadGateway, "token-review-failed", err.Error())
				return
			}
			if !authenticated || user != Account {
				log.Info("job token: refused an account", "user", user)
				write(w, http.StatusUnauthorized, "unauthenticated", "the token is not a token of "+Account)
				return
			}
			if err := admit(r); err != nil {
				log.Info("job token: refused a request", "method", r.Method, "path", r.URL.Path, "why", err.Error())
				write(w, http.StatusForbidden, "job-token-forbidden", err.Error())
				return
			}
			next.ServeHTTP(w, confine(r))
		})
	}
}

var (
	inClusterMu   sync.Mutex
	inClusterCore kubernetes.Interface
)

// InCluster is the Clients of the cluster this pod runs in. It builds the
// client on first use, not when the router is built, and keeps it once it
// is built.
func InCluster() (kubernetes.Interface, error) {
	inClusterMu.Lock()
	defer inClusterMu.Unlock()
	if inClusterCore != nil {
		return inClusterCore, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	inClusterCore = core
	return core, nil
}

// bearer returns the token of an "Authorization: Bearer" header, or "".
func bearer(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// projected reports whether a token is shaped like a projected
// ServiceAccount token: three parts, the middle one a JSON object with a
// "kubernetes.io" member. It only decides where the request goes; the
// TokenReview decides whether the token is genuine.
func projected(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	_, ok := claims["kubernetes.io"]
	return ok
}

// review asks the cluster who the token belongs to. It returns the user
// name and true when the cluster authenticates the token, false when it
// does not, and an error when the review could not run.
func review(ctx context.Context, clients Clients, token string) (string, bool, error) {
	core, err := clients()
	if err != nil {
		return "", false, fmt.Errorf("no cluster client to review the token with: %v", err)
	}
	if core == nil {
		return "", false, fmt.Errorf("no cluster client to review the token with")
	}
	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}
	resp, err := core.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return "", false, fmt.Errorf("token review API call failed: %v", err)
	}
	if !resp.Status.Authenticated {
		return "", false, nil
	}
	return strings.TrimSpace(resp.Status.User.Username), true, nil
}

// admit returns nil when the request is a row of allowed and names
// Organization, and otherwise says why it is refused.
func admit(r *http.Request) error {
	for _, rt := range allowed {
		if r.Method == rt.method && rt.path.MatchString(r.URL.Path) {
			return rt.names(r)
		}
	}
	return fmt.Errorf("%s %s is not a request this token may make: it may create, move and delete Applications of Organization %s, nothing else", r.Method, r.URL.Path, Organization)
}

// orgInBody: the JSON body's field "org" is Organization.
func orgInBody(r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var doc struct {
		Org string `json:"org"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("the body is not a JSON object: %v", err)
	}
	if doc.Org != Organization {
		return fmt.Errorf("the body names Organization %q; this token may change Organization %s only", doc.Org, Organization)
	}
	return nil
}

// inNamespace: the query names namespace Organization, exactly once.
func inNamespace(r *http.Request) error {
	ns := r.URL.Query()["namespace"]
	if len(ns) != 1 || ns[0] != Organization {
		return fmt.Errorf("the query must name namespace=%s exactly once; this token may change Organization %s only", Organization, Organization)
	}
	return nil
}

// placementInNamespace: a move. The query names namespace Organization and
// the body is a JSON object whose one key is "placement".
func placementInNamespace(r *http.Request) error {
	if err := inNamespace(r); err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("the body is not a JSON object: %v", err)
	}
	if _, ok := doc["placement"]; !ok || len(doc) != 1 {
		return fmt.Errorf("a move changes the placement only: the body must hold exactly one key, \"placement\"")
	}
	return nil
}

// readBody reads the body and puts an identical one back for the handler.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, fmt.Errorf("the request has no body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read the body: %v", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("the body is larger than %d bytes", maxBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// confine returns the request as the next handler sees it: with the claims
// of Account and without any credential or host of its own.
func confine(r *http.Request) *http.Request {
	claims := &auth.Claims{Sub: Account, Org: Organization, Tier: tier}
	out := r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
	out.Header = r.Header.Clone()
	for _, name := range []string{"Authorization", "Cookie", "X-Forwarded-Host", "X-User-Email"} {
		out.Header.Del(name)
	}
	out.Header.Set("X-User-Sub", Account)
	out.Host = ""
	return out
}

// write sends a refusal in the shape of the API's other refusals.
func write(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": detail})
}
