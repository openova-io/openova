// Kubeconfig → client constructors.
//
// Production wires NewDynamicClientFromKubeconfig and
// NewKubernetesClientFromKubeconfig as the Config.DynamicFactory /
// Config.CoreFactory; tests inject closures that return a
// fake.NewSimpleDynamicClient / fake.NewSimpleClientset so no real
// cluster is needed.
package helmwatch

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// NewDynamicClientFromKubeconfig builds a dynamic.Interface from raw
// kubeconfig YAML. The kubeconfig is the new Sovereign cluster's
// k3s.yaml (rewritten with the load-balancer's public IP — the
// in-VM 127.0.0.1 server URL is invariant the fetcher must rewrite,
// but that rewrite happens upstream in the Phase-0 fetch step, NOT
// here).
func NewDynamicClientFromKubeconfig(kubeconfigYAML string) (dynamic.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic.NewForConfig: %w", err)
	}
	return dyn, nil
}

// NewKubernetesClientFromKubeconfig builds a typed kubernetes.Interface
// from raw kubeconfig YAML. Used for Pod listing + log tailing on
// helm-controller in flux-system.
func NewKubernetesClientFromKubeconfig(kubeconfigYAML string) (kubernetes.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes.NewForConfig: %w", err)
	}
	return clientset, nil
}
