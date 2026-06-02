// Package validate — business-logic checks for Blueprint CRs.
//
// The CRD's openAPIV3Schema (products/catalyst/chart/crds/blueprint.yaml)
// already enforces the structural shape: spec.version regex, card.title
// non-empty, visibility enum, manifests.source.kind enum, etc.
//
// What this package adds is the union of checks the schema cannot
// express:
//
//   1. spec.placementSchema.modes[] is a non-empty subset of the canonical
//      mode set [single-region, active-active, active-hotstandby].
//      (CRD enforces the enum on each item but does not enforce
//      non-emptiness of the array — minItems: 1 is on the schema, but
//      the schema also marks placementSchema itself as
//      x-kubernetes-preserve-unknown-fields, so a hand-authored CR can
//      slip in `placementSchema: {}`.)
//   2. spec.manifests.source.kind, when present, is one of the three
//      legal values. (CRD has the enum, but a Blueprint may use the
//      v1alpha1 short-form `manifests.chart: ./chart` and have NO
//      `source` block — that path is legal and must NOT trigger an
//      error.)
//   3. spec.upgrades.from[] entries are syntactically-valid semver
//      ranges per docs/BLUEPRINT-AUTHORING.md §3 and §10.
//   4. spec.depends[].blueprint references either (a) a known Blueprint
//      in the catalog or (b) a known dependency in this same set of
//      Blueprint CRs. Resolution of (a) is the controller's
//      responsibility — this package returns the *list* of blueprint
//      names that must be checked; the caller does the lookup and
//      surfaces a Pending condition if none resolve.
//   5. metadata.name SHOULD match `bp-<name>` form OR the lower-case
//      kebab-case of card.title. Returns a *warning* (not error) if it
//      doesn't, since the existing 61-blueprint corpus has divergent
//      conventions (e.g. `bp-cilium` vs `bp-wordpress-tenant`). The
//      controller surfaces warnings as a status.conditions[] entry but
//      does not block publishing.
//
// Per slice C3 brief: when a `depends[].blueprint` doesn't resolve,
// surface a Pending condition rather than rejecting outright. So this
// package returns a Result struct whose fields are interpreted by the
// controller — Errors are hard rejections, Pending entries delay
// publication, Warnings are logged but accepted.
package validate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/semver"
)

// blueprintTopologySchemaURL — opaque, stable sentinel identifying the
// embedded `platform/_schemas/blueprint-topology.json`. Used only as the
// schema-compiler resource key and in error messages.
const blueprintTopologySchemaURL = "urn:openova:blueprint-topology-schema"

// TopologySchemaViolationCode is the Result.Findings code namespace
// surfaced on `Blueprint.status.conditions[]` for every jsonschema
// finding produced by `Validate`. The controller maps each finding to
// a Condition entry of `reason: TopologySchemaViolation` with the
// finding's path+message in `message`.
const TopologySchemaViolationCode = "topology-schema-violation"

// compiledTopologySchema is the singleton compiled schema. Compilation
// is non-trivial (reference walking, allOf flattening); we do it once
// per process under sync.Once. If the embedded schema is malformed the
// error is captured and surfaced on every Validate call so the
// controller's Degraded condition reports the bug.
var (
	compiledTopologySchemaOnce sync.Once
	compiledTopologySchema     *jsonschema.Schema
	compiledTopologySchemaErr  error
)

// compileBlueprintTopologySchema compiles the embedded canonical schema.
// Idempotent; safe for concurrent callers.
func compileBlueprintTopologySchema() (*jsonschema.Schema, error) {
	compiledTopologySchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(
			blueprintTopologySchemaURL,
			strings.NewReader(string(blueprintTopologySchemaJSON)),
		); err != nil {
			compiledTopologySchemaErr = fmt.Errorf("add embedded blueprint-topology schema: %w", err)
			return
		}
		s, err := compiler.Compile(blueprintTopologySchemaURL)
		if err != nil {
			compiledTopologySchemaErr = fmt.Errorf("compile blueprint-topology schema: %w", err)
			return
		}
		compiledTopologySchema = s
	})
	return compiledTopologySchema, compiledTopologySchemaErr
}

