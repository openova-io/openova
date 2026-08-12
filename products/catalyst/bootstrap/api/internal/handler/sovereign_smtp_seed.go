// Sovereign-side SMTP credential seeding (issue #883).
//
// Why this exists:
//
//   On a freshly franchised Sovereign, console-side magic-link / PIN
//   email delivery fails because there's no SMTP relay reachable inside
//   the cluster: the bootstrap-kit doesn't deploy a Stalwart on the new
//   Sovereign, the Sovereign-local org-services-secrets has empty SMTP_HOST/PORT/
//   FROM/USER/PASS, and services-auth defaults SMTP_HOST=localhost.
//
//   Phase-1 architectural decision (founder-confirmed): during initial
//   provisioning the Sovereign Console relays mail through the
//   mothership Stalwart at mail.openova.io:587.
//
//   Phase-2 cutover (issue #924, bp-stalwart-sovereign blueprint):
//   bootstrap-kit slot 95 installs a Sovereign-local Stalwart whose
//   post-install Job re-materialises catalyst-system/sovereign-smtp-
//   credentials with Sovereign-local infrastructure addresses
//   (`mail.<sovereignFQDN>` / `noreply@<sovereignFQDN>`) AND
//   credentials minted in-cluster. bp-catalyst-platform 1.4.20+
//   reads BOTH key shapes (`smtp-user`/`smtp-pass` AND legacy
//   `user`/`password`) so this Phase-1 seed and the Phase-2 chart's
//   write are interchangeable on the read side.
//
//   This Phase-1 step remains as a graceful fallback that runs ONLY
//   when no in-namespace Secret exists yet (Get → IsNotFound → Create).
//   The Phase-2 chart's post-install Job uses `kubectl apply` against
//   the same Secret and overwrites whatever Phase-1 seeded — so the
//   first cluster reconcile after slot 95 lands cuts over to
//   `noreply@<sovereignFQDN>` automatically, no operator action.
//
// What this seeds:
//
//   On the freshly-provisioned Sovereign (target cluster reached via
//   the cloud-init-postback kubeconfig), we create:
//
//     Secret catalyst-system/sovereign-smtp-credentials
//       smtp-user: <mothership smtp-user>
//       smtp-pass: <mothership smtp-pass>
//
//   The bp-catalyst-platform chart's auto-create step (#901) reads
//   this Secret via Helm `lookup` when rendering the Sovereign-local
//   `catalyst-openova-kc-credentials` Secret, so the chart-rendered
//   bytes carry working SMTP submission credentials and the auth
//   service's SMTP-PLAIN dial against mail.openova.io:587 succeeds
//   on first send-pin.
//
// Source of mothership SMTP creds:
//
//   The mothership catalyst-api Pod already has CATALYST_SMTP_USER /
//   CATALYST_SMTP_PASS in its environment (chart api-deployment.yaml
//   wires them via secretKeyRef → catalyst-openova-kc-credentials in
//   the catalyst namespace). We read those env vars directly — no new
//   K8s read against the mothership API is needed.
//
// When this runs:
//
//   PutKubeconfig (kubeconfig.go) calls SeedSovereignSMTPCredentials
//   AFTER the cloud-init kubeconfig postback has been persisted to
//   disk and BEFORE the runPhase1Watch goroutine fires. That window is
//   exactly the "Sovereign cluster up, bp-catalyst-platform not yet
//   installed" gap the chart's lookup needs.
//
//   The seed step is idempotent: an already-existing
//   sovereign-smtp-credentials Secret is a no-op (the chart's lookup
//   will read whatever bytes are there). This survives:
//     - retry of the kubeconfig PUT (single-use bearer normally
//       prevents this; defensive anyway)
//     - the kubeconfig-missing relaunch path (#538)
//     - operator manual replay during incident response
//
// Failure modes (per docs/INVIOLABLE-PRINCIPLES.md #1: surface, never hide):
//
//   - kubeconfig parse failure → seed skipped, warn-event emitted on
//     the deployment SSE bus, Phase-1 watch still launches. The
//     wizard surfaces the warning and the operator can manual-create
//     the Secret + retry send-pin once the Sovereign is up.
//   - mothership env vars empty → seed skipped, warn-event emitted.
//     Production catalyst-api always has both wired; CI / dev
//     environments run without them and surface the gap loudly.
//   - K8s API call failure (network blip, RBAC drift) → seed skipped,
//     warn-event emitted, Phase-1 continues. The chart's lookup will
//     not find the Secret; the auth pod will log SMTP-refused on
//     first send-pin, exactly the pre-fix behaviour. Better the
//     operator sees a loud warn at provision time than a silent
//     "ready" with broken email.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): the
// catalyst-api never logs the SMTP password. Logs include the
// deployment id, the target namespace + secret name, byte length of
// each value, and outcome class — never the plaintext.
package handler

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

