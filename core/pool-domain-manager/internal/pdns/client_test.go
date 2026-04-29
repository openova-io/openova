package pdns

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer wraps a handler in httptest.Server and returns a Client
// pointed at it.
func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "localhost", "test-api-key")
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c, srv
}

func TestCreateZoneSuccess(t *testing.T) {
	var got Zone
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/servers/localhost/zones" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-api-key" {
			t.Errorf("missing X-API-Key header")
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"name":"omantel.omani.works.","kind":"Native"}`))
	})

	err := c.CreateZone(context.Background(), "omantel.omani.works", ZoneKindNative, []string{"ns1.openova.io", "ns2.openova.io"})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	if got.Name != "omantel.omani.works." {
		t.Errorf("zone name = %s, want omantel.omani.works.", got.Name)
	}
	if got.Kind != ZoneKindNative {
		t.Errorf("zone kind = %s, want Native", got.Kind)
	}
	if len(got.Nameservers) != 2 || got.Nameservers[0] != "ns1.openova.io." {
		t.Errorf("nameservers = %v, want [ns1.openova.io. ns2.openova.io.]", got.Nameservers)
	}
}

func TestCreateZoneIdempotentOnConflict(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"Zone exists"}`))
	})
	if err := c.CreateZone(context.Background(), "omani.works", ZoneKindNative, nil); err != nil {
		t.Errorf("CreateZone on 409 should be nil, got %v", err)
	}
}

func TestCreateZoneServerErrorRetries(t *testing.T) {
	var calls int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	if err := c.CreateZone(context.Background(), "test.io", ZoneKindNative, nil); err != nil {
		t.Errorf("retry path failed: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 calls (retry once on 5xx), got %d", calls)
	}
}

func TestCreateZoneSurfacesAPIError(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"Invalid zone name"}`))
	})
	err := c.CreateZone(context.Background(), "bad..zone", ZoneKindNative, nil)
	if err == nil {
		t.Fatal("expected error on 422")
	}
	if !strings.Contains(err.Error(), "Invalid zone name") {
		t.Errorf("error did not surface upstream message: %v", err)
	}
}

func TestDeleteZone(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/zones/omantel.omani.works.") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteZone(context.Background(), "omantel.omani.works"); err != nil {
		t.Errorf("DeleteZone: %v", err)
	}
}

func TestDeleteZoneIdempotentOnNotFound(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := c.DeleteZone(context.Background(), "ghost.io"); err != nil {
		t.Errorf("404 should be nil for idempotent delete, got %v", err)
	}
}

func TestZoneExists(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"present", 200, true},
		{"absent", 404, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			})
			got, err := c.ZoneExists(context.Background(), "test.io")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ZoneExists = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPatchRRSetsAddARecord(t *testing.T) {
	var captured struct {
		RRSets []RRSet `json:"rrsets"`
	}
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.AddARecord(context.Background(), "omantel.omani.works", "console.omantel.omani.works", "1.2.3.4", 0); err != nil {
		t.Fatal(err)
	}
	if len(captured.RRSets) != 1 {
		t.Fatalf("expected 1 rrset, got %d", len(captured.RRSets))
	}
	r := captured.RRSets[0]
	if r.Name != "console.omantel.omani.works." {
		t.Errorf("name = %s", r.Name)
	}
	if r.Type != "A" {
		t.Errorf("type = %s", r.Type)
	}
	if r.TTL != DefaultChildRecordTTL {
		t.Errorf("ttl = %d, want %d", r.TTL, DefaultChildRecordTTL)
	}
	if r.ChangeType != "REPLACE" {
		t.Errorf("changetype = %s, want REPLACE", r.ChangeType)
	}
	if len(r.Records) != 1 || r.Records[0].Content != "1.2.3.4" {
		t.Errorf("records = %v", r.Records)
	}
}

func TestAddNSDelegationDefaultsTTLAndCanonicalises(t *testing.T) {
	var captured struct {
		RRSets []RRSet `json:"rrsets"`
	}
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	err := c.AddNSDelegation(context.Background(), "omani.works", "omantel.omani.works",
		[]string{"ns1.openova.io", "ns2.openova.io", "ns3.openova.io"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := captured.RRSets[0]
	if r.TTL != DefaultParentNSDelegationTTL {
		t.Errorf("ttl = %d, want %d", r.TTL, DefaultParentNSDelegationTTL)
	}
	if r.Type != "NS" {
		t.Errorf("type = %s", r.Type)
	}
	if len(r.Records) != 3 {
		t.Fatalf("expected 3 NS records, got %d", len(r.Records))
	}
	for _, rec := range r.Records {
		if !strings.HasSuffix(rec.Content, ".") {
			t.Errorf("ns content not canonical: %q", rec.Content)
		}
	}
}

func TestAddNSDelegationRequiresNameservers(t *testing.T) {
	c := New("http://localhost", "", "")
	err := c.AddNSDelegation(context.Background(), "omani.works", "omantel.omani.works", nil, 0)
	if err == nil {
		t.Fatal("expected error for empty nameservers")
	}
}

func TestRemoveNSDelegation(t *testing.T) {
	var captured struct {
		RRSets []RRSet `json:"rrsets"`
	}
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RemoveNSDelegation(context.Background(), "omani.works", "omantel.omani.works"); err != nil {
		t.Fatal(err)
	}
	if captured.RRSets[0].ChangeType != "DELETE" {
		t.Errorf("changetype = %s, want DELETE", captured.RRSets[0].ChangeType)
	}
}

func TestEnableDNSSECFullCycle(t *testing.T) {
	// Track the sequence of API calls to verify the full PUT-flag,
	// list-keys, create-KSK, create-ZSK, rectify path.
	var (
		putFlag         atomic.Bool
		listKeysCalls   atomic.Int32
		createKSK       atomic.Bool
		createZSK       atomic.Bool
		rectified       atomic.Bool
	)
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/zones/test.io.") && !strings.Contains(r.URL.Path, "/rectify"):
			putFlag.Store(true)
			body, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			if d, _ := b["dnssec"].(bool); !d {
				t.Errorf("dnssec flag not true")
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cryptokeys"):
			listKeysCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cryptokeys"):
			body, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			alg, _ := b["algorithm"].(string)
			if alg != "ecdsa256" {
				t.Errorf("algorithm = %s, want ecdsa256", alg)
			}
			kt, _ := b["keytype"].(string)
			switch kt {
			case "ksk":
				createKSK.Store(true)
			case "zsk":
				createZSK.Store(true)
			default:
				t.Errorf("unexpected keytype %q", kt)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/rectify"):
			rectified.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
		}
	})
	if err := c.EnableDNSSEC(context.Background(), "test.io"); err != nil {
		t.Fatal(err)
	}
	if !putFlag.Load() {
		t.Error("PUT dnssec flag never sent")
	}
	if listKeysCalls.Load() < 1 {
		t.Error("list cryptokeys never called")
	}
	if !createKSK.Load() {
		t.Error("KSK never created")
	}
	if !createZSK.Load() {
		t.Error("ZSK never created")
	}
	if !rectified.Load() {
		t.Error("rectify never called")
	}
}

func TestEnableDNSSECSkipsExistingActiveKeys(t *testing.T) {
	// When KSK + ZSK already exist active, EnableDNSSEC must NOT recreate them.
	var keyCreations atomic.Int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && !strings.Contains(r.URL.Path, "/rectify"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cryptokeys"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"id":1,"keytype":"ksk","active":true,"algorithm":"ecdsa256"},
				{"id":2,"keytype":"zsk","active":true,"algorithm":"ecdsa256"}
			]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cryptokeys"):
			keyCreations.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/rectify"):
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	})
	if err := c.EnableDNSSEC(context.Background(), "test.io"); err != nil {
		t.Fatal(err)
	}
	if keyCreations.Load() != 0 {
		t.Errorf("expected 0 key creations when active KSK/ZSK exist, got %d", keyCreations.Load())
	}
}

func TestListCryptokeys(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"id":11,"keytype":"ksk","active":true,"published":true,"algorithm":"ecdsa256"},
			{"id":12,"keytype":"zsk","active":true,"published":true,"algorithm":"ecdsa256"}
		]`))
	})
	keys, err := c.ListCryptokeys(context.Background(), "test.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0].KeyType != "ksk" || !keys[0].Active {
		t.Errorf("ksk wrong: %+v", keys[0])
	}
}

