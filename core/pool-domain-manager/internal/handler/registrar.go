// registrar.go — HTTP surface for the per-registrar NS-flip endpoint.
//
// New endpoint:
//
//	POST /api/v1/registrar/{registrar}/set-ns
//	Body:  {"domain": "...", "token": "...", "nameservers": ["...", "..."]}
//	Reply: {"success": true, "registrar": "...", "domain": "...",
//	        "nameservers": ["..."], "propagation": "..."}
//
// Token handling — per the issue body and docs/INVIOLABLE-PRINCIPLES.md
// #10 (credential hygiene):
//
//   - The token never enters a struct that gets logged.
//   - h.Log calls in this file ONLY ever pass {registrar, domain, outcome}.
//     Never `req.Token`. Never the request body. The redaction-by-omission
//     pattern is enforced by the request struct's logging-unfriendly shape:
//     `Token string \`json:"token"\`` — but we never call Log with the
//     whole struct, only the safe fields.
//
//   - The token's lifetime ends when this handler returns: the per-request
//     local variable goes out of scope and the GC reclaims it; nothing
//     persists it in PDM (no DB write, no in-memory cache).
//
//   - On successful set-ns we DO read back the nameservers via the
//     adapter's GetNameservers and include them in the response. This
//     does not "expose" the token because the token only appears in the
//     adapter's outbound request, not in our reply.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	registrar "github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// SetRegistry installs a registrar.Registry on the handler. Call from
// main after building the registry.
func (h *Handler) SetRegistry(r registrar.Registry) {
	h.Registry = r
}

// ValidateRequest is the JSON body for POST /api/v1/registrar/{r}/validate.
//
// The validate endpoint is the read-only twin of /set-ns: it asks the
// adapter to confirm credentials work and the domain is in the account,
// without flipping any nameservers. The wizard's BYO Flow B (#169) uses
// this before letting the customer continue, so a typo in the token
// surfaces at the prompt instead of mid-provisioning.
//
// Same hygiene rules as SetNSRequest — the Token field never gets logged
// (we never call h.Log with the whole struct).
type ValidateRequest struct {
	Domain string `json:"domain"`
	Token  string `json:"token"`
}

// ValidateResponse is the JSON reply on success.
type ValidateResponse struct {
	Valid     bool   `json:"valid"`
	Registrar string `json:"registrar"`
	Domain    string `json:"domain"`
}

// Validate handles POST /api/v1/registrar/{registrar}/validate.
//
// Closes #169 ([I] wizard: StepDomain — BYO with two delegation flows).
// The wizard calls this BEFORE letting the customer hit Continue so a
// bad token surfaces at the prompt, not during provisioning. The endpoint
// performs adapter.ValidateToken only — no writes, no NS flip, nothing
// the customer hasn't yet consented to.
func (h *Handler) Validate(w http.ResponseWriter, r *http.Request) {
	registrarName := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "registrar")))

	if h.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "no-registrars-configured",
			"detail": "this PDM build has no registrar adapters wired in",
		})
		return
	}

	adapter, err := h.Registry.Lookup(registrarName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":     "unsupported-registrar",
			"detail":    "no adapter for registrar " + registrarName,
			"supported": h.Registry.Names(),
		})
		return
	}

	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "request body must be JSON {domain, token}",
		})
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	token := req.Token

	if domain == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-domain",
			"detail": "domain is required",
		})
		return
	}
	if token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-token",
			"detail": "registrar API credentials are required",
		})
		return
	}

	if err := adapter.ValidateToken(r.Context(), token, domain); err != nil {
		h.Log.Info("registrar validate: failed",
			"registrar", registrarName,
			"domain", domain,
			"outcome", classifyOutcome(err),
		)
		writeRegistrarErr(w, err, registrarName, domain)
		return
	}

	h.Log.Info("registrar validate: ok",
		"registrar", registrarName,
		"domain", domain,
		"outcome", "ok",
	)
	writeJSON(w, http.StatusOK, ValidateResponse{
		Valid:     true,
		Registrar: registrarName,
		Domain:    domain,
	})
}

// SetNSRequest is the JSON body for POST /api/v1/registrar/{r}/set-ns.
//
// IMPORTANT: do NOT add struct tags that mark Token as loggable. The
// handler intentionally avoids logging this struct directly.
type SetNSRequest struct {
	Domain      string   `json:"domain"`
	Token       string   `json:"token"`
	Nameservers []string `json:"nameservers"`
}

// SetNSResponse is the JSON reply on success.
type SetNSResponse struct {
	Success     bool     `json:"success"`
	Registrar   string   `json:"registrar"`
	Domain      string   `json:"domain"`
	Nameservers []string `json:"nameservers"`
	// Propagation is a coarse, human-readable estimate the wizard can
	// surface ("up to 24 hours; typically 1-4 hours"). It's a constant
	// per registrar — no real propagation observation happens here.
	Propagation string `json:"propagation"`
}