// sovereignSMTPSeedNamespace — destination namespace on the new
// Sovereign for the Secret. Hardcoded to catalyst-system because
// bp-catalyst-platform installs into that namespace; the chart's
// lookup runs from that namespace too. The destination is part of
// the chart contract (#901), not configuration the operator should
// override.
const sovereignSMTPSeedNamespace = "catalyst-system"

// sovereignSMTPSeedSecretName — name of the Secret the chart's
// lookup reads. Same fixed-by-contract reasoning.
const sovereignSMTPSeedSecretName = "sovereign-smtp-credentials"

// sovereignSMTPSeedTimeout — bound on the seed step's K8s calls.
// 30 seconds is generous for the LB-fronted API server first contact
// after kubeconfig postback. Override via
// CATALYST_SOVEREIGN_SMTP_SEED_TIMEOUT (per principle #4).
const sovereignSMTPSeedTimeoutEnv = "CATALYST_SOVEREIGN_SMTP_SEED_TIMEOUT"
const defaultSovereignSMTPSeedTimeout = 30 * time.Second

// sovereignSMTPSeedPhase — SSE phase tag emitted on the deployment
// event bus. Mirrors the helmwatch PhaseComponent style so the
// wizard's reducer renders these events alongside the per-component
// install lines.
const sovereignSMTPSeedPhase = "sovereign-smtp-seed"

// ── #5921 — the NINTH tether, and the only one on the purchase path ──
//
// Everything this file seeds ends up in catalyst-system/sovereign-smtp-
// credentials, which `catalyst-system/marketplace-api` reads via
// secretKeyRef to send every customer sign-in code (chart template
// marketplace-api/deployment.yaml). Seeding `mail.openova.io` there
// means a post-cutover Sovereign with cutoverComplete=true still cannot
// sell anything unless the OpenOva mothership's mail server answers.
// ADR-0002 enumerates EIGHT tethers; outbound customer-auth SMTP is not
// among them, so no cutover step pivots it.
//
// RELAY SCOPE is therefore an explicit, recorded property of the seed —
// not an accident of which default fired:
//
//   sovereign-local — relay at `mail.<sovereignFQDN>`, sender
//                     `noreply@<sovereignFQDN>`. The Sovereign sends its
//                     own customer mail and owns the whole purchase path.
//                     REQUIRES a Sovereign-local submission relay to
//                     exist (see the "what remains" note below).
//   mothership      — relay at `mail.openova.io` (the Phase-1 behaviour
//                     this file has always had). Deliverable on a fresh
//                     Sovereign that has no mail stack of its own, and a
//                     live tether until pivoted.
//
// DEFAULT IS `mothership` ON PURPOSE. Flipping the default would point
// every fresh Sovereign at a relay that does not exist yet — slot 95
// (bp-stalwart-sovereign) was removed from the bootstrap-kit in 94ffe01ff
// because its post-install Job was timing out, so today NOTHING serves
// `mail.<sovereignFQDN>`. A default that breaks PIN delivery on every
// prov is not a sovereignty fix, it is an outage. The pivot is gated on
// the local relay actually existing, which is why the ENFORCEMENT half
// lives in the cutover's step-08 lint (run_smtp_relay_host_lint) rather
// than in a default flip here.
const sovereignSMTPRelayModeEnv = "CATALYST_SOVEREIGN_SMTP_RELAY_MODE"

