package einvoice

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

// Signing (DESIGN.md §17).
//
// The key comes from a mounted Secret and is parsed ONCE, here. Nothing in
// this file writes key material anywhere: not to an error (a parse failure
// says the key could not be parsed and names the PEM block type, never the
// bytes), not to a log, and not to the archive. The archive holds the
// document and its hash.

// Signature algorithms, named on the document so a verifier knows what to do
// without guessing from the key.
const (
	AlgRSASHA256     = "RSA-SHA256"   // RSASSA-PKCS1-v1_5 over SHA-256
	AlgECDSASHA256   = "ECDSA-SHA256" // ASN.1 DER ECDSA over SHA-256
	AlgEd25519       = "Ed25519"      // pure Ed25519 over the canonical bytes
	digestAlgorithm  = "SHA-256"      // what Hash is
	pemRSAPrivateKey = "RSA PRIVATE KEY"
)

// ErrNoSigningKey is what Sign returns when the profile is on and no key is
// configured. It is reported as a validation PROBLEM rather than surfacing
// as a mystery at signing time, so the issue is refused with a sentence an
// operator can act on.
var ErrNoSigningKey = errors.New("no signing key configured")

// signer holds the parsed key. The PEM bytes are not retained.
type signer struct {
	key   crypto.Signer
	alg   string
	keyID string
}

// newSigner parses a PEM private key. Nil (and no error) when pem is empty:
// a profile may be built before its key is mounted, and Validate is what
// reports that, with a message naming the environment variable to set.
func newSigner(pemBytes []byte, keyID string) (*signer, error) {
	if len(pemBytes) == 0 {
		return nil, nil
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("signing key: not a PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case pemRSAPrivateKey:
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("signing key: unsupported PEM block %q (PRIVATE KEY, RSA PRIVATE KEY or EC PRIVATE KEY)", block.Type)
	}
	if err != nil {
		// The error from x509 never carries key bytes, but it can carry the
		// ASN.1 offset; that is safe and useful.
		return nil, fmt.Errorf("signing key: %w", err)
	}
	s := &signer{keyID: keyID}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		s.key, s.alg = k, AlgRSASHA256
	case *ecdsa.PrivateKey:
		s.key, s.alg = k, AlgECDSASHA256
	case ed25519.PrivateKey:
		s.key, s.alg = k, AlgEd25519
	default:
		return nil, fmt.Errorf("signing key: unsupported key type %T (RSA, ECDSA or Ed25519)", key)
	}
	return s, nil
}

// sign hashes the canonical bytes and signs the digest, returning the
// signature base64 and the digest hex.
func (s *signer) sign(canonical []byte) (signature, hash string, err error) {
	sum := sha256.Sum256(canonical)
	hash = hex.EncodeToString(sum[:])
	if s == nil || s.key == nil {
		return "", hash, ErrNoSigningKey
	}
	var raw []byte
	if s.alg == AlgEd25519 {
		// Ed25519 signs the MESSAGE, not a digest.
		raw, err = s.key.Sign(rand.Reader, canonical, crypto.Hash(0))
	} else {
		raw, err = s.key.Sign(rand.Reader, sum[:], crypto.SHA256)
	}
	if err != nil {
		return "", hash, fmt.Errorf("sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), hash, nil
}

// PublicKeyPEM returns the PUBLIC half of the signing key, PEM-encoded — the
// thing a verifier needs and the only half that may ever leave this process.
func (s *signer) PublicKeyPEM() (string, error) {
	if s == nil || s.key == nil {
		return "", ErrNoSigningKey
	}
	der, err := x509.MarshalPKIXPublicKey(s.key.Public())
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// Verify checks a signature against a PEM public key. It is here so the
// contract is testable from outside the package — a signature nobody can
// verify is not a signature — and so an operator can check an archived
// document against the key they published.
func Verify(publicKeyPEM string, algorithm string, canonical []byte, signatureB64 string) error {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return errors.New("public key: not a PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("signature: not base64: %w", err)
	}
	sum := sha256.Sum256(canonical)
	switch algorithm {
	case AlgRSASHA256:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("signature says %s but the key is %T", algorithm, pub)
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig)
	case AlgECDSASHA256:
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("signature says %s but the key is %T", algorithm, pub)
		}
		if !ecdsa.VerifyASN1(k, sum[:], sig) {
			return errors.New("signature does not verify")
		}
		return nil
	case AlgEd25519:
		k, ok := pub.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("signature says %s but the key is %T", algorithm, pub)
		}
		if !ed25519.Verify(k, canonical, sig) {
			return errors.New("signature does not verify")
		}
		return nil
	default:
		return fmt.Errorf("unknown signature algorithm %q", algorithm)
	}
}

// HashHex is the digest the archive records: SHA-256 of the canonical bytes.
func HashHex(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
