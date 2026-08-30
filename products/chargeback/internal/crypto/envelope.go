// Package crypto implements envelope encryption for secrets at rest.
//
// Every secret (today: a Huawei IAM secret key) is encrypted with its own
// random data-encryption key (DEK); the DEK is wrapped with the key-encryption
// key (KEK) supplied through APP_ENCRYPTION_KEY. Rotating the KEK therefore
// re-wraps 32-byte DEKs instead of re-encrypting every secret, and a leaked
// ciphertext blob is useless without both layers.
//
// Blob layout (all AES-256-GCM, 12-byte nonces, 16-byte tags):
//
//	byte 0        version (1)
//	bytes 1..12   KEK nonce
//	bytes 13..60  wrapped DEK (32 + 16)
//	bytes 61..72  DEK nonce
//	bytes 73..    ciphertext + tag
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	version     = 1
	keyLen      = 32
	nonceLen    = 12
	tagLen      = 16
	wrappedLen  = keyLen + tagLen
	headerLen   = 1 + nonceLen + wrappedLen + nonceLen
	minBlobSize = headerLen + tagLen
)

// ErrKeyLength is returned when the KEK is not exactly 32 bytes.
var ErrKeyLength = errors.New("crypto: APP_ENCRYPTION_KEY must decode to 32 bytes")

// ErrMalformed is returned when a blob cannot be parsed or authenticated.
var ErrMalformed = errors.New("crypto: malformed or unauthenticated blob")

// Keyring holds the key-encryption key.
type Keyring struct {
	kek []byte
}

// NewKeyring builds a Keyring from a base64 (std or URL, padded or raw) KEK.
func NewKeyring(b64 string) (*Keyring, error) {
	if b64 == "" {
		return nil, ErrKeyLength
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		raw, err = enc.DecodeString(b64)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("crypto: decode APP_ENCRYPTION_KEY: %w", err)
	}
	return NewKeyringFromBytes(raw)
}

// NewKeyringFromBytes builds a Keyring from raw KEK bytes.
func NewKeyringFromBytes(kek []byte) (*Keyring, error) {
	if len(kek) != keyLen {
		return nil, ErrKeyLength
	}
	k := &Keyring{kek: make([]byte, keyLen)}
	copy(k.kek, kek)
	return k, nil
}

// Seal encrypts plaintext under a fresh DEK wrapped by the KEK.
func (k *Keyring) Seal(plaintext []byte) ([]byte, error) {
	dek := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("crypto: dek: %w", err)
	}
	kekNonce, wrapped, err := gcmSeal(k.kek, dek)
	if err != nil {
		return nil, err
	}
	dekNonce, ct, err := gcmSeal(dek, plaintext)
	if err != nil {
		return nil, err
	}
	blob := make([]byte, 0, headerLen+len(ct))
	blob = append(blob, version)
	blob = append(blob, kekNonce...)
	blob = append(blob, wrapped...)
	blob = append(blob, dekNonce...)
	blob = append(blob, ct...)
	return blob, nil
}

// Open decrypts a blob produced by Seal.
func (k *Keyring) Open(blob []byte) ([]byte, error) {
	if len(blob) < minBlobSize || blob[0] != version {
		return nil, ErrMalformed
	}
	off := 1
	kekNonce := blob[off : off+nonceLen]
	off += nonceLen
	wrapped := blob[off : off+wrappedLen]
	off += wrappedLen
	dekNonce := blob[off : off+nonceLen]
	off += nonceLen
	ct := blob[off:]
	dek, err := gcmOpen(k.kek, kekNonce, wrapped)
	if err != nil {
		return nil, ErrMalformed
	}
	pt, err := gcmOpen(dek, dekNonce, ct)
	if err != nil {
		return nil, ErrMalformed
	}
	return pt, nil
}

func gcmSeal(key, plaintext []byte) (nonce, ct []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, nil), nil
}

func gcmOpen(key, nonce, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, nil)
}
