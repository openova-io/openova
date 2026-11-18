package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"

	// Side-effect import: registers every cloud-provider adapter into the
	// providers registry at init(); tests run without the cmd/api main.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
)

// validateRequest is the body of POST /api/v1/credentials/validate.
type validateRequest struct {
	Provider     string            `json:"provider"`
	Installation string            `json:"installation"`
	Credentials  map[string]string `json:"credentials"`
}

type validateResponse struct {
	Valid   bool   `json:"valid"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// ValidateCredentials handles POST /api/v1/credentials/validate. The body is
// {"provider","installation","credentials":{<key>:<value>}}; a legacy body
// carries a credential in a field that the adapter names
// (providers.LegacyAliaser). Each value is checked against the credential
// fields of the effective cloud profile, and so is every required field
// unless a legacy field supplied a value; then the adapter's ValidateCreds
// calls the cloud. Answers: 400 with the field, 404 for an unknown provider,
// 200 with valid false for rejected credentials, 503 when the cloud cannot be
// reached. No credential value is logged.
func (h *Handler) ValidateCredentials(w http.ResponseWriter, r *http.Request) {
	var req validateRequest
	var fields map[string]json.RawMessage
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &fields) != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Provider))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{Field: "provider", Message: "required"})
		return
	}
	cp, err := providers.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, validateResponse{Message: "unknown cloud provider: " + name})
		return
	}
	profile, err := provisioner.EffectiveProfile(name, req.Installation)
	if err != nil {
		field := "provider"
		if strings.TrimSpace(req.Installation) != "" {
			field = "installation"
		}
		writeJSON(w, http.StatusBadRequest, validateResponse{Field: field, Message: err.Error()})
		return
	}
	creds, legacy := credentialsOf(cp, req.Credentials, fields)
	if err := providers.CheckCredentialValues(creds, profile, !legacy); err != nil {
		var fe *providers.FieldError
		if errors.As(err, &fe) {
			writeJSON(w, http.StatusBadRequest, validateResponse{Field: fe.Field, Message: fe.Message})
			return
		}
		h.log.Error("cloud profile credential fields", "provider", name, "err", err)
		writeJSON(w, http.StatusInternalServerError, validateResponse{Message: "the cloud profile cannot be read"})
		return
	}
	if verr := cp.ValidateCreds(r.Context(), creds); verr != nil {
		// The adapters word a refused credential "rejected" and wrap a
		// transport error otherwise; the wizard shows a card for each.
		if strings.Contains(verr.Error(), "rejected") {
			writeJSON(w, http.StatusOK, validateResponse{Message: "credentials rejected — ensure they have Read & Write permissions"})
			return
		}
		h.log.Error("provider creds validation error", "provider", name, "err", verr)
		writeJSON(w, http.StatusServiceUnavailable, validateResponse{Message: "could not reach the " + profile.Spec.DisplayName + " API — check network connectivity"})
		return
	}
	writeJSON(w, http.StatusOK, validateResponse{Valid: true, Message: "read/write access confirmed"})
}

// credentialsOf returns the credentials of a validation body: its map, and
// each legacy field the adapter names that the map lacks. legacy reports
// whether a legacy field supplied a value.
func credentialsOf(cp providers.CloudProvider, m map[string]string, fields map[string]json.RawMessage) (creds providers.ProviderCreds, legacy bool) {
	raw := make(map[string]string, len(m))
	for k, v := range m {
		raw[k] = strings.TrimSpace(v)
	}
	if a, ok := cp.(providers.LegacyAliaser); ok {
		for field, key := range a.LegacyCredentialAliases() {
			var v string
			if json.Unmarshal(fields[field], &v) != nil || strings.TrimSpace(v) == "" || raw[key] != "" {
				continue
			}
			raw[key], legacy = strings.TrimSpace(v), true
		}
	}
	return providers.ProviderCreds{Raw: raw}, legacy
}

// validateObjectStorageRequest carries the Object Storage keys of the
// wizard's StepCredentials Object Storage section (issue #371).
type validateObjectStorageRequest struct {
	Provider  string `json:"provider"`
	Region    string `json:"region"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// objectStorageValidator returns the adapter of provider when it validates
// Object Storage keys (providers.ObjectStorageValidator). An empty provider
// means the one adapter that has the capability (the wizard's body names
// none); it is refused when not exactly one has it.
func objectStorageValidator(provider string) (providers.ObjectStorageValidator, bool) {
	if name := strings.TrimSpace(provider); name != "" {
		v, ok, err := providers.As[providers.ObjectStorageValidator](name)
		return v, ok && err == nil
	}
	var found []providers.ObjectStorageValidator
	for _, name := range providers.Names() {
		if v, ok, err := providers.As[providers.ObjectStorageValidator](name); ok && err == nil {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		return nil, false
	}
	return found[0], true
}

// ValidateObjectStorageCredentials handles
// POST /api/v1/credentials/object-storage/validate with the answers of
// ValidateCredentials: 200 valid true; 200 valid false when the keys are
// rejected; 503 when the endpoint cannot be reached; 400 for missing or
// malformed input. The keys are never logged.
func (h *Handler) ValidateObjectStorageCredentials(w http.ResponseWriter, r *http.Request) {
	var req validateObjectStorageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "invalid request body",
		})
		return
	}

	provider, ok := objectStorageValidator(req.Provider)
	if !ok {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Field:   "provider",
			Message: "unsupported object storage provider: " + strings.TrimSpace(req.Provider),
		})
		return
	}

	region := strings.TrimSpace(req.Region)
	access := strings.TrimSpace(req.AccessKey)
	secret := strings.TrimSpace(req.SecretKey)

	if region == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "object storage region is required",
		})
		return
	}
	// The adapter's endpoint lookup is its region list: "" is an unknown
	// region.
	if provider.ObjectStorageEndpoint(region) == "" {
		writeJSON(w, http.StatusBadRequest, validateResponse{
			Valid:   false,
			Message: "region not supported by the chosen object storage provider",
		})
		return
	}
	// Refuse only obviously wrong lengths; the adapter answers a well-formed
	// pair that the endpoint rejects.
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

	valid, err := provider.ValidateObjectStorage(r.Context(), region, access, secret)
	if err != nil {
		h.log.Error("object-storage validation error", "provider", req.Provider, "region", region, "err", err)
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
	writeJSON(w, http.StatusOK, validateResponse{
		Valid:   false,
		Message: "credentials rejected — issue a fresh access/secret pair in the provider's console",
	})
}
