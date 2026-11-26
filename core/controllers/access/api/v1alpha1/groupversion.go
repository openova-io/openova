// Package v1alpha1 holds the Go types of the access kinds, group
// access.openova.io (ADR-0033 decision A4). The definitions are
// platform/guacamole/chart/crds/accesstarget.yaml and accessgrant.yaml; they
// arrive with the Blueprint bp-guacamole (decision A1). The types are
// hand-written, with their DeepCopy methods, as in environment/api/v1.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the group and version of both definitions.
var GroupVersion = schema.GroupVersion{Group: "access.openova.io", Version: "v1alpha1"}

var (
	// SchemeBuilder registers the access kinds.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds the access kinds to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &AccessTarget{}, &AccessTargetList{}, &AccessGrant{}, &AccessGrantList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
