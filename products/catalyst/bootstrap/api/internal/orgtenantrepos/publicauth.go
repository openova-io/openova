package orgtenantrepos

// The evidence of step 06's strip.
//
// Every block authenticates with the Secret its secretRef names
// (PublicAuthSecretName in TrustSecretNamespace). On the public catalog that
// needs the auth of the catalog's host, ghcr.io. Step 06
// (helmrepository-patches) adds the Sovereign registry's auth to that Secret in
// Phase 0, writes the trust Secret in Phase 0.5, and strips the public
// catalog's auth in Phase 3b (the chart's harbor.mothershipAuthsToStrip). The
// step can still fail after the strip (sync_secondary_ghcr_pull, a secondary
// region that cannot be reached), and a re-run or a restart of the engine
// blanks its status row: from the strip on only the Sovereign's own source
// works, whatever the row says ("no auth config for 'ghcr.io' in the
// docker-registry Secret 'ghcr-pull'" otherwise, hw139). Before the strip the
// public catalog works, until step 08 blocks egress.

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

// PublicAuthSecretName is the Secret the blocks' secretRef names (Template),
// the chart's harbor.ghcrSecretRef that step 06 adds to and strips
// (publicauth_test.go fails when the two differ).
const PublicAuthSecretName = "ghcr-pull"

// PublicAuthFunc reports whether the blocks' Secret still holds the public
// catalog's auth, and the error of the lookup.
type PublicAuthFunc func(context.Context) (bool, error)

// PublicCatalogHost is the host of GHCRBase, the auths key step 06 strips.
func PublicCatalogHost() string {
	return strings.SplitN(strings.TrimPrefix(GHCRBase, "oci://"), "/", 2)[0]
}

// PublicAuth is the lookup against core: the dockerconfigjson of
// PublicAuthSecretName has an auths entry for PublicCatalogHost, the test the
// strip itself makes (`.auths | has($h)`). No Secret, or no dockerconfigjson
// in it, holds none.
func PublicAuth(ctx context.Context, core kubernetes.Interface) (bool, error) {
	s, err := core.CoreV1().Secrets(TrustSecretNamespace).Get(ctx, PublicAuthSecretName, metav1.GetOptions{})
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
		return false, fmt.Errorf("%s/%s %s: %w", TrustSecretNamespace, PublicAuthSecretName, corev1.DockerConfigJsonKey, err)
	}
	_, held := cfg.Auths[PublicCatalogHost()]
	return held, nil
}

// PublicAuthInCluster is PublicAuth against the cluster this process runs in.
func PublicAuthInCluster(ctx context.Context) (bool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return false, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, err
	}
	return PublicAuth(ctx, core)
}
