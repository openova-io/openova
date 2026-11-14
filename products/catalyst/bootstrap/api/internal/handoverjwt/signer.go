// Package handoverjwt handles the RSA-2048 signing keypair for the handover
// JWT flow (issue #605, Phase-8b).
//
// # Purpose
//
// When provisioning completes (Phase-1 watch terminates OutcomeReady),
// catalyst-api mints a signed RS256 JWT so the wizard can redirect the
// browser to:
//
//	https://console.<sovereignFqdn>/auth/handover?token=<jwt>
//
// The Sovereign-side catalyst-api (Agent C) validates the JWT using the
// public key distributed via cloud-init (see
// infra/hetzner/cloudinit-control-plane.tftpl). Single-use enforcement is
// Sovereign-side only — each Sovereign sees only one jti for itself, so a
// local consumed-set is sufficient and avoids cross-cluster RPC during auth.
//
// # Keypair lifecycle
//
//   - On first boot (key file absent), LoadOrGenerate() generates a fresh
//     RSA-2048 keypair and writes:
//   - private key PEM  → keyPath  (mode 0600)
//   - public JWK JSON  → pubKeyPath (mode 0644)
//   - Subsequent restarts load the existing files (idempotent).
//
// # JWT shape (canonical — Agent C must accept exactly this)
//
//	{
//	  "iss":            "https://console.openova.io",
//	  "aud":            ["https://console.<sovereignFqdn>"],
//	  "sub":            "<openova-user-sub>",
//	  "email":          "<verified-email>",
//	  "email_verified": true,
//	  "sovereign_fqdn": "<deployment.SovereignFQDN>",
//	  "deployment_id":  "<deployment.ID>",
//	  "role":           "sovereign-admin",
//	  "jti":            "<uuid-v4>",
//	  "iat":            <unix>,
//	  "exp":            <unix + 300>
//	}
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//   - #4 (never hardcode): issuer URL and TTL are runtime-configurable via
//     CATALYST_HANDOVER_JWT_ISSUER / CATALYST_HANDOVER_JWT_TTL_SECONDS.
//   - #10 (credential hygiene): private key never logged; the only thing
//     that lands in structured logs is the RSA key-size and issuer URL.
package handoverjwt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// RSAKeyBits — minimum 2048 per NIST SP 800-57 guidance for JWT signing.
	RSAKeyBits = 2048

	// DefaultTTL — 5-minute window per the JWT contract in the issue brief.
	DefaultTTL = 5 * time.Minute

	// mothershipIssuer — the Catalyst-Zero (mothership) console origin. This
	// is the LAST-RESORT fallback only: it keeps Catalyst-Zero byte-unchanged
	// when neither the caller nor the env supplies an issuer. A franchised
	// Sovereign post-bp-self-sovereign-cutover MUST NOT stamp this URL as the
	// `iss` claim — see DefaultIssuer() + #2940 (Pillar 5 anti-tether).
	mothershipIssuer = "https://console.openova.io"
)

// DefaultIssuer resolves the JWT issuer used when the caller passes an empty
// issuer to New / LoadOrGenerate.
//
// #2940 (Pillar 5): previously a bare `const DefaultIssuer =
// "https://console.openova.io"` — a mothership-tether hardcode. A franchised
// Sovereign that constructed a Signer without an explicit issuer would stamp
// the mothership origin as the `iss` claim, so downstream validation rejected
// the token as foreign-issuer. Now it reads CATALYST_HANDOVER_JWT_ISSUER
// first (the same env the chart api-deployment supplies from the per-Sovereign
// runtime-config / cutover Step-07 pivot) and only falls back to the
// mothership origin when that env is unset — keeping Catalyst-Zero unchanged.
func DefaultIssuer() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_HANDOVER_JWT_ISSUER")); v != "" {
		return v
	}
	return mothershipIssuer
}

