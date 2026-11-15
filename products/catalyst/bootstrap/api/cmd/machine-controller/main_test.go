package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/operations"
)

func TestParseControllers(t *testing.T) {
	for value, want := range map[string][]string{
		"inventory": {"inventory"}, "operations": {"operations"}, "operations, inventory": {"inventory", "operations"},
	} {
		if got, err := parseControllers(value); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseControllers(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, bad := range []string{"", "patching", "inventory,inventory", "inventory,", " "} {
		if got, err := parseControllers(bad); err == nil {
			t.Errorf("parseControllers(%q) = %v, want an error", bad, got)
		}
	}
}

func TestRunRefusesUnknownController(t *testing.T) {
	if err := run([]string{"--controllers=patching"}); err == nil {
		t.Fatal("run accepted a controller this binary does not have")
	}
	if err := run(nil); err == nil {
		t.Fatal("run accepted an empty --controllers")
	}
}

func TestOperationsNeedsItsSettings(t *testing.T) {
	err := run([]string{"--controllers=operations"})
	if err == nil || !strings.Contains(err.Error(), "the controller operations needs --job-namespace") {
		t.Fatalf("run started the controller operations without its flags: %v", err)
	}
	good := operations.Settings{JobNamespace: "machine-ops", RunnerImage: "registry.example/machine-runner@sha256:0", EvidenceEndpoint: "https://s3.example",
		EvidenceRegion: "region-1", EvidenceBucket: "machine-evidence", Retention: 720 * time.Hour}
	if err := checkSettings(good); err != nil {
		t.Fatalf("checkSettings refused complete settings: %v", err)
	}
	for name, bad := range map[string]func(s operations.Settings) operations.Settings{
		"no job namespace": func(s operations.Settings) operations.Settings { s.JobNamespace = ""; return s },
		"no runner image":  func(s operations.Settings) operations.Settings { s.RunnerImage = ""; return s },
		"no endpoint":      func(s operations.Settings) operations.Settings { s.EvidenceEndpoint = ""; return s },
		"plain http":       func(s operations.Settings) operations.Settings { s.EvidenceEndpoint = "http://s3.example"; return s },
		"no region":        func(s operations.Settings) operations.Settings { s.EvidenceRegion = ""; return s },
		"no bucket":        func(s operations.Settings) operations.Settings { s.EvidenceBucket = ""; return s },
		"no retention":     func(s operations.Settings) operations.Settings { s.Retention = 0; return s },
	} {
		if checkSettings(bad(good)) == nil {
			t.Errorf("checkSettings accepted settings with %s", name)
		}
	}
}