// canonicalPlacementModes — must mirror the enum in
// products/catalyst/chart/crds/blueprint.yaml `placementSchema.modes`.
//
// Two tiers of placement modes coexist:
//
//  1. Application-tier modes — operator/tenant-facing modes for normal
//     application Blueprints (the marketplace 99%):
//     - single-region     (one region, no replication)
//     - active-active     (multi-region, all primary)
//     - active-hotstandby (multi-region, primary + warm standby)
//
//  2. Bootstrap-topology modes — used by `bp-*-vcluster` and other
//     bootstrap-kit Blueprints whose placement is dictated by the
//     Sovereign multi-region topology (docs/SOVEREIGN-MULTI-REGION-
//     DOD.md A4). These are NOT user-selectable; they document which
//     regions the bootstrap layer auto-installs the chart into:
//     - primary-only      (installed only in the primary region; e.g.
//                          bp-mgmt-vcluster, bp-vcluster-helmrepo)
//     - secondary-only    (installed only in secondary regions; e.g.
//                          bp-rtz-vcluster)
//     - every-region      (installed in every region — primary +
//                          all secondaries; e.g. bp-dmz-vcluster)
//
// Both tiers are validated here so the controller accepts the full
// 71-blueprint corpus. The CRD's openAPIV3Schema enum
// (products/catalyst/chart/crds/blueprint.yaml) is the structural mirror
// and must be kept in sync — see that file's `placementSchema.modes`
// items.enum.
var canonicalPlacementModes = map[string]struct{}{
	// Application-tier
	"single-region":     {},
	"active-active":     {},
	"active-hotstandby": {},
	// Bootstrap-topology tier (docs/SOVEREIGN-MULTI-REGION-DOD.md A4)
	"primary-only":   {},
	"secondary-only": {},
	"every-region":   {},
}

// canonicalManifestKinds — must mirror the enum in
// products/catalyst/chart/crds/blueprint.yaml `manifests.source.kind`.
var canonicalManifestKinds = map[string]struct{}{
	"HelmChart": {},
	"Kustomize": {},
	"OAM":       {},
}

// bpNamePattern — matches `bp-<lowercase-kebab>`.
var bpNamePattern = regexp.MustCompile(`^bp-[a-z][a-z0-9-]{0,61}$`)

