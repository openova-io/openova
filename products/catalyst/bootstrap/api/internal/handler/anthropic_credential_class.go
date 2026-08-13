// anthropic_credential_class.go — what the platform's Anthropic credential IS,
// as distinct from whether one is configured at all (#4277 / #4111 / #4482).
//
// THE COLLAPSE THIS FILE EXISTS TO UNDO
//
//	The Org-create → workspace-pod seam has to answer three different
//	questions, and until now it answered two:
//
//	  ABSENT   — the Sovereign holds no Anthropic credential at all. Reported:
//	             seedAnthropicToken returns skipped-no-env and logs the founder
//	             gap loudly. Correct, and it has been correct since #4277.
//	  VALID    — the credential is a claudeAiOauth pair the spawned claude-code
//	             can authenticate with. Reported: outcome "seeded".
//	  INVALID  — a credential IS configured and CANNOT work: a bare API key
//	             where an OAuth blob was required, a blob that is not JSON, a
//	             blob with no accessToken, or a blob whose accessToken already
//	             expired. Reported: ***also "seeded"***.
//
//	That third case took the second case's verdict. The producer wrote the
//	unusable value, logged "wrote OpenBao path so every Org's agenity
//	ExternalSecret resolves", and the seed reconciler — which asks only whether
//	`apiKey` OR `credentialsJson` is non-empty — then read the path as HEALTHY
//	and went silent for the life of the cluster. Meanwhile every per-Org
//	Agenity workspace either waited out its credential timeout and exited
//	Init:Error (#6163 FREEZE 2) or came up and 401'd on every agent spawn
//	(FREEZE 3). The Sovereign-level signal said green; the per-Org reality was
//	red; nothing in between reconciled the two.
//
//	Expiry is not a rare edge here. The seeded blob is a claudeAiOauth pair
//	whose accessToken lives HOURS and nothing in this repo refreshes it, so an
//	unrotated credential spends most of its life in the INVALID class.
//
// WHAT THIS FILE PROVIDES
//
//	One classifier, used by both halves of the seam so they cannot disagree:
//	the producer (sovereign_anthropic_seed.go) decides what to write and what
//	to call it, and the seed reconciler (sovereign_seed_reconciler.go) decides
//	whether the STORED value is healthy. A single function means "healthy" at
//	the Sovereign layer and "usable" at the workspace layer are the same
//	predicate rather than two drifting approximations of it.
//
//	The verdict deliberately matches the one the bp-agenity init container
//	reaches for the same blob (products/agenity/chart/templates/statefulset.yaml
//	seed-claude-creds — the `EXPIRED` / `OK` / `NOEXP` parse). Same input, same
//	answer, two layers apart.
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): nothing here returns, logs
// or embeds a token, key or blob — only a class, byte lengths and elapsed
// hours.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// anthropicCredentialClass — the three-way verdict the seam owes its operator,
// with the unusable case split by CAUSE because the remediation differs
// (supply one / fix the shape / rotate it).
type anthropicCredentialClass string

const (
	// anthropicCredAbsent — no credential configured at all. The founder gap
	// #4277 documents. Remediation: supply one.
	anthropicCredAbsent anthropicCredentialClass = "absent"
	// anthropicCredKeyOnly — an apiKey is set but credentialsJson is empty.
	// claude-code authenticates from the OAuth blob, so this cannot spawn an
	// agent: the bp-agenity init container waits out credentialWait and exits
	// non-zero (#6163 FREEZE 2). Remediation: supply credentialsJson.
	anthropicCredKeyOnly anthropicCredentialClass = "key-only"
	// anthropicCredMalformed — credentialsJson is present but is not a
	// claudeAiOauth blob: not JSON, no claudeAiOauth object, or no
	// accessToken. The commonest real shape is a bare `sk-ant-oat01-…` string
	// pasted into the credentialsJson field, which #4111 records as rejected.
	// Remediation: supply the full {"claudeAiOauth":{…}} document.
	anthropicCredMalformed anthropicCredentialClass = "malformed"
	// anthropicCredExpired — a well-formed blob whose accessToken expired.
	// Distinct from malformed because the credential is RIGHT and merely old:
	// remediation is rotation, not a shape fix.
	anthropicCredExpired anthropicCredentialClass = "expired"
	// anthropicCredValid — a claudeAiOauth blob with an accessToken that has
	// not expired (or carries no expiresAt, i.e. a non-expiring credential).
	// The ONLY class that may be described as seeded.
	anthropicCredValid anthropicCredentialClass = "valid"
)

// usable reports whether a credential of this class can actually authenticate a
// spawned agent. Exactly one class qualifies — which is the point: every caller
// asks this instead of re-deriving "well, apiKey is non-empty, close enough".
func (c anthropicCredentialClass) usable() bool { return c == anthropicCredValid }

// anthropicCredentialRemediation — the ONE concrete action that moves a class
// to valid, carried on the log line the sovereign-admin actually finds.
// Per docs/PRINCIPLES.md #10 these name the Secret and the field to populate,
// never a value.
func anthropicCredentialRemediation(c anthropicCredentialClass) string {
	switch c {
	case anthropicCredKeyOnly:
		return "the credentialsJson field is EMPTY — claude-code authenticates from the OAuth blob, not from apiKey. " +
			"Set credentialsJson on catalyst-system/sovereign-anthropic-credentials to the full {\"claudeAiOauth\":{…}} document (#4111)."
	case anthropicCredMalformed:
		return "credentialsJson is not a claudeAiOauth document (a bare sk-ant-… token in that field is rejected). " +
			"Set it to the full {\"claudeAiOauth\":{\"accessToken\":…,\"refreshToken\":…,\"expiresAt\":…}} JSON on " +
			"catalyst-system/sovereign-anthropic-credentials (#4111)."
	case anthropicCredExpired:
		return "the credential is the right SHAPE but its accessToken has expired, and nothing in this platform refreshes it. " +
			"ROTATE catalyst-system/sovereign-anthropic-credentials with a fresh credentialsJson; the next seed pass picks it " +
			"up live with no catalyst-api roll (#6163)."
	case anthropicCredAbsent:
		return seedRemediation["anthropic"]
	}
	return ""
}

