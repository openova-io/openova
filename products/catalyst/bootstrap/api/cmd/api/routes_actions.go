package main

import "github.com/go-chi/chi/v5"

func registerSessionActions(rg chi.Router, d routeDeps) {
	h := d.h
	rg.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
}
