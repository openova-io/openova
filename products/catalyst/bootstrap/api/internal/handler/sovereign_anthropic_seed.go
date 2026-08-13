// Sovereign-side Anthropic credential seeding (issue #4277).
//
// Why this exists:
//
//	Every Organization's bp-agenity dashboard runs a solo claude-code
//	agent. That agent authenticates from `~/.claude/.credentials.json`,
//	which the chart's `seed-claude-creds` init container materialises
//	from the per-Org `agenity-anthropic-token` Secret. That Secret is
//	populated by an ExternalSecret that reads the Sovereign's OpenBao at
//	`secret/catalyst/anthropic/token` via the `vault-region1`
//	ClusterSecretStore (properties `apiKey` + `credentialsJson`).
//
//	The READ side (chart init container + ExternalSecret) is wired and
//	shipped (#4111/#4233/#4261). The PRODUCER — the thing that writes the
//	OpenBao path at Org-create — did NOT exist. Result: on every fresh
//	funnel Org the `agenity-anthropic-token` ExternalSecret stayed
//	READY=False / SecretSyncedError, no OAuth cred reached the agent, and
//	the agentic journey (Pillar 4) failed until an operator hand-ran
//	`bao kv put` (the manual one-shot #4111 documents). This file is that
//	missing producer.
//
// Why `secret/catalyst/anthropic/token` (NOT bare `secret/anthropic/token`):
//
//	The agenity chart's historical default read `secret/anthropic/token`,
//	but NOTHING on a Sovereign can WRITE there: the `external-secrets`
//	OpenBao role (which `vault-region1` authenticates with) holds the
//	read-only `external-secrets-read` policy, and no other role grants
//	write on `secret/anthropic/*`. The catalyst-api Pod, by contrast,
//	ALREADY holds a write-capable role (`catalyst-api-write`, scoped to
//	`secret/{data,metadata}/catalyst/*` — see
//	platform/openbao/chart/templates/auth-bootstrap-job.yaml #3376) that
//	its existing `h.openbao` client (the same one that seals the cutover
//	fact at `secret/catalyst/cutover-complete`) authenticates with. So we
//	write under the `catalyst/` prefix the platform can ALREADY write —
//	zero new OpenBao policy/role, zero PushSecret, zero cloud-init change.
//	The agenity read-path (chart default + the org-gitops emitter
//	override) is moved to this path in lock-step.
//
// Where the credential VALUE comes from:
//
//	The catalyst-api carries the platform-supplied Anthropic credential
//	in its environment — CATALYST_ANTHROPIC_API_KEY (a real `sk-ant-…`
//	key, optional) and CATALYST_ANTHROPIC_CREDENTIALS_JSON (the full
//	`{"claudeAiOauth":{...}}` blob the spawned claude-code authenticates
//	with). These are wired via secretKeyRef in the catalyst chart exactly
//	like CATALYST_SMTP_USER/PASS (the #883 SMTP-relay precedent this file
//	mirrors). The blob is REFRESHABLE: the claude-code OAuth accessToken
//	is short-lived (hours) and headless claude-code does NOT reliably
//	refresh it, so the operator rotates the env-sourced Secret and the
//	next Org-create (or reconcile) re-seeds the fresh blob — PutKVv2
//	overwrites, so re-seed is idempotent.
//
//	🛑 FOUNDER-SUPPLIED-SECRET GAP: if BOTH env vars are empty the seed is
//	a LOUD no-op (same posture as the SMTP seed's skipped-no-env). The
//	platform holds no Anthropic credential by default — registering one is
//	a founder action (mirror of the Hetzner-token case). The exact target
//	to populate is documented on the deployment SSE event + this file's
//	doc comment: set CATALYST_ANTHROPIC_CREDENTIALS_JSON (and optionally
//	CATALYST_ANTHROPIC_API_KEY) on the catalyst-api via the chart values
//	`sovereign.anthropic.*` (products/catalyst/chart/values.yaml).
//
// When this runs:
//
//	runOrganizationPipeline calls seedAnthropicToken right after Step 1
//	(per-Org overlay committed) on every Org-create. The OpenBao path is
//	cluster-shared (one `secret/catalyst/anthropic/token` serves every
//	Org's agenity install on the Sovereign), so the FIRST Org-create makes
//	every Org converge; subsequent Org-creates re-seed harmlessly (and
//	keep the OAuth blob fresh). A nil `h.openbao` client (the Catalyst-Zero
//	orchestrator, never itself an agenity host) is a non-fatal skip.
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): this never logs the
// key or the credentials.json blob — only byte lengths + outcome class.
package handler

