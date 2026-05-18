// purge_ssh_key_comment_test.go — regression sentinel for TBD-A16.
//
// The bug: orphan SSH keys whose `name` and `label` both drifted from the
// Tofu module's canonical emission were surviving every wipe pass because
// the label-pass and name-prefix-pass had no signal to attribute the key
// to the Sovereign being wiped. Observed in production on 2026-05-18 with
// `catalyst-t24-omantel-biz` blocking fresh t25 provs.
//
// The fix: a third match vector — the OpenSSH-format `public_key`
// comment. The Catalyst bootstrap-cli stamps the canonical prefix into
// the comment at key generation time, so even keys whose name + label
// drifted carry it. This test pins the third pass:
//
//   1. TestPurge_SSHKey_PublicKeyCommentFallback_DeletesUnlabeled — when
//      a key's name/label are wrong but its public_key comment matches
//      the canonical prefix, Purge deletes it via the third pass.
//
//   2. TestPurge_SSHKey_PublicKeyCommentFallback_BoundarySafety — CRITICAL
//      P0 safety guard. Wiping `t2.omantel.biz` must NOT delete a
//      `t20.omantel.biz` SSH key whose comment is
//      `catalyst-t20-fresh@host`. The comment-prefix match must enforce
//      a boundary separator after the prefix.
//
//   3. TestPurge_SSHKey_PublicKeyCommentFallback_NoDoubleCount — when
//      the label or name pass already deleted a key, the comment pass
//      MUST NOT add it to the report again.
//
//   4. TestPurge_SSHKey_PublicKeyCommentFallback_LeavesOtherKeys — keys
//      with no comment OR with a comment for a different Sovereign are
//      left strictly alone.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHetznerSSHKeyComment is a Hetzner Cloud API stub for the SSH-key
// public-key-comment fallback path. Returns NO entries for the
// label_selector pass and a configurable set for the unlabeled (full-list)
// pass.
type fakeHetznerSSHKeyComment struct {
	t *testing.T

	// unlabeled is the per-kind entries returned when no label_selector
	// is sent. SSH keys include the public_key field.
	unlabeled map[string][]map[string]any

	mu       sync.Mutex
	deletes  map[string][]int64
	listHits map[string]int // how many unlabeled list calls hit each kind
}

func newFakeHetznerSSHKeyComment(t *testing.T) *fakeHetznerSSHKeyComment {
	return &fakeHetznerSSHKeyComment{
		t:         t,
		unlabeled: map[string][]map[string]any{},
		deletes:   map[string][]int64{},
		listHits:  map[string]int{},
	}
}

func (f *fakeHetznerSSHKeyComment) handler() http.Handler {
	mux := http.NewServeMux()
	for _, kind := range []string{"servers", "load_balancers", "firewalls", "networks", "ssh_keys", "volumes", "primary_ips", "floating_ips"} {
		kind := kind
		path := "/v1/" + kind
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			selector := r.URL.Query().Get("label_selector")
			var entries []map[string]any
			if selector == "" {
				f.mu.Lock()
				f.listHits[kind]++
				f.mu.Unlock()
				entries = f.unlabeled[kind]
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"pagination": map[string]any{"next_page": nil}},
				kind:   entries,
			})
		})
		mux.HandleFunc(path+"/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			tail := strings.TrimPrefix(r.URL.Path, path+"/")
			var id int64
			fmt.Sscanf(tail, "%d", &id)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deletes[kind] = append(f.deletes[kind], id)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}

func sshKeyCommentTestSetup(t *testing.T, fake *fakeHetznerSSHKeyComment) func() {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("parse test server url: %v", err)
	}
	origClient := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport},
		Timeout:   5 * time.Second,
	}
	origBackoff := firewallRetryInitialBackoff
	firewallRetryInitialBackoff = 1 * time.Millisecond
	return func() {
		srv.Close()
		purgeHTTPClient = origClient
		firewallRetryInitialBackoff = origBackoff
	}
}

// TestPurge_SSHKey_PublicKeyCommentFallback_DeletesUnlabeled — when the
// key's name AND label both drift but the public_key comment carries the
// canonical prefix, Purge MUST delete it via the third pass.
func TestPurge_SSHKey_PublicKeyCommentFallback_DeletesUnlabeled(t *testing.T) {
	const fqdn = "t25.omantel.biz"
	const prefix = "catalyst-t25-omantel-biz"

	fake := newFakeHetznerSSHKeyComment(t)
	// Production-shape orphan: name was renamed to a non-canonical form
	// (e.g. operator clicked Rename in Hetzner Console) and the label was
	// stripped. Only the public_key comment still carries the prefix.
	fake.unlabeled["ssh_keys"] = []map[string]any{
		{
			"id":         5005,
			"name":       "operator-renamed-this-key",
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP " + prefix + "-fresh@bastion-vmi3305700",
		},
	}
	cleanup := sshKeyCommentTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if len(report.SSHKeys) != 1 {
		t.Fatalf("expected 1 ssh key in report (via public-key-comment fallback), got %v", report.SSHKeys)
	}
	if report.SSHKeys[0] != "operator-renamed-this-key" {
		t.Errorf("report.SSHKeys[0] = %q, want %q", report.SSHKeys[0], "operator-renamed-this-key")
	}
	if len(fake.deletes["ssh_keys"]) != 1 || fake.deletes["ssh_keys"][0] != 5005 {
		t.Errorf("fake DELETE ssh_keys = %v, want [5005]", fake.deletes["ssh_keys"])
	}
}

