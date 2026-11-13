package main

import "github.com/go-chi/chi/v5"

// registerPublicOIDC registers the OIDC provider routes, when main() built
// the provider (CATALYST_OIDC_PROVIDER_ENABLED, #2725).
func registerPublicOIDC(r chi.Router, d routeDeps) {
	oidcProv := d.oidcProv
	if oidcProv == nil {
		return
	}
	r.Get("/oidc/.well-known/openid-configuration", oidcProv.Discovery)
	r.Get("/oidc/auth", oidcProv.Auth)
	r.Post("/oidc/token", oidcProv.Token)
	r.Get("/oidc/userinfo", oidcProv.Userinfo)
	r.Get("/oidc/certs", oidcProv.Certs)
}
