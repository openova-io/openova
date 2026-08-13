// pubkey_set_test.go — UAT rows 212/213, the key-SOURCE half.
//
// The resolver can hold a set (identity_keyset_test.go); this file asserts the
// binary can actually BUILD one from what the platform publishes:
//
//	OPENOVA_MCP_RS256_PUBKEY_PEM — `catalyst-handover-jwt-public` key
//	  `public.jwk`, a single RSA JWK (on a Sovereign: the mothership-injected
//	  inbound-handover key, preserved there by #4450).
//	OPENOVA_MCP_RS256_PUBKEY_SET — the same Secret's `signers.jwks`, a JWKS
//	  carrying every catalyst-api handoverjwt signer on this Sovereign.
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

// genKey lives in wrong_door_verdict_test.go (#6110) — same package, same
// signature, same 2048-bit RSA key. Declaring a second copy here compiled
// while the two files sat on different branches and stopped compiling the
// moment both were on main, which is exactly the shape a rebase must catch.

func asJWK(k *rsa.PublicKey) string {
	return fmt.Sprintf(`{"kty":"RSA","use":"sig","alg":"RS256","n":%q,"e":%q}`,
		base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()))
}

func asPEM(t *testing.T, k *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func hasKey(set []*rsa.PublicKey, want *rsa.PublicKey) bool {
	for _, k := range set {
		if k.N.Cmp(want.N) == 0 && k.E == want.E {
			return true
		}
	}
	return false
}

func TestParseRSAPublicKeySet_UnionsThePublicJWKAndTheSignersJWKS(t *testing.T) {
	mothership, regionA, regionB := genKey(t), genKey(t), genKey(t)

	publicJWK := asJWK(&mothership.PublicKey)
	signersJWKS := `{"keys":[` + asJWK(&mothership.PublicKey) + `,` +
		asJWK(&regionA.PublicKey) + `,` + asJWK(&regionB.PublicKey) + `]}`

	set, err := parseRSAPublicKeySet(publicJWK, signersJWKS)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Assert on the KEYS, not on a count that a duplicate could satisfy.
	for name, k := range map[string]*rsa.PublicKey{
		"mothership": &mothership.PublicKey,
		"region-a":   &regionA.PublicKey,
		"region-b":   &regionB.PublicKey,
	} {
		if !hasKey(set, k) {
			t.Errorf("%s key missing from the parsed set", name)
		}
	}
	// The mothership key appears in BOTH sources; it must be tried once.
	if len(set) != 3 {
		t.Fatalf("set size = %d, want 3 (the shared key deduped)", len(set))
	}
}

func TestParseRSAPublicKeySet_AcceptsEveryPublishedShape(t *testing.T) {
	a, b := genKey(t), genKey(t)

	cases := []struct {
		name    string
		sources []string
		want    []*rsa.PublicKey
	}{
		{"single JWK (the #5167 public.jwk mirror)", []string{asJWK(&a.PublicKey)}, []*rsa.PublicKey{&a.PublicKey}},
		{"single PKIX PEM (the per-Org OpenBao seed)", []string{asPEM(t, &a.PublicKey)}, []*rsa.PublicKey{&a.PublicKey}},
		{"JWKS document", []string{`{"keys":[` + asJWK(&a.PublicKey) + `,` + asJWK(&b.PublicKey) + `]}`}, []*rsa.PublicKey{&a.PublicKey, &b.PublicKey}},
		{"two concatenated PEM blocks", []string{asPEM(t, &a.PublicKey) + asPEM(t, &b.PublicKey)}, []*rsa.PublicKey{&a.PublicKey, &b.PublicKey}},
		{"mixed sources", []string{asPEM(t, &a.PublicKey), asJWK(&b.PublicKey)}, []*rsa.PublicKey{&a.PublicKey, &b.PublicKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parseRSAPublicKeySet(tc.sources...)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(set) != len(tc.want) {
				t.Fatalf("set size = %d, want %d", len(set), len(tc.want))
			}
			for i, w := range tc.want {
				if !hasKey(set, w) {
					t.Errorf("key %d missing", i)
				}
			}
		})
	}
}

// CONTROL — a JWKS carrying a non-RSA entry alongside an RSA one yields the RSA
// key and does not fail the document. A realm JWKS legitimately carries EC keys.
func TestParseRSAPublicKeySet_SkipsNonRSAEntriesWithoutFailing(t *testing.T) {
	a := genKey(t)
	doc := `{"keys":[{"kty":"EC","crv":"P-256","x":"AA","y":"BB"},` + asJWK(&a.PublicKey) + `]}`
	set, err := parseRSAPublicKeySet(doc)
	if err != nil {
		t.Fatalf("a mixed JWKS failed the whole document: %v", err)
	}
	if len(set) != 1 || !hasKey(set, &a.PublicKey) {
		t.Fatalf("set = %d key(s), want just the RSA one", len(set))
	}
}

// CONTROL — a malformed source next to a good one yields the good key AND a
// non-nil error, so the caller can serve while saying loudly what it dropped.
// A silently-dropped key is how this whole defect class recurs.
func TestParseRSAPublicKeySet_PartialFailureIsReportedNotSwallowed(t *testing.T) {
	a := genKey(t)
	set, err := parseRSAPublicKeySet(asJWK(&a.PublicKey), "not a key at all")
	if len(set) != 1 || !hasKey(set, &a.PublicKey) {
		t.Fatalf("the good key was lost: set has %d key(s)", len(set))
	}
	if err == nil {
		t.Fatal("a malformed source was swallowed silently")
	}
}

// CONTROL — garbage everywhere yields NO keys. The caller degrades on an empty
// set, so "unparseable" must never become "no verification".
func TestParseRSAPublicKeySet_AllGarbageYieldsNoKeys(t *testing.T) {
	set, err := parseRSAPublicKeySet("hello", `{"kty":"RSA"}`, `{"keys":[]}`)
	if len(set) != 0 {
		t.Fatalf("garbage produced %d key(s)", len(set))
	}
	if err == nil {
		t.Fatal("garbage parsed without an error")
	}
	if !strings.Contains(err.Error(), "RS256 pubkey") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// CONTROL — empty sources are not an error and not a key. This is the absent
// OPTIONAL secretKeyRef case (#4228/#5114): buildResolver must be able to tell
// "nothing wired" from "wired but broken", because only the first is routine.
func TestParseRSAPublicKeySet_EmptySourcesAreNeitherKeyNorError(t *testing.T) {
	set, err := parseRSAPublicKeySet("", "   ")
	if len(set) != 0 {
		t.Fatalf("empty sources produced %d key(s)", len(set))
	}
	if err != nil {
		t.Fatalf("empty sources reported an error: %v", err)
	}
}