// MothershipIssuer exposes the Catalyst-Zero origin so VERIFIERS can keep
// accepting a mothership-minted token after the per-Sovereign override has
// pivoted DefaultIssuer() away from it.
//
// #5614: a cut-over Sovereign sets CATALYST_HANDOVER_JWT_ISSUER, so
// DefaultIssuer() no longer returns this value — but the Sovereign must still
// redeem the mothership's post-provisioning handover (package doc above) and
// the "Enter org" support session (#3378 B2). A verifier that compares against
// DefaultIssuer() ALONE therefore trades one broken leg for another. The
// accessor exists so the accepted-issuer SET can be assembled without any
// caller re-typing the literal — the #2940 rule is "the mothership URL is
// written once", not "the mothership URL is never read".
func MothershipIssuer() string { return mothershipIssuer }

// Claims is the exact JWT claim shape Agent C must accept.
// Custom fields use lowercase JSON keys per RFC 7519 §4 convention.
type Claims struct {
	jwt.RegisteredClaims

	// Email — verified email address forwarded by the OIDC proxy.
	Email string `json:"email"`

	// EmailVerified — always true; the OIDC proxy guarantees email
	// verification before requests reach catalyst-api.
	EmailVerified bool `json:"email_verified"`

	// SovereignFQDN — the new Sovereign's fully-qualified domain name.
	SovereignFQDN string `json:"sovereign_fqdn"`

	// DeploymentID — the Catalyst-Zero deployment record identifier.
	DeploymentID string `json:"deployment_id"`

	// Role — always "sovereign-admin" for this handover flow.
	Role string `json:"role"`
}

// Signer holds a loaded RSA private key + the signing configuration.
// Thread-safe after construction; never mutated after New() returns.
type Signer struct {
	privateKey *rsa.PrivateKey
	issuer     string
	ttl        time.Duration
}

// New parses privPEM (PKCS#1 "RSA PRIVATE KEY" or PKCS#8 "PRIVATE KEY") and
// returns a Signer ready to mint tokens.
func New(privPEM []byte, issuer string, ttl time.Duration) (*Signer, error) {
	if issuer == "" {
		issuer = DefaultIssuer()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return nil, errors.New("handoverjwt: failed to decode PEM block from private key")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("handoverjwt: parse PKCS1 private key: %w", err)
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("handoverjwt: parse PKCS8 private key: %w", err)
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("handoverjwt: PKCS8 private key is not RSA")
		}
		key = rsaKey
	default:
		return nil, fmt.Errorf("handoverjwt: unsupported PEM block type %q (expected RSA PRIVATE KEY or PRIVATE KEY)", block.Type)
	}
	if bits := key.N.BitLen(); bits < RSAKeyBits {
		return nil, fmt.Errorf("handoverjwt: RSA key too small (%d bits, minimum %d)", bits, RSAKeyBits)
	}
	return &Signer{
		privateKey: key,
		issuer:     issuer,
		ttl:        ttl,
	}, nil
}

// MintToken mints a single-use RS256 JWT for the given deployment + user
// identity. The jti is a freshly-generated UUID v4; single-use enforcement
// lives on the Sovereign side (Agent C).
//
// Parameters:
//   - sovereignFQDN: new Sovereign's FQDN (e.g. otech23.omani.works)
//   - deploymentID:  Catalyst-Zero deployment record ID
//   - sub:           OIDC subject (from X-Forwarded-User header)
//   - email:         verified email (from X-Forwarded-Email header)
func (s *Signer) MintToken(sovereignFQDN, deploymentID, sub, email string) (string, error) {
	now := time.Now().UTC()
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("handoverjwt: generate jti uuid: %w", err)
	}

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   sub,
			Audience:  jwt.ClaimStrings{"https://console." + sovereignFQDN},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
			ID:        jti.String(),
		},
		Email:         email,
		EmailVerified: true,
		SovereignFQDN: sovereignFQDN,
		DeploymentID:  deploymentID,
		Role:          "sovereign-admin",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("handoverjwt: sign token: %w", err)
	}
	return signed, nil
}