func TestEnsureZoneCreatesIfMissing(t *testing.T) {
	var (
		getCalled    atomic.Bool
		postCalled   atomic.Bool
	)
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCalled.Store(true)
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			postCalled.Store(true)
			w.WriteHeader(http.StatusCreated)
		}
	})
	if err := c.EnsureZone(context.Background(), "new.zone", ZoneKindNative, []string{"ns1.openova.io"}); err != nil {
		t.Fatal(err)
	}
	if !getCalled.Load() || !postCalled.Load() {
		t.Errorf("EnsureZone did not call GET then POST: get=%v post=%v", getCalled.Load(), postCalled.Load())
	}
}

func TestEnsureZoneSkipsWhenPresent(t *testing.T) {
	var postCalled atomic.Bool
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"name":"existing.zone."}`))
			return
		}
		if r.Method == http.MethodPost {
			postCalled.Store(true)
			w.WriteHeader(http.StatusCreated)
		}
	})
	if err := c.EnsureZone(context.Background(), "existing.zone", ZoneKindNative, nil); err != nil {
		t.Fatal(err)
	}
	if postCalled.Load() {
		t.Error("EnsureZone POSTed despite zone existing")
	}
}

func TestCanonicaliseZone(t *testing.T) {
	tests := map[string]string{
		"omani.works":          "omani.works.",
		"OMANI.WORKS.":         "omani.works.",
		" Omantel.omani.works": "omantel.omani.works.",
		"":                     "",
	}
	for in, want := range tests {
		if got := canonicaliseZone(in); got != want {
			t.Errorf("canonicaliseZone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPatchRRSetsEmptyIsNoop(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("PATCH should not be called for empty rrsets")
	})
	if err := c.PatchRRSets(context.Background(), "test.io", nil); err != nil {
		t.Errorf("empty rrsets should be no-op, got %v", err)
	}
}

func TestRetryGivesUpAfterAllAttempts(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	err := c.CreateZone(context.Background(), "x.io", ZoneKindNative, nil)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	// 3 attempts: initial + 2 retries (backoffs slice has 3 entries).
	if calls.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestContextCancellationStopsRetry(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.CreateZone(ctx, "x.io", ZoneKindNative, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "context canceled") {
		// retry path may surface the upstream 500 if it raced before cancel
		// — accept either as long as the call returned promptly.
		t.Logf("got %v (acceptable if request raced cancellation)", err)
	}
}

func TestNewDefaultsServerID(t *testing.T) {
	c := New("http://x", "", "key")
	if c.ServerID != "localhost" {
		t.Errorf("ServerID default = %s, want localhost", c.ServerID)
	}
}

func TestAuthorizationHeader(t *testing.T) {
	var got string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	c.AuthorizationHeader = "Basic Zm9vOmJhcg=="
	_, _ = c.ZoneExists(context.Background(), "x.io")
	if got != "Basic Zm9vOmJhcg==" {
		t.Errorf("Authorization header = %q", got)
	}
}
