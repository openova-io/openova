// Package opsproxy forwards the console routes /api/v1/ops/* of catalyst-api to
// the operations-records service (products/ops-records, its package
// internal/api).
//
// The browser never calls ops-records. It calls catalyst-api at the same
// paths. The session route group has already verified the session, applied
// the Organization scope guard and agenttoken.Restrict. The proxy then:
//
//   - refuses a request without session claims or without an e-mail: 401
//     unauthenticated;
//   - refuses an agent token (claim typ "agent") on every method: 403
//     agent-token-forbidden. agenttoken.Restrict lets GET pass, so the proxy
//     refuses it itself;
//   - refuses a session that names no Organization and is not a
//     sovereign-admin (lifecycle.SovereignAdmin): 403 no-organization;
//   - removes every x-openova-* header, Cookie, Authorization and the query
//     parameter access_token the caller sent, and sets three headers itself:
//     x-openova-organization (the session's Organization; empty for a
//     sovereign-admin, who sees every Organization), x-openova-user (the
//     session's e-mail) and x-openova-tier (the session's tier as
//     ops-records names it, see tierOf);
//   - flushes every write of the answer at once, so the server-sent events of
//     GET /api/v1/ops/stream arrive as ops-records sends them;
//   - answers 502 ops-records-unavailable when ops-records cannot be reached,
//     and 503 ops-records-unconfigured when its address cannot be read.
//
// The address of ops-records is the environment variable URLEnv; unset, it is
// DefaultURL, the console port of the in-cluster Service.
package opsproxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/agenttoken"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/lifecycle"
)

const (
	// URLEnv names the base address of the console routes of ops-records.
	URLEnv = "CATALYST_OPS_RECORDS_URL"
	// DefaultURL is the console port of the Service ops-records in the
	// namespace ops-records.
	DefaultURL = "http://ops-records.ops-records.svc.cluster.local:8081"

	// The identity headers ops-records reads.
	HeaderOrganization = "x-openova-organization"
	HeaderUser         = "x-openova-user"
	HeaderTier         = "x-openova-tier"

	// TierSovereignAdmin is the tier of the view of every Organization.
	TierSovereignAdmin = "sovereign-admin"
)

// Route is one console route of ops-records.
type Route struct{ Method, Path string }

// Routes are the routes catalyst-api forwards, exactly those of ops-records.
var Routes = []Route{
	{http.MethodGet, "/api/v1/ops/incidents"},
	{http.MethodGet, "/api/v1/ops/incidents/{id}"},
	{http.MethodPost, "/api/v1/ops/incidents/{id}/actions"},
	{http.MethodGet, "/api/v1/ops/judgements"},
	{http.MethodGet, "/api/v1/ops/stream"},
}

// sessionTiers are the session tiers ops-records accepts as they are.
var sessionTiers = []string{"viewer", "developer", "operator", "admin", "owner"}

// Proxy serves the routes of Routes.
type Proxy struct {
	rp     *httputil.ReverseProxy
	broken error // the address cannot be read; every request answers 503
	log    *slog.Logger
}

// FromEnv returns the proxy to the address of URLEnv, or of DefaultURL.
func FromEnv(log *slog.Logger) *Proxy {
	base := strings.TrimSpace(os.Getenv(URLEnv))
	if base == "" {
		base = DefaultURL
	}
	return New(base, log)
}

// New returns the proxy to base, an http or https address.
func New(base string, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	p := &Proxy{log: log}
	target, err := url.Parse(base)
	if err == nil && ((target.Scheme != "http" && target.Scheme != "https") || target.Host == "") {
		err = fmt.Errorf("%q is not an http or https address", base)
	}
	if err != nil {
		p.broken = err
		log.Error("opsproxy: the address of ops-records cannot be read", "env", URLEnv, "error", err)
		return p
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:       func(pr *httputil.ProxyRequest) { pr.SetURL(target) },
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.log.Warn("opsproxy: ops-records cannot be reached", "path", r.URL.Path, "error", err)
			writeError(w, http.StatusBadGateway, "ops-records-unavailable", "the operations records cannot be reached now")
		},
	}
	return p
}

// ServeHTTP checks the session and forwards the request with the identity
// headers it sets itself.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || strings.TrimSpace(claims.Email) == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "the operations records need a session")
		return
	}
	if strings.EqualFold(strings.TrimSpace(claims.Typ), agenttoken.Type) {
		writeError(w, http.StatusForbidden, "agent-token-forbidden", "an agent token cannot use the console routes of the operations records")
		return
	}
	org, tier, ok := identityOf(claims)
	if !ok {
		writeError(w, http.StatusForbidden, "no-organization", "this session names no Organization and is not a sovereign-admin")
		return
	}
	if p.broken != nil {
		writeError(w, http.StatusServiceUnavailable, "ops-records-unconfigured", "the address of the operations records cannot be read")
		return
	}
	out := r.Clone(r.Context())
	for name := range out.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Openova-") {
			out.Header.Del(name)
		}
	}
	out.Header.Del("Cookie")
	out.Header.Del("Authorization")
	if q := out.URL.Query(); q.Has("access_token") {
		q.Del("access_token")
		out.URL.RawQuery = q.Encode()
	}
	out.Header.Set(HeaderOrganization, org)
	out.Header.Set(HeaderUser, strings.TrimSpace(claims.Email))
	out.Header.Set(HeaderTier, tier)
	p.rp.ServeHTTP(w, out)
}

// identityOf returns the Organization and the tier of a session that is not
// an agent token. A session that names an Organization is confined to it; one
// that names none sees every Organization only when it is a sovereign-admin
// and not Organization-scoped.
func identityOf(c *auth.Claims) (org, tier string, ok bool) {
	org = strings.ToLower(strings.TrimSpace(c.Org))
	scoped := strings.EqualFold(strings.TrimSpace(c.Tier), auth.OrgScopedTier)
	if org == "" {
		if scoped || !lifecycle.SovereignAdmin(c) {
			return "", "", false
		}
		return "", TierSovereignAdmin, true
	}
	return org, tierOf(c.Tier), true
}

// tierOf names a session tier as ops-records does: a tier of sessionTiers as
// it is; the tier of an Organization-scoped session (auth.OrgScopedTier), the
// administrator of its own Organization, as admin; any other value as viewer,
// which only reads.
func tierOf(sessionTier string) string {
	t := strings.ToLower(strings.TrimSpace(sessionTier))
	if t == auth.OrgScopedTier {
		return "admin"
	}
	for _, known := range sessionTiers {
		if t == known {
			return t
		}
	}
	return "viewer"
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": detail})
}
