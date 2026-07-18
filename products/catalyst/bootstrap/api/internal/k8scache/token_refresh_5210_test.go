// token_refresh_5210_test.go — regression coverage for #5210.
//
// The Sovereign-side (chroot) catalyst-api builds its informer + resource-GET
// kube clients from rest.InClusterConfig(). The plain BearerToken/BearerTokenFile
// transport client-go installs for that config refreshes the projected token on
// a timer but does NOT reset its cache on a 401, so after a cutover disruption
// rotated the bound token the reflectors 401-looped on a stale/expired token
// even though a fresh read of the projected token file authenticated fine.
//
// wrapInClusterTokenRefreshOn401 swaps in a transport.ResettableTokenSource so
// the transport re-reads the rotated token file the instant it sees a 401.
package k8scache

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

// seqRT is a fake http.RoundTripper that records the Authorization header it
// received on each call and returns a scripted status code sequence.
type seqRT struct {
	authSeen []string
	codes    []int
	i        int
}

func (r *seqRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.authSeen = append(r.authSeen, req.Header.Get("Authorization"))
	code := http.StatusOK
	if r.i < len(r.codes) {
		code = r.codes[r.i]
	}
	r.i++
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

// TestWrapInClusterTokenRefresh_ClearsStaticCredsAndWires proves the config
// mutation: the static bearer creds are cleared (so client-go does not ALSO
// install the non-resetting bearerAuthRoundTripper) and a WrapTransport is
// wired. Env-independent — no in-cluster env required.
func TestWrapInClusterTokenRefresh_ClearsStaticCredsAndWires(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-A"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &rest.Config{BearerToken: "token-A", BearerTokenFile: tokenFile}
	wrapInClusterTokenRefreshOn401(cfg)

	if cfg.BearerToken != "" {
		t.Errorf("BearerToken not cleared: %q — client-go would install a non-resetting bearerAuthRoundTripper", cfg.BearerToken)
	}
	if cfg.BearerTokenFile != "" {
		t.Errorf("BearerTokenFile not cleared: %q", cfg.BearerTokenFile)
	}
	if cfg.WrapTransport == nil {
		t.Fatal("WrapTransport not wired — token would never reset on 401")
	}
}

// TestWrapInClusterTokenRefresh_NoOpWithoutTokenFile proves the off-cluster /
// dev path (kubeconfig-embedded static token, no BearerTokenFile) is left
// untouched.
func TestWrapInClusterTokenRefresh_NoOpWithoutTokenFile(t *testing.T) {
	cfg := &rest.Config{BearerToken: "static-kubeconfig-token"}
	wrapInClusterTokenRefreshOn401(cfg)
	if cfg.BearerToken != "static-kubeconfig-token" {
		t.Errorf("static token unexpectedly mutated: %q", cfg.BearerToken)
	}
	if cfg.WrapTransport != nil {
		t.Error("WrapTransport wired for a config with no BearerTokenFile — should be a no-op")
	}
}

// TestWrapInClusterTokenRefresh_RereadsFileOn401 is the behavioural regression:
// a cached token that starts returning 401 (the post-rotation stale-token case)
// forces an immediate re-read of the rotated projected token file on the next
// request — the recovery the plain bearer transport lacked.
func TestWrapInClusterTokenRefresh_RereadsFileOn401(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("token-A"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &rest.Config{BearerToken: "token-A", BearerTokenFile: tokenFile}
	wrapInClusterTokenRefreshOn401(cfg)

	base := &seqRT{codes: []int{http.StatusOK, http.StatusUnauthorized, http.StatusOK}}
	rt := cfg.WrapTransport(base)

	do := func() {
		req, err := http.NewRequest(http.MethodGet, "https://apiserver.local/api", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("roundtrip: %v", err)
		}
		_ = resp.Body.Close()
	}

	// 1) prime the token cache with token-A (200).
	do()
	// kubelet rotates the projected token on disk.
	if err := os.WriteFile(tokenFile, []byte("token-B"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 2) cached token-A is still within its 1-minute cache window → the apiserver
	//    rejects it with 401 → the transport resets the token cache.
	do()
	// 3) next request must re-read the file and present the rotated token-B.
	do()

	if len(base.authSeen) != 3 {
		t.Fatalf("expected 3 upstream requests, got %d (%v)", len(base.authSeen), base.authSeen)
	}
	if base.authSeen[0] != "Bearer token-A" {
		t.Errorf("req1 Authorization = %q, want %q", base.authSeen[0], "Bearer token-A")
	}
	if base.authSeen[1] != "Bearer token-A" {
		t.Errorf("req2 Authorization = %q, want %q (cached, pre-401)", base.authSeen[1], "Bearer token-A")
	}
	if base.authSeen[2] != "Bearer token-B" {
		t.Errorf("req3 Authorization = %q, want %q (re-read after 401) — token did NOT refresh on 401 (#5210 regression)", base.authSeen[2], "Bearer token-B")
	}
}
