// sovereign_anthropic_refresh.go — keep the Sovereign's Anthropic OAuth
// credential ALIVE, instead of delivering an expired one faithfully (#6317).
//
// THE GAP THIS FILE CLOSES
//
//	anthropic_credential_class.go already states it, in its own words:
//
//	  "Expiry is not a rare edge here. The seeded blob is a claudeAiOauth pair
//	   whose accessToken lives HOURS and nothing in this repo refreshes it, so
//	   an unrotated credential spends most of its life in the INVALID class."
//
//	Every producer in the delivery chain re-applies whatever the root Secret
//	holds. None of them can make an expired blob usable, so the chain's steady
//	state is: deliver an expired credential, correctly, forever. Measured on
//	hw296 2026-08-14 — the workspace agent answered its own PTY with
//	"Please run /login · API Error: 401 OAuth access token has been revoked"
//	roughly four hours after G8/G9 were walked green. Nothing regressed; the
//	credential simply died, and rows 219/220/221/G8/G9 flap on that one cause.
//
//	The refresh material was alive and unspent the whole time
//	(refreshTokenExpiresAt ~4 weeks out). Only the exchange was missing.
//
// WHY HERE AND NOT IN A CRONJOB
//
//	products/axon/chart/templates/token-refresh-cronjob.yaml performs the right
//	OAuth exchange, but its WRITE TARGET does not transfer to Agenity:
//
//	  - Axon rewrites its own K8s Secret. Every per-Org agenity-anthropic-token
//	    Secret is ExternalSecret-managed with creationPolicy: Owner, so ESO
//	    overwrites the target on each sync and a CronJob write survives at most
//	    one refreshInterval.
//	  - Axon has ONE Secret in ONE namespace. Agenity has one per Organization.
//	  - Writing OpenBao alone would strand the root Secret: the exchange ROTATES
//	    the refresh token, so the root copy becomes dead material, and the seed
//	    reconciler — which re-seeds from the root whenever the stored blob is
//	    judged unusable — would eventually overwrite a working credential with
//	    an unrecoverable one.
//
//	The refresh belongs where the live read, the OpenBao write, the expiry
//	classifier and the loud-fail machinery already are: the seed reconciler.
//	One implementation serves every Organization, because the credential is
//	Sovereign-global.
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): nothing here logs, returns
// or embeds token material — only classes, byte lengths, sha256 prefixes and
// elapsed/remaining minutes.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// anthropicOAuthTokenEndpoint / anthropicOAuthClientID — the same exchange
// products/axon/chart/templates/token-refresh-cronjob.yaml performs. A var, not
// a const, so a test can point it at an httptest server without a network hop.
var (
	anthropicOAuthTokenEndpoint = "https://console.anthropic.com/v1/oauth/token"
	anthropicOAuthHTTPClient    = &http.Client{Timeout: 20 * time.Second}
)

const anthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

// anthropicRefreshLeadTime — how much life must REMAIN on the accessToken
// before a pass leaves it alone. Refresh happens BEFORE expiry, never after.
//
// THE MARGIN, DERIVED (#6317). The observed accessToken lifetime is ~5h
// (hw296: issued ~23:28Z, expiresAt 04:28:12Z). What has to fit inside the
// window is the whole propagation chain, worst case:
//
//	OpenBao write ............................. immediate
//	ExternalSecret refreshInterval ............ 15m  (agenity values.yaml)
//	kubelet Secret re-projection into /creds ... ~1m
//	emptyDir credential re-sync ............... 2m   (agenity sidecar)
//	                                            ────
//	                                            ~18m
//
// A 2h window leaves ~1h42m of slack on top of that, and the 10m reconciler
// cadence gives TWELVE attempts inside it — so the Anthropic endpoint can be
// unreachable for well over an hour and the credential still turns over before
// it dies. An hourly-or-worse schedule against a ~5h token, by contrast,
// guarantees a dead window on any single missed run.
const anthropicRefreshLeadTime = 2 * time.Hour

// anthropicRefreshOutcome — the VERIFIED result of one refresh attempt.
//
// A value, not a log line, so tests assert on the outcome instead of on prose.
// Every non-success value below is also logged at ERROR with its remediation:
// the failure mode this file exists to prevent is a refresh that quietly does
// nothing and leaves an expired token in place, which is indistinguishable
// from a refresh that worked if you only look at whether the pass returned.
type anthropicRefreshOutcome string

