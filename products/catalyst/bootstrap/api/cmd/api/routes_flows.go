package main

import "github.com/go-chi/chi/v5"

func registerSessionFlows(rg chi.Router, d routeDeps) {
	h := d.h
	// The flow canvas of a deployment: its snapshot and its stream, both
	// built from the API's own jobs store (flow_snapshot_local.go).
	// flowId == deploymentId.
	rg.Get("/api/v1/flows/{deploymentId}/snapshot", h.HandleFlowSnapshot)
	rg.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
}
