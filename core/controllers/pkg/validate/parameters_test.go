package validate

import (
	"strings"
	"testing"
)

// schema fixture used across tests. Mirrors a typical Blueprint
// configSchema: object with required `replicas` (integer >= 1) and
// optional `image` (string).
func sampleSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"required": []interface{}{"replicas"},
		"properties": map[string]interface{}{
			"replicas": map[string]interface{}{
				"type":    "integer",
				"minimum": 1,
			},
			"image": map[string]interface{}{
				"type":    "string",
				"pattern": "^[a-z0-9./:-]+$",
			},
		},
	}
}

func TestParameters_NilSchema(t *testing.T) {
	rep, err := Parameters(nil, map[string]interface{}{"anything": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Valid {
		t.Error("nil schema should treat all parameters as valid")
	}
}

func TestParameters_Valid(t *testing.T) {
	rep, err := Parameters(sampleSchema(), map[string]interface{}{
		"replicas": 3,
		"image":    "nginx:1.27",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Valid {
		t.Errorf("expected valid, got errors: %v", rep.Errors)
	}
}

func TestParameters_MissingRequired(t *testing.T) {
	rep, err := Parameters(sampleSchema(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Valid {
		t.Fatal("expected invalid for missing required field")
	}
	joined := strings.Join(rep.Errors, "\n")
	if !strings.Contains(joined, "replicas") {
		t.Errorf("error should mention 'replicas': %v", rep.Errors)
	}
}

func TestParameters_TypeMismatch(t *testing.T) {
	rep, err := Parameters(sampleSchema(), map[string]interface{}{
		"replicas": "three",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Valid {
		t.Fatal("expected invalid for type mismatch")
	}
}

func TestParameters_PathSurface(t *testing.T) {
	// Nested schema — verifies the JSON pointer in the error.
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"db": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"port": map[string]interface{}{
						"type":    "integer",
						"minimum": 1024,
					},
				},
				"required": []interface{}{"port"},
			},
		},
		"required": []interface{}{"db"},
	}
	rep, err := Parameters(schema, map[string]interface{}{
		"db": map[string]interface{}{"port": 80},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Valid {
		t.Fatal("expected invalid for port < minimum")
	}
	joined := strings.Join(rep.Errors, "\n")
	if !strings.Contains(joined, "/db/port") {
		t.Errorf("error path should be #/db/port, got: %v", rep.Errors)
	}
}

func TestParameters_NilParameters(t *testing.T) {
	// Required field, nil parameters → invalid.
	rep, err := Parameters(sampleSchema(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Valid {
		t.Errorf("nil parameters with required field should be invalid")
	}
}

// objectNoRequiredSchema mirrors the bp-postgres configSchema shape:
// `type: object` with NO top-level `required` and every property
// defaulting. `{}` (and an absent parameters block normalised to `{}`)
// must validate cleanly against it.
func objectNoRequiredSchema() map[string]interface{} {
	return map[string]interface{}{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]interface{}{
			"enabled": map[string]interface{}{
				"type":    "boolean",
				"default": true,
			},
			"topology": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"mode": map[string]interface{}{
						"type": "string",
						"enum": []interface{}{"singleton", "active-hot-standby"},
					},
				},
			},
		},
	}
}

// TestParameters_TypedNilMap is the #4282 regression: the controller
// reads `spec.parameters` via `unstructured.NestedMap`, which returns a
// TYPED-nil `map[string]interface{}` when the key is absent (the live
// shared-pg-d/shared-pg-e shape — those CRs carry no spec.parameters
// key at all). A typed-nil map boxed into `interface{}` is NOT `== nil`,
// so before the fix it slipped past the `!= nil` guard, marshalled to
// JSON `null`, and the bp-postgres `type: object` schema rejected it as
// `#: expected object, but got null` on EVERY reconcile — leaving the
// Application Failed forever. The validator must normalise it to `{}`.
func TestParameters_TypedNilMap(t *testing.T) {
	var typedNil map[string]interface{} // typed-nil — NOT an untyped nil
	rep, err := Parameters(objectNoRequiredSchema(), typedNil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Valid {
		t.Fatalf("typed-nil map parameters against an object-no-required "+
			"schema must validate (normalise to {}); got errors: %v", rep.Errors)
	}
	for _, e := range rep.Errors {
		if strings.Contains(e, "expected object, but got null") {
			t.Fatalf("regression #4282: typed-nil parameters produced the "+
				"forbidden null error: %q", e)
		}
	}
}

// TestParameters_TypedNilMap_RequiredStillCaught proves the typed-nil
// normalisation does NOT silence genuine `required` violations — a
// typed-nil map against a schema with required fields is still invalid
// (it validates as `{}`, which is missing the required field).
func TestParameters_TypedNilMap_RequiredStillCaught(t *testing.T) {
	var typedNil map[string]interface{}
	rep, err := Parameters(sampleSchema(), typedNil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.Valid {
		t.Error("typed-nil map against a schema with a required field must " +
			"still be invalid (normalises to {}, which lacks the required field)")
	}
}

// TestParameters_EmptyObject is the positive baseline: an explicit `{}`
// against the bp-postgres-shaped schema validates (no required fields).
func TestParameters_EmptyObject(t *testing.T) {
	rep, err := Parameters(objectNoRequiredSchema(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rep.Valid {
		t.Errorf("empty object against object-no-required schema must validate; "+
			"got errors: %v", rep.Errors)
	}
}

func TestParameters_MalformedSchema(t *testing.T) {
	// "type" with an unknown value is rejected by the compiler.
	bad := map[string]interface{}{
		"type": 123, // type must be a string or array
	}
	_, err := Parameters(bad, map[string]interface{}{})
	if err == nil {
		t.Fatal("expected compile error for malformed schema")
	}
}
