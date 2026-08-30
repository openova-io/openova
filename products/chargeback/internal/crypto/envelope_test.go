package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	kek := bytes.Repeat([]byte{0x42}, 32)
	k, err := NewKeyring(base64.StdEncoding.EncodeToString(kek))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := testKeyring(t)
	secret := []byte("not-a-real-secret-key-0123456789")
	blob, err := k.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, secret) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := k.Open(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round trip mismatch: %q", got)
	}
	// A second seal of the same plaintext must differ (fresh DEK + nonces).
	blob2, _ := k.Seal(secret)
	if bytes.Equal(blob, blob2) {
		t.Fatal("two seals produced identical blobs")
	}
}

func TestOpenRejectsTamperAndWrongKey(t *testing.T) {
	k := testKeyring(t)
	blob, _ := k.Seal([]byte("x"))
	tampered := append([]byte{}, blob...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := k.Open(tampered); err == nil {
		t.Fatal("tampered blob opened")
	}
	other, _ := NewKeyringFromBytes(bytes.Repeat([]byte{0x01}, 32))
	if _, err := other.Open(blob); err == nil {
		t.Fatal("wrong KEK opened blob")
	}
	if _, err := k.Open([]byte{1, 2, 3}); err == nil {
		t.Fatal("short blob opened")
	}
}

func TestKeyringLength(t *testing.T) {
	if _, err := NewKeyring(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := NewKeyring(""); err == nil {
		t.Fatal("empty key accepted")
	}
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if _, err := NewKeyring(raw); err != nil {
		t.Fatalf("raw url base64 rejected: %v", err)
	}
}
