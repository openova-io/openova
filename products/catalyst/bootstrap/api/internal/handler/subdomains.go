// Package handler — subdomains.go: pre-submit availability check.
//
// Closes the DNS-wildcard regression in #163 by routing every check for an
// OpenOva-managed pool domain through pool-domain-manager (PDM). PDM is the
// authoritative allocation source — it does not consult DNS at all, so the
// Dynadot wildcard parking record at the apex of omani.works (which made
// EVERY subdomain resolve to 185.53.179.128 and broke the previous
// LookupHost-based check) is now architecturally irrelevant for managed
// pools.
//
// Decision tree per request:
//
//   1. Validate the subdomain as an RFC 1035 label (cheap, local).
//   2. If poolDomain is in the runtime DYNADOT_MANAGED_DOMAINS list →
//      delegate to PDM via Client.Check. PDM owns the reserved-name list
//      and the allocation table; we just surface its response verbatim.
//   3. Otherwise the caller is asking about a BYO domain (a customer's own
//      DNS zone) — fall back to a DNS-based check via net.LookupHost. PDM
//      doesn't manage BYO zones; the customer's nameserver IS the source
//      of truth there.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4: PDM's URL is read from the
// POOL_DOMAIN_MANAGER_URL env var (default = in-cluster service FQDN). The
// reserved-name list lives ONLY in PDM after this commit — catalyst-api no
// longer maintains a copy.
//
// Per Lesson #24 in docs/INVIOLABLE-PRINCIPLES.md: this is a STRUCTURAL fix,
// not a bandaid. The previous DNS-based path is REMOVED for managed pools,
// not augmented. The only remaining net.LookupHost call lives in the BYO
// branch — and it is the right tool there because BYO zones are owned by
// the customer, not by OpenOva.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
)

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
//
// reason values (managed pools mirror PDM verbatim, BYO uses local strings):
//   "invalid-format"     subdomain is not a valid RFC 1035 label
//   "unsupported-pool"   poolDomain is not an OpenOva-managed pool (PDM)
//                        — only surfaced for the BYO path's sanity check;
//                        managed-pool requests delegate to PDM which owns
//                        this verdict.
//   "reserved"           subdomain is in PDM's reserved list (managed)
//   "reserved-state"     PDM holds a non-expired reservation (managed)
//   "active-state"       PDM has an active allocation (managed)
//   "exists"             BYO DNS resolver returned at least one record
//   "lookup-error"       BYO DNS lookup itself failed (transient)
//   "pdm-unavailable"    PDM call failed — wizard treats as transient
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

	// Managed pools — PDM is the authoritative source of truth.
	if pdm.IsManagedDomain(pool) {
		h.checkManagedPool(w, r.Context(), pool, sub)
		return
	}

	// BYO domain — fall back to the legacy DNS-based check. The customer
	// owns the zone; resolving the name is the only signal we have.
	h.checkBYO(w, r.Context(), pool, sub)
}

// checkManagedPool delegates to PDM. We surface PDM's response verbatim
// (available, reason, detail, fqdn) so the wizard can render PDM's
// authoritative messages without an extra mapping layer.
func (h *Handler) checkManagedPool(w http.ResponseWriter, ctx context.Context, pool, sub string) {
	if h.pdm == nil {
		// Defence-in-depth: if the deployment forgot POOL_DOMAIN_MANAGER_URL,
		// surface a transient error rather than silently falling back to DNS
		// (which would resurrect the wildcard-parking bug this file exists
		// to fix).
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "pdm-unavailable",
			Detail:    "pool-domain-manager client is not configured — operator must set POOL_DOMAIN_MANAGER_URL",
			FQDN:      sub + "." + pool,
		})
		return
	}

	pdmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	res, err := h.pdm.Check(pdmCtx, pool, sub)
	if err != nil {
		h.log.Error("pdm check failed", "pool", pool, "sub", sub, "err", err)
		writeJSON(w, http.StatusOK, SubdomainCheckResponse{
			Available: false,
			Reason:    "pdm-unavailable",
			Detail:    "pool-domain-manager is temporarily unreachable — try again",
			FQDN:      sub + "." + pool,
		})
		return
	}
	writeJSON(w, http.StatusOK, SubdomainCheckResponse{
		Available: res.Available,
		Reason:    res.Reason,
		Detail:    res.Detail,
		FQDN:      res.FQDN,
	})
}

// checkBYO performs the DNS-based availability check for customer-owned
// (Bring-Your-Own) domains. PDM doesn't manage BYO zones — the customer's
// nameserver is the source of truth — so net.LookupHost is the right
// primitive here.
func (h *Handler) checkBYO(w http.ResponseWriter, ctx context.Context, pool, sub string) {
	fqdn := sub + "." + pool

	// Two-second timeout — long enough for global DNS but short enough
	// that the wizard's debounced keystroke loop stays responsive.
	dnsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(dnsCtx, fqdn)
	if err != nil {
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

// pdmClient is implemented by *pdm.Client. The interface lets us pass a
// fake in tests without wiring a real HTTP server.
type pdmClient interface {
	Check(ctx context.Context, poolDomain, subdomain string) (*pdm.CheckResult, error)
	Reserve(ctx context.Context, poolDomain, subdomain, createdBy string) (*pdm.Reservation, error)
	Commit(ctx context.Context, poolDomain string, in pdm.CommitInput) error
	Release(ctx context.Context, poolDomain, subdomain string) error
}
