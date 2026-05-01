package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/objectstorage"

	// Side-effect import: registers the Hetzner Object Storage Provider
	// in the objectstorage registry at process init. Adding a new cloud
	// (AWS / GCP / Azure / OCI) means adding a sibling import here.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/objectstorage/hetzner"
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

// validateObjectStorageRequest carries the operator-supplied Object
// Storage credentials submitted by the wizard's StepCredentials Object
// Storage section (issue #371, vendor-agnostic since #425). The wizard
// POSTs to /api/v1/credentials/object-storage/validate before allowing
// the operator to advance, so a typo'd or insufficiently-permissioned
// credential pair surfaces at the wizard step rather than 5 minutes
// into `tofu apply`.
//
// The `Provider` field selects the cloud-specific Object Storage impl
// from the vendor-agnostic objectstorage.Provider registry. Today only
// "hetzner" is registered; AWS / GCP / Azure / OCI follow as separate
// tickets, each adding a sibling impl + side-effect import (see top
// of this file). Empty defaults to "hetzner" for back-compat with the
// existing wizard payload.
//
// Region semantics are vendor-specific (Hetzner: fsn1/nbg1/hel1; AWS:
// us-east-1/eu-west-1/...). The handler delegates region validation
// to the Provider's Endpoint lookup — if the region is unknown to the
// chosen Provider, Endpoint returns the empty string and the request
// surfaces as a 400 with the actionable error message.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the credentials are NEVER
// logged from this handler — only the validation outcome and (on
// error) the failure category are emitted to the structured log.
type validateObjectStorageRequest struct {
	Provider  string `json:"provider"`
	Region    string `json:"region"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// ValidateObjectStorageCredentials handles
// POST /api/v1/credentials/object-storage/validate. Same wire shape as
// ValidateCredentials (200 + valid:true on success; 200 + valid:false
// on rejected; 503 + valid:false on upstream unreachable; 400 +
// valid:false on missing/malformed input) so the wizard's error-card
// machinery can render the response without a per-endpoint switch.
//
// Issue #371 (Hetzner Object Storage validation) + #425 (vendor-
// agnostic abstraction): the handler resolves the Provider impl from
// the request's `provider` field via objectstorage.Resolve, then
// delegates to its Validate method. NOTHING in this handler is
// vendor-specific — adding AWS / GCP / Azure / OCI requires only a
// sibling impl package + a side-effect import at the top of this
// file.
func (h *Handler) ValidateObjectStorageCredentials(w http.ResponseWriter, r *http.Request) {
	var req validateObjectStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "invalid request body",
		})
		return
	}

	// Default to hetzner for back-compat with the existing wizard
	// payload (which omitted `provider`). When a future wizard build
	// starts emitting `provider: "aws"` etc., this default never
	// fires.
	providerName := strings.TrimSpace(req.Provider)
	if providerName == "" {
		providerName = "hetzner"
	}

	provider, err := objectstorage.Resolve(providerName)
	if err != nil {
		if errors.Is(err, objectstorage.ErrUnsupportedProvider) {
			writeJSON(w, http.StatusBadRequest, validateResponse{
				Valid:   false,
				Message: "unsupported object storage provider — only hetzner is registered today",
			})
			return
		}
		h.log.Error("object-storage provider resolve error", "provider", providerName, "err", err)
		writeJSON(w, http.StatusInternalServerError, validateResponse{
			Valid:   false,
			Message: "internal error resolving object storage provider",
		})
		return
	}

	region := strings.TrimSpace(req.Region)
	access := strings.TrimSpace(req.AccessKey)
	secret := strings.TrimSpace(req.SecretKey)

	if region == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "object storage region is required (e.g. fsn1 for Hetzner)",
		})
		return
	}
	// Region whitelist is vendor-specific — delegate to the Provider
	// via Endpoint, which returns "" for unknown regions. Hetzner
	// today: fsn1 / nbg1 / hel1 (European-only Object Storage).
	if provider.Endpoint(region) == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "region not supported by the chosen object storage provider",
		})
		return
	}
	// Hetzner S3 access keys are typically 20 chars, secret keys 40 —
	// but rotations may emit different lengths; reject only obviously-
	// wrong bounds. The Provider returns the actionable specific error
	// when the keys are well-formed but rejected at ListBuckets time.
	if len(access) < 16 {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "access key too short — object storage keys are at least 16 characters",
		})
		return
	}
	if len(secret) < 32 {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "secret key too short — object storage secrets are at least 32 characters",
		})
		return
	}

	valid, err := provider.Validate(r.Context(), region, access, secret)
	if err != nil {
		// Network / DNS / 5xx — wizard renders the "unreachable" hint
		// card. We log only the error class, NEVER the credential
		// values.
		h.log.Error("object-storage validation error", "provider", providerName, "region", region, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{
			Valid:   false,
			Message: "could not reach object storage endpoint — check provider status page or retry",
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
	// 401/403 from upstream — credentials authenticated but were
	// rejected (or the keys are wrong). The wizard's "rejected" hint
	// card surfaces the remediation: re-issue credentials in the
	// provider's console.
	writeJSON(w, http.StatusOK, validateResponse{
		Valid:   false,
		Message: "credentials rejected — issue a fresh access/secret pair in the provider's console",
	})
}