const (
	// SovereignSMTPRelayScopeMothership — the seed wrote mothership relay
	// coordinates. The Sovereign's customer-auth mail is TETHERED.
	SovereignSMTPRelayScopeMothership = "mothership"
	// SovereignSMTPRelayScopeLocal — the seed wrote Sovereign-local relay
	// coordinates derived from the deployment's own FQDN.
	SovereignSMTPRelayScopeLocal = "sovereign-local"
)

// sovereignSMTPRelay — the resolved outbound relay coordinates plus the
// SCOPE that produced them.
type sovereignSMTPRelay struct {
	Host  string
	Port  string
	From  string
	Scope string
}

// resolveSovereignSMTPRelay decides the relay coordinates the seed writes.
//
// Precedence, highest first:
//
//  1. CATALYST_SOVEREIGN_SMTP_RELAY_HOST — an explicit operator-pinned
//     relay. Honoured in either mode so a bespoke relay needs no rebuild.
//  2. CATALYST_SOVEREIGN_SMTP_RELAY_MODE=sovereign-local — derive
//     `mail.<sovereignFQDN>` from the deployment's own request.
//  3. mothership (default) — `mail.openova.io`.
//
// SCOPE IS MEASURED FROM THE RESOLVED HOST, NEVER FROM THE REQUESTED
// MODE. That distinction is the bug this function also fixes: the seed
// previously stamped every Secret `seed-phase: phase-1-mothership-relay`
// unconditionally, so an operator who pinned a Sovereign-local relay via
// CATALYST_SOVEREIGN_SMTP_RELAY_HOST got a Secret whose own provenance
// annotation said "mothership". Any cutover lint or migration script
// keying on that annotation would have read a value that described the
// code path instead of the bytes — a self-report that cannot go wrong is
// not evidence.
//
// FAIL-SAFE: sovereign-local with an empty SovereignFQDN cannot compose a
// host. Rather than seed `mail.` (an unresolvable host that would break
// PIN delivery for the life of the cluster), fall back to the mothership
// relay and report the scope HONESTLY as mothership, so the operator sees
// a tether rather than a silent breakage.
func resolveSovereignSMTPRelay(sovereignFQDN, smtpUser string) sovereignSMTPRelay {
	port := os.Getenv("CATALYST_SOVEREIGN_SMTP_RELAY_PORT")
	if port == "" {
		port = "587"
	}

	mode := strings.ToLower(strings.TrimSpace(os.Getenv(sovereignSMTPRelayModeEnv)))
	fqdn := strings.ToLower(strings.TrimSpace(sovereignFQDN))

	host := os.Getenv("CATALYST_SOVEREIGN_SMTP_RELAY_HOST")
	from := os.Getenv("CATALYST_SOVEREIGN_SMTP_RELAY_FROM")

	if host == "" {
		if mode == SovereignSMTPRelayScopeLocal && fqdn != "" {
			host = "mail." + fqdn
			if from == "" {
				from = "noreply@" + fqdn
			}
		} else {
			host = "mail.openova.io"
		}
	}
	if from == "" {
		from = smtpUser
	}

	return sovereignSMTPRelay{
		Host:  host,
		Port:  port,
		From:  from,
		Scope: classifySovereignSMTPRelayScope(host, fqdn),
	}
}