const (
	// anthropicRefreshNotDue — the credential is valid with more than
	// anthropicRefreshLeadTime remaining. The steady state; not an error.
	anthropicRefreshNotDue anthropicRefreshOutcome = "not-due"
	// anthropicRefreshNoCredential — nothing configured to refresh. The
	// #4277 founder gap, already reported loudly by the seed leg.
	anthropicRefreshNoCredential anthropicRefreshOutcome = "no-credential"
	// anthropicRefreshNoRefreshToken — a blob that cannot be refreshed at
	// all: key-only, malformed, or an OAuth pair with no refreshToken. The
	// credential WILL die and only a human rotation can prevent it.
	anthropicRefreshNoRefreshToken anthropicRefreshOutcome = "no-refresh-token"
	// anthropicRefreshExchangeFailed — the OAuth exchange did not return a
	// usable access token.
	anthropicRefreshExchangeFailed anthropicRefreshOutcome = "exchange-failed"
	// anthropicRefreshPersistFailed — the exchange SUCCEEDED and the result
	// could not be stored. The worst outcome: the old refresh token has
	// already been spent, so this must never pass silently.
	anthropicRefreshPersistFailed anthropicRefreshOutcome = "persist-failed"
	// anthropicRefreshNoDurableStore — the credential is due for renewal and
	// there is nowhere durable to put the result, so the exchange is NOT
	// attempted. Refusing to start is the whole point: see the pre-flight.
	anthropicRefreshNoDurableStore anthropicRefreshOutcome = "no-durable-store"
	// anthropicRefreshed — a fresh accessToken is stored and propagating.
	anthropicRefreshed anthropicRefreshOutcome = "refreshed"
	// anthropicRefreshTokenSpent — the stored refresh token is one the provider
	// has ALREADY definitively rejected (HTTP 400/401/403) on an earlier pass,
	// and the stored credential has not changed since. The exchange is NOT
	// re-attempted: a spent or revoked refresh token never recovers, so
	// re-sending it every pass only earns rate limits (hw305: HTTP 429 after
	// weeks of 10-minute retries) and buries the one line that matters —
	// rotate the root Secret. Reported loudly, every pass, until the operator
	// does.
	anthropicRefreshTokenSpent anthropicRefreshOutcome = "refresh-token-spent"
)

// anthropicRejectedRefresh remembers the ONE refresh token the OAuth endpoint
// last rejected definitively, by fingerprint only (never the token). Lives on
// the Handler so a catalyst-api restart forgets it — one retry per restart is
// cheap, and a token the provider has spent will simply be rejected again.
type anthropicRejectedRefresh struct {
	mu     sync.Mutex
	fp     string
	status int
	at     time.Time
}

func (m *anthropicRejectedRefresh) record(refreshToken string, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fp, m.status, m.at = credFingerprint(refreshToken), status, time.Now()
}

// match reports whether refreshToken is the one previously rejected.
func (m *anthropicRejectedRefresh) match(refreshToken string) (status int, at time.Time, rejected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fp == "" || m.fp != credFingerprint(refreshToken) {
		return 0, time.Time{}, false
	}
	return m.status, m.at, true
}

// anthropicOAuthStatusError — the token endpoint answered with a non-2xx
// status. The body is never carried: on some error shapes it holds token
// material. Status 400/401/403 are DEFINITIVE for a refresh_token grant
// (invalid_grant / revoked / forbidden): the same token will never succeed.
// Everything else (429, 5xx, 408) is transient and is retried next pass.
type anthropicOAuthStatusError struct {
	Status    int
	BodyBytes int
}

func (e *anthropicOAuthStatusError) Error() string {
	return fmt.Sprintf("oauth endpoint returned HTTP %d (%d-byte body withheld: may carry credential material)",
		e.Status, e.BodyBytes)
}

