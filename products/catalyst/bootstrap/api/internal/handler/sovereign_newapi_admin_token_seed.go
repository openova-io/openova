// Sovereign-side NewAPI admin-token OpenBao seeding (issue #4477 secondary
// item; ADR-0003 §3.2 + §6 — the unified-RBAC admin-API token seam; Refs #3374).
//
// Why this exists:
//
//	ADR-0003 wires Catalyst's per-user-key issuance through NewAPI's admin
//	API: the unified-rbac path in the OTECH control plane reads the
//	`catalyst-newapi-admin-token` Secret's `ADMIN_API_TOKEN` key and presents
//	it as `Authorization: Bearer <token>` against NewAPI's admin endpoints
//	(per-user-key create, sandbox-token mint) during the Organization
//	user-create flow. That Secret is materialised by an ExternalSecret which
//	reads the Sovereign's OpenBao via the `vault-region1` ClusterSecretStore
//	at the canonical path `catalyst/newapi/admin-token` (under the store's
//	`secret/` mount) with property `ADMIN_API_TOKEN`.
//
//	The READ side (chart ExternalSecret + reflector mirror into
//	catalyst-system) is wired and shipped. The PRODUCER — the thing that
//	WRITES that OpenBao path at provisioning time — did NOT exist. Result:
//	on every fresh prov the `catalyst-newapi-admin-token` ExternalSecret
//	stayed READY=False / SecretSyncedError (the live-observed fault on
//	`91dc05917e44d1c1`, omantel.biz, recorded as #4477's secondary item),
//	and no admin bearer reached unified-rbac, so per-user-key issuance
//	against NewAPI's admin API 401'd. This file is that missing producer.
//
// Why the VALUE is the bridge's ADMIN_SECRET (NOT a fresh random token):
//
//	NewAPI's sandbox-bridge validates the inbound admin bearer with
//	subtle.ConstantTimeCompare against its own NEWAPI_ADMIN_SECRET env,
//	which is sourced from the chart-generated, lookup-persisted
//	`newapi-bp-newapi-token-signing-key` Secret's `ADMIN_SECRET` key
//	(platform/newapi/chart/templates/sandbox-token-signing-key-secret.yaml).
//	A freshly-minted random token here would NOT match → every unified-rbac
//	call 401s. So the producer reads that Secret's `ADMIN_SECRET` (the
//	source-of-truth bearer the bridge accepts) and propagates it into
//	OpenBao verbatim. The ExternalSecret then distributes the SAME bytes to
//	the catalyst-system reflector mirror unified-rbac consumes — the bearer
//	and the value that validates it are guaranteed identical.
//
// Why `secret/catalyst/newapi/admin-token` (NOT bare
// `secret/sovereign/<fqdn>/newapi/admin-token`):
//
//	Identical reasoning to sovereign_anthropic_seed.go +
//	sovereign_mcp_bearer_seed.go: the catalyst-api Pod holds the
//	write-capable `catalyst-api-write` OpenBao role scoped to
//	`secret/{data,metadata}/catalyst/*` (platform/openbao/chart/templates/
//	auth-bootstrap-job.yaml #3376). It has NO write grant on
//	`secret/sovereign/*`, so a write to the historical
//	`sovereign/<fqdn>/newapi/admin-token` path would 403 permission-denied.
//	We therefore write under the `catalyst/` prefix the platform can ALREADY
//	write — zero new OpenBao policy/role — and the read-path (chart default
//	+ the bootstrap-kit overlay `catalystIntegration.externalSecret
//	.remoteRef.key`) is moved to `catalyst/newapi/admin-token` in lock-step.
//
// When this runs:
//
//	runOrganizationPipeline calls seedNewapiAdminToken right after Step 1
//	(per-Org overlay committed), beside seedAnthropicToken / seedMCPBearer.
//	The OpenBao path is cluster-shared (one `secret/catalyst/newapi/admin-
//	token` serves the whole Sovereign's unified-rbac), so the FIRST
//	Org-create makes the Sovereign converge; subsequent Org-creates re-seed
//	harmlessly (idempotent — PutKVv2 overwrites). A nil `h.openbao` client
//	(Catalyst-Zero orchestrator) is a non-fatal skip, as is a missing
//	bridge Secret (NewAPI not yet installed on this Sovereign — the next
//	reconcile re-seeds once the chart has rendered the token-signing-key
//	Secret).
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): never logs the token —
// only byte lengths + outcome class.
package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// newapiAdminTokenSeedMountPath / newapiAdminTokenSeedSecretPath — the
// OpenBao KV-v2 location the producer writes and the
// `catalyst-newapi-admin-token` ExternalSecret reads. Fixed-by-contract
// with the bp-newapi chart's catalystIntegration.externalSecret.remoteRef
// .key (platform/newapi/chart/values.yaml) and the bootstrap-kit overlay
// (clusters/_template/bootstrap-kit/80-newapi.yaml). The `catalyst/` prefix
// is the ONLY sub-tree a Sovereign's catalyst-api can write (see the file
// doc comment).
const (
	newapiAdminTokenSeedMountPath  = "secret"
	newapiAdminTokenSeedSecretPath = "catalyst/newapi/admin-token"
)

