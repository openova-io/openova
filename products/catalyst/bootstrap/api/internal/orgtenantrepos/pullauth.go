package orgtenantrepos

// The evidence of step 06's Phase 0.
//
// Every block authenticates with the Secret its secretRef names
// (PullSecretName in TrustSecretNamespace). Docker/OCI auth is an exact host
// match, so a block on the Sovereign's registry needs an auths entry for that
// registry's host. Step 06 (helmrepository-patches) merges it in Phase 0
// (keyed on the bare host registry.<fqdn>, #4682), before Phase 0.5 writes the
// trust Secret and before Phase 2b pivots this file in Git; Phase 3b later
// strips the public catalog's auth (ghcr.io) from the same Secret and leaves
// the Phase 0 entry in place. The entry therefore says from Phase 0 on, also
// while the step still runs, after it failed or after a re-run blanked its
// status row, that the blocks can authenticate to the Sovereign's registry.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// PullSecretName is the Secret the blocks' secretRef names (Template), the
// chart's harbor.ghcrSecretRef that step 06's Phase 0 merges the registry's
// auth into (pullauth_test.go fails when the two differ).
const PullSecretName = "ghcr-pull"

// PullAuthFunc reports whether the blocks' Secret holds an auth for host, and
// the error of the lookup.
type PullAuthFunc func(ctx context.Context, host string) (bool, error)

// OwnRegistryHost is the host of the url the blocks declare on the Sovereign's
// own registry (Base with usable), the auths key a block there needs; "" when
// Base has no own registry to give (no fqdn).
func OwnRegistryHost(env func(string) string) string {
	base := Base(env, true)
	if base == GHCRBase {
		return ""
	}
	return strings.SplitN(strings.TrimPrefix(base, "oci://"), "/", 2)[0]
}

// PullAuth is the lookup against core: the dockerconfigjson of PullSecretName
// has an auths entry for host, the entry step 06's Phase 0 adds
// (`.auths[$h] = ...`). No Secret, or no dockerconfigjson in it, holds none.
func PullAuth(ctx context.Context, core kubernetes.Interface, host string) (bool, error) {
	s, err := core.CoreV1().Secrets(TrustSecretNamespace).Get(ctx, PullSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	raw, ok := s.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return false, nil
	}
	var cfg struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, fmt.Errorf("%s/%s %s: %w", TrustSecretNamespace, PullSecretName, corev1.DockerConfigJsonKey, err)
	}
	_, held := cfg.Auths[host]
	return held, nil
}

// PullAuthInCluster is PullAuth against the cluster this process runs in.
func PullAuthInCluster(ctx context.Context, host string) (bool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return false, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, err
	}
	return PullAuth(ctx, core, host)
}
