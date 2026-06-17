// Package handler — applications_wire_compat.go: wire-shape compatibility
// for the /applications endpoints (qa-loop iter-7 Cluster-C Fix #36, #1227).
//
// Background:
//
// The canonical Application CR shape, mirrored 1-1 by the slice-I install
// API, is verbose for human/UI callers — a typical install POST is:
//
//   {
//     "blueprintRef":   { "name": "bp-wordpress", "version": "1.2.3" },
//     "name":           "wp-prod",
//     "organizationRef":"acme",
//     "environmentRef": "acme-prod",
//     "parameters":     { ... },
//     "placement":      { "mode": "single-region", "regions": ["fsn1"] }
//   }
//
// The Sovereign Console UI + qa-loop test matrix both want the
// minimum-shape:
//
//   {
//     "blueprint": "bp-wordpress",
//     "version":   "latest",
//     "namespace": "qa-omantel",
//     "name":      "qa-wp",
//     "values":    { ... }
//   }
//
// where placement / regions / environmentRef are inferred from
// Sovereign defaults (single-region in the Sovereign's primary region,
// environmentRef = organizationRef when not separately scoped).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state, not MVP) this file
// implements BOTH wire shapes via custom UnmarshalJSON. Neither shape is
// a "for now" — both are first-class. The matrix shape IS the natural UI
// shape; the canonical shape preserves 1-1 mapping for power callers
// that want to dictate placement explicitly.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the inferred
// defaults are sourced from the Sovereign deployment record where
// possible (primaryRegion etc.); literal fallbacks ("single-region",
// `[]string{"fsn1"}`) live ONLY when the Sovereign carries no hint.
//
// ──────────────────────────────────────────────────────────────────────
// Same compat shim applies to:
//   - applicationInstallRequest          POST .../applications
//   - applicationUpdateRequest           PUT  .../applications/{name}
//   - applicationChangePreviewRequest    POST .../applications/{name}/topology/preview
//   - applicationUpgradePreviewRequest   POST .../applications/{name}/upgrade/preview
//
// All four accept BOTH (a) the canonical struct shape and (b) the
// simplified shape via UnmarshalJSON. Order of unmarshal attempts:
//   1. Try the canonical shape (DisallowUnknownFields-friendly).
//   2. On any decode error OR on detecting simplified-shape sentinel
//      keys (`blueprint`, `values`, `toVersion`, string-form
//      `placement`), fall back to the simplified parser.
package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// osGetenv is a tiny indirection so tests can stub the env lookup.
// The function-variable shape lets one-off tests replace osGetenv with
// a fixture without touching the real environment.
var osGetenv = os.Getenv

// ── shared simplified-shape decoders ──────────────────────────────────

// applicationDefaultPrimaryRegion is the literal fallback used when
// the simplified-shape caller doesn't supply regions AND we have no
// Sovereign-side hint (e.g. test matrix on a fresh-provisioned cluster
// before the catalyst-cataloged region list is populated). Per
// docs/INVIOLABLE-PRINCIPLES.md #4 this MUST be the last resort.
//
// MUST be a 4-segment canonical region label per the Application +
// Environment + Continuum CRD validation `^[a-z]+-[a-z]+-[a-z]+-[a-z]+$`.
// Legacy "fsn1" rejected the Application at admission and blocked
// chart 1.4.105 (Fix #40 follow-up). Operator override available via
// `CATALYST_APPLICATION_DEFAULT_PRIMARY_REGION` env on the catalyst-api
// Deployment so a non-Hetzner Sovereign can substitute its own canonical
// label without a code change.
const applicationDefaultPrimaryRegion = "hz-fsn-rtz-prod"

// applicationDefaultPrimaryRegionFromEnv resolves the literal fallback
// at request time, honouring the operator override. Falls back to the
// constant when the env is unset or the value is malformed.
func applicationDefaultPrimaryRegionFromEnv() string {
	if v := strings.TrimSpace(osGetenv("CATALYST_APPLICATION_DEFAULT_PRIMARY_REGION")); v != "" {
		return v
	}
	return applicationDefaultPrimaryRegion
}

// applicationDefaultPlacementMode is the literal fallback placement.
// The canonical "singleton" (#3375 DoD-1) is the safest default — one
// cluster, no failover; the caller can always promote post-install via
// PUT .../applications/{name} with ?force=true.
const applicationDefaultPlacementMode = "singleton"

