package intent

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestOrganizationSampleReservedSlug checks the sample
// organization-sample-reserved-slug.yaml. Its one error is its slug, mgmt,
// which the not.enum list of spec.slug reserves: the schema refuses it on
// spec.slug, and accepts the same object with the slug acme-co.
func TestOrganizationSampleReservedSlug(t *testing.T) {
	obj := readSample(t, "organization-sample-reserved-slug.yaml")
	errs := Validate(obj)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `"spec.slug" must not validate the schema (not)`) {
		t.Fatalf("the sample with the slug mgmt: want one error, the not rule of spec.slug, got %v", errs)
	}
	ok := obj.DeepCopy()
	if err := unstructured.SetNestedField(ok.Object, "acme-co", "spec", "slug"); err != nil {
		t.Fatal(err)
	}
	if errs := Validate(ok); len(errs) != 0 {
		t.Fatalf("the sample with the slug acme-co: %v", errs)
	}
}
