// Package validate implements the per-Application parameter
// validation surface — the controller's check that
// `Application.spec.parameters` matches the parent Blueprint's
// `spec.configSchema`.
//
// Per slice C4 brief §3 (Validate Application.spec.parameters against
// Blueprint.spec.configSchema): the canonical JSON Schema validator is
// `github.com/santhosh-tekuri/jsonschema/v5`. This package wraps it
// so the controller has a single entry point and the test surface is
// stable.
//
// Why santhosh-tekuri/jsonschema/v5:
//
//   - Pure Go; no cgo, no dynamic loading.
//   - Implements JSON Schema draft 4/6/7/2019-09/2020-12 — the
//     Blueprint authoring contract (BLUEPRINT-AUTHORING.md §4) targets
//     draft-2020-12 by default.
//   - Returns structured `*ValidationError` with InstanceLocation
//     pointing at the failing field — so the controller's Condition
//     message can name every failing path, satisfying the brief's
//     "surface the path of every failing field" requirement.
//   - Battle-tested upstream, MIT-licensed.
//
// Usage:
//
//	rep, err := validate.Parameters(blueprintConfigSchema, applicationParameters)
//	if err != nil { /* internal/transport error */ }
//	if !rep.Valid { /* surface rep.Errors[] on the Application Condition */ }
package validate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Report is the validation outcome surfaced to the controller.
type Report struct {
	// Valid is true iff parameters fully satisfy the schema.
	Valid bool

	// Errors lists each failing field. Format:
	//
	//	"#/<json-pointer>: <reason>"
	//
	// e.g. "#/replicas: expected integer, but got string".
	//
	// Sorted by JSON-pointer for byte-stable output (so the
	// Condition.message is deterministic across reconcile passes —
	// important for idempotency, no spurious status churn).
	Errors []string
}