// classifySovereignSMTPRelayScope reports whether a relay host is the
// Sovereign's own or somebody else's, from the HOST STRING itself.
//
// A host is Sovereign-local when it is the Sovereign's own FQDN or any
// subdomain of it, or an in-cluster address. Everything else — including
// every openova.io host — is a tether. Deliberately NOT a
// "contains openova.io" test: the discriminator is "is this MINE", and a
// Sovereign whose own FQDN were an openova.io subdomain would still be
// answering for itself.
func classifySovereignSMTPRelayScope(host, sovereignFQDN string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	fqdn := strings.ToLower(strings.TrimSpace(sovereignFQDN))

	if fqdn != "" && (h == fqdn || strings.HasSuffix(h, "."+fqdn)) {
		return SovereignSMTPRelayScopeLocal
	}
	switch {
	case h == "localhost", h == "127.0.0.1":
		return SovereignSMTPRelayScopeLocal
	case strings.HasSuffix(h, ".svc"), strings.HasSuffix(h, ".svc.cluster.local"):
		return SovereignSMTPRelayScopeLocal
	}
	return SovereignSMTPRelayScopeMothership
}

// sovereignSMTPSeedPhaseLabel maps a relay scope onto the historical
// `seed-phase` annotation vocabulary. The mothership value is preserved
// verbatim so existing tooling that greps for `phase-1-mothership-relay`
// keeps matching the Secrets it always matched.
func sovereignSMTPSeedPhaseLabel(scope string) string {
	if scope == SovereignSMTPRelayScopeLocal {
		return "phase-2-sovereign-local-relay"
	}
	return "phase-1-mothership-relay"
}

// SovereignSMTPSeedClientFactory — test-only override for building a
// kubernetes.Interface from a kubeconfig YAML. Production wires
// helmwatch.NewKubernetesClientFromKubeconfig; tests inject a closure
// returning a fake.NewSimpleClientset so the seed path is exercised
// without standing up a real cluster.
type SovereignSMTPSeedClientFactory func(kubeconfigYAML string) (kubernetes.Interface, error)

// SetSovereignSMTPSeedClientFactory wires a test-only factory.
// Production leaves this nil and the helper falls back to
// helmwatch.NewKubernetesClientFromKubeconfig.
func (h *Handler) SetSovereignSMTPSeedClientFactory(f SovereignSMTPSeedClientFactory) {
	h.sovereignSMTPSeedClientFactory = f
}

// SovereignSMTPSeedOutcome — terminal classification of one seed
// attempt. The SSE event message includes this so the wizard's
// reducer can colour-code the line; tests assert on it without
// scraping the human-readable Message.
type SovereignSMTPSeedOutcome string

const (
	SovereignSMTPSeedOutcomeCreated      SovereignSMTPSeedOutcome = "created"
	SovereignSMTPSeedOutcomeAlreadyExists SovereignSMTPSeedOutcome = "already-exists"
	SovereignSMTPSeedOutcomeSkippedNoEnv  SovereignSMTPSeedOutcome = "skipped-no-env"
	SovereignSMTPSeedOutcomeClientFailure SovereignSMTPSeedOutcome = "client-failure"
	SovereignSMTPSeedOutcomeAPIFailure    SovereignSMTPSeedOutcome = "api-failure"
)

