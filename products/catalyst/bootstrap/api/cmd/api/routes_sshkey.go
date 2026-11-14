package main

import "github.com/go-chi/chi/v5"

func registerPublicSSHKey(r chi.Router, d routeDeps) {
	h := d.h
	r.Post("/api/v1/sshkey/generate", h.GenerateSSHKey)
}
