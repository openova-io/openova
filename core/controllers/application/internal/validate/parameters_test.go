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
