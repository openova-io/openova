package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"
)

// Handler holds shared state for all HTTP handlers.
//
// dynadotAPIKey + dynadotAPISecret are read from environment variables that
// are mounted from the dynadot-api-credentials K8s secret in the
// openova-system namespace via ESO at deploy time. They are injected into
// pool-domain ProvisionRequests so the provisioner can write DNS records
// for *.{subdomain}.{pool-domain}.
type Handler struct {
	log              *slog.Logger
	deployments      sync.Map // map[string]*Deployment
	dynadotAPIKey    string
	dynadotAPISecret string
}

// New creates a Handler.
func New(log *slog.Logger) *Handler {
	return &Handler{
		log:              log,
		dynadotAPIKey:    os.Getenv("DYNADOT_API_KEY"),
		dynadotAPISecret: os.Getenv("DYNADOT_API_SECRET"),
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
