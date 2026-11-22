package auth

import "context"

// orgConfinedKey marks a request that a server-side route has already
// confined to one Organization's namespace (appmanage.Confine: the
// own-Organization binding resolved the namespace from the request host and
// forced it). It is a context value, never a header, so a client cannot set
// it.
const orgConfinedKey contextKey = "auth.org-confined"

// WithOrgConfined returns ctx marked as confined to the caller's own
// Organization.
func WithOrgConfined(ctx context.Context) context.Context {
	return context.WithValue(ctx, orgConfinedKey, true)
}

// OrgConfined reports whether a server-side route confined the request to
// the caller's own Organization. A handler's tier gate accepts such a request
// for that Organization's own objects: the own-Organization binding is the
// authorization, as on the Organization install route.
func OrgConfined(ctx context.Context) bool {
	v, _ := ctx.Value(orgConfinedKey).(bool)
	return v
}
