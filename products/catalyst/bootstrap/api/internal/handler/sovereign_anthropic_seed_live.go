package handler

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// #6163 FREEZE 1 — the operator's Anthropic credential, read LIVE.
//
// The catalyst-api receives this credential as secretKeyRef env vars, and a
// container's environment is materialised once at container start. That made
// the ten-minute seed loop re-apply the boot-time snapshot forever: an operator
// who rotated catalyst-system/sovereign-anthropic-credentials saw nothing
// change until the pod rolled, which nothing triggers. Since the seeded blob is
// a claudeAiOauth pair whose accessToken lives hours, the outage re-created
// itself on a timer while the loop meant to heal it re-applied the stale value.
//
// This reads the Secret the operator actually edits, on every pass.
const (
	anthropicCredentialNamespace = "catalyst-system"
	anthropicCredentialSecret    = "sovereign-anthropic-credentials"
	anthropicCredentialAPIKeyKey = "apiKey"
	anthropicCredentialJSONKey   = "credentialsJson"
	anthropicCredentialReadBudget = 5 * time.Second
)

// anthropicSecretClientFor is the seam the production path wires to an
// in-cluster clientset. Tests override it; a nil return means "no in-cluster
// identity" and the caller falls back to the process env, which is exactly the
// pre-#6163 behaviour.
var anthropicSecretClientFor = func() (kubernetes.Interface, error) {
	cfg, err := inClusterConfigForMaterialize()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// anthropicCredentialFromSecret returns (apiKey, credentialsJSON) read live from
// catalyst-system/sovereign-anthropic-credentials.
//
// Every failure path returns ("", "") rather than an error: this is a FALLBACK
// SOURCE, not a gate. A Sovereign that never adopted the Secret, a Catalyst-Zero
// process with no in-cluster identity, and a transient apiserver blip must all
// leave the caller on its previous env-based behaviour instead of turning a
// working install into a credential gap.
func anthropicCredentialFromSecret() (string, string) {
	cli, err := anthropicSecretClientFor()
	if err != nil || cli == nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), anthropicCredentialReadBudget)
	defer cancel()

	sec, err := cli.CoreV1().Secrets(anthropicCredentialNamespace).
		Get(ctx, anthropicCredentialSecret, metav1.GetOptions{})
	if err != nil || sec == nil {
		return "", ""
	}
	// Read on the VALUE, never on the key: a present-but-empty key is a
	// credential gap, not a credential, and returning "" for it lets the env
	// fallback answer instead of seeding an empty path that pretends to work.
	return strings.TrimSpace(string(sec.Data[anthropicCredentialAPIKeyKey])),
		strings.TrimSpace(string(sec.Data[anthropicCredentialJSONKey]))
}
