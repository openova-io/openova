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

// validateObjectStorageRequest carries the operator-supplied Hetzner Object
// Storage credentials submitted by the wizard's StepCredentials object-
// storage section (issue #371). The wizard POSTs to
// /api/v1/credentials/object-storage/validate before allowing the operator
// to advance, so a typo'd or insufficiently-permissioned credential pair
// surfaces at the wizard step rather than 5 minutes into `tofu apply`.
//
// All three fields come straight from the operator's Hetzner Console UI
// (Object Storage → Manage Credentials). Region is one of fsn1 / nbg1 /
// hel1 — the European-only Object Storage availability zones as of
// 2026-04. Hetzner does NOT expose a Cloud API to mint these credentials,
// so the wizard has no choice but to ask the operator directly.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the credentials are NEVER logged
// from this handler — only the validation outcome and (on error) the
// failure category are emitted to the structured log.
type validateObjectStorageRequest struct {
	Region    string `json:"region"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// ValidateObjectStorageCredentials handles
// POST /api/v1/credentials/object-storage/validate. Same wire shape as
// ValidateCredentials (200 + valid:true on success; 200 + valid:false on
// rejected; 503 + valid:false on Hetzner unreachable; 400 + valid:false
// on missing/malformed input) so the wizard's TokenSection error-card
// machinery can render the response without a per-endpoint switch.
//
// Issue #371: gates the wizard's StepCredentials Object-Storage section's
// "Validate" button. The handler delegates to
// internal/hetzner.ValidateObjectStorageCredentials which speaks the
// minio-go S3 client against `<region>.your-objectstorage.com`.
func (h *Handler) ValidateObjectStorageCredentials(w http.ResponseWriter, r *http.Request) {
	var req validateObjectStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "invalid request body",
		})
		return
	}

	region := strings.TrimSpace(req.Region)
	access := strings.TrimSpace(req.AccessKey)
	secret := strings.TrimSpace(req.SecretKey)

	if region == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "object storage region is required (fsn1, nbg1, or hel1)",
		})
		return
	}
	switch region {
	case "fsn1", "nbg1", "hel1":
		// OK
	default:
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "region must be one of fsn1 / nbg1 / hel1 (Hetzner Object Storage is European-only as of 2026-04)",
		})
		return
	}
	// Hetzner S3 access keys are typically 20 chars, secret keys 40 — but
	// rotations may emit different lengths; reject only obviously-wrong
	// bounds. The upstream validator returns the actionable specific error
	// when the keys are well-formed but rejected at ListBuckets time.
	if len(access) < 16 {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "access key too short — Hetzner Object Storage keys are at least 16 characters",
		})
		return
	}
	if len(secret) < 32 {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "secret key too short — Hetzner Object Storage secrets are at least 32 characters",
		})
		return
	}

	valid, err := hetzner.ValidateObjectStorageCredentials(r.Context(), region, access, secret)
	if err != nil {
		// Network / DNS / 5xx — wizard renders the "unreachable" hint card.
		// We log only the error class, NEVER the credential values.
		h.log.Error("object-storage validation error", "region", region, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{
			Valid:   false,
			Message: "could not reach Hetzner Object Storage — check status.hetzner.com or retry",
		})
		return
	}
	if valid {
		writeJSON(w, http.StatusOK, validateResponse{
			Valid:   true,
			Message: "S3 access confirmed",
		})
		return
	}
	// 401/403 from Hetzner — credentials authenticated but were rejected
	// (or the keys are wrong). The wizard's "rejected" hint card surfaces
	// the remediation: re-issue credentials in the Hetzner Console.
	writeJSON(w, http.StatusOK, validateResponse{
		Valid:   false,
		Message: "credentials rejected — issue a fresh access/secret pair in Hetzner Console → Object Storage → Manage Credentials",
	})
}
