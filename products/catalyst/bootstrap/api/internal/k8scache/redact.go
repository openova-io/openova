// redact.go — helpers for stripping sensitive fields from
// unstructured.Unstructured objects before they leave the informer
// goroutine.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): Secret
// data + stringData are NEVER serialised onto the wire or persisted
// to the snapshot file. ConfigMap data is treated PII-adjacent and
// also stripped — operators view it via an authenticated GET path
// with SAR gating, never via the SSE event stream.
//
// The redactor preserves: apiVersion, kind, metadata (with managedFields
// stripped to keep the wire small), spec, status. The metadata.name +
// metadata.namespace + metadata.labels are essential for the UI's
// rendering pipeline so they always survive.
package k8scache

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// unstructuredDelete removes a top-level key from u.Object. No-op if
// the key is absent or u is nil.
func unstructuredDelete(u *unstructured.Unstructured, keys ...string) {
	if u == nil {
		return
	}
	cur := u.Object
	if len(keys) == 0 {
		return
	}
	for i, k := range keys {
		if i == len(keys)-1 {
			delete(cur, k)
			return
		}
		next, ok := cur[k].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

// unionKeys returns the lexically-stable union of all top-level keys
// found at u.Object[fields[0]], u.Object[fields[1]], ... (each
// expected to be map[string]any). Values are not inspected.
func unionKeys(u *unstructured.Unstructured, fields ...string) []string {
	if u == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, f := range fields {
		m, ok := u.Object[f].(map[string]any)
		if !ok {
			continue
		}
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	// Stable order so two redactions of the same body produce the
	// same byte stream — important for the snapshot's atomic-rename
	// idempotence.
	sortStrings(out)
	return out
}

// setNested sets m[keys[0]][keys[1]]... = value, creating intermediate
// maps as needed. Returns nil on success.
func setNested(m map[string]any, value any, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	cur := m
	for i := 0; i < len(keys)-1; i++ {
		next, ok := cur[keys[i]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[keys[i]] = next
		}
		cur = next
	}
	cur[keys[len(keys)-1]] = value
	return nil
}

// setNestedString — same as setNested but stores the value as a
// string. Used for the "<redacted>" sentinel.
func setNestedString(m map[string]any, v string, keys ...string) error {
	return setNested(m, v, keys...)
}

// sortStrings — small in-place insertion sort. Avoids pulling
// sort.Strings into hot paths (factory.dispatch is on the informer
// goroutine; allocation matters).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
