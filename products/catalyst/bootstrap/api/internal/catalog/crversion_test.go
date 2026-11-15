package catalog

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func cr(name, version string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"version": version},
	}}
}

// #7922: a listed row takes its Blueprint CR's version; a row no CR names, an
// empty CR version and a failed list keep the embedded one; the embedded
// catalogue is never changed.
func TestWithCRVersions_7922(t *testing.T) {
	listed, err := ListedBlueprints()
	if err != nil || len(listed) < 2 {
		t.Fatalf("ListedBlueprints: %d rows, %v", len(listed), err)
	}
	first, second := listed[0], listed[1]
	embeddedFirst := deref(first.Version)

	WithCRVersions(listed)([]unstructured.Unstructured{cr(first.ID, "9.9.9-kit"), cr(second.ID, "  ")}, nil)
	if got := deref(listed[0].Version); got != "9.9.9-kit" {
		t.Errorf("%s: version %q, want the CR's 9.9.9-kit", first.ID, got)
	}
	if got := deref(listed[1].Version); got != deref(second.Version) {
		t.Errorf("%s: an empty CR version replaced the embedded %q with %q", second.ID, deref(second.Version), got)
	}

	again, _ := ListedBlueprints()
	if got := deref(again[0].Version); got != embeddedFirst {
		t.Errorf("the embedded catalogue changed: %s is %q, was %q", first.ID, got, embeddedFirst)
	}
	WithCRVersions(again)([]unstructured.Unstructured{cr(first.ID, "9.9.9-kit")}, errors.New("no Blueprint CRD"))
	if got := deref(again[0].Version); got != embeddedFirst {
		t.Errorf("a failed list changed %s to %q", first.ID, got)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
