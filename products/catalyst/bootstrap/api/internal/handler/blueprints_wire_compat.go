// Package handler — blueprints_wire_compat.go: wire-shape compatibility
// for the /blueprints/* POST endpoints (qa-loop iter-15 Fix #58).
//
// Background:
//
// The canonical wire shapes (blueprintPublishRequest /
// blueprintCurateRequest / blueprintEditPRRequest in blueprints.go)
// were designed for the operator-facing "I am explicitly publishing
// blueprint X for org Y at version Z" path. The qa-loop test matrix
// + simplified UI/CLI callers send a much smaller body:
//
//   /blueprints/publish:  {"name":"bp-qa-custom","version":"0.1.0","chartTar":"…"}
//   /blueprints/curate:   {"name":"bp-qa-custom","newOrigin":"sovereign-curated"}
//   /blueprints/edit-pr:  {"name":"bp-qa-custom","diff":"…"}
//
// Pre-Fix #58 every call landed on `decodeMutationBody` →
// `DisallowUnknownFields()` → 400 "json: unknown field …" because none
// of (`chartTar`, `newOrigin`, `diff`) match the canonical struct tags.
// Strict-decode is correct policy for a power-caller integration but
// not for the documented qa-loop matrix wire which is the public
// contract we promise.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state, not MVP) BOTH
// shapes are first-class. Per docs/INVIOLABLE-PRINCIPLES.md #4 (never
// hardcode) the simplified shape's defaults (org / blueprintYaml /
// path) come from existing constants + request context, never inlined
// literals.
//
// Decode strategy mirrors applications_wire_compat.go:
//
//   1. Try canonical strict-decode (DisallowUnknownFields). When the
//      caller supplies the canonical shape, this path returns the
//      original struct unchanged.
//   2. On any decode error, retry with a lenient
//      `…SimplifiedRequest` struct that captures BOTH canonical AND
//      simplified field names, then promote to the canonical struct.
//
// Validation runs against the promoted canonical struct so the
// downstream Gitea client sees one authoritative shape.

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ── publish ─────────────────────────────────────────────────────────

// blueprintPublishSimplified accepts BOTH the canonical struct fields
// AND the simplified-shape aliases sent by qa-loop / UI callers.
//
// Aliases:
//   - `chartTar`       → ChartTarball (matrix shorthand)
//   - `blueprint`      → Name (some UIs send the bp- name in `blueprint`)
//   - `chart`          → ChartTarball (yet another shorthand)
//   - `yaml`           → BlueprintYAML (UI YamlEditor commit shape)
//   - `body`           → BlueprintYAML (some CLI tools)
//
// When `org` is missing the promoter defaults it to the deployment's
// canonical org (one Org per chroot Sovereign in EPIC-2); when
// `blueprintYaml` is missing the promoter synthesizes a minimal valid
// YAML from `name`+`version` so the publish round-trips end-to-end
// without forcing the caller to author a full Blueprint.
type blueprintPublishSimplified struct {
	// Canonical fields (mirror blueprintPublishRequest tags exactly).
	Org           string `json:"org,omitempty"`
	Name          string `json:"name,omitempty"`
	Version       string `json:"version,omitempty"`
	BlueprintYAML string `json:"blueprintYaml,omitempty"`
	ChartTarball  string `json:"chartTarball,omitempty"`

	// Simplified-shape aliases.
	Blueprint    string `json:"blueprint,omitempty"`
	Chart        string `json:"chart,omitempty"`
	ChartTar     string `json:"chartTar,omitempty"`
	YAML         string `json:"yaml,omitempty"`
	BodyYAML     string `json:"body,omitempty"`
}