// newapiAdminTokenSeedKVProperty — the KV-v2 field name the ExternalSecret
// reads (data[0].remoteRef.property = ADMIN_API_TOKEN). Fixed-by-contract
// with platform/newapi/chart/templates/external-secret.yaml.
const newapiAdminTokenSeedKVProperty = "ADMIN_API_TOKEN"

// newapiBridgeSecretNamespace / newapiBridgeSecretName / newapiBridgeSecretKey
// — the in-cluster Secret + key the bp-newapi chart auto-provisions
// (platform/newapi/chart/templates/sandbox-token-signing-key-secret.yaml).
// `ADMIN_SECRET` is the bearer the sandbox-bridge validates with
// subtle.ConstantTimeCompare, so it is the source-of-truth value the seed
// MUST propagate (a mismatch 401s every unified-rbac call).
//
// We read the `catalyst-system` REFLECTOR MIRROR rather than the chart's
// origin namespace (`newapi` on host installs, the mgmt vCluster on
// placement.role=vcluster-app). The bp-newapi chart's
// sandboxTokenSigningKey.reflectorNamespaces default
// (`catalyst-system,sandbox,sandbox-.*`) emberstack-mirrors this Secret
// into `catalyst-system` on EVERY placement role — the same namespace
// catalyst-api itself runs in (openbao chart serviceAccountNamespace:
// catalyst-system). Reading the mirror in catalyst-api's own namespace is
// placement-role-agnostic AND needs no cross-namespace RBAC. Overridable
// per principle #4 via the env vars below for overlays that re-home it.
const (
	newapiBridgeSecretNamespace = "catalyst-system"
	newapiBridgeSecretName      = "newapi-bp-newapi-token-signing-key"
	newapiBridgeSecretKey       = "ADMIN_SECRET"
)

// Env overrides (docs/PRINCIPLES.md #4 — runtime-configurable knobs). Empty
// ⇒ the const defaults above. A per-Sovereign overlay that renames the
// token-signing-key Secret or re-homes it sets these on the catalyst-api
// Pod without rebuilding the image.
const (
	newapiAdminTokenSeedSecretNamespaceEnv = "CATALYST_NEWAPI_ADMIN_TOKEN_SECRET_NAMESPACE"
	newapiAdminTokenSeedSecretNameEnv      = "CATALYST_NEWAPI_ADMIN_TOKEN_SECRET_NAME"
	newapiAdminTokenSeedSecretKeyEnv       = "CATALYST_NEWAPI_ADMIN_TOKEN_SECRET_KEY"
)

// newapiAdminTokenSeedTimeout bounds the in-cluster Secret Get. Generous
// for first-contact against the apiserver; overridable per principle #4.
const newapiAdminTokenSeedTimeout = 15 * time.Second

