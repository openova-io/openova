// Package regions resolves an API client for a PEER region of a
// two-region Sovereign, for the cross-region cnpg-pair switchover
// (switchover/crossregion.go).
//
// NO NEW CREDENTIAL CONTRACT. The continuum-controller runs in region A
// only. Region B's API server is reached through the one artefact every
// cross-region consumer already shares — catalyst-api's #5359 bridge
// Secret:
//
//	Secret catalyst/cutover-secondary-kubeconfigs
//	  data: "<regionKey>.yaml" -> kubeconfig bytes (one key per secondary)
//
// the same Secret the self-sovereign-cutover secondary legs mount as
// /secondary-kubeconfigs/<regionKey>.yaml and the organization-controller
// reads for its per-region console fan-out (#5246). catalyst-api keeps it
// materialized on every multi-region Sovereign (#6015/#6027 level-
// triggered materializer), cutover or not.
//
// A Continuum names regions by their canonical label
// (`hw-me-east-215-b-rtz-prod`) while the Secret keys them by region key
// (`me-east-215-b-1`). The match is pkg/kubeconfig.RegionDeclared — the
// SAME attribution rule the delivery contract uses — so the two can never
// disagree about which document belongs to which region.
//
// The controller's chart already grants `secrets: get` (ClusterRole), and
// the Secret is read through an UNCACHED reader (no list/watch needed).
package regions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openova-io/openova/core/controllers/pkg/kubeconfig"
)

// Defaults — overridable via env in cmd/main.go (Inviolable Principle #4).
const (
	DefaultSecretNamespace = "catalyst"
	DefaultSecretName      = "cutover-secondary-kubeconfigs"
	// clientTimeout bounds every call through a peer region's client so
	// a wedged peer apiserver degrades a switchover step into an error,
	// never a hang.
	clientTimeout = 20 * time.Second
)

// SecretData returns the data map of Secret namespace/name.
type SecretData func(ctx context.Context, namespace, name string) (map[string][]byte, error)

// NewDynamic builds a dynamic client from kubeconfig bytes. Swappable
// for tests.
type NewDynamic func(kubeconfigBytes []byte) (dynamic.Interface, error)

// SecretResolver implements switchover.RegionClients over the bridge
// Secret.
type SecretResolver struct {
	Namespace string
	Name      string
	Read      SecretData
	Build     NewDynamic
}

// DefaultBuild builds a dynamic client from kubeconfig bytes with a
// bounded request timeout.
func DefaultBuild(raw []byte) (dynamic.Interface, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = clientTimeout
	return dynamic.NewForConfig(cfg)
}

// ClientFor returns a dynamic client for the region whose canonical
// label is region. Errors name the Secret and the keys it holds so an
// operator can see WHY region B is unreachable.
func (r *SecretResolver) ClientFor(ctx context.Context, region string) (dynamic.Interface, error) {
	if r == nil || r.Read == nil {
		return nil, errors.New("regions: no secret reader wired")
	}
	ns, name := r.Namespace, r.Name
	if ns == "" {
		ns = DefaultSecretNamespace
	}
	if name == "" {
		name = DefaultSecretName
	}
	data, err := r.Read(ctx, ns, name)
	if err != nil {
		return nil, fmt.Errorf("read peer-region kubeconfig Secret %s/%s: %w", ns, name, err)
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var match []string
	for _, k := range keys {
		regionKey := strings.TrimSuffix(k, ".yaml")
		if regionKey == k {
			continue
		}
		if kubeconfig.RegionDeclared(regionKey, []string{region}) {
			match = append(match, k)
		}
	}
	switch len(match) {
	case 0:
		return nil, fmt.Errorf("no kubeconfig for region %q in Secret %s/%s (keys: %v)", region, ns, name, keys)
	case 1:
	default:
		return nil, fmt.Errorf("ambiguous kubeconfig for region %q in Secret %s/%s: %v", region, ns, name, match)
	}
	raw := string(data[match[0]])
	if defects := kubeconfig.Defects(raw); len(defects) > 0 {
		return nil, fmt.Errorf("kubeconfig %s in Secret %s/%s is unusable: %s", match[0], ns, name, kubeconfig.DescribeDefects(defects))
	}
	build := r.Build
	if build == nil {
		build = DefaultBuild
	}
	dyn, err := build([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("build client from %s in Secret %s/%s: %w", match[0], ns, name, err)
	}
	return dyn, nil
}
