// The network registry of a Sovereign reaches the cluster of every region (SOV-175).
//
// The output network_registry of the provider module exists only in the
// OpenTofu state of the deployment, on the parent. When a deployment is handed
// over, the step that applies the adoption claims from that state (handler file
// post_handover_adoption_apply.go) calls DeliverNetworkRegistry, which writes
// the output into the ConfigMap flux-system/sovereign-network-registry of each
// region whose kubeconfig the parent holds: <dir>/<deploymentID>.yaml for the
// primary and <dir>/<deploymentID>-<regionKey>.yaml for every other region.
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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
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

// NetworkRegistryKubeconfigs returns the kubeconfig file of every region of
// the deployment in dir: <deploymentID>.yaml first, then each
// <deploymentID>-<regionKey>.yaml by name. Other files of dir are not read.
func NetworkRegistryKubeconfigs(dir, deploymentID string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil || deploymentID == "" {
		return nil
	}
	var primary, others []string
	for _, e := range entries {
		n := e.Name()
		switch {
		case e.IsDir() || !strings.HasSuffix(n, ".yaml"):
		case n == deploymentID+".yaml":
			primary = append(primary, filepath.Join(dir, n))
		case strings.HasPrefix(n, deploymentID+"-") && len(n) > len(deploymentID)+len("-.yaml"):
			others = append(others, filepath.Join(dir, n))
		}
	}
	sort.Strings(others)
	return append(primary, others...)
}

// ApplyFunc writes cm into the cluster of one kubeconfig.
type ApplyFunc func(ctx context.Context, kubeconfig []byte, cm *unstructured.Unstructured) error

// DeliverNetworkRegistry reads the registry from state and writes its
// ConfigMap with every kubeconfig NetworkRegistryKubeconfigs finds. It
// returns one line per region, "<file>: <result>", for the log. A region that
// fails does not stop the others.
func DeliverNetworkRegistry(ctx context.Context, state []byte, dir, deploymentID string, apply ApplyFunc) []string {
	registry, err := NetworkRegistryFromState(state)
	if err != nil {
		return []string{"not delivered: " + err.Error()}
	}
	files := NetworkRegistryKubeconfigs(dir, deploymentID)
	if len(files) == 0 {
		return []string{"not delivered: no kubeconfig of deployment " + deploymentID + " in " + dir}
	}
	cm := NetworkRegistryConfigMap(registry)
	rev := cm.Object["data"].(map[string]any)[NetworkRegistryRevisionKey]
	out := make([]string, 0, len(files))
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Clean(f))
		if err == nil {
			err = apply(ctx, raw, cm.DeepCopy())
		}
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", filepath.Base(f), err))
			continue
		}
		out = append(out, fmt.Sprintf("%s: revision %s", filepath.Base(f), rev))
	}
	return out
}

// ApplyConfigMap server-side-applies cm with the cluster of kubeconfig.
func ApplyConfigMap(ctx context.Context, kubeconfig []byte, cm *unstructured.Unstructured) error {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return err
	}
	cfg.Timeout = 30 * time.Second
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	body, err := cm.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(cm.GetNamespace()).
		Patch(ctx, cm.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: networkRegistryManager, Force: &force})
	return err
}