// NewapiAdminTokenSeedOutcome — terminal classification of one seed attempt.
type NewapiAdminTokenSeedOutcome string

const (
	NewapiAdminTokenSeedOutcomeSeeded          NewapiAdminTokenSeedOutcome = "seeded"
	NewapiAdminTokenSeedOutcomeSkippedNoBao    NewapiAdminTokenSeedOutcome = "skipped-no-openbao"
	NewapiAdminTokenSeedOutcomeSkippedNoSecret NewapiAdminTokenSeedOutcome = "skipped-no-bridge-secret"
	NewapiAdminTokenSeedOutcomeClientFailure   NewapiAdminTokenSeedOutcome = "client-failure"
	NewapiAdminTokenSeedOutcomeWriteFailure    NewapiAdminTokenSeedOutcome = "write-failure"
)

// NewapiAdminTokenSecretReader returns the bridge ADMIN_SECRET bytes from the
// in-cluster `newapi-bp-newapi-token-signing-key` Secret. The narrow seam
// keeps the OpenBao-write path unit-testable without standing up a real
// apiserver: production wires inClusterNewapiBridgeAdminSecret;
// tests inject a closure returning a fixed value (or NotFound).
type NewapiAdminTokenSecretReader func(ctx context.Context, namespace, name, key string) (string, bool, error)

// SetNewapiAdminTokenSecretReader wires a test-only reader. Production leaves
// this nil and the helper falls back to inClusterNewapiBridgeAdminSecret.
func (h *Handler) SetNewapiAdminTokenSecretReader(r NewapiAdminTokenSecretReader) {
	h.newapiAdminTokenSecretReader = r
}

// inClusterNewapiBridgeAdminSecret reads the bridge ADMIN_SECRET from the
// in-cluster Secret via rest.InClusterConfig (catalyst-api always runs in
// the Sovereign it serves). Returns (value, found, err): found=false +
// nil err is the "Secret/key not present yet" branch (NewAPI not installed
// or the chart hasn't rendered the token-signing-key Secret yet) — the
// caller treats that as a non-fatal skip, not a failure.
func inClusterNewapiBridgeAdminSecret(ctx context.Context, namespace, name, key string) (string, bool, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return "", false, fmt.Errorf("in-cluster config unavailable: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", false, fmt.Errorf("build core client: %w", err)
	}
	return readNewapiBridgeAdminSecret(ctx, clientset, namespace, name, key)
}

// readNewapiBridgeAdminSecret is the clientset-bound core of the reader,
// split out so a fake.NewSimpleClientset can exercise the Get + key-extract
// logic directly. NotFound on the Secret ⇒ (",false,nil); a present Secret
// missing the key ⇒ ("",false,nil) (chart drift / partial render — re-seed
// next reconcile). Any other API error is a hard failure.
func readNewapiBridgeAdminSecret(ctx context.Context, clientset kubernetes.Interface, namespace, name, key string) (string, bool, error) {
	sec, err := clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	raw, ok := sec.Data[key]
	if !ok || len(raw) == 0 {
		return "", false, nil
	}
	return string(raw), true, nil
}

