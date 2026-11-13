package main

import "github.com/go-chi/chi/v5"

func registerPublicRegistrar(r chi.Router, d routeDeps) {
	h := d.h
	r.Post("/api/v1/registrar/{registrar}/validate", h.ValidateRegistrar)
}

func registerSessionRegistrar(rg chi.Router, d routeDeps) {
	h := d.h
	// (PUT /kubeconfig is registered ABOVE the session group — see
	// the cloud-init postback comment near r.Delete /auth/session.)
	// Registrar proxy — wizard's BYO Flow B (#169). /validate is OUTSIDE
	// session group (pre-auth wizard surface). /set-ns is called from
	// CreateDeployment when domainMode == byo-api (post-auth).
	rg.Post("/api/v1/registrar/{registrar}/set-ns", h.SetNSRegistrar)
}