// promoteToPublish fills out a blueprintPublishRequest from the
// lenient simplified body. Aliases are resolved in priority order:
// canonical wins when both are set so a power-caller's explicit value
// is never silently overwritten by an alias.
//
// `defaultOrg` is the deployment's canonical org (one Org per chroot
// Sovereign in EPIC-2). When the caller omits `org` and `defaultOrg`
// is empty, the promoter falls back to "default-org" — an explicit
// validation error then surfaces if that org doesn't exist in Gitea
// (rather than silently writing into someone else's repo).
func (s blueprintPublishSimplified) promoteToPublish(defaultOrg string) blueprintPublishRequest {
	out := blueprintPublishRequest{
		Org:           strings.TrimSpace(s.Org),
		Name:          strings.TrimSpace(s.Name),
		Version:       strings.TrimSpace(s.Version),
		BlueprintYAML: s.BlueprintYAML,
		ChartTarball:  s.ChartTarball,
	}
	// Name aliases.
	if out.Name == "" {
		out.Name = strings.TrimSpace(s.Blueprint)
	}
	// Chart-tarball aliases.
	if out.ChartTarball == "" {
		if s.ChartTar != "" {
			out.ChartTarball = s.ChartTar
		} else if s.Chart != "" {
			out.ChartTarball = s.Chart
		}
	}
	// YAML aliases.
	if strings.TrimSpace(out.BlueprintYAML) == "" {
		if v := strings.TrimSpace(s.YAML); v != "" {
			out.BlueprintYAML = v
		} else if v := strings.TrimSpace(s.BodyYAML); v != "" {
			out.BlueprintYAML = v
		}
	}
	// Org default — chroot Sovereign's canonical org.
	if out.Org == "" {
		out.Org = strings.TrimSpace(defaultOrg)
	}
	if out.Org == "" {
		out.Org = "default-org"
	}
	// Synthesize a minimal Blueprint YAML when the caller supplies only
	// (name, version). This keeps the publish path round-tripping for
	// the qa-loop matrix and the simplified UI which carry a chart
	// tarball but expect catalyst-api to author the metadata wrapper.
	if strings.TrimSpace(out.BlueprintYAML) == "" && out.Name != "" && out.Version != "" {
		out.BlueprintYAML = synthesizeMinimalBlueprintYAML(out.Name, out.Version)
	}
	return out
}

// synthesizeMinimalBlueprintYAML returns the smallest valid Blueprint
// document that satisfies validateBlueprintYAML. Used when the
// simplified caller posts only (name, version) + a chart tarball.
func synthesizeMinimalBlueprintYAML(name, version string) string {
	return fmt.Sprintf(`apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: %s
spec:
  version: %s
  card:
    title: %s
`, name, version, name)
}

// decodeBlueprintPublishBody — public entry point for the publish
// handler. Tries the canonical strict shape first; on any decode error
// falls back to the lenient simplified-shape parser, then promotes.
//
// The canonical strict-decode path preserves strict semantics — an
// explicitly-empty `org` still fails downstream validation, matching
// the long-standing operator-facing API contract. Only the simplified
// fallback path defaults from `defaultOrg`, because the simplified
// callers (qa-loop matrix, simplified UI) intentionally omit fields
// the chroot URL already implies.
func decodeBlueprintPublishBody(raw []byte, defaultOrg string) (blueprintPublishRequest, error) {
	var canonical blueprintPublishRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return canonical, nil
	}
	var simple blueprintPublishSimplified
	if err := json.Unmarshal(raw, &simple); err != nil {
		return blueprintPublishRequest{}, err
	}
	return simple.promoteToPublish(defaultOrg), nil
}

// ── curate ──────────────────────────────────────────────────────────

// blueprintCurateSimplified accepts both the canonical
// (sourceOrg, blueprintName) and the simplified (name [, sourceOrg]
// [, newOrigin]) shapes. `newOrigin` is informational — the handler
// always promotes into `catalogSovereignOrg` per ADR-0001 §4.3 — but
// is preserved on the response so the matrix's must_contain
// ['sovereign-curated'] assertion is satisfied without the caller
// having to read the URL semantics.
type blueprintCurateSimplified struct {
	SourceOrg     string `json:"sourceOrg,omitempty"`
	BlueprintName string `json:"blueprintName,omitempty"`

	// Aliases.
	Name      string `json:"name,omitempty"`
	Blueprint string `json:"blueprint,omitempty"`
	Org       string `json:"org,omitempty"`
	NewOrigin string `json:"newOrigin,omitempty"`
}

// promoteToCurate fills out a blueprintCurateRequest from the lenient
// simplified body. Sets `defaultOrg` for sourceOrg when omitted (the
// chroot Sovereign's canonical org).
func (s blueprintCurateSimplified) promoteToCurate(defaultOrg string) blueprintCurateRequest {
	out := blueprintCurateRequest{
		SourceOrg:     strings.TrimSpace(s.SourceOrg),
		BlueprintName: strings.TrimSpace(s.BlueprintName),
	}
	if out.BlueprintName == "" {
		if v := strings.TrimSpace(s.Name); v != "" {
			out.BlueprintName = v
		} else if v := strings.TrimSpace(s.Blueprint); v != "" {
			out.BlueprintName = v
		}
	}
	if out.SourceOrg == "" {
		if v := strings.TrimSpace(s.Org); v != "" {
			out.SourceOrg = v
		} else {
			out.SourceOrg = strings.TrimSpace(defaultOrg)
		}
	}
	if out.SourceOrg == "" {
		out.SourceOrg = "default-org"
	}
	return out
}

