package main

import "github.com/go-chi/chi/v5"

func registerSessionFlows(rg chi.Router, d routeDeps) {
	h := d.h
	// OpenovaFlow proxy (Agent #3 integration — PR #1389/#1390
	// follow-up). Proxies the FlowPage canvas's snapshot/stream/
	// ingest path to the bp-openova-flow-server inside the
	// Sovereign's catalyst-system namespace. flowId == deploymentId
	// (the openova-flow-server treats flowId as an opaque key). The
	// SSE pass-through is unbuffered (canonical pattern: see
	// internal/handler/deployments.go StreamLogs lines 1208-1287).
	// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
	// upstream URL is sourced from env OPENOVA_FLOW_SERVER_URL
	// (Sovereign-side defaults to the in-cluster Service DNS).
	rg.Get("/api/v1/flows/{deploymentId}/snapshot", h.HandleFlowSnapshot)
	rg.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
	rg.Post("/api/v1/flows/{deploymentId}/events", h.HandleFlowEvents)
}
