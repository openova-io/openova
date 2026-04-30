package dynadot

import (
	"strings"
	"sync"
)

// ManagedDomains is a thread-safe allowlist of pool domains the calling
// process is permitted to mutate via the Dynadot API. The cert-manager
// webhook consults it to refuse DNS-01 challenges for domains the
// operator hasn't enrolled — same defense as the catalyst-dns binary,
// just exposed at the package boundary.
//
// Population is up to the caller — typically the webhook reads a
// comma- or whitespace-separated `DYNADOT_MANAGED_DOMAINS` env var
// (mounted from the dynadot-api-credentials K8s secret's `domains`
// key) at startup. Per docs/INVIOLABLE-PRINCIPLES.md #4 the list is
// runtime-configurable; adding a fourth pool domain is a secret
// update, not a rebuild.
type ManagedDomains struct {
	mu  sync.RWMutex
	set map[string]struct{}
}

// NewManagedDomains parses a comma- or whitespace-separated list and
// returns a populated allowlist. Empty strings are dropped, entries
// are lower-cased, duplicates collapsed.
func NewManagedDomains(raw string) *ManagedDomains {
	m := &ManagedDomains{set: make(map[string]struct{})}
	for _, tok := range splitDomainsList(raw) {
		m.set[tok] = struct{}{}
	}
	return m
}

// Has reports whether the given domain is in the allowlist (case-
// insensitive, whitespace-trimmed).
func (m *ManagedDomains) Has(domain string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.set == nil {
		return false
	}
	_, ok := m.set[strings.ToLower(strings.TrimSpace(domain))]
	return ok
}

// List returns a sorted, deduplicated copy of the configured domains.
// Useful for /healthz exposure and operator logs.
func (m *ManagedDomains) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.set))
	for d := range m.set {
		out = append(out, d)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// splitDomainsList parses a `DYNADOT_MANAGED_DOMAINS`-style string —
// comma- or whitespace-separated, lower-cased, trimmed, deduped.
func splitDomainsList(raw string) []string {
	raw = strings.ToLower(raw)
	raw = strings.ReplaceAll(raw, ",", " ")
	parts := strings.Fields(raw)
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
