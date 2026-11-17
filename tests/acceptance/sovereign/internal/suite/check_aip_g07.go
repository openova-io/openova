package suite

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check AIP-G07, Agent tokens. catalyst-api issues a short-lived token for
// one agent of one Organization to an in-cluster workload named in
// CATALYST_AGENT_TOKEN_CALLERS (docs/contracts/model-contract.md section 2).
// The chart's default entry lets the ServiceAccount
// catalyst-system/catalyst-conformance ask for agent conformance-probe of
// Organization conformance at tier viewer. The check records nine steps,
// in the first region, the region the API runs in:
//
//  1. the API server of the region creates a token of 10 minutes for that
//     ServiceAccount (`kubectl create token`); it is not stored;
//  2. POST /api/v1/internal/agent-tokens with that token answers 200 with a
//     token whose header names a key and whose claims are typ agent,
//     org_id conformance, tier viewer and an audience with catalyst-api;
//  3. GET /api/v1/auth/signers.jwks answers 200 with one key, the key the
//     token names, and the token's signature verifies with it;
//  4. with the agent token, GET /api/v1/refs/organizations answers 200 and
//     lists no Organization but conformance: it reads in its own
//     Organization;
//  5. GET /api/v1/sovereigns/<deployment>/applications answers 403
//     org-scoped-forbidden: a route outside the Organization is refused;
//  6. POST /api/v1/org/applications answers 403 agent-token-forbidden: a
//     route that changes state is refused;
//  7. a request for tier admin answers 422 invalid-request;
//  8. a request for Organization other answers 403 caller-not-allowed;
//  9. a token of 60 seconds reads at once, and 90 seconds after it was
//     minted the same read answers 401: an expired token is refused.
//
// No token is printed or kept in the record: the evidence holds statuses,
// error codes, claims and the key identifier.
const aipG07ID = "AIP-G07"

const (
	aipG07NS        = "catalyst-system"      // namespace of the caller's ServiceAccount
	aipG07SA        = "catalyst-conformance" // the caller of the chart's default entry
	aipG07Org       = "conformance"          // the Organization of that entry
	aipG07Agent     = "conformance-probe"    // the agent of that entry
	aipG07Issue     = "/api/v1/internal/agent-tokens"
	aipG07KeySet    = "/api/v1/auth/signers.jwks"
	aipG07Read      = "/api/v1/refs/organizations"
	aipG07Write     = "/api/v1/org/applications"
	aipG07ShortLife = 60               // seconds, the token of step 9
	aipG07Late      = 90 * time.Second // when step 9 reads again, from the mint
)

type agentTokenCheck struct {
	kubectl kubectlFunc
	now     func() time.Time
	wait    func(ctx context.Context, d time.Duration) error // waits d, or until ctx ends
}

func init() {
	RegisterCheck(agentTokenCheck{kubectl: kube.Region.Run, now: time.Now, wait: agentTokenWait})
}

func (agentTokenCheck) ID() string { return aipG07ID }

func agentTokenWait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// agentAnswer is one answer of the API: its status, the error code of its
// body and the body as a JSON object (nil when the body is not one).
type agentAnswer struct {
	status int
	code   string
	body   map[string]any
}