// classifyAnthropicCredential returns the class of the (apiKey, credentialsJSON)
// pair plus a credential-free detail string for the log line.
//
// The detail carries byte lengths and elapsed/remaining hours only — never any
// part of the credential itself.
func classifyAnthropicCredential(apiKey, credentialsJSON string) (anthropicCredentialClass, string) {
	apiKey = strings.TrimSpace(apiKey)
	credentialsJSON = strings.TrimSpace(credentialsJSON)

	if credentialsJSON == "" {
		if apiKey == "" {
			return anthropicCredAbsent, "neither apiKey nor credentialsJson is set"
		}
		return anthropicCredKeyOnly, fmt.Sprintf("apiKey is set (%d bytes) but credentialsJson is empty", len(apiKey))
	}

	// UseNumber so a millisecond epoch never round-trips through float64 and
	// loses its low digits — 1_750_000_000_000 is well inside float64's exact
	// integer range today, but the parse should not depend on that holding.
	dec := json.NewDecoder(strings.NewReader(credentialsJSON))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return anthropicCredMalformed, fmt.Sprintf("credentialsJson (%d bytes) is not JSON", len(credentialsJSON))
	}
	oauth, ok := root["claudeAiOauth"].(map[string]any)
	if !ok {
		return anthropicCredMalformed, fmt.Sprintf("credentialsJson (%d bytes) has no claudeAiOauth object", len(credentialsJSON))
	}
	if token, _ := oauth["accessToken"].(string); strings.TrimSpace(token) == "" {
		return anthropicCredMalformed, fmt.Sprintf("credentialsJson (%d bytes) has no claudeAiOauth.accessToken", len(credentialsJSON))
	}

	expiresAtMillis, present := anthropicExpiresAtMillis(oauth["expiresAt"])
	if !present || expiresAtMillis <= 0 {
		// Mirrors the init container's NOEXP arm: no expiresAt is treated as a
		// non-expiring credential, not as a defect.
		return anthropicCredValid, "claudeAiOauth blob with no expiresAt — treated as non-expiring"
	}
	nowMillis := time.Now().UnixMilli()
	if expiresAtMillis <= nowMillis {
		return anthropicCredExpired, fmt.Sprintf("claudeAiOauth accessToken expired ~%dh ago", (nowMillis-expiresAtMillis)/3600000)
	}
	return anthropicCredValid, fmt.Sprintf("claudeAiOauth accessToken valid ~%dh longer", (expiresAtMillis-nowMillis)/3600000)
}

// anthropicExpiresAtMillis normalises the expiresAt field, which real blobs
// carry as a JSON number and some hand-written ones as a string. Returns
// (millis, present).
func anthropicExpiresAtMillis(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		if f, err := t.Float64(); err == nil {
			return int64(f), true
		}
	case float64:
		return int64(t), true
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// anthropicStoredCredentialUsable is the seed reconciler's health predicate for
// `secret/catalyst/anthropic/token`.
//
// It exists because presence is not health here. The reconciler's generic check
// asks whether a named property is non-empty, and by that measure a path
// carrying an apiKey and an empty credentialsJson — or a credentialsJson that
// expired six hours ago — reads HEALTHY and the loop goes quiet, while every
// Agenity workspace on the Sovereign refuses to start. Health is whether the
// STORED value can authenticate an agent, which is precisely what the workspace
// pod asks of the same bytes one layer down.
func anthropicStoredCredentialUsable(data map[string]any) bool {
	apiKey, _ := data["apiKey"].(string)
	credentialsJSON, _ := data["credentialsJson"].(string)
	class, _ := classifyAnthropicCredential(apiKey, credentialsJSON)
	return class.usable()
}

// storedAnthropicCredentialClass reads `secret/catalyst/anthropic/token` and
// classifies what is actually stored there.
//
// The second return is OBSERVED, and callers must honour it: a NotFound is
// positive evidence of absence, but a transport error is evidence of NOTHING
// and must never be rendered as "the stored credential is absent, go ahead and
// overwrite it". Reporting a verdict from absent evidence is how a blip would
// get to clobber a working credential.
func (h *Handler) storedAnthropicCredentialClass(ctx context.Context) (anthropicCredentialClass, bool) {
	if h == nil || h.openbao == nil {
		return anthropicCredAbsent, false
	}
	data, err := h.openbao.GetKVv2(ctx, anthropicSeedMountPath, anthropicSeedSecretPath)
	if err != nil {
		if errors.Is(err, openbao.ErrSecretNotFound) {
			return anthropicCredAbsent, true
		}
		return anthropicCredAbsent, false
	}
	apiKey, _ := data["apiKey"].(string)
	credentialsJSON, _ := data["credentialsJson"].(string)
	class, _ := classifyAnthropicCredential(apiKey, credentialsJSON)
	return class, true
}
