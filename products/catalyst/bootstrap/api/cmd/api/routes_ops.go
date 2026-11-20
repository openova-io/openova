package main

import (
	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/opsproxy"
)

// registerSessionOps forwards the console routes of the operations records
// (products/ops-records) through internal/opsproxy. The session group has
// verified the session and applied the Organization scope guard; the proxy
// sets the identity headers itself and refuses agent tokens.
func registerSessionOps(rg chi.Router, d routeDeps) {
	p := opsproxy.FromEnv(d.log)
	for _, route := range opsproxy.Routes {
		rg.Method(route.Method, route.Path, p)
	}
}
