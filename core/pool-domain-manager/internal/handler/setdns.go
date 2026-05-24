// SetDNS handler — writes raw zone-level records (A/AAAA/CNAME/MX/TXT)
// via the registrar's record-level API. Companion to SetNS but
// operates on a different surface: SetNS manipulates the NS
// delegation; SetDNS manipulates the records at the registrar's own
// nameservers.
//
// Adapters that don't implement registrar.DNSRegistrar return 501.

package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// SetDNSRequest is the JSON body for POST /api/v1/registrar/{r}/set-dns.
//
// IMPORTANT: same Token-redaction discipline as SetNSRequest — the
// handler intentionally avoids logging this struct directly.
type SetDNSRequest struct {
	Domain  string                `json:"domain"`
	Token   string                `json:"token"`
	Records []registrar.DNSRecord `json:"records"`
}

// SetDNSResponse is the JSON reply on success.
type SetDNSResponse struct {
	Success     bool                  `json:"success"`
	Registrar   string                `json:"registrar"`
	Domain      string                `json:"domain"`
	Records     []registrar.DNSRecord `json:"records"`
	Propagation string                `json:"propagation"`
}

// SetDNS handles POST /api/v1/registrar/{registrar}/set-dns.
func (h *Handler) SetDNS(w http.ResponseWriter, r *http.Request) {
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

	// Adapter MUST implement DNSRegistrar — otherwise the record-level
	// API isn't available for this registrar. Caller should fall back
	// to delegating DNS to a record-capable provider (PowerDNS,
	// Route53, etc.) via /set-ns.
	dnsAdapter, ok := adapter.(registrar.DNSRegistrar)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":     "set-dns-not-supported",
			"detail":    "registrar " + registrarName + " has no record-level API; delegate DNS via /set-ns instead",
			"registrar": registrarName,
		})
		return
	}

	var req SetDNSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "request body must be JSON {domain, token, records}",
		})
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	token := req.Token
	records := req.Records

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
	if invalid := validateRecords(records); invalid != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-records",
			"detail": invalid,
		})
		return
	}

	ctx := r.Context()

	// 1) Validate creds — fail fast on bad token.
	if err := adapter.ValidateToken(ctx, token, domain); err != nil {
		h.Log.Info("registrar set-dns: validate failed",
			"registrar", registrarName,
			"domain", domain,
			"outcome", classifyOutcome(err),
		)
		writeRegistrarErr(w, err, registrarName, domain)
		return
	}

	// 2) Write the record set.
	if err := dnsAdapter.SetDNSRecords(ctx, token, domain, records); err != nil {
		h.Log.Info("registrar set-dns: write failed",
			"registrar", registrarName,
			"domain", domain,
			"record_count", len(records),
			"outcome", classifyOutcome(err),
		)
		writeRegistrarErr(w, err, registrarName, domain)
		return
	}

	// 3) Read back to confirm. Optional — providers vary in their
	//    propagation lag for read-after-write. Empty result is fine
	//    when the provider doesn't honour read-after-write.
	confirmed, _ := dnsAdapter.GetDNSRecords(ctx, token, domain)

	h.Log.Info("registrar set-dns: ok",
		"registrar", registrarName,
		"domain", domain,
		"record_count_requested", len(records),
		"record_count_confirmed", len(confirmed),
	)
	writeJSON(w, http.StatusOK, SetDNSResponse{
		Success:     true,
		Registrar:   registrarName,
		Domain:      domain,
		Records:     confirmed,
		Propagation: propagationHint(registrarName),
	})
}

// validateRecords runs the structural checks that don't need a
// network round-trip. Returns an empty string when all records are
// well-formed, or a human-readable reason when one isn't.
func validateRecords(records []registrar.DNSRecord) string {
	if len(records) == 0 {
		return "records must be a non-empty array"
	}
	supported := map[string]bool{
		"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true,
	}
	for i, rec := range records {
		t := strings.ToUpper(strings.TrimSpace(rec.Type))
		if !supported[t] {
			return "record[" + itoa(i) + "].type must be one of A/AAAA/CNAME/MX/TXT (got " + rec.Type + ")"
		}
		if strings.TrimSpace(rec.Value) == "" {
			return "record[" + itoa(i) + "].value is required"
		}
		if t == "MX" && rec.Priority < 0 {
			return "record[" + itoa(i) + "].priority must be >= 0 for MX records"
		}
		if rec.TTL < 0 {
			return "record[" + itoa(i) + "].ttl must be >= 0"
		}
	}
	return ""
}

// itoa avoids importing strconv just for the validator's error
// strings — the validator runs once per request so the trade-off is
// fine for keeping the import set minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