import (
	"context"
	"os"
	"strings"
)

// anthropicSeedMountPath / anthropicSeedSecretPath — the OpenBao KV-v2
// location the producer writes and the agenity ExternalSecret reads.
// Fixed-by-contract with the agenity chart's
// anthropic.externalSecret.remoteKey (products/agenity/chart/values.yaml)
// and the org-gitops emitter override (organization_gitops.go). The
// `catalyst/` prefix is the ONLY path a Sovereign can write (see the
// file doc comment).
const (
	anthropicSeedMountPath  = "secret"
	anthropicSeedSecretPath = "catalyst/anthropic/token"
)

// anthropicSeedAPIKeyEnv / anthropicSeedCredentialsJSONEnv — the
// catalyst-api env vars carrying the platform-supplied Anthropic
// credential. Wired via secretKeyRef in the catalyst chart (mirror of
// CATALYST_SMTP_USER/PASS). At least ONE must be non-empty for the seed
// to write; both empty ⇒ founder-supplied gap, loud skip.
const (
	anthropicSeedAPIKeyEnv          = "CATALYST_ANTHROPIC_API_KEY"
	anthropicSeedCredentialsJSONEnv = "CATALYST_ANTHROPIC_CREDENTIALS_JSON"
)

// AnthropicSeedOutcome — terminal classification of one seed attempt.
type AnthropicSeedOutcome string

const (
	AnthropicSeedOutcomeSeeded       AnthropicSeedOutcome = "seeded"
	AnthropicSeedOutcomeSkippedNoEnv AnthropicSeedOutcome = "skipped-no-env"
	AnthropicSeedOutcomeSkippedNoBao AnthropicSeedOutcome = "skipped-no-openbao"
	AnthropicSeedOutcomeWriteFailure AnthropicSeedOutcome = "write-failure"
)

