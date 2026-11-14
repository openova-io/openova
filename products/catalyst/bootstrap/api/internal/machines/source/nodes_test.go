package source

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func testNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{ProviderID: "k3s://" + name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeHostName, Address: name},
				{Type: corev1.NodeExternalIP, Address: "203.0.113.7"},
				{Type: corev1.NodeInternalIP, Address: "10.0.0.7"},
				{Type: corev1.NodeInternalIP, Address: "10.0.1.7"},
			},
			NodeInfo: corev1.NodeSystemInfo{
				MachineID:     "m-" + name,
				SystemUUID:    "u-" + name,
				OSImage:       "Ubuntu 22.04.4 LTS",
				KernelVersion: "5.15.0-76-generic",
				Architecture:  "amd64",
			},
		},
	}
}

func TestNodeSighting(t *testing.T) {
	got := NodeSighting(testNode("cp-1"))
	want := Sighting{
		ExternalID:  "cp-1",
		DisplayName: "cp-1",
		Class:       ClassNode,
		PowerState:  PowerUnknown,
		ProviderID:  "k3s://cp-1",
		Addresses: []Address{
			{Type: "Public", IP: "203.0.113.7"},
			{Type: "Private", IP: "10.0.0.7"},
			{Type: "Private", IP: "10.0.1.7"},
		},
		Fingerprint: Fingerprint{FirmwareUUID: "u-cp-1", MachineID: "m-cp-1"},
		OSFamily:    "ubuntu",
		OSVersion:   "22.04",
		Arch:        "x86_64",
		Kernel:      "5.15.0-76-generic",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeSighting:\n got %+v\nwant %+v", got, want)
	}
	if p := PrimaryAddress(got.Addresses); p != "10.0.0.7" {
		t.Fatalf("PrimaryAddress = %q, want the first InternalIP 10.0.0.7", p)
	}
	if p := PrimaryAddress([]Address{{Type: "Public", IP: "203.0.113.7"}}); p != "" {
		t.Fatalf("PrimaryAddress without a Private address = %q, want empty", p)
	}
}

func TestParseOSImage(t *testing.T) {
	for _, c := range []struct{ image, family, version string }{
		{"Ubuntu 22.04.4 LTS", "ubuntu", "22.04"},
		{"Ubuntu 24.04 LTS", "ubuntu", "24.04"},
		{"Debian GNU/Linux 12 (bookworm)", "debian", "12"},
		{"Red Hat Enterprise Linux 9.2 (Plow)", "rhel", "9.2"},
		{"Rocky Linux 9.3 (Blue Onyx)", "rocky", "9.3"},
		{"AlmaLinux 9.4 (Seafoam Ocelot)", "almalinux", "9.4"},
		{"Oracle Linux Server 8.9", "oraclelinux", "8.9"},
		{"SUSE Linux Enterprise Server 15 SP5", "sles", "15"},
		{"Windows Server 2022 Datacenter", "windows", "2022"},
		{"Ubuntu", "unknown", ""},
		{"Ubuntu noble", "unknown", ""},
		{"Huawei Cloud EulerOS 2.0 (x86_64)", "unknown", ""},
		{"", "unknown", ""},
	} {
		f, v := ParseOSImage(c.image)
		if f != c.family || v != c.version {
			t.Errorf("ParseOSImage(%q) = %q, %q; want %q, %q", c.image, f, v, c.family, c.version)
		}
	}
}

func TestArch(t *testing.T) {
	for in, want := range map[string]string{"amd64": "x86_64", "arm64": "aarch64", "s390x": "s390x", "": ""} {
		if got := Arch(in); got != want {
			t.Errorf("Arch(%q) = %q, want %q", in, got, want)
		}
	}
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNodesEnumerate(t *testing.T) {
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(testNode("w-2"), testNode("cp-1"), testNode("w-1")).Build()
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	n := &Nodes{Reader: reader, Now: func() time.Time { return at }}
	if n.Type() != "nodes" {
		t.Fatalf("Type = %q", n.Type())
	}
	e, err := n.Enumerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !e.Complete || len(e.Scopes) != 1 || e.Scopes[0] != (ScopeResult{Name: "nodes", Readable: true, Count: 3}) {
		t.Fatalf("enumeration not complete with one readable scope of 3: %+v", e)
	}
	var ids []string
	for _, s := range e.Sightings {
		ids = append(ids, s.ExternalID)
	}
	if !reflect.DeepEqual(ids, []string{"cp-1", "w-1", "w-2"}) {
		t.Fatalf("sightings %v, want cp-1, w-1, w-2", ids)
	}
	if !e.StartedAt.Equal(at) || !e.FinishedAt.Equal(at) || e.ID != "" {
		t.Fatalf("times %v %v or id %q not as set", e.StartedAt, e.FinishedAt, e.ID)
	}
}

func TestNodesEnumerateListFails(t *testing.T) {
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("connection refused")
		},
	}).Build()
	e, err := (&Nodes{Reader: reader}).Enumerate(context.Background())
	if err == nil {
		t.Fatal("a failed list must be an error")
	}
	if e.Complete || len(e.Sightings) != 0 {
		t.Fatalf("a failed list returned %+v; it must return nothing", e)
	}
}