// SetNS handles POST /api/v1/registrar/{registrar}/set-ns.
func (h *Handler) SetNS(w http.ResponseWriter, r *http.Request) {
	registrarName := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "registrar")))

	if h.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "no-registrars-configured",
			"detail": "this PDM build has no registrar adapters wired in",
		})
		return
	}

	adapter, err := h.Registry.Lookup(registrarName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":     "unsupported-registrar",
			"detail":    "no adapter for registrar " + registrarName,
			"supported": h.Registry.Names(),
		})
		return
	}

	var req SetNSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "request body must be JSON {domain, token, nameservers}",
		})
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	token := req.Token // intentionally not normalised; some providers care
	ns := normaliseNS(req.Nameservers)

	if domain == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-domain",
			"detail": "domain is required",
		})
		return
	}
	if token == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-token",
			"detail": "registrar API credentials are required",
		})
		return
	}
	if len(ns) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-nameservers",
			"detail": "nameservers must be a non-empty array",
		})
		return
	}

	ctx := r.Context()

	// 1) Validate the credentials before the write — fail fast on bad
	//    creds so the wizard can re-prompt.
	if err := adapter.ValidateToken(ctx, token, domain); err != nil {
		h.Log.Info("registrar set-ns: validate failed",
			"registrar", registrarName,
			"domain", domain,
			"outcome", classifyOutcome(err),
		)
		writeRegistrarErr(w, err, registrarName, domain)
		return
	}

	// 2) Flip the nameservers.
	if err := adapter.SetNameservers(ctx, token, domain, ns); err != nil {
		h.Log.Info("registrar set-ns: write failed",
			"registrar", registrarName,
			"domain", domain,
			"outcome", classifyOutcome(err),
		)
		writeRegistrarErr(w, err, registrarName, domain)
		return
	}

	// 3) Read back the nameservers as a confirmation. Failure here is
	//    NOT fatal — the write succeeded, the read might race against
	//    propagation. We log and return whatever we have.
	confirmed, readErr := adapter.GetNameservers(ctx, token, domain)
	if readErr != nil {
		h.Log.Info("registrar set-ns: readback failed (write ok)",
			"registrar", registrarName,
			"domain", domain,
			"outcome", classifyOutcome(readErr),
		)
		confirmed = ns
	}

	h.Log.Info("registrar set-ns: success",
		"registrar", registrarName,
		"domain", domain,
		"outcome", "ok",
	)

	writeJSON(w, http.StatusOK, SetNSResponse{
		Success:     true,
		Registrar:   registrarName,
		Domain:      domain,
		Nameservers: confirmed,
		Propagation: propagationHint(registrarName),
	})
}

// classifyOutcome turns an error into a label safe to log alongside
// {registrar, domain}. NEVER returns the raw error message because some
// providers echo the token in error text.
func classifyOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, registrar.ErrInvalidToken):
		return "invalid-token"
	case errors.Is(err, registrar.ErrRateLimited):
		return "rate-limited"
	case errors.Is(err, registrar.ErrDomainNotInAccount):
		return "domain-not-in-account"
	case errors.Is(err, registrar.ErrAPIUnavailable):
		return "api-unavailable"
	case errors.Is(err, registrar.ErrUnsupportedRegistrar):
		return "unsupported-registrar"
	}
	return "unknown-error"
}

// writeRegistrarErr maps a typed registrar error to an HTTP status. The
// JSON body deliberately omits the underlying error message when the
// kind is `unknown-error` because we cannot vouch for token-redaction
// inside provider-specific error strings.
func writeRegistrarErr(w http.ResponseWriter, err error, registrarName, domain string) {
	switch {
	case errors.Is(err, registrar.ErrInvalidToken):
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":     "invalid-token",
			"detail":    "credentials rejected by " + registrarName,
			"registrar": registrarName,
			"domain":    domain,
		})
	case errors.Is(err, registrar.ErrRateLimited):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error":     "rate-limited",
			"detail":    registrarName + " rate-limited the request",
			"registrar": registrarName,
			"domain":    domain,
		})
	case errors.Is(err, registrar.ErrDomainNotInAccount):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":     "domain-not-in-account",
			"detail":    domain + " is not visible to the supplied " + registrarName + " credentials",
			"registrar": registrarName,
			"domain":    domain,
		})
	case errors.Is(err, registrar.ErrAPIUnavailable):
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":     "api-unavailable",
			"detail":    registrarName + " API is unreachable",
			"registrar": registrarName,
			"domain":    domain,
		})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":     "registrar-error",
			"detail":    "registrar API call failed (see PDM logs by registrar+domain)",
			"registrar": registrarName,
			"domain":    domain,
		})
	}
}

// normaliseNS lowercases + trims each entry; drops empty strings and
// duplicates. Returns nil when the input is nil/empty.
func normaliseNS(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// propagationHint returns a coarse, registrar-specific estimate the
// wizard surfaces to the customer. Values are conservative; real
// propagation depends on TTLs and resolver caching.
func propagationHint(registrarName string) string {
	switch registrarName {
	case "cloudflare":
		return "cloudflare typically reflects NS changes within minutes; full TLD propagation up to 24 hours"
	case "godaddy":
		return "godaddy typically reflects NS changes within 1-4 hours; full TLD propagation up to 48 hours"
	case "namecheap":
		return "namecheap typically reflects NS changes within 1-2 hours; full TLD propagation up to 48 hours"
	case "ovh":
		return "ovh applies NS changes asynchronously (task queue); reflected within 1-4 hours"
	case "dynadot":
		return "dynadot reflects NS changes within 30-60 minutes"
	}
	return "registrar-specific; full TLD propagation up to 48 hours"
}
