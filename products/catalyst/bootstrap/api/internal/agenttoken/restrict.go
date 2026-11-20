package agenttoken

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// Mutation is one request that changes state and that an agent token of at
// least MinTier may make.
type Mutation struct {
	Method  string
	Path    *regexp.Regexp // matched against the whole request path; anchor it
	MinTier string         // viewer or developer
	// Acting: only a token whose claim acting_person names a person of tier
	// admin (ActsForAdmin) may make the request.
	Acting bool
}

// MutationsAllowed is every request other than GET, HEAD and OPTIONS that
// an agent token may make at catalyst-api. It has one entry: an agent that
// acts for a person of tier admin in its Organization installs an
// Application there (the tool create_application). An agent token without
// that claim changes nothing. The package that needs another mutation adds
// its one entry.
var MutationsAllowed = []Mutation{
	{Method: http.MethodPost, Path: regexp.MustCompile(`^/api/v1/org/applications$`), MinTier: "viewer", Acting: true},
}

// ActsForAdmin reports whether the claims carry the claim acting_person of a
// person of tier admin: an email and the tier ActingTier.
func ActsForAdmin(claims *auth.Claims) bool {
	return claims != nil && claims.ActingPerson != nil && claims.ActingPerson.Tier == ActingTier &&
		strings.TrimSpace(claims.ActingPerson.Email) != ""
}

// Restrict is the middleware of the session route group that runs after
// the Organization scope guard. It does nothing for a request whose
// session claims are not those of an agent token (typ is not "agent").
// For an agent token:
//
//  1. an audience without catalyst-api: 401 agent-token-wrong-audience;
//  2. GET, HEAD and OPTIONS pass;
//  3. any other method passes only when an entry of MutationsAllowed
//     matches the method, the path and the tier, and, for an entry with
//     Acting, the token acts for a person of tier admin; otherwise 403
//     agent-token-forbidden.
//
// The Organization scope guard has already confined the token to the
// Organization of its claim org_id (claimsAreOrgScoped in
// internal/handler/org_scope.go).
func Restrict(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := auth.ClaimsFromContext(r.Context())
		if claims == nil || !strings.EqualFold(strings.TrimSpace(claims.Typ), Type) {
			next.ServeHTTP(w, r)
			return
		}
		if !slices.Contains(claims.Audience, AudienceAPI) {
			writeError(w, http.StatusUnauthorized, "agent-token-wrong-audience", "the audience of this agent token does not hold "+AudienceAPI)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		for _, m := range MutationsAllowed {
			if m.Acting && !ActsForAdmin(claims) {
				continue
			}
			if m.Method == r.Method && m.Path.MatchString(r.URL.Path) && tierRank[claims.Tier] >= tierRank[m.MinTier] && tierRank[m.MinTier] > 0 {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, "agent-token-forbidden", "an agent token may not "+r.Method+" "+r.URL.Path)
	})
}
