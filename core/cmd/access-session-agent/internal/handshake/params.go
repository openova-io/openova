package handshake

import (
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/config"
)

// RecPath is the recording folder, the same path in the guacd and the agent
// containers.
const RecPath = "/rec"

// ErrCredentialExpired says that a credential file holds a certificate whose
// time has passed: a reconnect after the credential expired (reconnect rule).
var ErrCredentialExpired = errors.New("a credential file holds an expired certificate")

// Params builds the parameters of connection n (from 1): the literal
// parameters of the configuration; the content of each parameter file, read
// from dir now (never kept between handshakes) with one trailing newline
// removed; and the parameters the agent sets itself. Recording and typescript
// parameters are set only when the session is recorded.
func Params(c *config.Config, n int, dir string, now time.Time) (map[string]string, error) {
	p := map[string]string{}
	for k, v := range c.Params {
		p[k] = v
	}
	for k, name := range c.ParamFiles {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if err != nil {
			return nil, fmt.Errorf("read the file of parameter %s: %w", k, err)
		}
		if expired(b, now) {
			return nil, fmt.Errorf("parameter %s: %w", k, ErrCredentialExpired)
		}
		p[k] = strings.TrimSuffix(string(b), "\n")
	}
	p["disable-copy"] = strconv.FormatBool(!c.Controls.ClipboardCopy)
	p["disable-paste"] = strconv.FormatBool(!c.Controls.ClipboardPaste)
	if c.Recording() {
		p["recording-path"], p["recording-name"], p["create-recording-path"] = RecPath, "screen-"+strconv.Itoa(n), "true"
		p["recording-include-keys"] = strconv.FormatBool(c.Controls.Keystrokes)
		if c.Terminal {
			p["typescript-path"], p["typescript-name"], p["create-typescript-path"] = RecPath, "typescript-"+strconv.Itoa(n), "true"
		}
	}
	return p, nil
}

// expired says whether b holds a PEM certificate that is no longer valid at now.
func expired(b []byte, now time.Time) bool {
	for rest := b; ; {
		var blk *pem.Block
		if blk, rest = pem.Decode(rest); blk == nil {
			return false
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(blk.Bytes); err == nil && now.After(cert.NotAfter) {
			return true
		}
	}
}

// CredentialID names what was issued for the session, never a secret: the
// serial of the client certificate (kind pod), the database user (kind
// database) or the SSH key identifier, which is the session UID (kind ssh).
func CredentialID(c *config.Config, dir string) (string, error) {
	switch c.Kind {
	case "ssh":
		return c.SessionUID, nil
	case "database":
		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "db-username")))
		user := strings.TrimSpace(string(b))
		if err == nil && user == "" {
			err = errors.New("the file db-username is empty")
		}
		return user, err
	case "pod":
		name, ok := c.ParamFiles["client-cert"]
		if !ok {
			return "", errors.New("kind pod has no parameter file client-cert")
		}
		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if err != nil {
			return "", err
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			return "", fmt.Errorf("the file %s holds no PEM block", name)
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return "", fmt.Errorf("the file %s: %w", name, err)
		}
		return serial(cert.SerialNumber.Bytes()), nil
	}
	return "", fmt.Errorf("kind %q has no credential identifier", c.Kind)
}

// serial writes a serial number as OpenBao does: hexadecimal bytes joined by colons.
func serial(b []byte) string {
	if len(b) == 0 {
		b = []byte{0}
	}
	parts := make([]string, len(b))
	for i := range b {
		parts[i] = hex.EncodeToString(b[i : i+1])
	}
	return strings.Join(parts, ":")
}
