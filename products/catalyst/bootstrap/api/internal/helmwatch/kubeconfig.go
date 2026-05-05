// Kubeconfig → client constructors.
//
// Production wires NewDynamicClientFromKubeconfig and
// NewKubernetesClientFromKubeconfig as the Config.DynamicFactory /
// Config.CoreFactory; tests inject closures that return a
// fake.NewSimpleDynamicClient / fake.NewSimpleClientset so no real
// cluster is needed.
package helmwatch

import (
	"context"
	"fmt"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// restConfigFromKubeconfig parses the raw kubeconfig YAML and stamps
// a per-request Timeout on the resulting rest.Config (issue #923).
// Without this, individual List/Watch/Get calls from the informer
// have no per-request deadline — a hung TLS handshake to the
// Sovereign apiserver after a catalyst-api Pod restart can block
// for the full WatchTimeout (60m) silently. DefaultRESTConfigTimeout
// (30s) caps each REST call so client-go's internal retry loop has
// a chance to recover when the LB warms up or kube-vip stops
// flapping.
//
// Pulled out of NewDynamicClientFromKubeconfig / NewKubernetesClient-
// FromKubeconfig so the production reachability probe
// (NewReachabilityProbeFromKubeconfig) can share the same parse +
// timeout-stamping path.
func restConfigFromKubeconfig(kubeconfigYAML string) (*rest.Config, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	// Stamp the per-request timeout. clientcmd never sets one by
	// default, so a hung handshake stays hung forever (issue #923).
	cfg.Timeout = DefaultRESTConfigTimeout
	return cfg, nil
}

// NewDynamicClientFromKubeconfig builds a dynamic.Interface from raw
// kubeconfig YAML. The kubeconfig is the new Sovereign cluster's
// k3s.yaml (rewritten with the load-balancer's public IP — the
// in-VM 127.0.0.1 server URL is invariant the fetcher must rewrite,
// but that rewrite happens upstream in the Phase-0 fetch step, NOT
// here).
//
// Per issue #923 the rest.Config carries a 30s per-request timeout
// so a hung TLS handshake after Pod restart fails fast and the
// pre-flight reachability probe can retry with backoff instead of
// the informer hanging silently for the entire WatchTimeout.
func NewDynamicClientFromKubeconfig(kubeconfigYAML string) (dynamic.Interface, error) {
	cfg, err := restConfigFromKubeconfig(kubeconfigYAML)
	if err != nil {
		return nil, err
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
//
// Same rest.Config.Timeout treatment as NewDynamicClientFromKubeconfig
// — issue #923.
func NewKubernetesClientFromKubeconfig(kubeconfigYAML string) (kubernetes.Interface, error) {
	cfg, err := restConfigFromKubeconfig(kubeconfigYAML)
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes.NewForConfig: %w", err)
	}
	return clientset, nil
}

// NewReachabilityProbeFromKubeconfig is the production default for
// Config.Reachability (issue #923). It returns a closure that, on
// each call, builds a discovery client from the kubeconfig and asks
// the apiserver for its server-version. The discovery client uses
// the same rest.Config.Timeout as the rest of the helmwatch path
// (DefaultRESTConfigTimeout, 30s), so the per-attempt timeout that
// matters in the probe loop is whichever is shorter:
// rest.Config.Timeout vs the per-attempt context the caller passes.
//
// We rebuild the client per call rather than caching it: rest.Config
// caches connection pools internally, but a transient TLS handshake
// failure can leave a poisoned connection in the pool. A fresh client
// every attempt sidesteps that — the probe loop only runs at most
// ReachabilityOverallBudget (10m default), so the allocation cost is
// negligible.
//
// On success: returns nil. On any failure (parse, factory init,
// ServerVersion call): returns a wrapped error so the probe loop's
// emitted message names the failure mode. The watcher's reconnect
// loop classifies any non-nil return as "retry with backoff".
func NewReachabilityProbeFromKubeconfig(kubeconfigYAML string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cfg, err := restConfigFromKubeconfig(kubeconfigYAML)
		if err != nil {
			return fmt.Errorf("reachability: %w", err)
		}
		client, err := discovery.NewDiscoveryClientForConfig(cfg)
		if err != nil {
			return fmt.Errorf("reachability: build discovery client: %w", err)
		}
		// Use RESTClient().Get on /version so the apiserver's CA
		// validation + auth path runs end-to-end. ServerVersion()
		// uses RESTClient().AbsPath("/version") under the hood; we
		// call the same path explicitly so we can attach the
		// caller's per-attempt context for fast cancellation.
		raw := client.RESTClient().Get().AbsPath("/version").Do(ctx)
		if err := raw.Error(); err != nil {
			return fmt.Errorf("reachability: GET /version: %w", err)
		}
		return nil
	}
}