// Parameters compiles `schema` and validates `parameters` against it.
//
// `schema` and `parameters` are passed as the controller reads them
// from unstructured.Unstructured: arbitrary `interface{}` trees of
// `map[string]interface{}` / `[]interface{}` / primitives (the
// JSON-equivalent shape).
//
// A nil schema (Blueprint without a configSchema field) is treated as
// "no constraints" — every parameters tree validates. Per
// BLUEPRINT-AUTHORING.md §3 configSchema is OPTIONAL; not every
// Blueprint requires one.
//
// A non-nil schema with malformed body returns an error (the controller
// surfaces this as `Degraded` — the Blueprint itself is bugged, not the
// Application). The blueprint-controller (slice C3) catches most of
// these at Blueprint admission time, but a malformed-after-merge case
// is still possible.
//
// A nil parameters block on a non-nil schema is treated as `{}` — empty
// object. JSON Schema's `required` keyword catches any required-field
// omissions.
func Parameters(schema interface{}, parameters interface{}) (Report, error) {
	if schema == nil {
		return Report{Valid: true}, nil
	}
	// Reject typed-nil maps (the common shape from
	// unstructured.NestedMap when the field is absent — returns a
	// `map[string]interface{}(nil)` that is non-nil-interface but is
	// effectively empty).
	if m, ok := schema.(map[string]interface{}); ok && len(m) == 0 {
		return Report{Valid: true}, nil
	}

	// Compile the schema. The library expects a JSON-shaped
	// `interface{}` tree; we re-marshal through JSON to normalise
	// (the unstructured.Unstructured world emits map[string]interface{}
	// vs structured types from yaml.Unmarshal which can include
	// map[interface{}]interface{}).
	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return Report{}, fmt.Errorf("validate: marshal schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	// Resource URL is informational; it's only used in error messages
	// and to dedupe sub-schema references. We use a sentinel
	// `urn:openova:application-parameters-schema` — opaque, not
	// dereferenceable, and stable across reconciles.
	const schemaURL = "urn:openova:application-parameters-schema"
	if err := compiler.AddResource(schemaURL, strings.NewReader(string(schemaBytes))); err != nil {
		return Report{}, fmt.Errorf("validate: add schema resource: %w", err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return Report{}, fmt.Errorf("validate: compile schema: %w", err)
	}

	// Normalise parameters through JSON for the same reason.
	//
	// A nil/absent parameters block is treated as `{}` — an empty
	// object. This is the canonical shape an Application carries when no
	// explicit parameters are supplied (every configSchema property
	// defaults; the engine applies its own defaults). JSON Schema's
	// `required` keyword still catches any genuinely required-field
	// omissions, so this is safe.
	//
	// CRITICAL (#4282): `parameters != nil` is NOT sufficient to detect
	// "no parameters". The controller reads `spec.parameters` via
	// `unstructured.NestedMap`, which — when the key is absent or
	// explicitly `null` — returns a TYPED-nil `map[string]interface{}`.
	// A typed-nil map boxed into an `interface{}` is `!= nil` (the
	// interface carries a non-nil type descriptor), so it slips past the
	// `!= nil` guard, then `json.Marshal` of a nil map emits `null`, and
	// the schema rejects it as `#: expected object, but got null`. That
	// was the live, recurring `shared-pg-d`/`shared-pg-e` failure: those
	// Application CRs carry no `spec.parameters` key at all, so every
	// reconcile pass produced this false-Invalid regardless of which
	// producer wrote the CR. We therefore normalise FIRST, then treat a
	// post-marshal `nil` (typed-nil map/slice/pointer, or literal null)
	// as the empty object.
	var normalised interface{} = map[string]interface{}{}
	if parameters != nil {
		paramBytes, err := json.Marshal(parameters)
		if err != nil {
			return Report{}, fmt.Errorf("validate: marshal parameters: %w", err)
		}
		var v interface{}
		if err := json.Unmarshal(paramBytes, &v); err != nil {
			return Report{}, fmt.Errorf("validate: unmarshal parameters: %w", err)
		}
		if v != nil {
			normalised = v
		}
		// v == nil here means `parameters` was a typed-nil map/slice/
		// pointer that marshalled to JSON null → keep the `{}` default.
	}

	if err := compiled.Validate(normalised); err != nil {
		// jsonschema returns a `*ValidationError` tree. We flatten the
		// leaf errors (those with no Causes — i.e. the actual failing
		// instance locations) into a stable, sorted list.
		var ve *jsonschema.ValidationError
		errs := []string{}
		if asValErr(err, &ve) {
			collect(ve, &errs)
		} else {
			errs = []string{err.Error()}
		}
		// Sort + dedup.
		seen := map[string]struct{}{}
		out := errs[:0]
		for _, e := range errs {
			if _, ok := seen[e]; ok {
				continue
			}
			seen[e] = struct{}{}
			out = append(out, e)
		}
		sort.Strings(out)
		return Report{Valid: false, Errors: out}, nil
	}
	return Report{Valid: true}, nil
}

// collect walks the ValidationError tree and accumulates a flat list of
// "<path>: <message>" strings for each leaf error.
func collect(ve *jsonschema.ValidationError, out *[]string) {
	if ve == nil {
		return
	}
	if len(ve.Causes) == 0 {
		path := ve.InstanceLocation
		if path == "" {
			path = "#"
		} else {
			path = "#" + path
		}
		*out = append(*out, fmt.Sprintf("%s: %s", path, ve.Message))
		return
	}
	for _, c := range ve.Causes {
		collect(c, out)
	}
}

// asValErr is a tiny errors.As helper kept inline to avoid a stdlib
// import for one call site — see go vet's recommendation that
// errors.As is preferred for typed-error extraction.
func asValErr(err error, target **jsonschema.ValidationError) bool {
	if err == nil {
		return false
	}
	for cur := err; cur != nil; {
		if ve, ok := cur.(*jsonschema.ValidationError); ok {
			*target = ve
			return true
		}
		// Unwrap manually since santhosh's ValidationError doesn't
		// wrap a stdlib chain in v5.
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return false
}
