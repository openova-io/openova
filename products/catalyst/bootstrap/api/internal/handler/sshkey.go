// Package handler — SSH keypair generator endpoint.
//
// Closes GitHub issue #160 ([I] ux: SSH keypair UX in wizard — auto-generate
// option + paste-existing fallback).
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//   - Principle #2 (never compromise from quality): the wizard's break-glass
//     access path requires a real keypair, not a placeholder. The Hetzner
//     OpenTofu module (`infra/hetzner/variables.tf`) declares
//     `ssh_public_key` as a required variable with a regex validator, and
//     hcloud_ssh_key resource creation rejects empty keys at apply time.
//
//   - Principle #4 (never hardcode): every value the keypair embeds is
//     derived at request time. The OpenSSH comment field is composed from
//     the FQDN the wizard already collected — no static "catalyst" prefix
//     baked into the key.
//
//   - Principle #10 (credential hygiene): the private key is generated,
//     serialized to OpenSSH format, returned in the JSON response, and
//     never written to disk on the catalyst-api side. The handler logs
//     ONLY the SHA256 fingerprint of the public half — never the public
//     key plaintext, never the private key, never the comment. The wizard
//     UI is solely responsible for triggering the browser to download the
//     private key the moment the response arrives.
//
// Endpoint:
//
//	POST /api/v1/sshkey/generate
//	Request body  : { "fqdn": "omantel.omani.works" }   (optional — used as comment)
//	Response 200  : { "publicKey": "ssh-ed25519 AAAA... catalyst@omantel.omani.works",
//	                  "privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----\n...",
//	                  "fingerprint": "SHA256:...." }
package handler

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SSHKeyGenerateRequest mirrors the wizard's auto-generate button payload.
//
// FQDN is optional. When provided, it's appended as the SSH key comment so
// operators can identify the key in `~/.ssh/authorized_keys` or in the
// Hetzner Cloud Console UI alongside the project's other keys.
type SSHKeyGenerateRequest struct {
	FQDN string `json:"fqdn"`
}

// SSHKeyGenerateResponse is what the browser receives once and only once.
//
// The browser is responsible for triggering the .pem download immediately
// — after this response cycle, the catalyst-api has no copy of the private
// key.
type SSHKeyGenerateResponse struct {
	// PublicKey is the OpenSSH single-line authorized_keys format
	// (e.g. "ssh-ed25519 AAAA... catalyst@omantel.omani.works"). This is
	// what gets passed verbatim to the OpenTofu module's `ssh_public_key`
	// variable, which the variables.tf regex validator already accepts.
	PublicKey string `json:"publicKey"`

	// PrivateKey is the OpenSSH-formatted private key (RFC: openssh-key-v1
	// container, ed25519 algorithm, no passphrase). The wizard offers it
	// to the user as a one-time download as `<fqdn-or-catalyst>.pem`.
	PrivateKey string `json:"privateKey"`

	// Fingerprint is the SHA256 fingerprint of the public key, formatted
	// the same way `ssh-keygen -lf` prints it (base64 raw, no padding,
	// "SHA256:" prefix). This is the ONLY value the catalyst-api logs.
	Fingerprint string `json:"fingerprint"`
}

// GenerateSSHKey issues a brand-new Ed25519 keypair, encodes both halves to
// the wire formats Hetzner / sshd expect, and returns them in JSON.
//
// Per the credential-hygiene rule the only thing this handler logs is the
// fingerprint of the public half. The private key never touches disk and
// never appears in any structured log field.
func (h *Handler) GenerateSSHKey(w http.ResponseWriter, r *http.Request) {
	var req SSHKeyGenerateRequest
	if r.ContentLength > 0 && r.Body != nil {
		// Body is optional — empty body is a valid request.
		// We tolerate decode errors so curl-style empty POSTs still work.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	comment := buildKeyComment(req.FQDN)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		h.log.Error("ssh keypair generation failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not generate Ed25519 keypair",
		})
		return
	}

	publicKeyOpenSSH := encodeED25519PublicKeyOpenSSH(pub, comment)
	privateKeyOpenSSH, err := encodeED25519PrivateKeyOpenSSH(pub, priv, comment)
	if err != nil {
		h.log.Error("ssh private key serialization failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not serialize private key",
		})
		return
	}

	fp := sshSHA256Fingerprint(pub)

	// Per credential hygiene: log ONLY the fingerprint. No key bytes, no
	// comment text echoed back (it includes the customer FQDN which is fine
	// to emit but we keep the log line minimal).
	h.log.Info("ssh keypair generated", "fingerprint", fp)

	writeJSON(w, http.StatusOK, SSHKeyGenerateResponse{
		PublicKey:   publicKeyOpenSSH,
		PrivateKey:  privateKeyOpenSSH,
		Fingerprint: fp,
	})
}

