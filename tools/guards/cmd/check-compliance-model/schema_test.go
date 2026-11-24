package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return v
}

// TestEveryKeywordCanFail validates one document that breaks each keyword once and one
// that holds it, so a keyword that the validator skipped would show as a missing line.
func TestEveryKeywordCanFail(t *testing.T) {
	cases := []struct {
		keyword, schema, good, bad, want string
	}{
		{"type", `{"type": "integer"}`, `3`, `3.5`, "$: is number, want integer"},
		{"type string", `{"type": "string"}`, `"a"`, `1`, "$: is integer, want string"},
		{"enum", `{"enum": ["a", "b"]}`, `"b"`, `"c"`, `$: "c" is not one of ["a","b"]`},
		{"const", `{"const": "x"}`, `"x"`, `"y"`, `$: "y" is not "x"`},
		{"minLength", `{"minLength": 3}`, `"abc"`, `"ab"`, "$: is 2 characters, want at least 3"},
		{"maxLength", `{"maxLength": 2}`, `"ab"`, `"abc"`, "$: is 3 characters, want at most 2"},
		{"pattern", `{"pattern": "^[a-z]+$"}`, `"ab"`, `"A"`, `$: "A" does not match ^[a-z]+$`},
		{"minimum", `{"minimum": 1}`, `1`, `0`, "$: 0 is below the minimum 1"},
		{"maximum", `{"maximum": 1}`, `1`, `2`, "$: 2 is above the maximum 1"},
		{"minItems", `{"minItems": 1}`, `[1]`, `[]`, "$: has 0 items, want at least 1"},
		{"maxItems", `{"maxItems": 1}`, `[1]`, `[1, 2]`, "$: has 2 items, want at most 1"},
		{"uniqueItems", `{"uniqueItems": true}`, `[1, 2]`, `[1, 1]`, "$: items 0 and 1 are equal"},
		{"items", `{"items": {"type": "string"}}`, `["a"]`, `["a", 1]`, "$[1]: is integer, want string"},
		{"required", `{"required": ["a"]}`, `{"a": 1}`, `{"b": 1}`, "$: a is required"},
		{"properties", `{"properties": {"a": {"type": "string"}}}`, `{"a": "x"}`, `{"a": 1}`, "$.a: is integer, want string"},
		{"additionalProperties false", `{"properties": {"a": {}}, "additionalProperties": false}`, `{"a": 1}`, `{"a": 1, "b": 2}`, "$: b is not an allowed key"},
		{"additionalProperties schema", `{"additionalProperties": {"type": "string"}}`, `{"a": "x"}`, `{"a": 1}`, "$.a: is integer, want string"},
		{"propertyNames", `{"propertyNames": {"pattern": "^[a-z]+$"}}`, `{"ab": 1}`, `{"A": 1}`, `$ key A: "A" does not match ^[a-z]+$`},
		{"$ref", `{"$ref": "#/$defs/s", "$defs": {"s": {"type": "string"}}}`, `"a"`, `1`, "$: is integer, want string"},
		{"if then", `{"if": {"properties": {"t": {"const": "m"}}}, "then": {"required": ["m"]}}`, `{"t": "m", "m": 1}`, `{"t": "m"}`, "$: m is required"},
		{"if else", `{"if": {"properties": {"t": {"const": "m"}}}, "else": {"not": {"required": ["m"]}}}`, `{"t": "c"}`, `{"t": "c", "m": 1}`, `$: matches a schema it must not match ({"required":["m"]})`},
		{"allOf", `{"allOf": [{"type": "object"}, {"required": ["a"]}]}`, `{"a": 1}`, `{}`, "$: a is required"},
	}
	for _, tc := range cases {
		t.Run(tc.keyword, func(t *testing.T) {
			schema := mustJSON(t, tc.schema).(map[string]any)
			if lines, err := Validate(schema, mustJSON(t, tc.good)); err != nil || len(lines) != 0 {
				t.Fatalf("good document %s: %v %v", tc.good, lines, err)
			}
			lines, err := Validate(schema, mustJSON(t, tc.bad))
			if err != nil || len(lines) != 1 || lines[0] != tc.want {
				t.Fatalf("bad document %s: %q %v, want [%q]", tc.bad, lines, err, tc.want)
			}
		})
	}
}

func TestASchemaThatCannotBeAppliedIsAnError(t *testing.T) {
	for schema, want := range map[string]string{
		`{"format": "email"}`:                        "schema keyword format is not supported",
		`{"$ref": "#/$defs/none"}`:                   `$ref "#/$defs/none" does not resolve`,
		`{"$ref": "other.json"}`:                     "only #/$defs/<name> is supported",
		`{"type": ["string", "null"]}`:               "schema keyword type is not a string",
		`{"minLength": "3"}`:                         "schema keyword minLength is not a number",
		`{"items": {"properties": {"a": {"x": 1}}}}`: "schema keyword x is not supported",
	} {
		_, err := Validate(mustJSON(t, schema).(map[string]any), mustJSON(t, `["a", {"a": 1}]`))
		if schema == `{"minLength": "3"}` {
			_, err = Validate(mustJSON(t, schema).(map[string]any), "abc")
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", schema, err, want)
		}
	}
}
