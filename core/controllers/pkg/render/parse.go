package render

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// ParseHelmRelease decodes a rendered HelmRelease document (Result.HelmReleaseYAML)
// into an unstructured object so the caller can label it and commit it into a
// per-region file (ADR-0015 D1). It rejects anything that is not a single
// HelmRelease. Integers decode as int64 (unstructured DeepCopy-safe).
func ParseHelmRelease(doc []byte) (*unstructured.Unstructured, error) {
	js, err := yaml.YAMLToJSON(doc)
	if err != nil {
		return nil, fmt.Errorf("parse rendered HelmRelease: %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(js); err != nil {
		return nil, fmt.Errorf("parse rendered HelmRelease: %w", err)
	}
	if u.GetKind() != "HelmRelease" || u.GetName() == "" {
		return nil, fmt.Errorf("parse rendered HelmRelease: got kind %q name %q", u.GetKind(), u.GetName())
	}
	return u, nil
}
