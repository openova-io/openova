package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MachineFromUnstructured converts an unstructured object of kind Machine.
func MachineFromUnstructured(u *unstructured.Unstructured) (*Machine, error) {
	m := &Machine{}
	if err := fromUnstructured(u, MachineKind, m); err != nil {
		return nil, err
	}
	return m, nil
}

// MachineToUnstructured converts a Machine and sets its apiVersion and kind.
func MachineToUnstructured(m *Machine) (*unstructured.Unstructured, error) {
	return toUnstructured(m, MachineKind)
}

// MachineSourceFromUnstructured converts an unstructured object of kind MachineSource.
func MachineSourceFromUnstructured(u *unstructured.Unstructured) (*MachineSource, error) {
	s := &MachineSource{}
	if err := fromUnstructured(u, MachineSourceKind, s); err != nil {
		return nil, err
	}
	return s, nil
}

// MachineSourceToUnstructured converts a MachineSource and sets its apiVersion and kind.
func MachineSourceToUnstructured(s *MachineSource) (*unstructured.Unstructured, error) {
	return toUnstructured(s, MachineSourceKind)
}

func fromUnstructured(u *unstructured.Unstructured, want schema.GroupVersionKind, out interface{}) error {
	if u == nil {
		return fmt.Errorf("machines: no object to convert to %s", want.Kind)
	}
	if got := u.GroupVersionKind(); got != want {
		return fmt.Errorf("machines: object %s/%s is %s, want %s", u.GetNamespace(), u.GetName(), got, want)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out); err != nil {
		return fmt.Errorf("machines: convert %s %s/%s: %w", want.Kind, u.GetNamespace(), u.GetName(), err)
	}
	return nil
}

func toUnstructured(in interface{}, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(in)
	if err != nil {
		return nil, fmt.Errorf("machines: convert %s: %w", gvk.Kind, err)
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(gvk)
	return u, nil
}