func (c agentTokenCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if cc.API == nil || cc.API.Base == "" {
		return errors.New("the run has no console address")
	}
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	region := cc.Regions[0]
	client := cc.API.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	call := func(method, path, bearer string, body any) (agentAnswer, error) {
		return agentCall(ctx, client, cc.API.Base, method, path, bearer, body)
	}
	deployment := "aip-g07"
	if cc.Deploy != nil && cc.Deploy.ID != "" {
		deployment = cc.Deploy.ID
	}
	n := 0
	step := func(name string, fn func(ev Evidence) error) bool {
		n++
		return checkStep(rec, aipG07ID, n, name, fn)
	}
	// caller is the ServiceAccount token of step 1; mint asks the issue
	// route with it.
	var caller string
	mint := func(org, tier string, life int) (agentAnswer, error) {
		body := map[string]any{"organization": org, "agent": aipG07Agent, "tier": tier}
		if life > 0 {
			body["ttlSeconds"] = life
		}
		return call(http.MethodPost, aipG07Issue, caller, body)
	}

	if !step("the API server of region "+region.ID+" creates a token of ServiceAccount "+aipG07NS+"/"+aipG07SA, func(ev Evidence) error {
		out, err := c.kubectl(region, ctx, nil, "-n", aipG07NS, "create", "token", aipG07SA, "--duration=10m")
		if err != nil {
			return err
		}
		caller = strings.TrimSpace(string(out))
		if strings.Count(caller, ".") != 2 {
			return fmt.Errorf("the API server returned no token for %s/%s", aipG07NS, aipG07SA)
		}
		ev["service_account"] = aipG07NS + "/" + aipG07SA
		return nil
	}) {
		return nil
	}
	var token, kid string
	if !step("the issue route mints a viewer token for agent "+aipG07Agent+" of Organization "+aipG07Org, func(ev Evidence) error {
		a, err := mint(aipG07Org, "viewer", 0)
		if err != nil {
			return err
		}
		ev["status"], ev["error"] = a.status, a.code
		if a.status != http.StatusOK {
			return fmt.Errorf("POST %s answered %d %s, want 200", aipG07Issue, a.status, a.code)
		}
		token, _ = a.body["token"].(string)
		header, claims, err := jwtParts(token)
		if err != nil {
			return err
		}
		kid, _ = header["kid"].(string)
		ev["kid"], ev["expires_at"] = kid, a.body["expiresAt"]
		for _, k := range []string{"typ", "org_id", "agent", "tier", "aud", "sub"} {
			ev["claim_"+k] = claims[k]
		}
		switch {
		case header["alg"] != "RS256" || kid == "":
			return fmt.Errorf("the token header is %v, want alg RS256 and a kid", header)
		case claims["typ"] != "agent" || claims["org_id"] != aipG07Org || claims["agent"] != aipG07Agent || claims["tier"] != "viewer":
			return fmt.Errorf("the token claims typ %v, org_id %v, agent %v, tier %v; want agent, %s, %s, viewer",
				claims["typ"], claims["org_id"], claims["agent"], claims["tier"], aipG07Org, aipG07Agent)
		case !holds(claims["aud"], "catalyst-api"):
			return fmt.Errorf("the token audience %v does not hold catalyst-api", claims["aud"])
		}
		return nil
	}) {
		return nil
	}

	step("the key set route lists the key the token names, and the signature verifies with it", func(ev Evidence) error {
		a, err := call(http.MethodGet, aipG07KeySet, "", nil)
		if err != nil {
			return err
		}
		ev["status"] = a.status
		if a.status != http.StatusOK {
			return fmt.Errorf("GET %s answered %d %s, want 200", aipG07KeySet, a.status, a.code)
		}
		keys, _ := a.body["keys"].([]any)
		ev["keys"] = len(keys)
		if len(keys) != 1 {
			return fmt.Errorf("GET %s lists %d keys, want 1", aipG07KeySet, len(keys))
		}
		key, _ := keys[0].(map[string]any)
		ev["key_kid"] = key["kid"]
		if key["kid"] != kid {
			return fmt.Errorf("the key set names key %v, the token names %s", key["kid"], kid)
		}
		return verifyRS256(token, key)
	})

	step("the agent token reads in Organization "+aipG07Org+" and sees no other Organization", func(ev Evidence) error {
		a, err := call(http.MethodGet, aipG07Read, token, nil)
		if err != nil {
			return err
		}
		ev["status"], ev["error"] = a.status, a.code
		if a.status != http.StatusOK {
			return fmt.Errorf("GET %s answered %d %s, want 200", aipG07Read, a.status, a.code)
		}
		orgs, ok := a.body["organizations"].([]any)
		if !ok {
			return fmt.Errorf("GET %s answered no list organizations", aipG07Read)
		}
		names := []string{}
		for _, o := range orgs {
			m, _ := o.(map[string]any)
			name, _ := m["name"].(string)
			names = append(names, name)
		}
		ev["organizations"] = names
		for _, name := range names {
			if name != aipG07Org {
				return fmt.Errorf("GET %s with an agent token of Organization %s lists Organization %q", aipG07Read, aipG07Org, name)
			}
		}
		return nil
	})

	outside := "/api/v1/sovereigns/" + url.PathEscape(deployment) + "/applications"
	step("the agent token is refused on a route outside its Organization", func(ev Evidence) error {
		return refusedWith(ev, func() (agentAnswer, error) { return call(http.MethodGet, outside, token, nil) },
			"GET "+outside, http.StatusForbidden, "org-scoped-forbidden")
	})

	step("the agent token is refused on a route that changes state", func(ev Evidence) error {
		return refusedWith(ev, func() (agentAnswer, error) { return call(http.MethodPost, aipG07Write, token, map[string]any{}) },
			"POST "+aipG07Write, http.StatusForbidden, "agent-token-forbidden")
	})

	step("a token of tier admin is refused", func(ev Evidence) error {
		return refusedWith(ev, func() (agentAnswer, error) { return mint(aipG07Org, "admin", 0) },
			"POST "+aipG07Issue+" for tier admin", http.StatusUnprocessableEntity, "invalid-request")
	})

	step("a token of Organization other is refused", func(ev Evidence) error {
		return refusedWith(ev, func() (agentAnswer, error) { return mint("other", "viewer", 0) },
			"POST "+aipG07Issue+" for Organization other", http.StatusForbidden, "caller-not-allowed")
	})

	step(fmt.Sprintf("a token of %d seconds reads at once and is refused %d seconds after it was minted", aipG07ShortLife, int(aipG07Late/time.Second)), func(ev Evidence) error {
		minted := c.now()
		a, err := mint(aipG07Org, "viewer", aipG07ShortLife)
		if err != nil {
			return err
		}
		if a.status != http.StatusOK {
			return fmt.Errorf("POST %s for %d seconds answered %d %s, want 200", aipG07Issue, aipG07ShortLife, a.status, a.code)
		}
		short, _ := a.body["token"].(string)
		fresh, err := call(http.MethodGet, aipG07Read, short, nil)
		if err != nil {
			return err
		}
		ev["status_fresh"] = fresh.status
		if fresh.status != http.StatusOK {
			return fmt.Errorf("GET %s with a fresh token answered %d %s, want 200", aipG07Read, fresh.status, fresh.code)
		}
		if err := c.wait(ctx, aipG07Late-c.now().Sub(minted)); err != nil {
			return err
		}
		late, err := call(http.MethodGet, aipG07Read, short, nil)
		if err != nil {
			return err
		}
		ev["status_late"], ev["seconds_after_mint"] = late.status, int(c.now().Sub(minted)/time.Second)
		if late.status != http.StatusUnauthorized {
			return fmt.Errorf("GET %s with a token that expired answered %d, want 401", aipG07Read, late.status)
		}
		return nil
	})
	return nil
}