// seedNewapiAdminToken propagates NewAPI's bridge ADMIN_SECRET (the bearer
// the sandbox-bridge validates) into the Sovereign's OpenBao
// `secret/catalyst/newapi/admin-token` path so the
// `catalyst-newapi-admin-token` ExternalSecret resolves and unified-rbac's
// admin-API calls authenticate (issue #4477 secondary item). Idempotent:
// PutKVv2 overwrites, so repeated Org-creates re-seed harmlessly.
//
// Returns the terminal outcome so the caller can emit an SSE / log line
// without coupling this helper to the emit machinery. NEVER returns an
// error that fails the Org pipeline — a missing bridge Secret or a transient
// OpenBao blip must not block the rest of the Org (unified-rbac's per-user-
// key issuance stays offline only until the path is seeded). The outcome is
// surfaced loudly instead.
func (h *Handler) seedNewapiAdminToken(ctx context.Context) NewapiAdminTokenSeedOutcome {
	// Catalyst-Zero (the orchestrator) has no in-cluster OpenBao client and
	// never hosts NewAPI — skip without noise beyond a debug line.
	// Production Sovereign catalyst-api always has h.openbao wired
	// (SetOpenBao, main.go).
	if h.openbao == nil {
		if h.log != nil {
			h.log.Debug("newapi admin-token seed: skipped — no OpenBao client (orchestrator / Catalyst-Zero)")
		}
		return NewapiAdminTokenSeedOutcomeSkippedNoBao
	}

	if ctx == nil {
		ctx = context.Background()
	}

	ns := envOr(newapiAdminTokenSeedSecretNamespaceEnv, newapiBridgeSecretNamespace)
	name := envOr(newapiAdminTokenSeedSecretNameEnv, newapiBridgeSecretName)
	key := envOr(newapiAdminTokenSeedSecretKeyEnv, newapiBridgeSecretKey)

	reader := h.newapiAdminTokenSecretReader
	if reader == nil {
		reader = inClusterNewapiBridgeAdminSecret
	}

	rctx, cancel := context.WithTimeout(ctx, newapiAdminTokenSeedTimeout)
	defer cancel()

	token, found, err := reader(rctx, ns, name, key)
	if err != nil {
		// A real API error (RBAC drift, apiserver unreachable) — surface
		// loud + skip; the pipeline must not fail on it.
		if h.log != nil {
			h.log.Error("newapi admin-token seed: read bridge admin secret failed",
				"namespace", ns,
				"name", name,
				"key", key,
				"err", err,
			)
		}
		return NewapiAdminTokenSeedOutcomeClientFailure
	}
	if !found || strings.TrimSpace(token) == "" {
		// NewAPI not yet installed on this Sovereign, or the chart has not
		// rendered the token-signing-key Secret yet. Loud skip; the next
		// Org reconcile re-seeds once the Secret exists. NOT a failure —
		// seeding an empty path would pin the ESO/reflector empty-seed trap.
		if h.log != nil {
			h.log.Warn("newapi admin-token seed: SKIPPED — bridge admin secret not present yet; catalyst-newapi-admin-token ExternalSecret stays unresolved until NewAPI's token-signing-key Secret renders",
				"namespace", ns,
				"name", name,
				"key", key,
				"openbaoPath", newapiAdminTokenSeedMountPath+"/"+newapiAdminTokenSeedSecretPath,
			)
		}
		return NewapiAdminTokenSeedOutcomeSkippedNoSecret
	}

	// KV-v2 payload. Field name MUST match the ExternalSecret's
	// remoteRef.property value (ADMIN_API_TOKEN).
	data := map[string]any{
		newapiAdminTokenSeedKVProperty: token,
	}

	if err := h.openbao.PutKVv2(ctx, newapiAdminTokenSeedMountPath, newapiAdminTokenSeedSecretPath, data); err != nil {
		if h.log != nil {
			h.log.Error("newapi admin-token seed: OpenBao write failed",
				"openbaoPath", newapiAdminTokenSeedMountPath+"/"+newapiAdminTokenSeedSecretPath,
				"err", err,
			)
		}
		return NewapiAdminTokenSeedOutcomeWriteFailure
	}

	if h.log != nil {
		// Per docs/PRINCIPLES.md #10: byte length + outcome only, never the
		// token plaintext.
		h.log.Info("newapi admin-token seed: wrote OpenBao path so the catalyst-newapi-admin-token ExternalSecret resolves + unified-rbac's admin-API calls authenticate",
			"openbaoPath", newapiAdminTokenSeedMountPath+"/"+newapiAdminTokenSeedSecretPath,
			"sourceSecret", ns+"/"+name,
			"adminTokenBytes", len(token),
		)
	}
	return NewapiAdminTokenSeedOutcomeSeeded
}
