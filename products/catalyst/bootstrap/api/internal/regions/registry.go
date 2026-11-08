// Package regions is the ONE place catalyst-api learns which regions this
// Sovereign has (ADR-0015 D3/D4).
//
// A region in any API request must name an entry of this registry; the
// create paths answer 422 otherwise. The ids are the canonical cluster
// region ids the bootstrap-kit stamps (e.g. "hw-me-east-215-a-rtz-prod"),
// carried verbatim — never trimmed, split or re-spelled. The label is
// display-only.
//
// Source (the bp-catalyst-platform `sovereign-fqdn` ConfigMap, mounted as
// env on the catalyst-api Deployment), first non-empty wins:
//
//  1. SOVEREIGN_REGIONS_JSON — [{cloudRegion, provider, ...}, ...]
//  2. SOVEREIGN_PRIMARY_REGION + SOVEREIGN_REPLICA_REGION
//  3. CATALYST_CONFIGURED_REGIONS — comma-separated
//
// The first region is the primary. Another ADR-0015 slice may move the
// registry to a CR; callers depend only on Registry(), so that move
// touches this file alone.
package regions

import (
	"encoding/json"
	"os"
	"strings"
)

// Region is one registry entry.
type Region struct {
	// ID — the canonical region id, verbatim. The only value a request
	// may carry and the only value written to an Application CR.
	ID string `json:"id"`
	// Label — display text (equal to ID).
	Label string `json:"label"`
	// Primary — the Sovereign's declared primary region.
	Primary bool `json:"primary"`
	// Provider — the cloud provider when the source declares it.
	Provider string `json:"provider,omitempty"`
}

// getenv is swapped by tests.
var getenv = os.Getenv

// SetEnvSource replaces the env reader (tests). Returns a restore func.
func SetEnvSource(f func(string) string) (restore func()) {
	prev := getenv
	getenv = f
	return func() { getenv = prev }
}

// Registry returns the Sovereign's regions in declaration order; the first
// is the primary. Empty when no source is configured — callers must treat
// that as "no region is valid", never invent one.
func Registry() []Region {
	ids, providers := fromJSON()
	if len(ids) == 0 {
		ids = fromPrimaryReplica()
	}
	if len(ids) == 0 {
		ids = fromCSV(getenv("CATALYST_CONFIGURED_REGIONS"))
	}
	out := make([]Region, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Region{ID: id, Label: id, Provider: providers[id]})
	}
	if len(out) > 0 {
		out[0].Primary = true
	}
	return out
}

// Lookup returns the registry entry whose ID equals id exactly.
func Lookup(id string) (Region, bool) {
	for _, r := range Registry() {
		if r.ID == id {
			return r, true
		}
	}
	return Region{}, false
}

// Primary returns the declared primary region.
func Primary() (Region, bool) {
	reg := Registry()
	if len(reg) == 0 {
		return Region{}, false
	}
	return reg[0], true
}

// IDs returns the registry ids in order.
func IDs() []string {
	reg := Registry()
	out := make([]string, 0, len(reg))
	for _, r := range reg {
		out = append(out, r.ID)
	}
	return out
}

func fromJSON() ([]string, map[string]string) {
	providers := map[string]string{}
	raw := strings.TrimSpace(getenv("SOVEREIGN_REGIONS_JSON"))
	if raw == "" {
		return nil, providers
	}
	var specs []struct {
		CloudRegion string `json:"cloudRegion"`
		Provider    string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, providers
	}
	ids := make([]string, 0, len(specs))
	for _, s := range specs {
		id := strings.TrimSpace(s.CloudRegion)
		if id == "" {
			continue
		}
		ids = append(ids, id)
		providers[id] = strings.TrimSpace(s.Provider)
	}
	return ids, providers
}

func fromPrimaryReplica() []string {
	primary := strings.TrimSpace(getenv("SOVEREIGN_PRIMARY_REGION"))
	if primary == "" {
		return nil
	}
	out := []string{primary}
	if replica := strings.TrimSpace(getenv("SOVEREIGN_REPLICA_REGION")); replica != "" && replica != primary {
		out = append(out, replica)
	}
	return out
}

func fromCSV(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