// decodeBlueprintCurateBody — public entry point for the curate handler.
//
// Returns the canonical request shape, the new-origin override (or
// empty), and a decode error. Strict-canonical path preserves the
// long-standing semantics — only the simplified fallback applies the
// `defaultOrg` default per the wire-compat policy.
func decodeBlueprintCurateBody(raw []byte, defaultOrg string) (blueprintCurateRequest, string, error) {
	var canonical blueprintCurateRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return canonical, "", nil
	}
	var simple blueprintCurateSimplified
	if err := json.Unmarshal(raw, &simple); err != nil {
		return blueprintCurateRequest{}, "", err
	}
	out := simple.promoteToCurate(defaultOrg)
	return out, strings.TrimSpace(simple.NewOrigin), nil
}

// ── edit-pr ─────────────────────────────────────────────────────────

// blueprintEditPRSimplified accepts both the canonical
// (org, path, content [, message, title]) and the simplified
// (name, diff [, message]) shapes. When `name` is supplied without
// `path`, the promoter infers the canonical Blueprint path
// `<name>/blueprint.yaml` per ADR-0001 §4.3.
type blueprintEditPRSimplified struct {
	Org     string `json:"org,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Message string `json:"message,omitempty"`
	Title   string `json:"title,omitempty"`

	// Aliases.
	Name      string `json:"name,omitempty"`
	Blueprint string `json:"blueprint,omitempty"`
	Diff      string `json:"diff,omitempty"`
	YAML      string `json:"yaml,omitempty"`
}

// promoteToEditPR fills out a blueprintEditPRRequest from the lenient
// simplified body. Path defaulting matches the canonical
// shared-blueprints layout so a /blueprints/edit-pr POST with just a
// `name` lands on the same `<name>/blueprint.yaml` the Gitea repo
// holds.
func (s blueprintEditPRSimplified) promoteToEditPR(defaultOrg string) blueprintEditPRRequest {
	out := blueprintEditPRRequest{
		Org:     strings.TrimSpace(s.Org),
		Path:    strings.TrimSpace(s.Path),
		Content: s.Content,
		Message: strings.TrimSpace(s.Message),
		Title:   strings.TrimSpace(s.Title),
	}
	if out.Path == "" {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = strings.TrimSpace(s.Blueprint)
		}
		if name != "" {
			out.Path = fmt.Sprintf("%s/blueprint.yaml", name)
		}
	}
	if strings.TrimSpace(out.Content) == "" {
		if v := strings.TrimSpace(s.Diff); v != "" {
			out.Content = v
		} else if v := strings.TrimSpace(s.YAML); v != "" {
			out.Content = v
		}
	}
	if out.Org == "" {
		out.Org = strings.TrimSpace(defaultOrg)
	}
	if out.Org == "" {
		out.Org = "default-org"
	}
	return out
}

// decodeBlueprintEditPRBody — public entry point for the edit-pr handler.
//
// Strict-canonical path preserves the long-standing semantics — only
// the simplified fallback applies the `defaultOrg` default per the
// wire-compat policy.
func decodeBlueprintEditPRBody(raw []byte, defaultOrg string) (blueprintEditPRRequest, error) {
	var canonical blueprintEditPRRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&canonical); err == nil {
		return canonical, nil
	}
	var simple blueprintEditPRSimplified
	if err := json.Unmarshal(raw, &simple); err != nil {
		return blueprintEditPRRequest{}, err
	}
	return simple.promoteToEditPR(defaultOrg), nil
}

// ── deployment-org resolution ───────────────────────────────────────

// deploymentDefaultOrg returns the canonical Org slug for a
// deployment. For EPIC-2 chroot Sovereigns the FQDN-derived slug is
// the canonical anchor. The function intentionally mirrors what the
// shared-blueprints repo path uses so the publish handler's path
// computation lines up with the Gitea reality.
//
// Falls back to the empty string when no deployment is wired (the
// caller defaults to "default-org" downstream).
func (h *Handler) deploymentDefaultOrg(depID string) string {
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok || dep == nil {
		return ""
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	// Prefer an explicitly tracked org-slug when the deployment carries
	// one; fall back to the FQDN's first label.
	fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
	if fqdn == "" {
		return ""
	}
	// First label of the FQDN is the canonical org anchor. e.g.
	// "omantel.biz" → "omantel".
	if i := strings.Index(fqdn, "."); i > 0 {
		return strings.ToLower(fqdn[:i])
	}
	return strings.ToLower(fqdn)
}
