// Package handler — subdomains.go: pre-submit availability check.
//
// Closes #124 ([I] ux: error handling — what happens if subdomain already
// taken). The wizard's StepOrg debounces keystrokes and POSTs the
// candidate subdomain + pool-domain pair here BEFORE the user clicks
// Next, so the validator catches collisions early instead of failing
// at provisioning time when Dynadot rejects the duplicate record.
//
// How "taken" is determined:
//
// 1. Pool-domain check — only OpenOva-managed pool domains are
//    candidates for this endpoint; BYO domains are the customer's
//    responsibility. We reject any pool the wizard doesn't recognise
//    (defence-in-depth — the wizard already filters its own dropdown,
//    but the handler must validate independently).
//
// 2. Reserved-name check — short list of RFC 1035 / OpenOva
//    Sovereign-control-plane subdomains we never let a tenant claim
//    (api, admin, console, gitea, harbor, www, mail). Tenants get
//    *those* names automatically as part of the Sovereign FQDN
//    structure once they pick their root subdomain.
//
// 3. DNS resolution — net.DefaultResolver.LookupHost on
//    "<subdomain>.<pool>" with a 2-second timeout. If anything
//    resolves, the name is considered taken (whether it's an A,
//    AAAA, or CNAME, the global DNS already knows about it).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") the pool
// list is shared with the package-level IsManagedDomain check in
// internal/dynadot/. The reserved-name list is centralised here.
//
// Per the auto-memory `feedback_dynadot_dns.md`: NEVER run exploratory
// set_dns2 calls. We deliberately do NOT call Dynadot's API for the
// availability check — Dynadot's API is write-only-safe. The global
// DNS resolver is the eventually-consistent source of truth for what
// names already point somewhere.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/dynadot"
)

// reservedSubdomains — names we never let a tenant claim as their
// Sovereign root subdomain. Tenants get *.omantel.omani.works style
// records automatically; the wizard prevents claiming any name that
// would collide with the canonical control-plane sub-records.
var reservedSubdomains = map[string]struct{}{
	"api":      {},
	"admin":    {},
	"console":  {},
	"gitea":    {},
	"harbor":   {},
	"keycloak": {},
	"www":      {},
	"mail":     {},
	"smtp":     {},
	"imap":     {},
	"vpn":      {},
	"openova":  {},
	"catalyst": {},
	"docs":     {},
	"status":   {},
	"app":      {},
	"system":   {},
	"openbao":  {},
	"vault":    {},
	"flux":     {},
	"k8s":      {},
}

type subdomainCheckRequest struct {
	Subdomain string `json:"subdomain"`
	// PoolDomain — the apex pool domain (e.g. "omani.works"), NOT the
	// wizard's pool id (e.g. "omani-works"). The wizard maps id → domain
	// before sending this request.
	PoolDomain string `json:"poolDomain"`
}

// SubdomainCheckResponse — wire format the wizard renders.
//
// available=true        → subdomain is free, user can submit.
// available=false       → subdomain is taken; reason explains why.
// (no error field)      → backend reached resolver / pool list cleanly.
//
// reason values:
//   "invalid-format"     subdomain is not a valid RFC 1035 label
//   "unsupported-pool"   poolDomain is not an OpenOva-managed pool
//   "reserved"           subdomain is in reservedSubdomains
//   "exists"             DNS resolver returned at least one record
//   "lookup-error"       DNS lookup itself failed (transient — user retries)
type SubdomainCheckResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Detail carries a one-line human-readable explanation for the wizard
	// to surface in the inline-error UI.
	Detail string `json:"detail,omitempty"`
	// FQDN echoes back the name that was checked, so the wizard can confirm
	// the input matches what the backend evaluated.
	FQDN string `json:"fqdn,omitempty"`
}

// CheckSubdomain handles POST /api/v1/subdomains/check.
//
// Request body: subdomainCheckRequest (subdomain + poolDomain).
// Response   : SubdomainCheckResponse, always JSON, always HTTP 200
//              for well-formed requests — clients use the body's
//              `available` field, not the HTTP status, to decide.
//              Malformed JSON → 400.
func (h *Handler) CheckSubdomain(w http.ResponseWriter, r *http.Request) {
	var req subdomainCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, SubdomainCheckResponse{
			Available: false,
			Reason:    "invalid-format",
			Detail:    "request body could not be parsed as JSON",
		})
		return
	}

	sub := strings.ToLower(strings.TrimSpace(req.Subdomain))
	pool := strings.ToLower(strings.TrimSpace(req.PoolDomain))

	if !isValidDNSLabel(sub) {
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "invalid-format",
			Detail:    "subdomain must be a-z, 0-9 and hyphens, start with a letter, max 63 characters",
		})
		return
	}

	if !dynadot.IsManagedDomain(pool) {
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "unsupported-pool",
			Detail:    "pool domain " + pool + " is not managed by OpenOva — pick a different pool or use BYO",
		})
		return
	}

	if _, taken := reservedSubdomains[sub]; taken {
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "reserved",
			Detail:    "this subdomain is reserved for the Sovereign control plane — pick a different name",
		})
		return
	}

	fqdn := sub + "." + pool

	// Two-second timeout — long enough for global DNS but short enough
	// that the wizard's debounced keystroke loop stays responsive.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(ctx, fqdn)
	if err != nil {
		// NXDOMAIN is "not taken" — the most common, success case. Any
		// other error class (timeout, server-fail) is a transient lookup
		// problem the wizard surfaces but doesn't treat as taken.
		if isNXDomain(err) {
			writeJSON(w, http.StatusOK, SubdomainCheckResponse{
				Available: true,
				FQDN:      fqdn,
			})
			return
		}
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "lookup-error",
			Detail:    "DNS lookup failed: " + err.Error(),
			FQDN:      fqdn,
		})
		return
	}

	if len(addrs) == 0 {
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: true,
			FQDN:      fqdn,
		})
		return
	}

	writeJSON(w, http.StatusOK, SubdomainCheckResponse{
		Available: false,
		Reason:    "exists",
		Detail:    "this subdomain already resolves in DNS — pick a different name",
		FQDN:      fqdn,
	})
}

// isValidDNSLabel mirrors the wizard's isValidSubdomain rule (RFC 1035).
// Kept here so the backend can validate independently of the React layer.
func isValidDNSLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			continue
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // must start with a letter
			}
			continue
		case r == '-':
			if i == 0 || i == len(s)-1 {
				return false // cannot start or end with hyphen
			}
			continue
		default:
			return false
		}
	}
	return true
}

// isNXDomain reports whether the resolver error is a "no such host"
// — go's net package surfaces this as net.DNSError with IsNotFound.
func isNXDomain(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return false
}