// applicationSimplifiedInstall mirrors the simplified-shape install
// body. ALL fields are optional from a JSON-decode standpoint; the
// wire-promote logic enforces the same validation rules as the
// canonical shape.
type applicationSimplifiedInstall struct {
	// Blueprint can be either a bare string ("bp-wordpress") OR an
	// object ({"name":"bp-wordpress","version":"1.2.3"}) for the
	// canonical-shape compatibility.
	BlueprintBare string `json:"-"`
	Blueprint     string `json:"blueprint,omitempty"`

	Version         string                 `json:"version,omitempty"`
	Name            string                 `json:"name,omitempty"`
	Namespace       string                 `json:"namespace,omitempty"`
	OrganizationRef string                 `json:"organizationRef,omitempty"`
	EnvironmentRef  string                 `json:"environmentRef,omitempty"`
	Values          map[string]interface{} `json:"values,omitempty"`
	Parameters      map[string]interface{} `json:"parameters,omitempty"`

	// PlacementRaw captures the raw placement value before we know
	// whether it's a string or an object. We re-decode in
	// promoteToCanonical.
	PlacementRaw json.RawMessage `json:"placement,omitempty"`
	Regions      []string        `json:"regions,omitempty"`

	// BlueprintRefRaw — when the canonical-shape `blueprintRef` field
	// is present we accept it too.
	BlueprintRefRaw json.RawMessage `json:"blueprintRef,omitempty"`

	// ToVersion — used by the upgrade/preview simplified shape.
	ToVersion string `json:"toVersion,omitempty"`
}

// promoteToCanonicalInstall fills out an applicationInstallRequest from
// a simplified-shape body. Returns the canonical struct + an error if
// the simplified body is internally inconsistent.
func (s applicationSimplifiedInstall) promoteToCanonicalInstall() (applicationInstallRequest, error) {
	out := applicationInstallRequest{
		Name:            strings.TrimSpace(s.Name),
		OrganizationRef: strings.TrimSpace(s.OrganizationRef),
		EnvironmentRef:  strings.TrimSpace(s.EnvironmentRef),
		Parameters:      mergeMaps(s.Values, s.Parameters),
	}

	// Promote blueprint name/version — accept either the simplified
	// `blueprint` + `version` pair OR the canonical `blueprintRef` object.
	if len(s.BlueprintRefRaw) > 0 {
		var ref applicationBlueprintRef
		if err := json.Unmarshal(s.BlueprintRefRaw, &ref); err != nil {
			return out, fmt.Errorf("blueprintRef: %w", err)
		}
		out.BlueprintRef = ref
	}
	if out.BlueprintRef.Name == "" && strings.TrimSpace(s.Blueprint) != "" {
		out.BlueprintRef.Name = strings.TrimSpace(s.Blueprint)
	}
	if out.BlueprintRef.Version == "" && strings.TrimSpace(s.Version) != "" {
		out.BlueprintRef.Version = strings.TrimSpace(s.Version)
	}

	// `namespace` is the simplified-shape alias for organizationRef.
	if out.OrganizationRef == "" && strings.TrimSpace(s.Namespace) != "" {
		out.OrganizationRef = strings.TrimSpace(s.Namespace)
	}
	// environmentRef defaults to organizationRef (one Org, one Env per
	// chroot Sovereign in EPIC-2).
	if out.EnvironmentRef == "" {
		out.EnvironmentRef = out.OrganizationRef
	}

	// Promote placement — accept either the simplified string-form OR
	// the canonical object-form OR neither (default single-region).
	if len(s.PlacementRaw) > 0 {
		mode, regions, err := decodePlacementValue(s.PlacementRaw)
		if err != nil {
			return out, fmt.Errorf("placement: %w", err)
		}
		out.Placement.Mode = mode
		if len(regions) > 0 {
			out.Placement.Regions = regions
		}
	}
	if len(out.Placement.Regions) == 0 && len(s.Regions) > 0 {
		out.Placement.Regions = append(out.Placement.Regions, s.Regions...)
	}
	if out.Placement.Mode == "" {
		out.Placement.Mode = applicationDefaultPlacementMode
	}
	if len(out.Placement.Regions) == 0 {
		out.Placement.Regions = []string{applicationDefaultPrimaryRegionFromEnv()}
	}

	return out, nil
}