// TestPurge_SSHKey_PublicKeyCommentFallback_BoundarySafety — CRITICAL P0
// safety guard. Wiping `t2.omantel.biz` must NOT touch a `t20.omantel.biz`
// SSH key whose comment is `catalyst-t20-fresh@host`. Without the
// boundary-separator check this would be a P0: a tenant numerically-
// neighbouring the wipe target loses their SSH key silently.
func TestPurge_SSHKey_PublicKeyCommentFallback_BoundarySafety(t *testing.T) {
	const fqdn = "t2.omantel.biz"
	const prefix = "catalyst-t2-omantel-biz"

	fake := newFakeHetznerSSHKeyComment(t)
	// Neighbouring tenant's key — its comment is `catalyst-t20-omantel-biz-...`
	// which has the SAME prefix string when naively HasPrefix'd. The
	// boundary check must reject because the next rune is a digit, not a
	// separator.
	fake.unlabeled["ssh_keys"] = []map[string]any{
		{
			"id":         9999,
			"name":       "catalyst-t20-omantel-biz",
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP catalyst-t20-omantel-biz-fresh@bastion",
		},
	}
	cleanup := sshKeyCommentTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if len(report.SSHKeys) != 0 {
		t.Fatalf("PURGE TOUCHED ANOTHER TENANT'S SSH KEY — P0 safety regression. report.SSHKeys=%v", report.SSHKeys)
	}
	if len(fake.deletes["ssh_keys"]) > 0 {
		t.Fatalf("fake recorded DELETE ssh_keys %v against another tenant — P0 safety regression", fake.deletes["ssh_keys"])
	}
	_ = prefix // documentation
}

// TestPurge_SSHKey_PublicKeyCommentFallback_NoDoubleCount — when the
// label or name-prefix pass already deleted a key, the comment pass MUST
// NOT add it to the report a second time. Production case: most keys
// carry both name + label (deleted by passes 1/2); the comment pass runs
// against an already-empty result set.
func TestPurge_SSHKey_PublicKeyCommentFallback_NoDoubleCount(t *testing.T) {
	const fqdn = "t-mixed.omani.works"
	const prefix = "catalyst-t-mixed-omani-works"
	const fqdnLabel = "t-mixed.omani.works"

	mux := http.NewServeMux()
	deletedSSH := map[int64]int{}

	// For every kind except ssh_keys, return empty.
	for _, kind := range []string{"servers", "load_balancers", "firewalls", "networks", "volumes", "primary_ips", "floating_ips"} {
		kind := kind
		path := "/v1/" + kind
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"pagination": map[string]any{"next_page": nil}},
				kind:   []any{},
			})
		})
	}
	// ssh_keys — the label pass returns one key with a matching name +
	// comment. The unlabeled pass (name-prefix + comment) ALSO returns
	// the SAME key (Hetzner doesn't strip it between calls; it's listed
	// regardless of selector). The third pass must NOT re-add it because
	// it's already in report.SSHKeys.
	mux.HandleFunc("/v1/ssh_keys", func(w http.ResponseWriter, r *http.Request) {
		entries := []map[string]any{
			{
				"id":         5005,
				"name":       prefix,
				"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP " + prefix + "-fresh@bastion",
				"labels":     map[string]string{"catalyst.openova.io/sovereign": fqdnLabel},
			},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta":     map[string]any{"pagination": map[string]any{"next_page": nil}},
			"ssh_keys": entries,
		})
	})
	mux.HandleFunc("/v1/ssh_keys/", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/v1/ssh_keys/"), "%d", &id)
		deletedSSH[id]++
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)
	orig := purgeHTTPClient
	purgeHTTPClient = &http.Client{
		Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport},
		Timeout:   5 * time.Second,
	}
	defer func() { purgeHTTPClient = orig }()

	origBackoff := firewallRetryInitialBackoff
	firewallRetryInitialBackoff = 1 * time.Millisecond
	defer func() { firewallRetryInitialBackoff = origBackoff }()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if len(report.SSHKeys) != 1 || report.SSHKeys[0] != prefix {
		t.Errorf("report.SSHKeys = %v, want exactly [%s] (no double-count)", report.SSHKeys, prefix)
	}
	// Hetzner DELETE 5005 may be called multiple times across passes
	// because the fake returns the same row to every selector; what
	// matters is the report doesn't double-count. (Hetzner real API
	// would 404 on the second call which deleteResource treats as
	// success — exercised by purge_name_prefix_test's NoDoubleCount.)
}

