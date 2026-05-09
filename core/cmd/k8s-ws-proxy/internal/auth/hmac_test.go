package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestNewVerifier_EmptySecret(t *testing.T) {
	if _, err := NewVerifier(nil, 0); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("nil secret: got %v, want ErrEmptySecret", err)
	}
	if _, err := NewVerifier([]byte{}, 0); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("empty secret: got %v, want ErrEmptySecret", err)
	}
}

func TestVerifier_Verify_HappyPath(t *testing.T) {
	secret := []byte("topsecret-should-be-32+char-min")
	v, err := NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.SetClockForTest(func() time.Time { return now })

	path := "/proxy/exec/default/web-7d8b/web"
	ts := now.Unix()
	mac := ComputeHex(secret, ts, path)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderHMAC, mac)

	if err := v.VerifyRequest(req); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestVerifier_Verify_ExpiredOlder(t *testing.T) {
	secret := []byte("topsecret-should-be-32+char-min")
	v, err := NewVerifier(secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.SetClockForTest(func() time.Time { return now })

	old := now.Add(-2 * time.Minute).Unix()
	path := "/proxy/exec/default/web-7d8b/web"
	mac := ComputeHex(secret, old, path)

	if err := v.Verify(strconv.FormatInt(old, 10), mac, path); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired-old: got %v, want ErrExpired", err)
	}
}

func TestVerifier_Verify_ExpiredFuture(t *testing.T) {
	secret := []byte("topsecret-should-be-32+char-min")
	v, err := NewVerifier(secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v.SetClockForTest(func() time.Time { return now })

	future := now.Add(90 * time.Second).Unix()
	path := "/proxy/exec/default/web-7d8b/web"
	mac := ComputeHex(secret, future, path)

	if err := v.Verify(strconv.FormatInt(future, 10), mac, path); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired-future: got %v, want ErrExpired", err)
	}
}

func TestVerifier_Verify_MalformedTimestamp(t *testing.T) {
	v, err := NewVerifier([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Verify("not-a-number", "deadbeef", "/x"); !errors.Is(err, ErrMalformedTime) {
		t.Fatalf("got %v, want ErrMalformedTime", err)
	}
}

func TestVerifier_Verify_MalformedMAC(t *testing.T) {
	v, err := NewVerifier([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	v.SetClockForTest(func() time.Time { return time.Unix(1735689600, 0) })

	cases := []struct {
		name string
		mac  string
	}{
		{"not-hex", "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"wrong-length", "abcdef"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Verify("1735689600", tc.mac, "/x")
			if !errors.Is(err, ErrMalformedHMAC) {
				t.Fatalf("got %v, want ErrMalformedHMAC", err)
			}
		})
	}
}

func TestVerifier_Verify_BadSignature(t *testing.T) {
	secret := []byte("real-secret")
	v, err := NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	v.SetClockForTest(func() time.Time { return time.Unix(now, 0) })
	path := "/proxy/exec/default/web/web"

	wrongMAC := ComputeHex([]byte("attacker-secret"), now, path)
	if err := v.Verify(strconv.FormatInt(now, 10), wrongMAC, path); !errors.Is(err, ErrSignatureBad) {
		t.Fatalf("got %v, want ErrSignatureBad", err)
	}

	signed := ComputeHex(secret, now, "/proxy/exec/a/a/a")
	if err := v.Verify(strconv.FormatInt(now, 10), signed, "/proxy/exec/b/b/b"); !errors.Is(err, ErrSignatureBad) {
		t.Fatalf("path-tamper: got %v, want ErrSignatureBad", err)
	}

	signedT := ComputeHex(secret, now, path)
	if err := v.Verify(strconv.FormatInt(now+1, 10), signedT, path); !errors.Is(err, ErrSignatureBad) {
		t.Fatalf("ts-tamper: got %v, want ErrSignatureBad", err)
	}
}

func TestVerifier_VerifyRequest_HeadersMissing(t *testing.T) {
	v, err := NewVerifier([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if err := v.VerifyRequest(r); !errors.Is(err, ErrMissingTimestamp) {
		t.Fatalf("got %v, want ErrMissingTimestamp", err)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	r2.Header.Set(HeaderTimestamp, "1735689600")
	if err := v.VerifyRequest(r2); !errors.Is(err, ErrMissingHMAC) {
		t.Fatalf("got %v, want ErrMissingHMAC", err)
	}
}

func TestVerifier_VerifyRequest_PathOnly(t *testing.T) {
	// URL.Path (NOT RequestURI) — query-string is excluded from signature.
	secret := []byte("k")
	v, err := NewVerifier(secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	v.SetClockForTest(func() time.Time { return time.Unix(now, 0) })
	path := "/proxy/exec/default/web/web"
	mac := ComputeHex(secret, now, path)

	r := httptest.NewRequest(http.MethodGet, path+"?command=sh&tty=true", nil)
	r.Header.Set(HeaderTimestamp, strconv.FormatInt(now, 10))
	r.Header.Set(HeaderHMAC, mac)
	if err := v.VerifyRequest(r); err != nil {
		t.Fatalf("expected success on path-only signature, got %v", err)
	}
}

func TestComputeHex_Stable(t *testing.T) {
	// Lock the wire format. If this assertion ever changes, every
	// upstream caller's signature shape changes too. Compute should
	// always return 32 bytes (SHA-256) and ComputeHex 64 hex chars.
	got := ComputeHex([]byte("k"), 1735689600, "/proxy/exec/default/web/web")
	if len(got) != 64 {
		t.Fatalf("ComputeHex shape: got len=%d (%q), want hex64", len(got), got)
	}
	raw := Compute([]byte("k"), 1735689600, "/proxy/exec/default/web/web")
	if len(raw) != 32 {
		t.Fatalf("Compute returned %d bytes, want 32", len(raw))
	}
}