// seedAnthropicToken writes the Sovereign's OpenBao
// `secret/catalyst/anthropic/token` path with the platform-supplied
// Anthropic credential so every Org's agenity ExternalSecret resolves
// (issue #4277). Idempotent: PutKVv2 overwrites, so repeated Org-creates
// re-seed harmlessly and keep the (expiring) OAuth blob current.
//
// Returns the terminal outcome so the caller can emit an SSE / log line
// without coupling this helper to the emit machinery. NEVER returns an
// error that fails the Org pipeline — a credential gap or transient
// OpenBao blip must not block the rest of the Org (the agenity HR still
// installs; only its chat-runtime stays offline until the path is
// seeded). The outcome is surfaced loudly instead.
func (h *Handler) seedAnthropicToken(ctx context.Context) AnthropicSeedOutcome {
	// Catalyst-Zero (the orchestrator) has no in-cluster OpenBao client
	// and is never itself an agenity host — skip without noise beyond a
	// debug line. Production Sovereign catalyst-api always has h.openbao
	// wired (SetOpenBao, main.go).
	if h.openbao == nil {
		if h.log != nil {
			h.log.Debug("anthropic seed: skipped — no OpenBao client (orchestrator / Catalyst-Zero)")
		}
		return AnthropicSeedOutcomeSkippedNoBao
	}

	// #6163 FREEZE 1 — read the operator's Secret LIVE, with the process env
	// as the fallback.
	//
	// `CATALYST_ANTHROPIC_API_KEY` / `CATALYST_ANTHROPIC_CREDENTIALS_JSON` are
	// secretKeyRef env vars, and a container's environment is materialised ONCE
	// at container start. Reading them with os.Getenv on every pass meant this
	// ten-minute self-heal loop re-wrote, forever, the credential the process
	// booted with. The seeded value is a claudeAiOauth pair whose accessToken
	// lives hours; when it expired the operator edited
	// catalyst-system/sovereign-anthropic-credentials and NOTHING changed,
	// because the running process could not see the edit. Rotation only ever
	// took effect on a catalyst-api roll, which nothing triggers — so the loop
	// meant to heal the outage was re-applying the stale snapshot that caused
	// it. A self-repeat, not a self-heal.
	//
	// Live read first, env second. The fallback is not politeness: a Sovereign
	// that never adopted the Secret (or a Catalyst-Zero/CI process with no
	// in-cluster identity) keeps exactly its previous behaviour, so this cannot
	// regress an install that works today.
	apiKey, credsJSON := anthropicCredentialFromSecret()
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv(anthropicSeedAPIKeyEnv))
	}
	if credsJSON == "" {
		credsJSON = strings.TrimSpace(os.Getenv(anthropicSeedCredentialsJSONEnv))
	}
	if apiKey == "" && credsJSON == "" {
		// 🛑 FOUNDER-SUPPLIED-SECRET GAP: the platform holds no Anthropic
		// credential. Surface loud + skip rather than seed an empty path
		// that pretends to work (the reflector/ESO empty-seed trap the
		// agenity README §"Why not chart-seed it?" warns about).
		//
		// ERROR, not WARN (#4277). This branch is the single point every
		// caller passes through — the Org-create pipeline at provision
		// time, the catalyst-api startup pass and the 10-minute seed
		// reconciler — so raising it here makes the gap loud at PROVISION
		// TIME from one edit, without any call site having to remember to
		// inspect the returned outcome. It is not a transient condition
		// that might clear on its own: with no source credential the
		// OpenBao path is never written, and every Organization's
		// agenity-anthropic-token ExternalSecret is left permanently
		// SecretSyncedError with its Agenity workspace agent unable to
		// authenticate. A permanently broken delivery is an error.
		if h.log != nil {
			h.log.Error("🛑 anthropic seed SKIPPED — the platform holds no Anthropic credential, so this Sovereign's OpenBao path is NOT written and EVERY Organization's agenity-anthropic-token ExternalSecret will stay SecretSyncedError (#4277)",
				"apiKeyEnv", anthropicSeedAPIKeyEnv,
				"credentialsJsonEnv", anthropicSeedCredentialsJSONEnv,
				"openbaoPath", anthropicSeedMountPath+"/"+anthropicSeedSecretPath,
				"remediation", seedRemediation["anthropic"],
			)
		}
		return AnthropicSeedOutcomeSkippedNoEnv
	}

	// KV-v2 payload. Field names MUST match the agenity ExternalSecret's
	// remoteRef.property values: `apiKey` (anthropic.externalSecret
	// .remoteProperty) + `credentialsJson` (remoteCredentialsProperty).
	// Both keys are always present so the ExternalSecret's optional
	// credentialsJson property resolves cleanly; an empty value for a
	// missing env var is fine (the chart's seed-claude-creds init
	// container treats a 0-byte credentialsJson as key-only mode).
	data := map[string]any{
		"apiKey":          apiKey,
		"credentialsJson": credsJSON,
	}

	if err := h.openbao.PutKVv2(ctx, anthropicSeedMountPath, anthropicSeedSecretPath, data); err != nil {
		if h.log != nil {
			h.log.Error("anthropic seed: OpenBao write failed",
				"openbaoPath", anthropicSeedMountPath+"/"+anthropicSeedSecretPath,
				"err", err,
			)
		}
		return AnthropicSeedOutcomeWriteFailure
	}

	if h.log != nil {
		// Per docs/PRINCIPLES.md #10: byte lengths only, never plaintext.
		h.log.Info("anthropic seed: wrote OpenBao path so every Org's agenity ExternalSecret resolves",
			"openbaoPath", anthropicSeedMountPath+"/"+anthropicSeedSecretPath,
			"apiKeyBytes", len(apiKey),
			"credentialsJsonBytes", len(credsJSON),
		)
	}
	return AnthropicSeedOutcomeSeeded
}