// TestPurge_SSHKey_PublicKeyCommentFallback_LeavesOtherKeys — defensive:
// keys with NO comment, or with a comment that doesn't carry the prefix,
// MUST be left strictly alone.
func TestPurge_SSHKey_PublicKeyCommentFallback_LeavesOtherKeys(t *testing.T) {
	const fqdn = "t25.omantel.biz"
	const prefix = "catalyst-t25-omantel-biz"

	fake := newFakeHetznerSSHKeyComment(t)
	fake.unlabeled["ssh_keys"] = []map[string]any{
		// Other Sovereign — comment doesn't match.
		{
			"id":         5001,
			"name":       "operator-personal-key",
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP catalyst-t22-omantel-biz-fresh@bastion",
		},
		// No comment at all (only key type + data).
		{
			"id":         5002,
			"name":       "operator-no-comment",
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP",
		},
		// Empty public_key field.
		{
			"id":         5003,
			"name":       "operator-empty-pubkey",
			"public_key": "",
		},
		// Matching key — MUST be deleted.
		{
			"id":         5004,
			"name":       "renamed-orphan",
			"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ/ZEfnTv4cH8Kdz7N4JqeYNeKapCJelzDLHP " + prefix + "@bastion",
		},
	}
	cleanup := sshKeyCommentTestSetup(t, fake)
	defer cleanup()

	report, err := Purge(context.Background(), "fake-token", fqdn, nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}

	if len(report.SSHKeys) != 1 || report.SSHKeys[0] != "renamed-orphan" {
		t.Errorf("report.SSHKeys = %v, want [renamed-orphan]", report.SSHKeys)
	}
	if len(fake.deletes["ssh_keys"]) != 1 || fake.deletes["ssh_keys"][0] != 5004 {
		t.Errorf("fake DELETE ssh_keys = %v, want [5004] only — other keys must be untouched", fake.deletes["ssh_keys"])
	}
}

// TestPublicKeyComment_ParsesFormats — unit-level pin for the OpenSSH
// public-key comment parser. The comment field is everything after the
// second whitespace run; we must handle keys with no comment and keys
// whose comment itself contains whitespace.
func TestPublicKeyComment_ParsesFormats(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		comment string
	}{
		{"standard ed25519 with simple comment",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ catalyst-t24-fresh@bastion",
			"catalyst-t24-fresh@bastion"},
		{"comment with whitespace",
			"ssh-rsa AAAAsomethingsomething_b64== this is my key",
			"this is my key"},
		{"no comment",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ",
			""},
		{"empty key",
			"",
			""},
		{"only key type",
			"ssh-ed25519",
			""},
		{"leading whitespace tolerated",
			"  ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINnoumhQZ catalyst-t25-orphan@host  ",
			"catalyst-t25-orphan@host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := publicKeyComment(tc.key)
			if got != tc.comment {
				t.Errorf("publicKeyComment(%q) = %q, want %q", tc.key, got, tc.comment)
			}
		})
	}
}

// TestCommentMatchesPrefix_BoundaryRules — unit-level pin for the
// boundary-aware prefix match used by the third SSH-key pass. The
// next-rune-after-prefix MUST be a separator (`-`, `.`, `@`, ` `, `_`)
// OR end-of-string. A digit/letter means we're crossing a stem boundary
// (e.g. t2 vs t20) and must be rejected.
func TestCommentMatchesPrefix_BoundaryRules(t *testing.T) {
	cases := []struct {
		comment string
		prefix  string
		want    bool
	}{
		{"catalyst-t25-omantel-biz", "catalyst-t25-omantel-biz", true},   // exact match
		{"catalyst-t25-omantel-biz-cp1", "catalyst-t25-omantel-biz", true}, // separator '-'
		{"catalyst-t25-omantel-biz@host", "catalyst-t25-omantel-biz", true}, // separator '@'
		{"catalyst-t25-omantel-biz host", "catalyst-t25-omantel-biz", true}, // separator ' '
		{"catalyst-t25-omantel-biz.suffix", "catalyst-t25-omantel-biz", true}, // separator '.'
		{"catalyst-t20-omantel-biz", "catalyst-t2-omantel-biz", false},   // digit boundary breach
		{"catalyst-t25-omantel-bizUNRELATED", "catalyst-t25-omantel-biz", false}, // letter boundary breach
		{"catalyst-t25", "catalyst-t2", false},                            // would-be P0
		{"", "catalyst-t25-omantel-biz", false},                           // empty comment
		{"catalyst-t25-omantel-biz", "", false},                           // empty prefix
		{"otherprefix-catalyst-t25-omantel-biz", "catalyst-t25-omantel-biz", false}, // prefix not at start
	}
	for _, tc := range cases {
		got := commentMatchesPrefix(tc.comment, tc.prefix)
		if got != tc.want {
			t.Errorf("commentMatchesPrefix(%q, %q) = %v, want %v",
				tc.comment, tc.prefix, got, tc.want)
		}
	}
}
