package main

import "github.com/go-chi/chi/v5"

func registerSessionWhoami(rg chi.Router, d routeDeps) {
	h := d.h
	// Whoami — UI auth guard polls this; 401 → redirect to /login.
	rg.Get("/api/v1/whoami", h.HandleWhoami)
}
