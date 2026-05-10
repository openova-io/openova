package jsonutil

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestScrubNulls_RemovesNullMapEntries(t *testing.T) {
	in := map[string]interface{}{
		"a":                       1,
		"deprecatedFirstTimestamp": nil,
		"series":                  nil,
		"name":                    "qa-wp",
	}
	out, ok := ScrubNulls(in).(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", out)
	}
	if _, present := out["deprecatedFirstTimestamp"]; present {
		t.Errorf("expected deprecatedFirstTimestamp to be removed; map=%v", out)
	}
	if _, present := out["series"]; present {
		t.Errorf("expected series to be removed; map=%v", out)
	}
	if got, _ := out["a"]; got != 1 {
		t.Errorf("expected a=1, got %v", got)
	}
	if got, _ := out["name"]; got != "qa-wp" {
		t.Errorf("expected name=qa-wp, got %v", got)
	}
}

func TestScrubNulls_RecursesIntoNestedMaps(t *testing.T) {
	in := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":             "qa-wp-0",
			"deletionTimestamp": nil,
			"labels": map[string]interface{}{
				"app":                "qa-wp",
				"deprecatedSelector": nil,
			},
		},
	}
	out := ScrubNulls(in).(map[string]interface{})
	meta := out["metadata"].(map[string]interface{})
	if _, present := meta["deletionTimestamp"]; present {
		t.Errorf("expected nested deletionTimestamp removed; meta=%v", meta)
	}
	labels := meta["labels"].(map[string]interface{})
	if _, present := labels["deprecatedSelector"]; present {
		t.Errorf("expected nested deprecatedSelector removed; labels=%v", labels)
	}
	if labels["app"] != "qa-wp" {
		t.Errorf("expected app=qa-wp preserved, got %v", labels["app"])
	}
}

func TestScrubNulls_FiltersNullSliceElements(t *testing.T) {
	in := []interface{}{"a", nil, "b", nil}
	out := ScrubNulls(in).([]interface{})
	want := []interface{}{"a", "b"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("expected %v, got %v", want, out)
	}
}

func TestScrubNulls_SerializedHasNoNullLiterals(t *testing.T) {
	// The matrix asserter checks that the JSON envelope contains no
	// `null` literal substring. This test confirms the post-scrub
	// serialized payload satisfies that contract for a representative
	// k8s-style Event envelope.
	in := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{
				"deprecatedFirstTimestamp": nil,
				"series":                   nil,
				"reason":                   "Created",
				"message":                  "started qa-wp-0",
			},
		},
	}
	out := ScrubNulls(in)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("expected no `null` literal in scrubbed payload, got %s", raw)
	}
	if !strings.Contains(string(raw), `"reason":"Created"`) {
		t.Errorf("expected populated reason preserved, got %s", raw)
	}
}

func TestScrubNulls_PreservesNonNullPrimitives(t *testing.T) {
	in := map[string]interface{}{
		"string": "x",
		"bool":   false,
		"int":    0,
		"float":  3.14,
		"empty":  "",
	}
	out := ScrubNulls(in).(map[string]interface{})
	for _, k := range []string{"string", "bool", "int", "float", "empty"} {
		if _, ok := out[k]; !ok {
			t.Errorf("expected key %q preserved, missing in %v", k, out)
		}
	}
}
