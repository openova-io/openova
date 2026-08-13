package handler

// sovereign_anthropic_seed_mothership.go — #4277 / #4111.
//
// ── The hop that was never written ──────────────────────────────────
//
// Every other platform credential a fresh Sovereign needs has a producer
// with a name and a file. `powerdns_api_key`, `harbor_robot_token`,
// `ghcr_pull_token` and `hcloud_token` ride the tfvars → terraform →
// cloud-init rail; the SMTP relay credential rides the mothership-writes-
// the-Sovereign's-apiserver rail (#883, sovereign_smtp_seed.go, called
// from PutKubeconfig). The Anthropic credential had CONSUMERS at four
// layers —
//
//	catalyst-openova-kc-credentials-secret.yaml   (helm lookup, source-wins)
//	sovereign_anthropic_seed_live.go             (live Secret read, #6163)
//	api-deployment.yaml CATALYST_ANTHROPIC_*      (env, boot snapshot)
//	sovereign_anthropic_seed.go                   (OpenBao PutKVv2)
//
// — and ZERO producers. `catalyst-system/sovereign-anthropic-credentials`
// was read in two places and written in none, on any Sovereign, by any
// Go code, Helm template, script, terraform module or cloud-init
// template. The only way it ever came into existence was a human running
// `kubectl create secret` on that specific Sovereign, once per Sovereign,
// forever. The chart comment that anticipated this file
// (catalyst-openova-kc-credentials-secret.yaml, the `$anthSrc` block:
// "the operator (or a future mothership seed, mirroring #883 SMTP)")
// has been carrying the word "future" since it was written.
//
// This is that seed. It is deliberately the #883 shape rather than the
// tfvars shape: no terraform variable in two providers, no cloud-init
// substitute (Hetzner's user_data is capped at 32256 B and an OAuth blob
// is not small), and it lands in the exact Secret both existing readers
// already watch.
//
// ── What this does NOT do ───────────────────────────────────────────
//
// It does not conjure a credential. If the mothership itself holds none,
// the seed says so and skips — the same loud no-op posture as the SMTP
// seed's skipped-no-env branch and as seedAnthropicToken's ABSENT branch.
// What it changes is the SHAPE of the remaining gap: from "a human must
// run kubectl on every Sovereign that will ever exist" to "the founder
// populates one Secret on the mothership, once, and every Sovereign
// provisioned after that is seeded with no human step". That is the
// difference between a gate and a chore.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// sovereignAnthropicSeedTimeoutEnv bounds the seed's K8s calls. Same
// budget and the same reasoning as the SMTP seed: generous for first
// contact with an LB-fronted apiserver right after kubeconfig postback.
const (
	sovereignAnthropicSeedTimeoutEnv     = "CATALYST_SOVEREIGN_ANTHROPIC_SEED_TIMEOUT"
	defaultSovereignAnthropicSeedTimeout = 30 * time.Second
)

// sovereignAnthropicSeedPhase — SSE phase tag on the deployment event
// bus, alongside sovereign-smtp-seed.
const sovereignAnthropicSeedPhase = "sovereign-anthropic-seed"

// SovereignAnthropicSeedOutcome — terminal classification of one attempt.
//
// `skipped-unusable` is deliberately distinct from `skipped-no-env`.
// #6245 established that a credential which is configured and cannot
// authenticate must not be reported as a seeded one; the same rule has
// to hold one hop earlier, or the mothership ships an expired OAuth blob
// to a fresh Sovereign and the Sovereign-side classifier is left to
// discover it after the fact.
type SovereignAnthropicSeedOutcome string

