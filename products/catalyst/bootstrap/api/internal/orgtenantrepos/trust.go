package orgtenantrepos

// The trust reference of the blocks on the Sovereign's registry.
//
// The in-cluster registry serves an LE-staging (node-untrusted) certificate on
// a fresh install, and a Flux OCI HelmRepository has no insecureSkipVerify:
// spec.certSecretRef is the only way source-controller trusts it. Cutover step
// 06 (helmrepository-patches) writes that trust Secret in Phase 0.5 and gives
// every HelmRepository it pivots a certSecretRef to it, in the live object and
// in Git (inject_trust_ref, Phase 2b for the org-tenants tree). A block this
// package writes on the Sovereign's registry after step 06 has run must carry
// the same reference, or its source fails x509 while step 08, which judges the
// url only, passes it.

import (
	"context"
	"log/slog"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TrustSecretName is the chart's helmRepositoryTrust.destSecretName
// (platform/self-sovereign-cutover/chart/values.yaml): the ca.crt-only Secret
// step 06 writes when trust is enabled (the default) and the in-cluster
// Harbor's CA is readable.
const TrustSecretName = "cutover-harbor-ca"

// TrustSecretNamespace is the namespace of the six blocks, where step 06
// writes the Secret (helmRepositories.namespace) and where Flux resolves their
// certSecretRef.
const TrustSecretNamespace = "flux-system"

// TrustFunc returns the certSecretRef name of the blocks on the Sovereign's
// registry ("" for none) and the error of the lookup, if any.
type TrustFunc func(context.Context) (string, error)

// Trust is the step-06 rule read back from the cluster: TrustSecretName when
// the Secret exists, "" when it does not (step 06 has not reached Phase 0.5,
// trust is off, or the CA was unreadable; step 06 then pivots without a
// reference too, and its Phase 2b injects one into the file once it writes the
// Secret). Any other read error returns TrustSecretName with the error, since
// trust is on by default: a reference to a Secret that appears later heals by
// itself, a block without one fails x509 until something rewrites the file.
func Trust(ctx context.Context, core kubernetes.Interface) (string, error) {
	_, err := core.CoreV1().Secrets(TrustSecretNamespace).Get(ctx, TrustSecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		return TrustSecretName, nil
	case apierrors.IsNotFound(err):
		return "", nil
	}
	return TrustSecretName, err
}

// TrustInCluster is Trust against the cluster this process runs in.
func TrustInCluster(ctx context.Context) (string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return TrustSecretName, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return TrustSecretName, err
	}
	return Trust(ctx, core)
}

// File is the shared file the writer emits: every block on Base(env, status)
// and, off the public catalog, with the certSecretRef trust returns (nil:
// TrustInCluster). The public catalog needs no reference and is never looked
// up. A lookup error is logged on log (when not nil); the reference it
// returned is kept.
func File(ctx context.Context, env func(string) string, status map[string]string, trust TrustFunc, log *slog.Logger) string {
	base := Base(env, status)
	if base == GHCRBase {
		return Template
	}
	if trust == nil {
		trust = TrustInCluster
	}
	ref, err := trust(ctx)
	if err != nil && log != nil {
		log.Warn("org-tenant: the cutover trust Secret is unreadable — the shared HelmRepositories reference it anyway",
			"secret", TrustSecretNamespace+"/"+TrustSecretName, "err", err)
	}
	return Render(base, ref)
}

// NeedsRewrite reports whether doc, a shared file in Git, has to be rewritten
// to want (File): it still names the public catalog, or want gives the blocks
// a certSecretRef doc lacks (written on the Sovereign's registry before step 06
// wrote the Secret, and missed by its Phase 2b). A reference is never removed.
func NeedsRewrite(doc []byte, want string) bool {
	return NamesPublicCatalog(doc) || trustRefs(string(doc)) < trustRefs(want)
}

func trustRefs(doc string) int {
	n := 0
	for _, line := range strings.Split(doc, "\n") {
		if strings.TrimSpace(line) == "certSecretRef:" {
			n++
		}
	}
	return n
}
