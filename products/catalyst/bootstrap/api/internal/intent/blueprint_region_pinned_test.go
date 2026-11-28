package intent

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The Blueprint definition types spec.regionPinned as a boolean, so a
// Blueprint cannot declare the pin in a form the API's readers would not see
// (they read only the boolean true).
func TestValidate_BlueprintRegionPinnedIsBoolean(t *testing.T) {
	good := readSample(t, "blueprint-sample-valid.yaml")
	if err := unstructured.SetNestedField(good.Object, true, "spec", "regionPinned"); err != nil {
		t.Fatal(err)
	}
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("regionPinned: true: %v", errs)
	}
	bad := good.DeepCopy()
	if err := unstructured.SetNestedField(bad.Object, "yes", "spec", "regionPinned"); err != nil {
		t.Fatal(err)
	}
	errs := Validate(bad)
	if len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "spec.regionPinned") {
		t.Fatalf("regionPinned: \"yes\": %v, want an error naming spec.regionPinned", errs)
	}
}