const (
	SovereignAnthropicSeedOutcomeCreated         SovereignAnthropicSeedOutcome = "created"
	SovereignAnthropicSeedOutcomeAlreadyExists   SovereignAnthropicSeedOutcome = "already-exists"
	SovereignAnthropicSeedOutcomeSkippedNoEnv    SovereignAnthropicSeedOutcome = "skipped-no-env"
	SovereignAnthropicSeedOutcomeSkippedUnusable SovereignAnthropicSeedOutcome = "skipped-unusable"
	SovereignAnthropicSeedOutcomeClientFailure   SovereignAnthropicSeedOutcome = "client-failure"
	SovereignAnthropicSeedOutcomeAPIFailure      SovereignAnthropicSeedOutcome = "api-failure"
)

// SovereignAnthropicSeedClientFactory — test-only override, mirroring
// SovereignSMTPSeedClientFactory.
type SovereignAnthropicSeedClientFactory func(kubeconfigYAML string) (kubernetes.Interface, error)

// SetSovereignAnthropicSeedClientFactory wires a test-only factory.
func (h *Handler) SetSovereignAnthropicSeedClientFactory(f SovereignAnthropicSeedClientFactory) {
	h.sovereignAnthropicSeedClientFactory = f
}

// seedSovereignAnthropicCredentials creates
// catalyst-system/sovereign-anthropic-credentials on the freshly
// provisioned Sovereign from the MOTHERSHIP's own Anthropic credential.
//
// Idempotent and non-destructive in the same way as the SMTP seed: an
// existing Secret short-circuits to AlreadyExists and is never
// overwritten, so a Sovereign whose operator already rotated the
// credential by hand keeps their bytes. Rotation of a seeded Sovereign
// stays the operator's edit + the #6163 live read; this hop only closes
// the FIRST-write gap.
func (h *Handler) seedSovereignAnthropicCredentials(ctx context.Context, dep *Deployment, kubeconfigYAML string) SovereignAnthropicSeedOutcome {
	apiKey := strings.TrimSpace(os.Getenv(anthropicSeedAPIKeyEnv))
	credsJSON := strings.TrimSpace(os.Getenv(anthropicSeedCredentialsJSONEnv))

	// Classify with the SAME predicate the Sovereign-side seam uses
	// (#6245) rather than re-deriving "well, one of them is non-empty".
	// A key-only / malformed / expired credential is not a credential:
	// claude-code authenticates from the claudeAiOauth blob, so seeding
	// any of those ships a Secret that looks populated on inspection and
	// fails at first use — the precise fake-green this seam exists to
	// refuse.
	class, detail := classifyAnthropicCredential(apiKey, credsJSON)

	if class == anthropicCredAbsent {
		// 🛑 FOUNDER-SUPPLIED-SECRET GAP, now visible at PROVISION time on
		// the mothership rather than only after someone opens an Agenity
		// workspace on the new Sovereign. Skip loudly; never seed an empty
		// Secret that pretends to work.
		h.log.Error("sovereign anthropic seed: the MOTHERSHIP holds no Anthropic credential, so this Sovereign's "+
			"catalyst-system/sovereign-anthropic-credentials Secret is NOT written and every Organization's "+
			"agenity workspace on it will start without one",
			"deploymentID", dep.ID,
			"sovereignFQDN", dep.Request.SovereignFQDN,
			"class", string(class),
			"detail", detail,
			"remediation", "populate "+anthropicSeedAPIKeyEnv+" + "+anthropicSeedCredentialsJSONEnv+" on the MOTHERSHIP's "+
				"catalyst-openova-kc-credentials Secret (or catalyst-system/sovereign-anthropic-credentials on the "+
				"mothership, which source-wins into it). One edit, once — every Sovereign provisioned afterwards is "+
				"seeded with no per-Sovereign kubectl. Refs #4277.",
		)
		return SovereignAnthropicSeedOutcomeSkippedNoEnv
	}

	if !class.usable() {
		h.log.Error("sovereign anthropic seed: the mothership's Anthropic credential is present but NOT usable, "+
			"so it is not propagated to this Sovereign — shipping it would hand the Sovereign a Secret that "+
			"inspects as populated and fails at first use",
			"deploymentID", dep.ID,
			"sovereignFQDN", dep.Request.SovereignFQDN,
			"class", string(class),
			"detail", detail,
			"remediation", anthropicCredentialRemediation(class),
		)
		return SovereignAnthropicSeedOutcomeSkippedUnusable
	}

	factory := h.sovereignAnthropicSeedClientFactory
	if factory == nil {
		factory = helmwatch.NewKubernetesClientFromKubeconfig
	}
	clientset, err := factory(kubeconfigYAML)
	if err != nil {
		h.log.Error("sovereign anthropic seed: build kubernetes client from kubeconfig failed",
			"deploymentID", dep.ID, "err", err)
		return SovereignAnthropicSeedOutcomeClientFailure
	}

	timeout := defaultSovereignAnthropicSeedTimeout
	if v, _ := time.ParseDuration(os.Getenv(sovereignAnthropicSeedTimeoutEnv)); v > 0 {
		timeout = v
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Existence check first: an operator-supplied Secret always wins.
	_, getErr := clientset.CoreV1().Secrets(anthropicCredentialNamespace).
		Get(cctx, anthropicCredentialSecret, metav1.GetOptions{})
	if getErr == nil {
		h.log.Info("sovereign anthropic seed: target Secret already exists; leaving it untouched (idempotent)",
			"deploymentID", dep.ID,
			"namespace", anthropicCredentialNamespace,
			"name", anthropicCredentialSecret,
		)
		return SovereignAnthropicSeedOutcomeAlreadyExists
	}
	if !apierrors.IsNotFound(getErr) {
		h.log.Error("sovereign anthropic seed: pre-create Get failed",
			"deploymentID", dep.ID,
			"namespace", anthropicCredentialNamespace,
			"name", anthropicCredentialSecret,
			"err", getErr,
		)
		return SovereignAnthropicSeedOutcomeAPIFailure
	}

	// catalyst-system may not exist yet — bp-catalyst-platform installs
	// later in Phase 1. Same pre-create as the SMTP seed.
	if _, nsErr := clientset.CoreV1().Namespaces().Create(cctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: anthropicCredentialNamespace},
	}, metav1.CreateOptions{}); nsErr != nil && !apierrors.IsAlreadyExists(nsErr) {
		h.log.Error("sovereign anthropic seed: pre-create Namespace failed",
			"deploymentID", dep.ID,
			"namespace", anthropicCredentialNamespace,
			"err", nsErr,
		)
		return SovereignAnthropicSeedOutcomeAPIFailure
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      anthropicCredentialSecret,
			Namespace: anthropicCredentialNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "catalyst-api",
				"app.kubernetes.io/part-of":    "sovereign-bootstrap",
				"catalyst.openova.io/seed":     "anthropic-credentials",
			},
			Annotations: map[string]string{
				// A UUID, not a credential — safe to log and to read off
				// the object during incident response.
				"catalyst.openova.io/seeded-by-deployment-id": dep.ID,
			},
		},
		Type: corev1.SecretTypeOpaque,
		// Key names are the chart contract, not a choice: the helm lookup
		// in catalyst-openova-kc-credentials-secret.yaml indexes `apiKey`
		// and `credentialsJson`, and sovereign_anthropic_seed_live.go
		// reads the same two. They are shared constants for that reason.
		Data: map[string][]byte{
			anthropicCredentialAPIKeyKey: []byte(apiKey),
			anthropicCredentialJSONKey:   []byte(credsJSON),
		},
	}

	if _, createErr := clientset.CoreV1().Secrets(anthropicCredentialNamespace).
		Create(cctx, secret, metav1.CreateOptions{}); createErr != nil {
		if apierrors.IsAlreadyExists(createErr) {
			// Raced another writer between Get and Create. Theirs wins.
			h.log.Info("sovereign anthropic seed: target Secret appeared during create; leaving it untouched",
				"deploymentID", dep.ID)
			return SovereignAnthropicSeedOutcomeAlreadyExists
		}
		h.log.Error("sovereign anthropic seed: Secret create failed",
			"deploymentID", dep.ID,
			"namespace", anthropicCredentialNamespace,
			"name", anthropicCredentialSecret,
			"err", createErr,
		)
		return SovereignAnthropicSeedOutcomeAPIFailure
	}

	// The detail string carries byte lengths and remaining hours only —
	// classifyAnthropicCredential is explicit that it never includes any
	// part of the credential. Logging it is what lets an operator tell a
	// long-lived blob from one that expires this afternoon.
	h.log.Info("sovereign anthropic seed: wrote catalyst-system/sovereign-anthropic-credentials on the new Sovereign; "+
		"its Organizations' agenity workspaces resolve their credential with no per-Sovereign kubectl",
		"deploymentID", dep.ID,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"class", string(class),
		"detail", detail,
	)
	return SovereignAnthropicSeedOutcomeCreated
}

