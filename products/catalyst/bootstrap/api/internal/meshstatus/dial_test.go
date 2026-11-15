package meshstatus

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func meshProxy() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: ProxyName, Namespace: Namespace}}
}

func node(name string, controlPlane bool, addrs ...corev1.NodeAddress) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if controlPlane {
		n.Labels[ControlPlaneLabel] = "true"
	}
	n.Status.Addresses = addrs
	return n
}

func internal(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip}
}

func external(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: ip}
}

// The address a peer dials. The public address of a region is "198.51.100.7".
func TestDialAddress(t *testing.T) {
	const public = "198.51.100.7"
	cases := []struct {
		name string
		env  string
		objs []runtime.Object
		want string
	}{
		{
			name: "a region with the host-network proxy is dialled at the internal address of its control-plane node",
			objs: []runtime.Object{meshProxy(),
				node("w1", false, internal("10.0.1.9")),
				node("cp1", true, external(public), internal("10.0.1.5"))},
			want: "10.0.1.5",
		},
		{
			name: "of several control-plane nodes the first by name is taken, whatever the order of the list",
			objs: []runtime.Object{meshProxy(),
				node("cp2", true, internal("10.0.1.6")), node("cp1", true, internal("10.0.1.5"))},
			want: "10.0.1.5",
		},
		{
			name: "a region without the proxy keeps the address of its Service",
			objs: []runtime.Object{node("cp1", true, internal("10.0.1.5"))},
			want: public,
		},
		{
			name: "a region whose control-plane node has no internal address keeps the address of its Service",
			objs: []runtime.Object{meshProxy(), node("cp1", true, external(public)), node("w1", false, internal("10.0.1.9"))},
			want: public,
		},
		{
			name: "the environment variable keeps the address of the Service",
			env:  DialLoadBalancer,
			objs: []runtime.Object{meshProxy(), node("cp1", true, internal("10.0.1.5"))},
			want: public,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DialAddressEnv, tc.env)
			if got := DialAddress(context.Background(), cluster(tc.objs...), public); got != tc.want {
				t.Fatalf("DialAddress = %q, want %q", got, tc.want)
			}
		})
	}
	if got := DialAddress(context.Background(), nil, public); got != public {
		t.Fatalf("without a client DialAddress = %q, want %q", got, public)
	}
}

// A region that cannot be read gives no address: neither the public one nor a
// guess. A changing answer would rewrite the agents' host entries and restart
// them on every pass.
func TestDialAddress_UnreadableRegionGivesNoAddress(t *testing.T) {
	for _, resource := range []string{"daemonsets", "nodes"} {
		c := cluster(meshProxy(), node("cp1", true, internal("10.0.1.5")))
		c.PrependReactor("*", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("the API server is away")
		})
		if got := DialAddress(context.Background(), c, "198.51.100.7"); got != "" {
			t.Fatalf("%s unreadable: DialAddress = %q, want no address", resource, got)
		}
	}
}
