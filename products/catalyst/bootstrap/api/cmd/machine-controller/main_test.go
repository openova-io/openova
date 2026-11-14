package main

import (
	"reflect"
	"testing"
)

func TestParseControllers(t *testing.T) {
	got, err := parseControllers("inventory")
	if err != nil || !reflect.DeepEqual(got, []string{"inventory"}) {
		t.Fatalf("parseControllers(inventory) = %v, %v", got, err)
	}
	for _, bad := range []string{"", "operations", "inventory,inventory", "inventory,", " "} {
		if got, err := parseControllers(bad); err == nil {
			t.Errorf("parseControllers(%q) = %v, want an error", bad, got)
		}
	}
}

func TestRunRefusesUnknownController(t *testing.T) {
	if err := run([]string{"--controllers=operations"}); err == nil {
		t.Fatal("run accepted a controller this binary does not have")
	}
	if err := run(nil); err == nil {
		t.Fatal("run accepted an empty --controllers")
	}
}
