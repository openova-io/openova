// The network registry of a Sovereign reaches the cluster of every region (SOV-175).
//
// The output network_registry of the provider module exists only in the
// OpenTofu state of the deployment, on the parent. When a deployment is handed
// over, the step that applies the adoption claims from that state (handler file
// post_handover_adoption_apply.go) calls DeliverNetworkRegistry, which writes
// the output into the ConfigMap flux-system/sovereign-network-registry of each
// region of the deployment that the parent's cluster cache (k8scache) holds:
// the cluster <deploymentID> (the primary) and every <deploymentID>-<regionKey>.
// The cache's dynamic client is the parent's one channel to a child cluster,
// so this file builds no client of its own.
// The writer of the Sovereign object, `sovereign-ops sovereign-object`, runs
// in every region and copies the document into status.network. Nothing of it
// passes through the user data of a node.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// The ConfigMap that carries the registry, and its two keys. Both key names
// are Flux substitution names: the Kustomization of the writer substitutes the
// revision into the name of the writer's Job, so a new registry runs the
// writer again.
const (
	NetworkRegistryNamespace   = "flux-system"
	NetworkRegistryName        = "sovereign-network-registry"
	NetworkRegistryKey         = "NETWORK_REGISTRY_JSON"
	NetworkRegistryRevisionKey = "NETWORK_REGISTRY_REVISION"
	networkRegistryManager     = "catalyst-api/network-registry"
)

// ErrNoNetworkRegistry: the state was written by a module without the output.
var ErrNoNetworkRegistry = errors.New("the OpenTofu state holds no output network_registry")

// NetworkRegistryFromState returns the value of the output network_registry of
// an OpenTofu state file: the registry as JSON text, exactly as the module
// wrote it.
func NetworkRegistryFromState(state []byte) (string, error) {
	var st struct {
		Outputs map[string]struct {
			Value json.RawMessage `json:"value"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(state, &st); err != nil {
		return "", fmt.Errorf("read the OpenTofu state: %w", err)
	}
	out, ok := st.Outputs["network_registry"]
	if !ok {
		return "", ErrNoNetworkRegistry
	}
	var doc string
	if err := json.Unmarshal(out.Value, &doc); err != nil {
		return "", fmt.Errorf("output network_registry is not a string: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(doc), &obj); err != nil {
		return "", fmt.Errorf("output network_registry is not a JSON object: %w", err)
	}
	return doc, nil
}

// NetworkRegistryConfigMap returns the ConfigMap that carries registry. The
// revision is the first 12 hexadecimal digits of the SHA-256 of the document.
func NetworkRegistryConfigMap(registry string) *unstructured.Unstructured {
	sum := sha256.Sum256([]byte(registry))
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      NetworkRegistryName,
			"namespace": NetworkRegistryNamespace,
			"labels":    map[string]any{"app.kubernetes.io/managed-by": "catalyst-api"},
		},
		"data": map[string]any{
			NetworkRegistryKey:         registry,
			NetworkRegistryRevisionKey: hex.EncodeToString(sum[:])[:12],
		},
	}}
}

// Clusters is the part of the parent's cluster cache (k8scache.Factory) that
// the delivery uses: the registered cluster ids and the dynamic client of one.
type Clusters interface {
	Clusters() []string
	DynamicClientFor(clusterID string) (dynamic.Interface, error)
}

// NetworkRegistryClusters returns the clusters of the deployment among ids:
// <deploymentID> first, then each <deploymentID>-<regionKey> by name.
func NetworkRegistryClusters(ids []string, deploymentID string) []string {
	if deploymentID == "" {
		return nil
	}
	var primary, others []string
	for _, id := range ids {
		switch {
		case id == deploymentID:
			primary = append(primary, id)
		case strings.HasPrefix(id, deploymentID+"-") && len(id) > len(deploymentID)+1:
			others = append(others, id)
		}
	}
	sort.Strings(others)
	return append(primary, others...)
}

// ApplyFunc writes cm with the dynamic client of one cluster.
type ApplyFunc func(ctx context.Context, dyn dynamic.Interface, cm *unstructured.Unstructured) error

// DeliverNetworkRegistry reads the registry from state and writes its
// ConfigMap into every cluster of the deployment that clusters holds. It
// returns one line per cluster, "<cluster id>: <result>", for the log. A
// cluster that fails does not stop the others.
func DeliverNetworkRegistry(ctx context.Context, state []byte, deploymentID string, clusters Clusters, apply ApplyFunc) []string {
	registry, err := NetworkRegistryFromState(state)
	if err != nil {
		return []string{"not delivered: " + err.Error()}
	}
	if clusters == nil || (reflect.ValueOf(clusters).Kind() == reflect.Ptr && reflect.ValueOf(clusters).IsNil()) {
		return []string{"not delivered: the parent has no cluster cache"}
	}
	ids := NetworkRegistryClusters(clusters.Clusters(), deploymentID)
	if len(ids) == 0 {
		return []string{"not delivered: the cluster cache holds no cluster of deployment " + deploymentID}
	}
	cm := NetworkRegistryConfigMap(registry)
	rev := cm.Object["data"].(map[string]any)[NetworkRegistryRevisionKey]
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		dyn, err := clusters.DynamicClientFor(id)
		if err == nil {
			err = apply(ctx, dyn, cm.DeepCopy())
		}
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		out = append(out, fmt.Sprintf("%s: revision %s", id, rev))
	}
	return out
}

// ApplyConfigMap server-side-applies cm with dyn.
func ApplyConfigMap(ctx context.Context, dyn dynamic.Interface, cm *unstructured.Unstructured) error {
	body, err := cm.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(cm.GetNamespace()).
		Patch(ctx, cm.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: networkRegistryManager, Force: &force})
	return err
}
