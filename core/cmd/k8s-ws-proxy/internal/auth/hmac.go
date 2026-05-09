// Package auth verifies the X-Catalyst-HMAC header used to authenticate
// upstream callers (catalyst-api or Guacamole's k8s-shell adapter)
// against the local k8s-ws-proxy DaemonSet.
//
// Wire contract:
//
//	X-Catalyst-Timestamp: <unix-seconds>
//	X-Catalyst-HMAC:      hex(HMAC-SHA256(shared-secret, "<unix-seconds>:<request-path>"))
//
// The path component is the URL.Path of the incoming request (NOT the
// raw URI; query string and fragment are excluded). We use unix-seconds
// rather than RFC3339 because operators routinely run the upstream
// caller on a host whose clock may drift by a few seconds — comparing
// integers is cheaper than parsing a date.
//
// Skew window: requests whose timestamp is more than DefaultSkew (5
// minutes) older OR younger than the local clock are rejected as
// expired. Five minutes accommodates VM clock drift while keeping the
// replay window narrow.
//
// The shared secret is loaded from a K8s Secret (per
// docs/INVIOLABLE-PRINCIPLES.md #5) via env var SHARED_SECRET_FILE;
// the binary refuses to start if the file is missing or empty. There
// is no in-binary fallback secret.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultSkew is the maximum tolerated clock skew in either direction
// between caller and proxy.
const DefaultSkew = 5 * time.Minute

// Header names. Public so tests + callers can reference one source of
// truth.
const (
	HeaderHMAC      = "X-Catalyst-HMAC"
	HeaderTimestamp = "X-Catalyst-Timestamp"
)

// Sentinel errors. Verify returns one of these so callers (the WebSocket
// upgrader) can map the failure mode to a typed log/metric without
// string-matching.
var (
	ErrMissingHMAC      = errors.New("auth: X-Catalyst-HMAC header missing")
	ErrMissingTimestamp = errors.New("auth: X-Catalyst-Timestamp header missing")
	ErrMalformedHMAC    = errors.New("auth: X-Catalyst-HMAC malformed (expected hex(SHA256))")
	ErrMalformedTime    = errors.New("auth: X-Catalyst-Timestamp not an integer unix-seconds")
	ErrExpired          = errors.New("auth: timestamp outside acceptable skew window")
	ErrSignatureBad     = errors.New("auth: HMAC signature does not match")
	ErrEmptySecret      = errors.New("auth: shared secret is empty")
)

// Verifier holds the shared secret + the skew window. Construct via
// NewVerifier; Verify is safe for concurrent use.
type Verifier struct {
	secret []byte
	skew   time.Duration
	now    func() time.Time // injectable clock for tests
}

// NewVerifier builds a Verifier from a non-empty secret. Returns
// ErrEmptySecret when secret is empty (the binary fails fast on
// startup; the verifier never sees a runtime hot-swap to an empty
// secret).
func NewVerifier(secret []byte, skew time.Duration) (*Verifier, error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	if skew <= 0 {
		skew = DefaultSkew
	}
	return &Verifier{
		secret: secret,
		skew:   skew,
		now:    time.Now,
	}, nil
}

// VerifyRequest extracts the timestamp + HMAC headers from r and validates
// them against r.URL.Path. Returns nil on success, otherwise one of the
// sentinel errors above. Constant-time HMAC compare via crypto/hmac.Equal
// guards against timing oracles.
func (v *Verifier) VerifyRequest(r *http.Request) error {
	tsRaw := strings.TrimSpace(r.Header.Get(HeaderTimestamp))
	if tsRaw == "" {
		return ErrMissingTimestamp
	}
	macRaw := strings.TrimSpace(r.Header.Get(HeaderHMAC))
	if macRaw == "" {
		return ErrMissingHMAC
	}
	return v.Verify(tsRaw, macRaw, r.URL.Path)
}

// Verify is the testable core: given the raw timestamp + hex MAC + the
// path the caller signed, returns nil iff the MAC matches AND the
// timestamp is within the skew window.
func (v *Verifier) Verify(tsRaw, macHex, path string) error {
	tsInt, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return ErrMalformedTime
	}
	got, err := hex.DecodeString(macHex)
	if err != nil || len(got) != sha256.Size {
		return ErrMalformedHMAC
	}
	now := v.now().Unix()
	skewSecs := int64(v.skew.Seconds())
	if tsInt < now-skewSecs || tsInt > now+skewSecs {
		return fmt.Errorf("%w: ts=%d now=%d skew=%ds", ErrExpired, tsInt, now, skewSecs)
	}
	want := Compute(v.secret, tsInt, path)
	if !hmac.Equal(want, got) {
		return ErrSignatureBad
	}
	return nil
}

// Compute returns the canonical HMAC-SHA256 over "<unix-seconds>:<path>"
// keyed by secret. Exposed so callers (catalyst-api, tests, the
// optional `k8s-ws-proxy sign` debug subcommand) can produce signatures
// without re-implementing the algorithm.
func Compute(secret []byte, unixSeconds int64, path string) []byte {
	mac := hmac.New(sha256.New, secret)
	// fmt.Fprintf into the mac is slower than the explicit string build
	// because hmac's Write expects []byte and fmt.Fprintf would allocate
	// a buffer first. Use strconv directly.
	mac.Write([]byte(strconv.FormatInt(unixSeconds, 10)))
	mac.Write([]byte{':'})
	mac.Write([]byte(path))
	return mac.Sum(nil)
}

// ComputeHex returns the lowercase hex encoding of Compute. Convenient
// for callers that put the value directly into the
// X-Catalyst-HMAC header.
func ComputeHex(secret []byte, unixSeconds int64, path string) string {
	return hex.EncodeToString(Compute(secret, unixSeconds, path))
}

// SetClockForTest replaces the verifier's clock source. Test-only.
func (v *Verifier) SetClockForTest(now func() time.Time) {
	v.now = now
}