// promoteToCanonicalUpdate fills out an applicationUpdateRequest from
// a simplified-shape body. Only the keys the caller supplied are
// promoted.
func (s applicationSimplifiedInstall) promoteToCanonicalUpdate() (applicationUpdateRequest, error) {
	out := applicationUpdateRequest{}

	if len(s.BlueprintRefRaw) > 0 {
		var ref applicationBlueprintRef
		if err := json.Unmarshal(s.BlueprintRefRaw, &ref); err != nil {
			return out, fmt.Errorf("blueprintRef: %w", err)
		}
		out.BlueprintRef = &ref
	}
	if strings.TrimSpace(s.Blueprint) != "" || strings.TrimSpace(s.Version) != "" || strings.TrimSpace(s.ToVersion) != "" {
		if out.BlueprintRef == nil {
			out.BlueprintRef = &applicationBlueprintRef{}
		}
		if out.BlueprintRef.Name == "" {
			out.BlueprintRef.Name = strings.TrimSpace(s.Blueprint)
		}
		if out.BlueprintRef.Version == "" {
			if v := strings.TrimSpace(s.Version); v != "" {
				out.BlueprintRef.Version = v
			} else if v := strings.TrimSpace(s.ToVersion); v != "" {
				out.BlueprintRef.Version = v
			}
		}
	}

	merged := mergeMaps(s.Values, s.Parameters)
	if len(merged) > 0 {
		out.Parameters = merged
	}

	if len(s.PlacementRaw) > 0 {
		mode, regions, err := decodePlacementValue(s.PlacementRaw)
		if err != nil {
			return out, fmt.Errorf("placement: %w", err)
		}
		out.Placement = &applicationPlacement{Mode: mode, Regions: regions}
	}
	if out.Placement != nil && len(s.Regions) > 0 && len(out.Placement.Regions) == 0 {
		out.Placement.Regions = append(out.Placement.Regions, s.Regions...)
	}
	// Special-case: caller supplied bare regions but no placement → assume
	// they want to keep the existing mode (handler picks it up from the CR).
	if out.Placement == nil && len(s.Regions) > 0 {
		out.Placement = &applicationPlacement{Regions: append([]string{}, s.Regions...)}
	}

	return out, nil
}

// decodePlacementValue handles BOTH the simplified string-form
// (`"placement": "single-region"`) AND the canonical object-form
// (`"placement": {"mode":"single-region","regions":[...]}`).
func decodePlacementValue(raw json.RawMessage) (mode string, regions []string, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil, nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return "", nil, fmt.Errorf("placement: %w", err)
		}
		return strings.TrimSpace(s), nil, nil
	}
	if trimmed[0] == '{' {
		var p applicationPlacement
		if err := json.Unmarshal(trimmed, &p); err != nil {
			return "", nil, fmt.Errorf("placement: %w", err)
		}
		return strings.TrimSpace(p.Mode), p.Regions, nil
	}
	return "", nil, fmt.Errorf("placement: must be a string mode or {mode,regions} object")
}

// mergeMaps copies entries from a, then overlays b. Used to merge the
// simplified `values` into the canonical `parameters`.
func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ── decodeApplicationInstallBody — public entry point for the install handler.
//
// Tries the canonical shape first (with DisallowUnknownFields). The
// applicationInstallRequest struct (PR #1227, 1.4.101) now carries
// short-form alias fields (BlueprintShort/VersionShort/NamespaceShort/
// ValuesShort) so the canonical decode succeeds for BOTH the
// long-form and short-form payloads; the alias fields are then
// collapsed onto the canonical fields by
// applicationInstallRequestNormalize. Returns the normalised canonical
// struct.
//
// The Path-B fallback (separate applicationSimplifiedInstall struct)
// is kept as a defensive net for callers using mixed/non-tagged shapes
// the strict decoder rejects (e.g. nested `placement` as a bare string
// when `placement` is currently a struct on the canonical shape).
func decodeApplicationInstallBody(raw []byte) (applicationInstallRequest, error) {
	// Path A: canonical shape, strict. The struct's short-form alias
	// fields let the matrix's simplified payload decode here too;
	// promotion to canonical fields happens in
	// applicationInstallRequestNormalize.
	var canonical applicationInstallRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return applicationInstallRequestNormalize(canonical), nil
	}
	// Path B: simplified shape via standalone struct, lenient
	// unknown-field handling. Catches shapes that the canonical
	// decoder rejects (e.g. simplified placement as a string instead
	// of an object).
	var simple applicationSimplifiedInstall
	if err := json.Unmarshal(raw, &simple); err != nil {
		return applicationInstallRequest{}, err
	}
	out, err := simple.promoteToCanonicalInstall()
	if err != nil {
		return applicationInstallRequest{}, err
	}
	return applicationInstallRequestNormalize(out), nil
}