func (e *anthropicOAuthStatusError) definitive() bool {
	return e.Status == http.StatusBadRequest || e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// credFingerprint — first 8 hex chars of the sha256 of a credential, so two
// log lines can be compared for "did this actually change?" without any part
// of the credential appearing in a log. Never reversible to the token.
func credFingerprint(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// refreshAnthropicCredential renews the Sovereign's Anthropic OAuth credential
// when it is at or past anthropicRefreshLeadTime of its life, and stores the
// result so the whole delivery chain picks it up.
//
// Reads the credential from the same live source the producer uses, so an
// operator rotation and a refresh can never disagree about what "current" is.
func (h *Handler) refreshAnthropicCredential(ctx context.Context) anthropicRefreshOutcome {
	apiKey, credsJSON := anthropicCredentialFromSecret()
	if credsJSON == "" {
		// Env fallback mirrors the producer: a Sovereign that never adopted the
		// Secret, or a Catalyst-Zero process with no in-cluster identity, keeps
		// exactly its previous behaviour.
		apiKey, credsJSON = anthropicCredentialFromEnv(apiKey, credsJSON)
	}
	if strings.TrimSpace(credsJSON) == "" {
		// Absent is the seed leg's story to tell, and it already tells it at
		// ERROR with the founder remediation. Saying it twice per pass would
		// bury the refresh-specific failures below.
		return anthropicRefreshNoCredential
	}

	oauth, remaining, ok := anthropicOAuthFields(credsJSON)
	if !ok || strings.TrimSpace(stringField(oauth, "refreshToken")) == "" {
		if h.log != nil {
			class, detail := classifyAnthropicCredential(apiKey, credsJSON)
			h.log.Error("🛑 anthropic refresh IMPOSSIBLE — the stored credential carries no usable claudeAiOauth.refreshToken, so it CANNOT be renewed and will stay dead once its accessToken expires (#6317)",
				"credentialClass", string(class),
				"detail", detail,
				"credentialsJsonBytes", len(credsJSON),
				"remediation", "supply a full {\"claudeAiOauth\":{\"accessToken\":…,\"refreshToken\":…,\"expiresAt\":…}} document on "+
					anthropicCredentialNamespace+"/"+anthropicCredentialSecret+" (key "+anthropicCredentialJSONKey+"). "+
					"A blob without refreshToken must be re-issued by hand every few hours and no platform component can prevent that.",
			)
		}
		return anthropicRefreshNoRefreshToken
	}

	// Refresh BEFORE expiry. An already-expired blob still refreshes — the
	// refresh token outlives the access token by weeks, which is exactly the
	// hw296 state this file was written for — so `remaining <= 0` is a due
	// case, not a give-up case.
	if remaining > anthropicRefreshLeadTime {
		return anthropicRefreshNotDue
	}

	// PRE-FLIGHT: never spend a refresh token you cannot store.
	//
	// The exchange ROTATES the refresh token — the old one is consumed whether
	// or not we manage to keep the new one. So a renewal attempted with nowhere
	// durable to write is not a harmless failure: it burns the only material
	// that could have renewed the credential later, and each subsequent pass
	// retries with a token the provider has already rotated away. That converts
	// a credential which merely EXPIRES into one that cannot be recovered
	// without a human re-issue.
	//
	// Two conditions, both real, both checked BEFORE the exchange, not after:
	//   - no in-cluster identity (a Catalyst-Zero or CI process), where the
	//     credential can only have come from the process env and there is no
	//     Secret to write back to;
	//   - an in-cluster identity but NO root Secret — the credential reached
	//     this process through chart values (sovereign.anthropic.* → the
	//     kc-credentials Secret → env) and nobody created
	//     catalyst-system/sovereign-anthropic-credentials. The write-back is
	//     a strategic-merge PATCH, which cannot create; before this check the
	//     exchange ran, the PATCH 404'd, and the ROTATED refresh token was
	//     lost on the first pass — the values copy was dead from then on.
	if _, err := anthropicSecretRootWritable(); err != nil {
		if h.log != nil {
			remediation := "this process has no in-cluster identity, so it can read the credential from its env but cannot write a renewed one back. " +
				"Run the refresh where catalyst-api holds a ServiceAccount and the " + anthropicCredentialSecret + " Secret exists; " +
				"until then the credential must be rotated by hand before it expires."
			if apierrors.IsNotFound(err) {
				remediation = "create the root Secret from the SAME credential this process holds — " +
					"kubectl -n " + anthropicCredentialNamespace + " create secret generic " + anthropicCredentialSecret +
					" --from-literal=" + anthropicCredentialJSONKey + "='<credentialsJson>' (and " + anthropicCredentialAPIKeyKey + " if set). " +
					"The chart's lookup source-wins it over the values copy, the live read (#6163) picks it up on the next pass, " +
					"and every renewal is stored there from then on. One kubectl, once."
			}
			h.log.Error("🛑 anthropic refresh SKIPPED — the credential is due for renewal but there is nowhere durable to store the result, and the exchange ROTATES the refresh token. Attempting it would burn the only material that can renew this credential (#6317)",
				"err", err.Error(),
				"rootSecret", anthropicCredentialNamespace+"/"+anthropicCredentialSecret,
				"rootSecretAbsent", apierrors.IsNotFound(err),
				"remainingMin", int(remaining.Minutes()),
				"remediation", remediation,
			)
		}
		return anthropicRefreshNoDurableStore
	}

	oldAccess := strings.TrimSpace(stringField(oauth, "accessToken"))
	oldRefresh := strings.TrimSpace(stringField(oauth, "refreshToken"))

	// A refresh token the provider has ALREADY rejected definitively is not
	// re-sent. It cannot start working again, and the credential it belongs to
	// has not changed (same fingerprint) — the only thing that clears this is
	// an operator rotation, which changes the token and therefore the key.
	if status, at, rejected := h.anthropicRefreshRejected.match(oldRefresh); rejected {
		if h.log != nil {
			h.log.Error("🛑 anthropic refresh NOT RETRIED — the stored refresh token was already rejected by the OAuth endpoint and has not changed since; it is SPENT or REVOKED and will never renew this credential. Every Organization's Agenity workspace agent 401s until the root Secret is rotated (#6317)",
				"rejectedHTTPStatus", status,
				"rejectedAt", at.UTC().Format(time.RFC3339),
				"refreshTokenSha256Prefix", credFingerprint(oldRefresh),
				"remainingMin", int(remaining.Minutes()),
				"rootSecret", anthropicCredentialNamespace+"/"+anthropicCredentialSecret,
				"remediation", "rotate "+anthropicCredentialNamespace+"/"+anthropicCredentialSecret+" with a FRESHLY issued credentialsJson from a "+
					"DEDICATED claude-code login that no other Sovereign or the mothership shares — a refresh token is single-use, so two "+
					"refreshers on one credential spend each other's token. The next pass exchanges the new token automatically.",
			)
		}
		return anthropicRefreshTokenSpent
	}

	if h.log != nil {
		h.log.Info("[ANTHROPIC-REFRESH] credential is within the renewal window — exchanging refresh token (#6317)",
			"remainingMin", int(remaining.Minutes()),
			"leadTimeMin", int(anthropicRefreshLeadTime.Minutes()),
			"accessTokenSha256Prefix", credFingerprint(oldAccess),
		)
	}

	tok, err := exchangeAnthropicRefreshToken(ctx, oldRefresh)
	if err != nil {
		// A definitive 4xx means THIS refresh token is spent or revoked. Remember
		// its fingerprint so the next pass reports the verdict instead of
		// re-spending the request; a transient status (429/5xx) or a transport
		// error is retried next pass as before.
		definitive := false
		var statusErr *anthropicOAuthStatusError
		if errors.As(err, &statusErr) && statusErr.definitive() {
			definitive = true
			h.anthropicRefreshRejected.record(oldRefresh, statusErr.Status)
		}
		if h.log != nil {
			remediation := "if this persists past the remaining lifetime, the refresh token itself is spent or revoked — rotate " +
				anthropicCredentialNamespace + "/" + anthropicCredentialSecret + " with a freshly issued credentialsJson."
			if definitive {
				remediation = "the OAuth endpoint rejected this refresh token DEFINITIVELY (spent or revoked) — it will not be re-sent. Rotate " +
					anthropicCredentialNamespace + "/" + anthropicCredentialSecret + " with a freshly issued credentialsJson from a DEDICATED " +
					"claude-code login no other refresher shares; the next pass exchanges the new token automatically."
			}
			h.log.Error("🛑 anthropic refresh FAILED — the OAuth exchange did not return a usable access token; the credential is UNCHANGED and will expire on schedule, after which every Organization's Agenity workspace agent 401s (#6317)",
				"err", err.Error(),
				"definitive", definitive,
				"endpoint", anthropicOAuthTokenEndpoint,
				"remainingMin", int(remaining.Minutes()),
				"remediation", remediation,
			)
		}
		return anthropicRefreshExchangeFailed
	}

	newCredsJSON, err := anthropicCredentialWithRefreshedTokens(credsJSON, tok)
	if err != nil {
		if h.log != nil {
			h.log.Error("🛑 anthropic refresh FAILED — the exchange succeeded but the refreshed credential could not be rebuilt; the OLD refresh token has already been SPENT (#6317)",
				"err", err.Error(),
			)
		}
		return anthropicRefreshPersistFailed
	}

	// apiKey is rewritten ONLY when it was a COPY of the access token — which
	// is how the seed pair is issued (measured on hw296: both 108 bytes, same
	// sha256 prefix, both sk-ant-oat…). An apiKey that DIFFERS is an
	// independent, possibly long-lived key the operator supplied on purpose,
	// and overwriting it with a 5h OAuth access token would destroy it.
	newAPIKey := apiKey
	if strings.TrimSpace(apiKey) != "" && strings.TrimSpace(apiKey) == oldAccess {
		newAPIKey = tok.AccessToken
	}

	if err := h.persistRefreshedAnthropicCredential(ctx, newAPIKey, newCredsJSON); err != nil {
		if h.log != nil {
			h.log.Error("🛑 anthropic refresh FAILED TO PERSIST — a FRESH credential was obtained and could not be stored, and the OLD refresh token has already been SPENT by the exchange. The next pass will retry with a refresh token the provider may have already rotated away (#6317)",
				"err", err.Error(),
				"rootSecret", anthropicCredentialNamespace+"/"+anthropicCredentialSecret,
				"openbaoPath", anthropicSeedMountPath+"/"+anthropicSeedSecretPath,
				"remediation", "grant catalyst-api update/patch on the root Secret (catalyst chart clusterrole-cutover-driver.yaml) "+
					"and confirm OpenBao is reachable. If the retry loop reports exchange-failed from here on, the refresh token was rotated "+
					"and lost — rotate the root Secret with a freshly issued credentialsJson.",
			)
		}
		return anthropicRefreshPersistFailed
	}

	if h.log != nil {
		// Byte lengths + fingerprints only. The two DIFFERENT prefixes are the
		// evidence that the value actually turned over — a refresh that wrote
		// the same bytes back would show identical prefixes here.
		h.log.Info("[ANTHROPIC-REFRESH] ✅ credential refreshed and stored — every Organization's Agenity workspace picks it up through the normal ExternalSecret path (#6317)",
			"oldAccessTokenSha256Prefix", credFingerprint(oldAccess),
			"newAccessTokenSha256Prefix", credFingerprint(tok.AccessToken),
			"refreshTokenRotated", credFingerprint(oldRefresh) != credFingerprint(tok.RefreshToken),
			"newLifetimeMin", int(time.Until(tok.ExpiresAt).Minutes()),
			"credentialsJsonBytes", len(newCredsJSON),
			"apiKeyRewritten", newAPIKey != apiKey,
		)
	}
	return anthropicRefreshed
}

// anthropicCredentialFromEnv — the producer's env fallback, factored out so the
// refresh reads the credential from exactly the same places in exactly the same
// order. Values already found take precedence.
func anthropicCredentialFromEnv(apiKey, credsJSON string) (string, string) {
	if strings.TrimSpace(apiKey) == "" {
		apiKey = strings.TrimSpace(os.Getenv(anthropicSeedAPIKeyEnv))
	}
	if strings.TrimSpace(credsJSON) == "" {
		credsJSON = strings.TrimSpace(os.Getenv(anthropicSeedCredentialsJSONEnv))
	}
	return apiKey, credsJSON
}

// anthropicOAuthFields parses the claudeAiOauth object and the REMAINING life
// of its accessToken. ok=false when the document is not a claudeAiOauth blob.
//
// A blob with no expiresAt reports a remaining life far past any lead time, so
// the "non-expiring credential" case the classifier recognises is never
// needlessly refreshed.
func anthropicOAuthFields(credentialsJSON string) (map[string]any, time.Duration, bool) {
	dec := json.NewDecoder(strings.NewReader(credentialsJSON))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, 0, false
	}
	oauth, ok := root["claudeAiOauth"].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	expiresAtMillis, present := anthropicExpiresAtMillis(oauth["expiresAt"])
	if !present || expiresAtMillis <= 0 {
		return oauth, time.Duration(1<<62 - 1), true
	}
	return oauth, time.Until(time.UnixMilli(expiresAtMillis)), true
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// anthropicRefreshedToken — the fields the OAuth exchange returns that matter.
type anthropicRefreshedToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	// RefreshTokenExpiresAt is zero when the provider did not say.
	RefreshTokenExpiresAt time.Time
}

// exchangeAnthropicRefreshToken performs the refresh_token grant.
//
// Parsed with encoding/json, not with grep over the response body: the shell
// implementation in the Axon CronJob extracts fields by regex and rebuilds the
// document from the ones it knows, which silently DROPS every field it does not
// (refreshTokenExpiresAt among them). Here the response is decoded and the
// stored document is edited in place, so unknown fields survive a refresh.
func exchangeAnthropicRefreshToken(ctx context.Context, refreshToken string) (anthropicRefreshedToken, error) {
	var out anthropicRefreshedToken

	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     anthropicOAuthClientID,
	})
	if err != nil {
		return out, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicOAuthTokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := anthropicOAuthHTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("post %s: %w", anthropicOAuthTokenEndpoint, err)
	}
	defer resp.Body.Close()

	// Cap the read: a hung or hostile endpoint must not be able to grow this
	// process's heap through a token refresh.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body may carry the provider's error CODE, but it may also carry
		// token material on some error shapes — report the status only. Typed,
		// so the caller can tell a definitive rejection from a transient one.
		return out, &anthropicOAuthStatusError{Status: resp.StatusCode, BodyBytes: len(raw)}
	}

	var parsed struct {
		AccessToken           string      `json:"access_token"`
		RefreshToken          string      `json:"refresh_token"`
		ExpiresIn             json.Number `json:"expires_in"`
		RefreshTokenExpiresIn json.Number `json:"refresh_token_expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out, fmt.Errorf("decode response (%d bytes): %w", len(raw), err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		// A 200 with no access_token is a FAILED refresh wearing a success
		// status. Treated as an error so it can never be stored as a credential.
		return out, fmt.Errorf("oauth endpoint returned HTTP %d with no access_token (%d-byte body)", resp.StatusCode, len(raw))
	}

	out.AccessToken = strings.TrimSpace(parsed.AccessToken)
	// Providers rotate refresh tokens; when this one does not, the existing
	// token stays valid and is carried forward by the caller.
	out.RefreshToken = strings.TrimSpace(parsed.RefreshToken)
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	if secs, err := parsed.ExpiresIn.Int64(); err == nil && secs > 0 {
		out.ExpiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	}
	if secs, err := parsed.RefreshTokenExpiresIn.Int64(); err == nil && secs > 0 {
		out.RefreshTokenExpiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return out, nil
}

// anthropicCredentialWithRefreshedTokens rewrites accessToken / refreshToken /
// expiresAt (and refreshTokenExpiresAt when the provider supplied one) INSIDE
// the existing document, preserving every other field — scopes,
// subscriptionType, rateLimitTier and anything the provider adds later.
func anthropicCredentialWithRefreshedTokens(credentialsJSON string, tok anthropicRefreshedToken) (string, error) {
	dec := json.NewDecoder(strings.NewReader(credentialsJSON))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return "", fmt.Errorf("decode stored credential: %w", err)
	}
	oauth, ok := root["claudeAiOauth"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("stored credential has no claudeAiOauth object")
	}
	oauth["accessToken"] = tok.AccessToken
	oauth["refreshToken"] = tok.RefreshToken
	if !tok.ExpiresAt.IsZero() {
		oauth["expiresAt"] = json.Number(fmt.Sprintf("%d", tok.ExpiresAt.UnixMilli()))
	}
	if !tok.RefreshTokenExpiresAt.IsZero() {
		oauth["refreshTokenExpiresAt"] = json.Number(fmt.Sprintf("%d", tok.RefreshTokenExpiresAt.UnixMilli()))
	}
	root["claudeAiOauth"] = oauth

	buf, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("re-marshal credential: %w", err)
	}
	return string(buf), nil
}

// persistRefreshedAnthropicCredential stores the refreshed pair in BOTH places
// the delivery chain reads.
//
// ORDER IS LOAD-BEARING. The root Secret is written FIRST because it is the
// durable one: it survives a catalyst-api restart, and it is what the seed
// producer re-reads on every pass. The exchange ROTATES the refresh token, so a
// refreshed blob that reached OpenBao but not the root Secret would leave the
// root holding SPENT refresh material — and the seed reconciler would then
// happily re-seed that dead value over a working OpenBao path the moment the
// stored blob was judged unusable.
//
// OpenBao is written second so ESO propagates immediately. If only that write
// fails, the next reconcile pass reads the refreshed root Secret, finds the
// stored path unhealthy, and the existing seed leg repairs it — so this half is
// self-healing and the error is reported rather than retried here.
func (h *Handler) persistRefreshedAnthropicCredential(ctx context.Context, apiKey, credentialsJSON string) error {
	if err := writeAnthropicRootSecret(ctx, apiKey, credentialsJSON); err != nil {
		return fmt.Errorf("root Secret %s/%s: %w", anthropicCredentialNamespace, anthropicCredentialSecret, err)
	}
	if h.openbao == nil {
		return nil
	}
	if err := h.openbao.PutKVv2(ctx, anthropicSeedMountPath, anthropicSeedSecretPath, map[string]any{
		"apiKey":          apiKey,
		"credentialsJson": credentialsJSON,
	}); err != nil {
		return fmt.Errorf("openbao %s/%s: %w", anthropicSeedMountPath, anthropicSeedSecretPath, err)
	}
	return nil
}

// writeAnthropicRootSecret patches the operator-rotatable root Secret.
//
// A strategic-merge PATCH, not an Update: it touches only the two credential
// keys and cannot clobber anything else an operator keeps in that Secret, and
// it needs no read-modify-write round trip to avoid a conflict.
//
// `data` with base64 values, not `stringData`: stringData is a write-only
// convenience the API server folds into data, so a patch built from it cannot
// be asserted against the object it produces without trusting that server-side
// conversion. Writing data directly makes the stored bytes the same under a
// real apiserver and under the fake clientset the tests drive.
// anthropicSecretRootWritable reports whether there is somewhere DURABLE to
// store a renewed credential: an in-cluster identity AND the root Secret
// itself. Used as the pre-flight above, and by writeAnthropicRootSecret
// itself, so the check and the write can never disagree about what
// "reachable" means.
//
// The existence check is load-bearing, not a nicety. The write-back is a
// strategic-merge PATCH, which cannot create. A Sovereign whose credential
// arrived through chart values (sovereign.anthropic.* → env) and whose
// operator never created catalyst-system/sovereign-anthropic-credentials has
// a working credential and NO root Secret; before this check the exchange ran
// first, the PATCH 404'd, and the rotated refresh token was lost on the very
// first renewal pass. A NotFound is returned as-is so the caller can name the
// one-kubectl remediation; any other read error is "we do not know", which
// for a refresh token that is consumed on use must also mean do-not-spend.
//
// It deliberately does NOT probe RBAC with a speculative write: the shipped
// ClusterRole grants the name-scoped patch, and a dry-run would double every
// refresh's API traffic to defend against a misconfiguration that the persist
// failure already reports loudly.
func anthropicSecretRootWritable() (kubernetes.Interface, error) {
	cli, err := anthropicSecretClientFor()
	if err != nil {
		return nil, err
	}
	if cli == nil {
		return nil, fmt.Errorf("no in-cluster identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), anthropicCredentialReadBudget)
	defer cancel()
	if _, err := cli.CoreV1().Secrets(anthropicCredentialNamespace).
		Get(ctx, anthropicCredentialSecret, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, err
		}
		return nil, fmt.Errorf("read root Secret %s/%s: %w", anthropicCredentialNamespace, anthropicCredentialSecret, err)
	}
	return cli, nil
}

func writeAnthropicRootSecret(ctx context.Context, apiKey, credentialsJSON string) error {
	cli, err := anthropicSecretRootWritable()
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{
		"data": map[string]string{
			anthropicCredentialAPIKeyKey: base64.StdEncoding.EncodeToString([]byte(apiKey)),
			anthropicCredentialJSONKey:   base64.StdEncoding.EncodeToString([]byte(credentialsJSON)),
		},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, anthropicCredentialReadBudget)
	defer cancel()
	_, err = cli.CoreV1().Secrets(anthropicCredentialNamespace).
		Patch(ctx, anthropicCredentialSecret, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}