// PublicJWK returns the public key as a minimal RFC 7517 JSON Web Key (JWK)
// object suitable for embedding in the Sovereign's cloud-init and for serving
// via GET /api/v1/handover/public-key.
//
// The JWK contains: kty, use, alg, n, e (all fields required for RS256
// signature verification per RFC 7518 §6.3).
func (s *Signer) PublicJWK() ([]byte, error) {
	pub := &s.privateKey.PublicKey
	jwk := map[string]interface{}{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
	return json.Marshal(jwk)
}

// GenerateKeypair generates a fresh RSA-2048 keypair and returns:
//   - privPEM: PKCS#1 PEM-encoded private key (type "RSA PRIVATE KEY")
//   - pubJWK:  JSON Web Key bytes for the public half (RFC 7517)
func GenerateKeypair() (privPEM []byte, pubJWK []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, RSAKeyBits)
	if err != nil {
		return nil, nil, fmt.Errorf("handoverjwt: generate RSA-%d key: %w", RSAKeyBits, err)
	}

	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	})

	tmp := &Signer{privateKey: key}
	pubJWK, err = tmp.PublicJWK()
	if err != nil {
		return nil, nil, err
	}
	return privPEM, pubJWK, nil
}

// LoadOrGenerate loads the signing key from keyPath (a PEM file). If the
// file is absent it generates a fresh keypair, writes the private key PEM to
// keyPath (mode 0600) and the public JWK JSON to pubKeyPath (mode 0644),
// then returns the loaded Signer.
//
// This is the entry point used by cmd/api/main.go. keyPath and pubKeyPath
// are the volume-mounted paths of the K8s Secret / ConfigMap that hold
// the keypair (see chart/templates/handover-signing-key-secret.yaml).
func LoadOrGenerate(keyPath, pubKeyPath, issuer string, ttl time.Duration) (*Signer, error) {
	privPEM, err := os.ReadFile(keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("handoverjwt: read private key %s: %w", keyPath, err)
		}
		// Key file absent → generate a fresh keypair.
		newPriv, newPub, genErr := GenerateKeypair()
		if genErr != nil {
			return nil, genErr
		}
		if mkErr := os.MkdirAll(dirOf(keyPath), 0o700); mkErr != nil {
			return nil, fmt.Errorf("handoverjwt: mkdir for key path %s: %w", keyPath, mkErr)
		}
		if wErr := os.WriteFile(keyPath, newPriv, 0o600); wErr != nil {
			return nil, fmt.Errorf("handoverjwt: write private key %s: %w", keyPath, wErr)
		}
		if pubKeyPath != "" {
			if mkErr := os.MkdirAll(dirOf(pubKeyPath), 0o700); mkErr != nil {
				return nil, fmt.Errorf("handoverjwt: mkdir for pubkey path %s: %w", pubKeyPath, mkErr)
			}
			if wErr := os.WriteFile(pubKeyPath, newPub, 0o644); wErr != nil {
				return nil, fmt.Errorf("handoverjwt: write public JWK %s: %w", pubKeyPath, wErr)
			}
		}
		privPEM = newPriv
	}
	return New(privPEM, issuer, ttl)
}

// SignCustomClaims signs an arbitrary jwt.Claims implementation using the
// signer's RS256 private key. This is the extension point for the PIN-auth
// flow (issue #688): the handler builds its own session claims map and calls
// this method rather than MintToken (which is purpose-built for the
// handover flow's fixed claim shape).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the private key is never returned or
// logged by this package; the caller receives only the compact JWT string.
func (s *Signer) SignCustomClaims(claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("handoverjwt: sign custom claims: %w", err)
	}
	return signed, nil
}

// PublicRSAKey returns the RSA public key for verifying tokens this Signer
// has produced. Used by the magic-validate endpoint to verify the token without
// requiring a separate public-key file read.
func (s *Signer) PublicRSAKey() (*rsa.PublicKey, error) {
	if s.privateKey == nil {
		return nil, errors.New("handoverjwt: signer has no private key")
	}
	return &s.privateKey.PublicKey, nil
}

// dirOf returns the directory component of a file path.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}