// decodeApplicationUpdateBody — public entry point for the update handler.
// Same dual-shape strategy. PUT bodies are partial; an empty `{}` is a
// no-op (caller intentionally bumps annotations / observedGeneration).
func decodeApplicationUpdateBody(raw []byte) (applicationUpdateRequest, error) {
	var canonical applicationUpdateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return applicationUpdateRequestNormalize(canonical), nil
	}
	var simple applicationSimplifiedInstall
	if err := json.Unmarshal(raw, &simple); err != nil {
		return applicationUpdateRequest{}, err
	}
	out, err := simple.promoteToCanonicalUpdate()
	if err != nil {
		return applicationUpdateRequest{}, err
	}
	return applicationUpdateRequestNormalize(out), nil
}

// decodeApplicationPreviewBody — public entry point for the install-
// preview handler (POST .../applications/preview, no name in URL).
// Same dual-shape strategy as decodeApplicationInstallBody.
func decodeApplicationPreviewBody(raw []byte) (applicationPreviewRequest, error) {
	var canonical applicationPreviewRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return applicationPreviewRequestNormalize(canonical), nil
	}
	var simple applicationSimplifiedInstall
	if err := json.Unmarshal(raw, &simple); err != nil {
		return applicationPreviewRequest{}, err
	}
	install, err := simple.promoteToCanonicalInstall()
	if err != nil {
		return applicationPreviewRequest{}, err
	}
	return applicationPreviewRequest{
		BlueprintRef:    install.BlueprintRef,
		Name:            install.Name,
		OrganizationRef: install.OrganizationRef,
		EnvironmentRef:  install.EnvironmentRef,
		Parameters:      install.Parameters,
		Placement:       install.Placement,
	}, nil
}

// decodeApplicationChangePreviewBody — public entry point for the
// topology / upgrade preview handlers. Same dual-shape strategy. The
// topology preview accepts the simplified `{"placement":"<mode>",
// "regions":[...]}` shape; the upgrade preview accepts the simplified
// `{"toVersion":"x.y.z"}` shape; both also accept the canonical
// {"placement":{...},"blueprintRef":{...}} shape.
func decodeApplicationChangePreviewBody(raw []byte) (applicationChangePreviewRequest, error) {
	var canonical applicationChangePreviewRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return applicationChangePreviewRequestNormalize(canonical), nil
	}
	var simple applicationSimplifiedInstall
	if err := json.Unmarshal(raw, &simple); err != nil {
		return applicationChangePreviewRequest{}, err
	}
	out := applicationChangePreviewRequest{
		EnvironmentRef: strings.TrimSpace(simple.EnvironmentRef),
	}
	merged := mergeMaps(simple.Values, simple.Parameters)
	if len(merged) > 0 {
		out.Parameters = merged
	}
	if len(simple.PlacementRaw) > 0 {
		mode, regions, err := decodePlacementValue(simple.PlacementRaw)
		if err != nil {
			return out, fmt.Errorf("placement: %w", err)
		}
		out.Placement = &applicationPlacement{Mode: mode, Regions: regions}
	}
	if out.Placement != nil && len(simple.Regions) > 0 && len(out.Placement.Regions) == 0 {
		out.Placement.Regions = append(out.Placement.Regions, simple.Regions...)
	}
	if out.Placement == nil && len(simple.Regions) > 0 {
		out.Placement = &applicationPlacement{Regions: append([]string{}, simple.Regions...)}
	}
	if len(simple.BlueprintRefRaw) > 0 {
		var ref applicationBlueprintRef
		if err := json.Unmarshal(simple.BlueprintRefRaw, &ref); err != nil {
			return out, fmt.Errorf("blueprintRef: %w", err)
		}
		out.BlueprintRef = &ref
	}
	if strings.TrimSpace(simple.Blueprint) != "" || strings.TrimSpace(simple.Version) != "" || strings.TrimSpace(simple.ToVersion) != "" {
		if out.BlueprintRef == nil {
			out.BlueprintRef = &applicationBlueprintRef{}
		}
		if out.BlueprintRef.Name == "" {
			out.BlueprintRef.Name = strings.TrimSpace(simple.Blueprint)
		}
		if out.BlueprintRef.Version == "" {
			if v := strings.TrimSpace(simple.ToVersion); v != "" {
				out.BlueprintRef.Version = v
			} else if v := strings.TrimSpace(simple.Version); v != "" {
				out.BlueprintRef.Version = v
			}
		}
	}
	return out, nil
}
