package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
)

// Handler holds shared state for all HTTP handlers.
type Handler struct {
	log         *slog.Logger
	deployments sync.Map // map[string]*Deployment
}

// New creates a Handler.
func New(log *slog.Logger) *Handler {
	return &Handler{log: log}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
