package auth

import (
	"context"
	"strings"
)

// orgConfinedKey marks a request that a server-side route has already
// confined to one Organization's namespace (appmanage.Confine: the
// own-Organization binding resolved the namespace from the request host and
// forced it). It is a context value, never a header, so a client cannot set
// it.
const orgConfinedKey contextKey = "auth.org-confined"

// ClaimsAreOrgScoped reports whether a session is confined to a single
// Organization: a customer session minted on an Organization console host
// (tier OrgScopedTier, always with a non-empty Org), or an agent token
// (typ "agent"), confined to the Organization of its org_id claim whatever
// its tier. It is the one predicate the own-Organization binding
// (handler.ResolveOrganization) and appmanage.Confine both key off, so a
// request is never marked confined without that binding having run.
func ClaimsAreOrgScoped(claims *Claims) bool {
	if claims == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(claims.Tier), OrgScopedTier) ||
		strings.EqualFold(strings.TrimSpace(claims.Typ), "agent")
}

// WithOrgConfined returns ctx marked as confined to the caller's own
// Organization.
func WithOrgConfined(ctx context.Context) context.Context {
	return context.WithValue(ctx, orgConfinedKey, true)
}

// OrgConfined reports whether a server-side route confined the request to
// the caller's own Organization. A handler's tier gate accepts such a request
// for that Organization's own objects: the own-Organization binding is the
// authorization, as on the Organization install route. Only an
// Organization-scoped session (ClaimsAreOrgScoped) is ever marked: any other
// session keeps the tier gate it meets on the Sovereign route.
func OrgConfined(ctx context.Context) bool {
	v, _ := ctx.Value(orgConfinedKey).(bool)
	return v
}