// emitSovereignAnthropicSeedEvent mirrors emitSovereignSMTPSeedEvent so
// the provisioning wizard renders this line next to the SMTP one.
//
// Every message names the Secret and what the operator loses without it.
// None of them carries a credential — the outcome and the Secret's
// coordinates are all that reaches the SSE buffer.
func (h *Handler) emitSovereignAnthropicSeedEvent(dep *Deployment, outcome SovereignAnthropicSeedOutcome) {
	target := anthropicCredentialNamespace + "/" + anthropicCredentialSecret
	level := "info"
	var msg string
	switch outcome {
	case SovereignAnthropicSeedOutcomeCreated:
		msg = "Sovereign Anthropic seed: created Secret " + target + " on the new Sovereign, so every Organization's " +
			"Agenity workspace resolves its credential with no per-Sovereign kubectl."
	case SovereignAnthropicSeedOutcomeAlreadyExists:
		msg = "Sovereign Anthropic seed: Secret " + target + " already present on the new Sovereign — leaving " +
			"operator-supplied bytes in place (idempotent)."
	case SovereignAnthropicSeedOutcomeSkippedNoEnv:
		level = "warn"
		msg = "Sovereign Anthropic seed: SKIPPED — the mothership catalyst-api has empty " + anthropicSeedAPIKeyEnv +
			" / " + anthropicSeedCredentialsJSONEnv + ". Provisioning continues, but Agenity workspaces on this " +
			"Sovereign cannot reach Anthropic until Secret " + target + " exists (#4277)."
	case SovereignAnthropicSeedOutcomeSkippedUnusable:
		level = "warn"
		msg = "Sovereign Anthropic seed: SKIPPED — the mothership's Anthropic credential is configured but cannot " +
			"authenticate (key-only, malformed or expired), and shipping it would give this Sovereign a Secret that " +
			"inspects as populated and fails at first use. Rotate the mothership credential, then create " + target +
			" here or re-provision (#6245)."
	case SovereignAnthropicSeedOutcomeClientFailure:
		level = "warn"
		msg = "Sovereign Anthropic seed: FAILED to build a Kubernetes client from the cloud-init kubeconfig. " +
			"Provisioning continues; an operator can create Secret " + target + " on the new cluster."
	case SovereignAnthropicSeedOutcomeAPIFailure:
		level = "warn"
		msg = "Sovereign Anthropic seed: FAILED talking to the new Sovereign's API server (RBAC drift, network blip, " +
			"or LB still reconciling). Provisioning continues; an operator can create Secret " + target + " on the new cluster."
	default:
		level = "warn"
		msg = fmt.Sprintf("Sovereign Anthropic seed: unknown outcome %q (programming error in catalyst-api).", outcome)
	}

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   sovereignAnthropicSeedPhase,
		Level:   level,
		Message: msg,
	})
}
