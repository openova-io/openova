package meshstatus

import (
	"context"
	"os"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The objects DialAddress reads.
const (
	// ProxyName names the DaemonSet of bp-cilium that answers the mesh dial
	// port on the host network of every node (chart value
	// cilium.clustermeshProxy). It exists where the address of the Service
	// clustermesh-apiserver is a public address of the control-plane node.
	ProxyName = "clustermesh-proxy"
	// ControlPlaneLabel marks a control-plane node.
	ControlPlaneLabel = "node-role.kubernetes.io/control-plane"
	// DialAddressEnv selects the address the agents of a peer region dial.
	// "load-balancer" keeps the address of the Service clustermesh-apiserver
	// in every case. Unset, empty or any other value gives the default.
	DialAddressEnv = "CATALYST_CLUSTERMESH_DIAL_ADDRESS"
	// DialLoadBalancer is the value of DialAddressEnv described above.
	DialLoadBalancer = "load-balancer"
)

// DialAddress returns the address that the agents of the OTHER regions dial
// to reach the mesh endpoint of the region behind client. lbIP is the address
// of that region's Service clustermesh-apiserver.
//
// A region that runs the DaemonSet clustermesh-proxy answers the dial port on
// every node's own address. There the result is the internal address of the
// region's first control-plane node by name: the same node the public address
// leads to, reached over the private network between the regions. That path
// is admitted by a rule on the regions' own address ranges, which are fixed
// when the Sovereign is created. The public path is admitted per source
// address, and a public source address of a region can be replaced after the
// rules were written.
//
// The result is lbIP when the region has no such DaemonSet, has no
// control-plane node with an internal address, or DialAddressEnv says so. It
// is "" when the region cannot be read: an address that was not read is not
// replaced by a guess, and the caller leaves the peer out of this pass.
func DialAddress(ctx context.Context, client kubernetes.Interface, lbIP string) string {
	if client == nil || os.Getenv(DialAddressEnv) == DialLoadBalancer {
		return lbIP
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if _, err := client.AppsV1().DaemonSets(Namespace).Get(callCtx, ProxyName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return lbIP
		}
		return ""
	}
	nodes, err := client.CoreV1().Nodes().List(callCtx, metav1.ListOptions{LabelSelector: ControlPlaneLabel})
	if err != nil {
		return ""
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
				return addr.Address
			}
		}
	}
	return lbIP
}