// refusedWith records the answer of do and fails unless it has the status
// and the error code given.
func refusedWith(ev Evidence, do func() (agentAnswer, error), what string, status int, code string) error {
	a, err := do()
	if err != nil {
		return err
	}
	ev["status"], ev["error"] = a.status, a.code
	if a.status != status || a.code != code {
		return fmt.Errorf("%s answered %d %q, want %d %q", what, a.status, a.code, status, code)
	}
	return nil
}

// agentCall sends one request. bearer, when not empty, is sent as
// `Authorization: Bearer`; no error and no evidence ever holds it.
func agentCall(ctx context.Context, client *http.Client, base, method, path, bearer string, body any) (agentAnswer, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return agentAnswer{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return agentAnswer{}, fmt.Errorf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := client.Do(req)
	if err != nil {
		return agentAnswer{}, fmt.Errorf("%s %s: no answer", method, path)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return agentAnswer{}, fmt.Errorf("%s %s: read the body: %v", method, path, err)
	}
	a := agentAnswer{status: res.StatusCode}
	if json.Unmarshal(data, &a.body) == nil {
		a.code, _ = a.body["error"].(string)
	}
	return a, nil
}

// jwtParts decodes the header and the claims of a compact JWT without
// verifying it.
func jwtParts(token string) (header, claims map[string]any, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, errors.New("the answer holds no token of three parts")
	}
	for i, out := range []*map[string]any{&header, &claims} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			return nil, nil, fmt.Errorf("part %d of the token is not base64url", i+1)
		}
		if err := json.Unmarshal(b, out); err != nil {
			return nil, nil, fmt.Errorf("part %d of the token is not a JSON object", i+1)
		}
	}
	return header, claims, nil
}

// verifyRS256 verifies the signature of a compact JWT with an RSA key of a
// key set.
func verifyRS256(token string, key map[string]any) error {
	n, _ := key["n"].(string)
	e, _ := key["e"].(string)
	nb, errN := base64.RawURLEncoding.DecodeString(n)
	eb, errE := base64.RawURLEncoding.DecodeString(e)
	if key["kty"] != "RSA" || errN != nil || errE != nil || len(nb) == 0 || len(eb) == 0 || len(eb) > 4 {
		return fmt.Errorf("the key of the key set is not an RSA key with n and e")
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	parts := strings.Split(token, ".")
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("the signature of the token is not base64url")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return errors.New("the signature of the token does not verify with the key of the key set")
	}
	return nil
}

// holds reports whether a claim that is a string or a list holds v.
func holds(claim any, v string) bool {
	switch x := claim.(type) {
	case string:
		return x == v
	case []any:
		for _, item := range x {
			if item == v {
				return true
			}
		}
	}
	return false
}
