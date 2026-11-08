// Package regions is the ONE region-id registry of a Sovereign (ADR-0015 D3).
//
// Region identity is the cluster region id — the value cloud-init stamps as
// the `openova.io/region` node label and threads into bootstrap-kit as
// SOVEREIGN_REGION_CANONICAL_LABEL, e.g. `hw-me-east-215-a-rtz-prod`. The
// Sovereign's region list is the `sovereign-fqdn` ConfigMap key `regionsJson`
// (SOVEREIGN_REGIONS_JSON, `[{"cloudRegion":"<id>",...},...]`), in
// provisioning order: index 0 is the first region, index 1 the second, …
//
// Every component that needs a region id reads it from a Registry built here.
// Legacy spellings that still arrive on the wire (a canonical cluster id's
// region letter `rtz-B`, the bare region `me-east-215-b`) are resolved by
// Canonical — and ONLY by Canonical. Nothing else in the controllers or the
// API may fold region spellings; scripts/check-region-vocabulary.sh enforces
// that for the paths ADR-0015 D1/D3 touched.
package regions

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// EnvRegionsJSON is the environment variable carrying the Sovereign's region
// list (the `sovereign-fqdn` ConfigMap key `regionsJson`).
const EnvRegionsJSON = "SOVEREIGN_REGIONS_JSON"

// EnvRegionID is the environment variable carrying the id of the region the
// process runs in (bootstrap-kit SOVEREIGN_REGION_CANONICAL_LABEL).
const EnvRegionID = "SOVEREIGN_REGION_ID"

// Registry is the ordered region list of one Sovereign.
type Registry struct {
	ids []string
}

// New builds a Registry from canonical ids, in order. Empty and duplicate
// entries are dropped.
func New(ids ...string) Registry {
	out := Registry{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out.ids = append(out.ids, id)
	}
	return out
}

// Parse reads the region list from its JSON wire form. It accepts the
// provisioning shape (`[{"cloudRegion":"<id>",...}]`) and a plain string
// array (`["<id>",...]`). An empty string yields an empty Registry.
func Parse(raw string) (Registry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return Registry{}, nil
	}
	// The sovereign-fqdn ConfigMap stores `toJson` of the chart value, which
	// is a JSON STRING when the value arrived as a string: unwrap once.
	if strings.HasPrefix(raw, `"`) {
		var inner string
		if err := json.Unmarshal([]byte(raw), &inner); err != nil {
			return Registry{}, fmt.Errorf("regions: %s: %w", EnvRegionsJSON, err)
		}
		return Parse(inner)
	}
	var objs []struct {
		CloudRegion string `json:"cloudRegion"`
	}
	if err := json.Unmarshal([]byte(raw), &objs); err == nil {
		ids := make([]string, 0, len(objs))
		for _, o := range objs {
			ids = append(ids, o.CloudRegion)
		}
		r := New(ids...)
		if len(r.ids) > 0 || len(objs) == 0 {
			return r, nil
		}
	}
	var strs []string
	if err := json.Unmarshal([]byte(raw), &strs); err != nil {
		return Registry{}, fmt.Errorf("regions: %s is neither [{cloudRegion}] nor [string]: %w", EnvRegionsJSON, err)
	}
	return New(strs...), nil
}

// FromEnv builds the Registry from SOVEREIGN_REGIONS_JSON. When the list is
// absent but SOVEREIGN_REGION_ID is set, the Registry holds that single
// region (a single-region Sovereign whose list was never threaded).
func FromEnv() (Registry, error) {
	r, err := Parse(os.Getenv(EnvRegionsJSON))
	if err != nil {
		return Registry{}, err
	}
	if r.Len() == 0 {
		r = New(os.Getenv(EnvRegionID))
	}
	return r, nil
}

// IDs returns the canonical ids in provisioning order (a copy).
func (r Registry) IDs() []string {
	out := make([]string, len(r.ids))
	copy(out, r.ids)
	return out
}

// Len is the number of regions.
func (r Registry) Len() int { return len(r.ids) }

// Contains reports whether id is one of the canonical ids.
func (r Registry) Contains(id string) bool {
	for _, x := range r.ids {
		if x == id {
			return true
		}
	}
	return false
}

// At returns the id at provisioning index i.
func (r Registry) At(i int) (string, bool) {
	if i < 0 || i >= len(r.ids) {
		return "", false
	}
	return r.ids[i], true
}

// Canonical resolves a region token to the canonical region id. Accepted:
//
//   - the canonical id itself (`hw-me-east-215-b-rtz-prod`);
//   - a canonical cluster id `<tier>-<LETTER>` (`rtz-B`, `mgmt-a`): the
//     letter is the provisioning index (A = first region, B = second, …);
//   - the bare region embedded in exactly one canonical id
//     (`me-east-215-b` ⊂ `hw-me-east-215-b-rtz-prod`).
//
// Anything else — `primary`, `A`, a placeholder such as
// `platform-bootstrap-owned-host`, an id of another Sovereign — is an error.
// A caller never guesses a region.
func (r Registry) Canonical(token string) (string, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return "", fmt.Errorf("regions: empty region")
	}
	if r.Contains(t) {
		return t, nil
	}
	if i := strings.LastIndex(t, "-"); i > 0 && i == len(t)-2 {
		switch t[:i] {
		case "mgmt", "dmz", "rtz":
			letter := strings.ToUpper(t[i+1:])[0]
			if letter >= 'A' && letter <= 'Z' {
				if id, ok := r.At(int(letter - 'A')); ok {
					return id, nil
				}
				return "", fmt.Errorf("regions: cluster id %q names region %c but the Sovereign has %d region(s) %v", t, letter, len(r.ids), r.ids)
			}
		}
	}
	var match []string
	for _, id := range r.ids {
		if bareRegionOf(id) == t {
			match = append(match, id)
		}
	}
	if len(match) == 1 {
		return match[0], nil
	}
	return "", fmt.Errorf("regions: %q is not a region of this Sovereign %v", t, r.ids)
}

// bareRegionOf returns the region segment of a `{prov}-{region}-{bb}-{env}`
// cluster region id (`hw-me-east-215-a-rtz-prod` → `me-east-215-a`), or ""
// when id does not have at least four dash-separated segments. Used only to
// read a legacy bare-region token back to the canonical id.
func bareRegionOf(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[1:len(parts)-2], "-")
}

// Peers returns the members of placement other than self, in placement order.
func Peers(self string, placement []string) []string {
	out := make([]string, 0, len(placement))
	for _, p := range placement {
		if p != self {
			out = append(out, p)
		}
	}
	return out
}