// kebabPattern — matches a lowercase kebab-case identifier.
var kebabPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}$`)

// titleToKebab returns the lowercase-kebab-case projection of title.
// Spaces and underscores → "-"; non-alnum characters dropped; multiple
// consecutive hyphens collapsed; leading/trailing hyphens trimmed.
//
// Examples:
//   "WordPress" → "wordpress"
//   "WordPress Tenant" → "wordpress-tenant"
//   "Hetzner CSI" → "hetzner-csi"
//   "kube-prometheus-stack" → "kube-prometheus-stack"
func titleToKebab(title string) string {
	var sb strings.Builder
	prevHyphen := true // drop leading hyphens
	for _, r := range title {
		switch {
		case r >= 'A' && r <= 'Z':
			sb.WriteRune(r - 'A' + 'a')
			prevHyphen = false
		case r >= 'a' && r <= 'z':
			sb.WriteRune(r)
			prevHyphen = false
		case r >= '0' && r <= '9':
			sb.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '_' || r == '-' || r == '/' || r == '.':
			if !prevHyphen {
				sb.WriteRune('-')
				prevHyphen = true
			}
		default:
			// Drop any other rune.
		}
	}
	out := sb.String()
	out = strings.TrimRight(out, "-")
	return out
}

// Result captures the outcome of a Blueprint validation pass.
type Result struct {
	// Errors block publication; the controller refuses to mirror the
	// Blueprint and the CR's Ready condition reports False with reason
	// "ValidationFailed".
	Errors []string

	// PendingDeps lists `spec.depends[].blueprint` values that the
	// controller could not resolve in the catalog at validation time.
	// Per slice C3 brief: the controller surfaces a Pending condition
	// (not a hard error) so a Blueprint added in the same Gitea PR can
	// still resolve once the dependency lands. Empty = no pending deps.
	PendingDeps []string

	// Warnings are logged + surfaced on status.conditions[] but do not
	// block publication. Used for the metadata.name vs card.title-kebab
	// soft check, etc.
	Warnings []string

	// Findings carries coded validation results — currently used for
	// G117.1 AC2 (#2740) jsonschema topology-schema-violation entries.
	// Each Finding has a stable Code namespace so the controller can
	// emit a structured Condition entry per finding.
	Findings []Finding
}

// Finding is a single coded validation observation. Used by the
// blueprint-controller to surface structured
// `Blueprint.status.conditions[]` entries — e.g.
// `reason: TopologySchemaViolation` with the failing JSON-pointer + the
// jsonschema message in `message`.
type Finding struct {
	// Code is a stable, kebab-case namespace string. See
	// TopologySchemaViolationCode.
	Code string
	// Path is the JSON-pointer of the failing instance location.
	// Empty when the finding is whole-object scoped.
	Path string
	// Message is the human-readable description of the violation.
	Message string
}

// HasErrors returns true if Errors is non-empty.
func (r Result) HasErrors() bool {
	return len(r.Errors) > 0
}

// Validate runs business-logic checks against bp (an unstructured
// Blueprint CR) and returns a Result. The catalog parameter holds
// known Blueprint names — pass an empty set in unit tests if dependency
// resolution is not under test.
func Validate(bp *unstructured.Unstructured, catalog map[string]struct{}) Result {
	var res Result

	if bp == nil {
		res.Errors = append(res.Errors, "blueprint object is nil")
		return res
	}

	name := bp.GetName()
	if name == "" {
		res.Errors = append(res.Errors, "metadata.name is empty")
	}

	spec, found, err := unstructured.NestedMap(bp.Object, "spec")
	if err != nil || !found {
		res.Errors = append(res.Errors, "spec is missing or not a map")
		return res
	}

	// --- card.title (CRD enforces required, but reaffirm here so the
	// dependent name-vs-title check has a value to use).
	cardTitle, _, _ := unstructured.NestedString(spec, "card", "title")

	// --- name vs card.title soft check.
	// Two acceptable forms:
	//   1. bp-<kebab>
	//   2. <kebab> matching titleToKebab(card.title)
	// Anything else is a *warning* per slice C3 brief.
	if name != "" {
		titleKebab := titleToKebab(cardTitle)
		switch {
		case bpNamePattern.MatchString(name):
			// Optional tighter check: when bp-<kebab> form, kebab must
			// match titleKebab. We only warn — the existing corpus has
			// e.g. `bp-cert-manager-dynadot-webhook` whose card.title
			// is "cert-manager DNS-01 (Dynadot)" — close but not exact.
			withoutPrefix := strings.TrimPrefix(name, "bp-")
			if titleKebab != "" && withoutPrefix != titleKebab {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"metadata.name %q does not match bp-<card.title-kebab> (got %q, kebab(title) = %q); accepted but consider aligning",
					name, withoutPrefix, titleKebab,
				))
			}
		case kebabPattern.MatchString(name) && (titleKebab == "" || name == titleKebab):
			// bare kebab matching the title — accepted without warning.
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"metadata.name %q is neither bp-<kebab> nor kebab(card.title) %q; accepted but consider renaming",
				name, titleKebab,
			))
		}
	}

	// --- placementSchema.modes[] — non-empty subset of canonical set.
	if pSchema, ok := nestedAsMap(spec, "placementSchema"); ok {
		modes, _, _ := unstructured.NestedStringSlice(pSchema, "modes")
		// Empty placementSchema = OK (use system default). Empty
		// modes[] when placementSchema is present and modes is set =
		// error.
		if rawModes, hasModes := pSchema["modes"]; hasModes {
			if rawModes == nil {
				res.Errors = append(res.Errors, "spec.placementSchema.modes is null; expected non-empty array")
			} else if len(modes) == 0 {
				res.Errors = append(res.Errors, "spec.placementSchema.modes is empty; expected non-empty array")
			}
			for _, m := range modes {
				if _, ok := canonicalPlacementModes[m]; !ok {
					res.Errors = append(res.Errors, fmt.Sprintf(
						"spec.placementSchema.modes contains %q; legal values: single-region, active-active, active-hotstandby, primary-only, secondary-only, every-region",
						m,
					))
				}
			}
		}
		// Optional: default mode must be one of modes[] (when both set).
		if defaultMode, _, _ := unstructured.NestedString(pSchema, "default"); defaultMode != "" {
			if _, ok := canonicalPlacementModes[defaultMode]; !ok {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.placementSchema.default = %q; legal values: single-region, active-active, active-hotstandby, primary-only, secondary-only, every-region",
					defaultMode,
				))
			}
			if len(modes) > 0 && !containsString(modes, defaultMode) {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.placementSchema.default = %q is not in modes[]: %v",
					defaultMode, modes,
				))
			}
		}
		// G93.2 (Refs #2667) — defaultOnMultiRegion is the Blueprint
		// author's declarative preferred default for multi-region
		// Sovereigns. Same canonical-set + in-modes[] invariants as
		// `default`. If a Blueprint forgets to add the mode to modes[]
		// but declares it here, every install on a multi-region
		// Sovereign would land an Application CR with a placement
		// outside the allowed modes — the application-controller would
		// then mark every Application Failed. Fail-fast at publish.
		if dMR, _, _ := unstructured.NestedString(pSchema, "defaultOnMultiRegion"); dMR != "" {
			if _, ok := canonicalPlacementModes[dMR]; !ok {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.placementSchema.defaultOnMultiRegion = %q; legal values: single-region, active-active, active-hotstandby, primary-only, secondary-only, every-region",
					dMR,
				))
			}
			if len(modes) > 0 && !containsString(modes, dMR) {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.placementSchema.defaultOnMultiRegion = %q is not in modes[]: %v",
					dMR, modes,
				))
			}
		}
	}

	// --- manifests.source.kind — when present, in canonical set.
	if mfsts, ok := nestedAsMap(spec, "manifests"); ok {
		if src, ok := nestedAsMap(mfsts, "source"); ok {
			kind, _, _ := unstructured.NestedString(src, "kind")
			if kind != "" {
				if _, ok := canonicalManifestKinds[kind]; !ok {
					res.Errors = append(res.Errors, fmt.Sprintf(
						"spec.manifests.source.kind = %q; legal values: HelmChart, Kustomize, OAM",
						kind,
					))
				}
			}
		}
	}

	// --- upgrades.from[] — each must be a valid semver range.
	if upgrades, ok := nestedAsMap(spec, "upgrades"); ok {
		from, _, _ := unstructured.NestedStringSlice(upgrades, "from")
		for _, fr := range from {
			if err := semver.IsValidRange(fr); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.upgrades.from contains invalid semver range %q: %v",
					fr, err,
				))
			}
		}
		// blocks[] uses the same grammar.
		blocks, _, _ := unstructured.NestedStringSlice(upgrades, "blocks")
		for _, b := range blocks {
			if err := semver.IsValidRange(b); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"spec.upgrades.blocks contains invalid semver range %q: %v",
					b, err,
				))
			}
		}
	}

	// --- depends[].blueprint resolution against catalog.
	// Result.PendingDeps captures unresolved names; the controller
	// surfaces a Pending condition for them. This is non-blocking
	// per the brief.
	if depsRaw, found, err := unstructured.NestedSlice(spec, "depends"); err == nil && found {
		for _, d := range depsRaw {
			depMap, ok := d.(map[string]interface{})
			if !ok {
				// Schema requires depends[] items be objects with a
				// `blueprint` key. CRD validation catches this at
				// admission, but be defensive — bare-string entries
				// have appeared in 5/61 blueprint files historically
				// (slice B4 fixed those, but a future regression
				// shouldn't crash this controller).
				continue
			}
			depName, _, _ := unstructured.NestedString(depMap, "blueprint")
			if depName == "" {
				continue
			}
			if catalog == nil {
				continue
			}
			if _, ok := catalog[depName]; !ok {
				// Could be the same Blueprint as `bp-<x>` vs `<x>`.
				// Try both forms for resolution.
				alt := strings.TrimPrefix(depName, "bp-")
				altPrefixed := "bp-" + depName
				if _, ok := catalog[alt]; ok {
					continue
				}
				if _, ok := catalog[altPrefixed]; ok {
					continue
				}
				res.PendingDeps = append(res.PendingDeps, depName)
			}
		}
	}

	// --- G117.1 AC2 (#2740) — JSON Schema topology gate.
	// Run the canonical platform/_schemas/blueprint-topology.json
	// against the full Blueprint object. Every leaf jsonschema
	// violation becomes a Finding with code `topology-schema-violation`.
	//
	// Findings are surfaced on Blueprint.status.conditions[] as
	// `reason: TopologySchemaViolation`. Per locked decision in the
	// G117.1 brief, schema violations are advisory at this slice (the
	// controller logs + conditions them but does NOT hard-block
	// publication — that escalation lands in a follow-up slice once
	// the existing corpus is fully migrated). They DO NOT populate
	// res.Errors for that reason.
	res.Findings = append(res.Findings, validateAgainstTopologySchema(bp)...)

	return res
}

// validateAgainstTopologySchema runs the embedded blueprint-topology
// JSON Schema against the full Blueprint object and returns one Finding
// per leaf violation, plus cross-field findings the JSON Schema cannot
// express (currently: endpoint variant-membership). Returns nil when
// the document is clean.
func validateAgainstTopologySchema(bp *unstructured.Unstructured) []Finding {
	if bp == nil {
		return nil
	}
	schema, err := compileBlueprintTopologySchema()
	if err != nil {
		return []Finding{{
			Code:    TopologySchemaViolationCode,
			Message: fmt.Sprintf("internal: %v", err),
		}}
	}

	// Normalise the unstructured Blueprint through JSON. The
	// jsonschema library expects JSON-shaped maps; unstructured
	// already uses map[string]interface{}, but we round-trip to
	// coerce numeric YAML-`int64` to JSON-`float64` per draft-07
	// rules and to strip non-JSON-encodable values.
	raw, err := json.Marshal(bp.Object)
	if err != nil {
		return []Finding{{
			Code:    TopologySchemaViolationCode,
			Message: fmt.Sprintf("marshal blueprint for schema validation: %v", err),
		}}
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []Finding{{
			Code:    TopologySchemaViolationCode,
			Message: fmt.Sprintf("unmarshal blueprint for schema validation: %v", err),
		}}
	}

	var findings []Finding
	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if asTopologyValErr(err, &ve) {
			findings = collectTopologyFindings(ve)
		} else {
			findings = []Finding{{
				Code:    TopologySchemaViolationCode,
				Message: err.Error(),
			}}
		}
	}
	// Cross-field semantic check that the embedded JSON Schema can't
	// express: every endpoint.variant must be a member of
	// spec.topology.supported[].
	findings = append(findings, validateEndpointVariantMembership(bp)...)
	// Soft plausibility check the embedded JSON Schema can't express:
	// hostnameTemplate must look like a Go-text/template-rendered
	// hostname, not a URL. Anything containing "://" or starting with
	// "http:" / "https:" is a finding.
	findings = append(findings, validateEndpointHostnameTemplate(bp)...)
	return dedupFindings(findings)
}

// hostnameTemplateURLLike — patterns that disqualify a hostnameTemplate.
// Tightening this regex is allowed in a follow-up slice; for now we
// trip on the most common authoring mistake (pasting a full URL into
// hostnameTemplate). Per docs/BLUEPRINT-AUTHORING.md §6.endpoints,
// hostnameTemplate is a hostname after Go-template render — no scheme,
// no path, no query.
var hostnameTemplateURLLike = regexp.MustCompile(`^(?:https?:|.*://)`)

// validateEndpointHostnameTemplate emits one finding per endpoint
// whose hostnameTemplate looks like a URL (contains "://" or starts
// with "http:" / "https:"). Empty endpoints[] skips silently.
func validateEndpointHostnameTemplate(bp *unstructured.Unstructured) []Finding {
	if bp == nil {
		return nil
	}
	endpoints, _, _ := unstructured.NestedSlice(bp.Object, "spec", "endpoints")
	var out []Finding
	for i, ep := range endpoints {
		epMap, ok := ep.(map[string]interface{})
		if !ok {
			continue
		}
		hostnameTemplate, _, _ := unstructured.NestedString(epMap, "hostnameTemplate")
		if hostnameTemplate == "" {
			continue
		}
		if hostnameTemplateURLLike.MatchString(hostnameTemplate) {
			out = append(out, Finding{
				Code:    TopologySchemaViolationCode,
				Path:    fmt.Sprintf("#/spec/endpoints/%d/hostnameTemplate", i),
				Message: fmt.Sprintf("hostnameTemplate %q looks like a URL; expected a Go-text/template-rendered hostname (no scheme, no path)", hostnameTemplate),
			})
		}
	}
	return out
}

// validateEndpointVariantMembership emits a finding when a variant
// is declared on the Blueprint but not in spec.topology.supported[].
// Two sources of variant references are checked (soft cross-field
// gates the embedded JSON Schema can't express):
//
//   - spec.topology.perTopology.* keys — every key defines a variant.
//     Per locked decision #7 in the G117.1 brief, a perTopology entry
//     for a variant NOT in supported[] is incoherent (the controller
//     will never select it).
//   - spec.endpoints[].variant — when present, must be a member of
//     supported[]. The canonical schema currently does NOT define a
//     `variant` property on EndpointSpec (additionalProperties: false),
//     so this branch fires only when the variant field is added in a
//     future schema bump; today its impact is "no-op" (the schema
//     itself flags the unknown property).
//
// Empty supported[] disables the check (the schema-level
// Topology.supported.minItems=1 already guarantees a finding then).
func validateEndpointVariantMembership(bp *unstructured.Unstructured) []Finding {
	if bp == nil {
		return nil
	}
	supportedSlice, _, _ := unstructured.NestedStringSlice(bp.Object, "spec", "topology", "supported")
	if len(supportedSlice) == 0 {
		return nil
	}
	supportedSet := make(map[string]struct{}, len(supportedSlice))
	for _, v := range supportedSlice {
		supportedSet[v] = struct{}{}
	}
	var out []Finding

	// 1) perTopology.* keys
	if perTopology, found, _ := unstructured.NestedMap(bp.Object, "spec", "topology", "perTopology"); found {
		keys := make([]string, 0, len(perTopology))
		for k := range perTopology {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := supportedSet[k]; !ok {
				out = append(out, Finding{
					Code:    TopologySchemaViolationCode,
					Path:    fmt.Sprintf("#/spec/topology/perTopology/%s", k),
					Message: fmt.Sprintf("perTopology variant %q is not in spec.topology.supported %v", k, supportedSlice),
				})
			}
		}
	}

	// 2) endpoints[].variant (forward-compat — see doc comment).
	endpoints, _, _ := unstructured.NestedSlice(bp.Object, "spec", "endpoints")
	for i, ep := range endpoints {
		epMap, ok := ep.(map[string]interface{})
		if !ok {
			continue
		}
		variant, _, _ := unstructured.NestedString(epMap, "variant")
		if variant == "" {
			continue
		}
		if _, ok := supportedSet[variant]; !ok {
			out = append(out, Finding{
				Code:    TopologySchemaViolationCode,
				Path:    fmt.Sprintf("#/spec/endpoints/%d/variant", i),
				Message: fmt.Sprintf("endpoint variant %q is not in spec.topology.supported %v", variant, supportedSlice),
			})
		}
	}
	return out
}

// collectTopologyFindings walks a santhosh-tekuri/jsonschema/v5
// *ValidationError tree and emits one Finding per leaf cause. Each
// finding's Path is the JSON-pointer of the failing instance location.
func collectTopologyFindings(ve *jsonschema.ValidationError) []Finding {
	var out []Finding
	var walk func(*jsonschema.ValidationError)
	walk = func(v *jsonschema.ValidationError) {
		if v == nil {
			return
		}
		if len(v.Causes) == 0 {
			path := v.InstanceLocation
			if path == "" {
				path = "#"
			} else {
				path = "#" + path
			}
			out = append(out, Finding{
				Code:    TopologySchemaViolationCode,
				Path:    path,
				Message: v.Message,
			})
			return
		}
		for _, c := range v.Causes {
			walk(c)
		}
	}
	walk(ve)
	return out
}

// dedupFindings removes duplicates (by Path+Message) and stable-sorts
// by Path then Message so Condition messages are byte-deterministic
// across reconcile passes — same idempotency rationale as
// core/controllers/pkg/validate.Parameters.
func dedupFindings(in []Finding) []Finding {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		k := f.Path + "\x00" + f.Message
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// asTopologyValErr is a tiny errors.As-equivalent for the v5 lib's
// untyped error chain (see core/controllers/pkg/validate.asValErr).
func asTopologyValErr(err error, target **jsonschema.ValidationError) bool {
	if err == nil {
		return false
	}
	for cur := err; cur != nil; {
		if ve, ok := cur.(*jsonschema.ValidationError); ok {
			*target = ve
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return false
}

// nestedAsMap returns m[key] as map[string]interface{} when the value
// is a JSON object, or (nil, false) otherwise. Helper for the multi-step
// nested-map pattern that unstructured.NestedMap on its own can't
// express (it returns nil if the path doesn't exist OR returns an error
// if the path is the wrong type, and the controller treats both as
// "ignore this path").
func nestedAsMap(m map[string]interface{}, key string) (map[string]interface{}, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return nil, false
	}
	mm, ok := v.(map[string]interface{})
	return mm, ok
}

// containsString reports whether s is in xs.
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