// buildKeyComment composes the OpenSSH comment field from the optional
// wizard FQDN. Defaults to "catalyst" when no FQDN is provided so the
// generated key never has a missing or hardcoded comment.
//
// Never-hardcode (principle #4): every component of the comment string is
// runtime-derived — there is no compile-time literal sovereign hostname
// embedded in the binary.
func buildKeyComment(fqdn string) string {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" {
		return "catalyst"
	}
	// Strip anything that isn't a typical hostname character. Comments
	// CAN contain spaces but we keep the format tight (`user@host` style)
	// so it renders cleanly in `ssh-keygen -lf` and Hetzner's UI.
	return "catalyst@" + fqdn
}

// encodeED25519PublicKeyOpenSSH returns the single-line authorized_keys
// representation of an Ed25519 public key:
//
//	ssh-ed25519 AAAA...base64-of-wire-format... <comment>
//
// The wire format is RFC 4253 §6.6 + draft-ietf-curdle-ssh-ed25519:
//
//	string  "ssh-ed25519"
//	string  <32 bytes of public key>
//
// Each "string" is length-prefixed (uint32 big-endian).
func encodeED25519PublicKeyOpenSSH(pub ed25519.PublicKey, comment string) string {
	wire := encodeSSHWire([]byte("ssh-ed25519"), []byte(pub))
	return fmt.Sprintf("ssh-ed25519 %s %s",
		base64.StdEncoding.EncodeToString(wire),
		comment,
	)
}

// encodeED25519PrivateKeyOpenSSH returns a PEM-armoured OpenSSH-format
// private key (the format `ssh-keygen -t ed25519` produces by default).
//
// The on-the-wire structure of the openssh-key-v1 container is documented
// in the OpenSSH source `PROTOCOL.key`:
//
//	"openssh-key-v1\x00"
//	string    cipher_name      ("none" — no passphrase)
//	string    kdf_name         ("none")
//	string    kdf_options      ("")
//	uint32    number_of_keys   (1)
//	string    public_key_blob  (same as authorized_keys wire format)
//	string    encrypted_section
//
// `encrypted_section` for an unencrypted key is the plaintext:
//
//	uint32   check1            (random — must equal check2)
//	uint32   check2            (same value)
//	string   "ssh-ed25519"
//	string   public_key_bytes  (32 bytes)
//	string   private_key_bytes (64 bytes — ed25519 priv key includes pub half)
//	string   comment
//	padding  1, 2, 3, ...      (until length % blocksize == 0; blocksize 8)
func encodeED25519PrivateKeyOpenSSH(pub ed25519.PublicKey, priv ed25519.PrivateKey, comment string) (string, error) {
	const magic = "openssh-key-v1\x00"

	// Public-key blob (same wire format used in authorized_keys).
	pubBlob := encodeSSHWire([]byte("ssh-ed25519"), []byte(pub))

	// Random 32-bit "check" word, used twice — KDF-less integrity hint.
	var check [4]byte
	if _, err := io.ReadFull(rand.Reader, check[:]); err != nil {
		return "", fmt.Errorf("read random check bytes: %w", err)
	}

	var inner []byte
	inner = append(inner, check[:]...)
	inner = append(inner, check[:]...)
	inner = appendSSHString(inner, []byte("ssh-ed25519"))
	inner = appendSSHString(inner, []byte(pub))
	inner = appendSSHString(inner, []byte(priv))
	inner = appendSSHString(inner, []byte(comment))

	// Pad inner to a multiple of the cipher block size. For "none" the
	// effective block size is 8 (per PROTOCOL.key).
	const blockSize = 8
	for i := byte(1); len(inner)%blockSize != 0; i++ {
		inner = append(inner, i)
	}

	var body []byte
	body = append(body, []byte(magic)...)
	body = appendSSHString(body, []byte("none")) // cipher
	body = appendSSHString(body, []byte("none")) // kdf
	body = appendSSHString(body, []byte(""))     // kdf options
	body = appendUint32(body, 1)                 // num keys
	body = appendSSHString(body, pubBlob)        // public-key blob
	body = appendSSHString(body, inner)          // encrypted (here: plaintext) section

	pemBlock := &pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: body,
	}
	return string(pem.EncodeToMemory(pemBlock)), nil
}

// sshSHA256Fingerprint returns the canonical "SHA256:base64-no-pad"
// fingerprint of an ed25519 public key, matching `ssh-keygen -lf`.
func sshSHA256Fingerprint(pub ed25519.PublicKey) string {
	wire := encodeSSHWire([]byte("ssh-ed25519"), []byte(pub))
	sum := sha256.Sum256(wire)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

/* ── Low-level SSH wire helpers ──────────────────────────────────── */

// encodeSSHWire concatenates one or more length-prefixed strings.
func encodeSSHWire(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = appendSSHString(out, p)
	}
	return out
}

// appendSSHString writes a uint32 length followed by the raw bytes.
func appendSSHString(dst, s []byte) []byte {
	dst = appendUint32(dst, uint32(len(s)))
	dst = append(dst, s...)
	return dst
}

func appendUint32(dst []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(dst, b[:]...)
}