// seedSovereignSMTPCredentials creates the
// catalyst-system/sovereign-smtp-credentials Secret on the freshly-
// provisioned Sovereign. The function is exported through the
// instance method so PutKubeconfig can drive it.
//
// Returns the terminal outcome so the caller can choose to emit an
// SSE event (PutKubeconfig does) without coupling this helper to the
// emit machinery.
//
// Idempotent: an existing Secret short-circuits to AlreadyExists. The
// helper does NOT update an existing Secret — operator-supplied bytes
// take precedence over mothership re-seed. (A future Phase-2 patch
// can rotate via UPDATE if needed.)
func (h *Handler) seedSovereignSMTPCredentials(ctx context.Context, dep *Deployment, kubeconfigYAML string) SovereignSMTPSeedOutcome {
	smtpUser := os.Getenv("CATALYST_SMTP_USER")
	smtpPass := os.Getenv("CATALYST_SMTP_PASS")
	if smtpUser == "" || smtpPass == "" {
		// Production catalyst-api always has both wired via
		// secretKeyRef (chart api-deployment.yaml). An empty value
		// here is a CI / dev environment OR a misconfigured
		// catalyst-openova-kc-credentials Secret on the mothership.
		// Either way the new Sovereign cannot relay mail without
		// these credentials; surface loud + skip rather than seed
		// an empty Secret that pretends to work.
		h.log.Warn("sovereign smtp seed: mothership SMTP env unset; skipping seed",
			"deploymentID", dep.ID,
			"hasUser", smtpUser != "",
			"hasPass", smtpPass != "",
		)
		return SovereignSMTPSeedOutcomeSkippedNoEnv
	}

	// #4748 — the Phase-1 relay host/port/from must be the mothership's
	// PUBLIC SMTP endpoint (mail.openova.io:587), NOT the mothership's own
	// CATALYST_SMTP_HOST — that is the in-cluster `stalwart-web.stalwart.svc.
	// cluster.local` Service (#4696), which does NOT exist on a customer
	// Sovereign (Sovereigns run no Stalwart). Seeding ONLY smtp-user/smtp-pass
	// (the original bug) left the chart's `catalyst-openova-kc-credentials`
	// contract Secret with no source `smtp-host` to win precedence over, so it
	// fell back to the `.Values.sovereign.smtp.host` in-cluster-Stalwart default
	// → `no such host` → operator PIN-login 502 on EVERY fresh Sovereign
	// (hw221 evidence). Seed the reachable public relay so the source-wins
	// precedence in catalyst-openova-kc-credentials-secret.yaml repoints the
	// contract Secret to a host that actually answers. Env-overridable so a
	// bespoke relay can be pinned without a rebuild; defaults match the seed's
	// stated intent (mail.openova.io:587).
	//
	// #5921 — the relay is now resolved through a SCOPED helper so the
	// Sovereign-local path exists and the Secret records which scope it
	// actually got. See resolveSovereignSMTPRelay for the precedence and
	// for why the default stays `mothership`.
	relay := resolveSovereignSMTPRelay(dep.Request.SovereignFQDN, smtpUser)
	relayHost, relayPort, relayFrom := relay.Host, relay.Port, relay.From

	factory := h.sovereignSMTPSeedClientFactory
	if factory == nil {
		factory = helmwatch.NewKubernetesClientFromKubeconfig
	}
	clientset, err := factory(kubeconfigYAML)
	if err != nil {
		h.log.Error("sovereign smtp seed: build kubernetes client from kubeconfig failed",
			"deploymentID", dep.ID,
			"err", err,
		)
		return SovereignSMTPSeedOutcomeClientFailure
	}

	timeout := defaultSovereignSMTPSeedTimeout
	if v, _ := time.ParseDuration(os.Getenv(sovereignSMTPSeedTimeoutEnv)); v > 0 {
		timeout = v
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check existence first so we never accidentally overwrite an
	// operator-supplied Secret. apierrors.IsNotFound is the only
	// "proceed with create" branch; any other error is a hard fail
	// (RBAC drift, API server unreachable, malformed kubeconfig).
	_, getErr := clientset.CoreV1().Secrets(sovereignSMTPSeedNamespace).Get(cctx, sovereignSMTPSeedSecretName, metav1.GetOptions{})
	if getErr == nil {
		h.log.Info("sovereign smtp seed: target Secret already exists; skipping create (idempotent)",
			"deploymentID", dep.ID,
			"namespace", sovereignSMTPSeedNamespace,
			"name", sovereignSMTPSeedSecretName,
		)
		return SovereignSMTPSeedOutcomeAlreadyExists
	}
	if !apierrors.IsNotFound(getErr) {
		h.log.Error("sovereign smtp seed: pre-create Get failed",
			"deploymentID", dep.ID,
			"namespace", sovereignSMTPSeedNamespace,
			"name", sovereignSMTPSeedSecretName,
			"err", getErr,
		)
		return SovereignSMTPSeedOutcomeAPIFailure
	}

	// catalyst-system may not exist yet (Phase-1 watch hasn't fired,
	// bp-catalyst-platform hasn't installed). Pre-create the namespace
	// so the Secret create succeeds. apierrors.IsAlreadyExists is the
	// idempotent branch; any other error is a hard fail.
	if _, nsErr := clientset.CoreV1().Namespaces().Create(cctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sovereignSMTPSeedNamespace},
	}, metav1.CreateOptions{}); nsErr != nil && !apierrors.IsAlreadyExists(nsErr) {
		h.log.Error("sovereign smtp seed: pre-create Namespace failed",
			"deploymentID", dep.ID,
			"namespace", sovereignSMTPSeedNamespace,
			"err", nsErr,
		)
		return SovereignSMTPSeedOutcomeAPIFailure
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sovereignSMTPSeedSecretName,
			Namespace: sovereignSMTPSeedNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "catalyst-api",
				"app.kubernetes.io/part-of":    "sovereign-bootstrap",
				"catalyst.openova.io/seed":     "smtp-credentials",
			},
			Annotations: map[string]string{
				// Trace: which mothership deployment minted this
				// Secret. Useful for incident-response when an
				// operator wonders why a Sovereign has unexpected
				// SMTP credentials. The value is a UUID — not a
				// credential — and is safe to log.
				"catalyst.openova.io/seeded-by-deployment-id": dep.ID,
				// Phase marker: when Phase-2 spins up per-Sovereign
				// Stalwart-relay, the migration script can target
				// every Secret with this annotation for rotation.
				//
				// #5921 — this used to be the CONSTANT string
				// "phase-1-mothership-relay", stamped even when an
				// operator had pinned a Sovereign-local relay through
				// CATALYST_SOVEREIGN_SMTP_RELAY_HOST. It described the
				// code path rather than the bytes, so it could never
				// disagree with reality and was worthless as evidence.
				// It is now derived from the RESOLVED host.
				"catalyst.openova.io/seed-phase": sovereignSMTPSeedPhaseLabel(relay.Scope),
				// #5921 — machine-readable tether marker. `mothership`
				// means every customer sign-in code this Sovereign
				// sends transits OUR mail server; the cutover step-08
				// smtp-relay-host lint refuses to certify sovereignty
				// while that is true.
				"catalyst.openova.io/relay-scope": relay.Scope,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			// #4748 — host/port/from are the PUBLIC relay endpoint, not the
			// mothership's in-cluster Stalwart. Without these three keys the
			// chart's contract Secret has nothing to win precedence over and
			// falls back to the unreachable in-cluster-Stalwart default.
			"smtp-host": []byte(relayHost),
			"smtp-port": []byte(relayPort),
			"smtp-from": []byte(relayFrom),
			"smtp-user": []byte(smtpUser),
			"smtp-pass": []byte(smtpPass),
		},
	}
	if _, createErr := clientset.CoreV1().Secrets(sovereignSMTPSeedNamespace).Create(cctx, secret, metav1.CreateOptions{}); createErr != nil {
		// Race-window guard: a parallel writer (operator manual
		// kubectl apply, retry of this PUT) raced our Get→Create.
		// Treat AlreadyExists as success — the bytes are there.
		if apierrors.IsAlreadyExists(createErr) {
			h.log.Info("sovereign smtp seed: target Secret created concurrently; treating as success",
				"deploymentID", dep.ID,
				"namespace", sovereignSMTPSeedNamespace,
				"name", sovereignSMTPSeedSecretName,
			)
			return SovereignSMTPSeedOutcomeAlreadyExists
		}
		h.log.Error("sovereign smtp seed: Create Secret failed",
			"deploymentID", dep.ID,
			"namespace", sovereignSMTPSeedNamespace,
			"name", sovereignSMTPSeedSecretName,
			"err", createErr,
		)
		return SovereignSMTPSeedOutcomeAPIFailure
	}

	// Per docs/INVIOLABLE-PRINCIPLES.md #10: byte lengths only,
	// never plaintext.
	h.log.Info("sovereign smtp seed: created",
		"deploymentID", dep.ID,
		"namespace", sovereignSMTPSeedNamespace,
		"name", sovereignSMTPSeedSecretName,
		"smtpRelayHost", relayHost, // #4748 — not a secret; the reachable public relay
		"smtpRelayPort", relayPort,
		"smtpRelayScope", relay.Scope, // #5921 — `mothership` == customer-auth mail is tethered

		"smtpUserBytes", len(smtpUser),
		"smtpPassBytes", len(smtpPass),
	)
	return SovereignSMTPSeedOutcomeCreated
}

