package mcp

import (
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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// KeySetPath is the route of catalyst-api that publishes the public keys of its signers.
const KeySetPath = "/api/v1/auth/signers.jwks"

// Audience is the audience every platform tool server requires of an agent token.
const Audience = "platform-tools"

// Key set timing: a key set is read again when it is older than KeySetMaxAge, the route's
// cache life, or when a token names a key it does not hold; never twice within KeySetFloor.
const (
	KeySetMaxAge = 5 * time.Minute
	KeySetFloor  = 30 * time.Second
)

// Tier ranks. An agent token's tier is capped at developer.
const (
	TierViewer    = "viewer"
	TierDeveloper = "developer"
)

var (
	agentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	slugPattern  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	higherTiers  = []string{"operator", "admin", "owner", "org-admin", "sovereign-admin"}
)

// Identity is the verified caller of a tool.
type Identity struct {
	Organization string // claim org_id
	Agent        string // claim agent
	Tier         string // TierViewer or TierDeveloper
}

// KeySet reads the public keys of catalyst-api's signers from URL, on demand: there is no
// timer. A key is kept only when its kid is the RFC 7638 thumbprint of the key itself.
type KeySet struct {
	URL    string           // catalyst-api's base address and KeySetPath
	Client *http.Client     // nil means a client with a 5 second time limit
	Now    func() time.Time // nil means time.Now

	mu    sync.Mutex
	keys  map[string]*rsa.PublicKey
	read  time.Time // when keys were read
	tried time.Time // when the route was last asked
}

// Key returns the key named kid.
func (k *KeySet) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if k.Now != nil {
		now = k.Now()
	}
	key, held := k.keys[kid]
	if held && now.Sub(k.read) < KeySetMaxAge {
		return key, nil
	}
	if !k.tried.IsZero() && now.Sub(k.tried) < KeySetFloor {
		if held {
			return key, nil
		}
		return nil, fmt.Errorf("mcp: the key %q is not in the key set", kid)
	}
	k.tried = now
	keys, err := k.fetch(ctx)
	if err != nil {
		if held {
			return key, nil // the last key set read stays in use while the route fails
		}
		return nil, err
	}
	k.keys, k.read = keys, now
	if key, held = keys[kid]; !held {
		return nil, fmt.Errorf("mcp: the key %q is not in the key set", kid)
	}
	return key, nil
}

func (k *KeySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	client := k.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: key set: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: key set: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: key set: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("mcp: key set: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range set.Keys {
		n, errN := base64.RawURLEncoding.DecodeString(j.N)
		e, errE := base64.RawURLEncoding.DecodeString(j.E)
		if j.Kty != "RSA" || (j.Alg != "" && j.Alg != "RS256") || errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if key.N.BitLen() >= 2048 && j.Kid == KeyID(key) {
			keys[j.Kid] = key
		}
	}
	return keys, nil
}

// KeyID returns the RFC 7638 thumbprint of key: SHA-256 of {"e":..,"kty":"RSA","n":..},
// base64url without padding. catalyst-api writes it as the kid of an agent token.
func KeyID(key *rsa.PublicKey) string {
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Verifier accepts agent tokens: RS256, signed by a key of the key set named by the token's
// kid, typ "agent", aud containing Audience, iss equal to Issuer, not expired, with an
// Organization, an agent name and a tier.
type Verifier struct {
	Keys   *KeySet
	Issuer string
	Now    func() time.Time // nil means time.Now
}

// audience reads aud as one string or a list of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	*a = list
	return nil
}

type claims struct {
	Iss   string   `json:"iss"`
	Aud   audience `json:"aud"`
	Typ   string   `json:"typ"`
	OrgID string   `json:"org_id"`
	Agent string   `json:"agent"`
	Tier  string   `json:"tier"`
	Exp   *float64 `json:"exp"`
	Nbf   *float64 `json:"nbf"`
}

// Verify returns the identity of an agent token, or why it is refused.
func (v *Verifier) Verify(ctx context.Context, bearer string) (Identity, error) {
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return Identity{}, errors.New("the bearer is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &header); err != nil || header.Alg != "RS256" || header.Kid == "" {
		return Identity{}, errors.New("the token is not RS256 with a kid")
	}
	key, err := v.Keys.Key(ctx, header.Kid)
	if err != nil {
		return Identity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, errors.New("the signature is not base64url")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return Identity{}, errors.New("the signature does not verify")
	}
	var c claims
	if err := decodePart(parts[1], &c); err != nil {
		return Identity{}, errors.New("the claims cannot be read")
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	switch {
	case !strings.EqualFold(strings.TrimSpace(c.Typ), "agent"):
		return Identity{}, errors.New("the token is not an agent token")
	case !slices.Contains(c.Aud, Audience):
		return Identity{}, fmt.Errorf("aud does not contain %q", Audience)
	case v.Issuer == "" || c.Iss != v.Issuer:
		return Identity{}, fmt.Errorf("the issuer %q is not trusted", c.Iss)
	case c.Exp == nil || float64(now.Unix()) >= *c.Exp:
		return Identity{}, errors.New("the token has expired or has no exp")
	case c.Nbf != nil && float64(now.Unix()) < *c.Nbf:
		return Identity{}, errors.New("the token is not valid yet")
	case !slugPattern.MatchString(c.OrgID):
		return Identity{}, errors.New("the token names no Organization")
	case !agentPattern.MatchString(c.Agent):
		return Identity{}, errors.New("the token names no agent")
	}
	tier := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(c.Tier)), "catalyst-")
	switch {
	case tier == TierViewer || tier == TierDeveloper:
	case slices.Contains(higherTiers, tier):
		tier = TierDeveloper
	default:
		return Identity{}, fmt.Errorf("the tier %q is not on the ladder", c.Tier)
	}
	return Identity{Organization: c.OrgID, Agent: c.Agent, Tier: tier}, nil
}

func decodePart(part string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
