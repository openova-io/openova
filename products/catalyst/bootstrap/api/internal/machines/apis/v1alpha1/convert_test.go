package v1alpha1

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMachineRoundTrip(t *testing.T) {
	seen := metav1.NewTime(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC).Local()) // a decoded time is local
	in := &Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "cp-1", Namespace: "catalyst-infra", Labels: map[string]string{LabelClass: ClassNode}},
		Spec:       MachineSpec{SourceRef: SourceRef{Name: "nodes"}, ExternalID: "cp-1", Class: ClassNode},
		Status: MachineStatus{
			Phase: PhaseActive, Ownership: OwnershipDeclared, OwnerRef: &OwnerRef{APIVersion: "v1", Kind: "Node", Name: "cp-1"},
			PrimaryAddress: "10.0.0.1", Addresses: []Address{{Type: "Private", IP: "10.0.0.1"}},
			OS: &OS{Family: "ubuntu", Version: "22.04"}, FirstSeen: &seen, LastSeen: &seen, LastEnumerationID: "01M8RDA4M0",
		},
	}
	u, err := MachineToUnstructured(in)
	if err != nil {
		t.Fatal(err)
	}
	if u.GetAPIVersion() != "machines.openova.io/v1alpha1" || u.GetKind() != "Machine" {
		t.Fatalf("apiVersion %q kind %q", u.GetAPIVersion(), u.GetKind())
	}
	if got, _, _ := unstructuredString(u.Object, "spec", "externalId"); got != "cp-1" {
		t.Fatalf("spec.externalId = %q", got)
	}
	out, err := MachineFromUnstructured(u)
	if err != nil {
		t.Fatal(err)
	}
	in.TypeMeta = out.TypeMeta
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the Machine:\n in %+v\nout %+v", in, out)
	}
	u.SetKind("MachineSource")
	if _, err := MachineFromUnstructured(u); err == nil {
		t.Fatal("a MachineSource was converted as a Machine")
	}
	if _, err := MachineFromUnstructured(nil); err == nil {
		t.Fatal("nil was converted")
	}
}

func TestMachineSourceRoundTrip(t *testing.T) {
	percent := int32(30)
	in := &MachineSource{
		ObjectMeta: metav1.ObjectMeta{Name: "nodes", Namespace: "catalyst-infra"},
		Spec:       MachineSourceSpec{Type: SourceTypeNodes, Stale: &StaleSpec{HoldPercent: &percent, RetireAfter: "24h"}},
		Status:     MachineSourceStatus{Held: &Held{EnumerationID: "E1", WouldMarkStale: 10, Live: 40, Since: metav1.NewTime(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC).Local())}},
	}
	u, err := MachineSourceToUnstructured(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MachineSourceFromUnstructured(u)
	if err != nil {
		t.Fatal(err)
	}
	in.TypeMeta = out.TypeMeta
	if !reflect.DeepEqual(in, out) || out.Kind != "MachineSource" {
		t.Fatalf("round trip changed the MachineSource:\n in %+v\nout %+v", in, out)
	}
	u.SetAPIVersion("machines.openova.io/v1")
	if _, err := MachineSourceFromUnstructured(u); err == nil {
		t.Fatal("another version was converted")
	}
}

func unstructuredString(obj map[string]interface{}, path ...string) (string, bool, error) {
	var cur interface{} = obj
	for _, p := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur = m[p]
	}
	s, ok := cur.(string)
	return s, ok, nil
}