// emitSovereignSMTPSeedEvent — pushes a typed event onto the
// deployment SSE buffer so the wizard's reducer can render the seed
// outcome inline with the per-component HelmRelease events.
//
// Pulled out into its own helper so PutKubeconfig has a single call
// site and the message phrasing stays consistent across outcomes.
func (h *Handler) emitSovereignSMTPSeedEvent(dep *Deployment, outcome SovereignSMTPSeedOutcome) {
	level := "info"
	var msg string
	switch outcome {
	case SovereignSMTPSeedOutcomeCreated:
		msg = fmt.Sprintf("Sovereign SMTP seed: created Secret %s/%s on the new Sovereign so the Phase-1 console relays mail through mail.openova.io:587 until Phase-2 spins up the per-Sovereign Stalwart-relay.", sovereignSMTPSeedNamespace, sovereignSMTPSeedSecretName)
	case SovereignSMTPSeedOutcomeAlreadyExists:
		msg = fmt.Sprintf("Sovereign SMTP seed: Secret %s/%s already present on the new Sovereign — leaving operator-supplied bytes in place (idempotent).", sovereignSMTPSeedNamespace, sovereignSMTPSeedSecretName)
	case SovereignSMTPSeedOutcomeSkippedNoEnv:
		level = "warn"
		msg = "Sovereign SMTP seed: SKIPPED — mothership catalyst-api has empty CATALYST_SMTP_USER / CATALYST_SMTP_PASS. The new Sovereign's bp-catalyst-platform install will continue, but PIN email delivery will fail until an operator manually creates Secret " + sovereignSMTPSeedNamespace + "/" + sovereignSMTPSeedSecretName + " on the new cluster."
	case SovereignSMTPSeedOutcomeClientFailure:
		level = "warn"
		msg = "Sovereign SMTP seed: FAILED to build a Kubernetes client from the cloud-init kubeconfig — Phase-1 watch will still attempt to launch. PIN email delivery will likely fail until an operator manually creates Secret " + sovereignSMTPSeedNamespace + "/" + sovereignSMTPSeedSecretName + " on the new cluster."
	case SovereignSMTPSeedOutcomeAPIFailure:
		level = "warn"
		msg = "Sovereign SMTP seed: FAILED talking to the new Sovereign's API server (RBAC drift, network blip, or LB still reconciling). Phase-1 watch will still attempt to launch. Operator can manually create Secret " + sovereignSMTPSeedNamespace + "/" + sovereignSMTPSeedSecretName + " on the new cluster if PIN email delivery fails after provisioning completes."
	default:
		// Defensive default — unknown outcome should never happen,
		// but keep the SSE buffer in a consistent state.
		level = "warn"
		msg = fmt.Sprintf("Sovereign SMTP seed: unknown outcome %q (programming error in catalyst-api).", outcome)
	}

	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   sovereignSMTPSeedPhase,
		Level:   level,
		Message: msg,
	})
}
