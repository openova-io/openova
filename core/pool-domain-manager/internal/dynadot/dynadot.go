// Package dynadot — config helpers for the OpenOva-pool managed-domain
// list and the runtime feature flag for whether a given domain is "managed
// by OpenOva" (i.e. the pool's authoritative zone is one Catalyst owns).
//
// Historical context: this package used to be the SOLE caller of
// api.dynadot.com — when PDM wrote DNS records on /commit it talked to
// Dynadot's set_dns2 API directly. After openova#168 the Sovereign DNS
// flow moved to PowerDNS (see internal/pdns), which owns every Sovereign
// zone authoritatively. This package now retains ONLY the
// managed-domain-list configuration helpers; the registrar adapter under
// internal/registrar/dynadot still handles BYO NS-delegation writes for
// Flow B (#170), but pool DNS writes no longer flow through here.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the managed-domain list comes from
// runtime configuration (DYNADOT_MANAGED_DOMAINS env var). Adding a fourth
// pool domain is purely a secret update — no rebuild.
package dynadot

import (
	"errors"
	"os"
	"strings"
	"sync"
)

// managedDomainsState mirrors the catalyst-api dynadot package's runtime
// resolution: env-var first, then legacy single-domain fallback, then a
// minimal built-in default (kept ONLY so unit tests work without an env).
var managedDomainsState struct {
	once sync.Once
	set  map[string]struct{}
}

func resolveManagedDomains() map[string]struct{} {
	managedDomainsState.once.Do(func() {
		managedDomainsState.set = computeManagedDomains()
	})
	return managedDomainsState.set
}

func computeManagedDomains() map[string]struct{} {
	out := make(map[string]struct{})
	if raw := os.Getenv("DYNADOT_MANAGED_DOMAINS"); strings.TrimSpace(raw) != "" {
		for _, tok := range splitDomainsList(raw) {
			out[tok] = struct{}{}
		}
		if len(out) > 0 {
			return out
		}
	}
	if d := strings.ToLower(strings.TrimSpace(os.Getenv("DYNADOT_DOMAIN"))); d != "" {
		out[d] = struct{}{}
		return out
	}
	out["openova.io"] = struct{}{}
	out["omani.works"] = struct{}{}
	return out
}

// ResetManagedDomains clears the cache so tests can re-evaluate after
// mutating env vars.
func ResetManagedDomains() {
	managedDomainsState.once = sync.Once{}
	managedDomainsState.set = nil
}

// ManagedDomains returns a sorted, deduplicated copy of the configured
// managed-domain list. Useful for /healthz exposure and operator logs.
func ManagedDomains() []string {
	set := resolveManagedDomains()
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// IsManagedDomain reports whether the given domain is one whose DNS Catalyst
// (PowerDNS) manages on behalf of OpenOva.
func IsManagedDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	_, ok := resolveManagedDomains()[domain]
	return ok
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

// Errors surfaced by the package for callers that want to type-switch.
var (
	// ErrUnmanagedDomain — caller asked for an action against a domain not
	// in DYNADOT_MANAGED_DOMAINS. Hard fail to defend against
	// misconfiguration. Despite the package name, this is a generic "pool
	// not managed" error — pool DNS writes flow through PowerDNS now.
	ErrUnmanagedDomain = errors.New("domain is not in the managed-pool list")
)
