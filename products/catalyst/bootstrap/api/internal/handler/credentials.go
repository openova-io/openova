package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
)

type validateRequest struct {
	Token    string `json:"token"`
	Provider string `json:"provider"`
}

type validateResponse struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
}

func (h *Handler) ValidateCredentials(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	token := strings.TrimSpace(req.Token)
	if len(token) < 64 {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "token too short — Hetzner API tokens are at least 64 characters",
		})
		return
	}

	valid, err := hetzner.ValidateToken(r.Context(), token)
	if err != nil {
		h.log.Error("hetzner validation error", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{
			Valid:   false,
			Message: "could not reach Hetzner API — check network connectivity",
		})
		return
	}

	if valid {
		writeJSON(w, http.StatusOK, validateResponse{
			Valid:   true,
			Message: "read/write access confirmed",
		})
	} else {
		writeJSON(w, http.StatusOK, validateResponse{
			Valid:   false,
			Message: "token rejected — ensure it has Read & Write permissions",
		})
	}
}
