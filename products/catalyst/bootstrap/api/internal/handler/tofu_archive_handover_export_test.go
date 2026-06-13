// Tests for the mother-side phase0 tofu-archive handover export (#3376).
//
// What this file proves:
//
//  1. postTofuArchiveWithRetry delivers the archive to the child's
//     /api/v1/handover/tofu-archive on the success path (single hit, no
//     retry storm).
//  2. It RETRIES a 503 ("not handover target" — the child's OpenBao token
//     not yet wired) because the receiver-side k8s-auth login may still be
//     settling, then succeeds when the child flips to 200.
//  3. It gives up immediately on a 4xx (a genuine reject — bad payload),
//     no retry storm.
package handler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeArchiveReceiver mimics the child's ReceiveTofuArchive endpoint. It
// returns firstStatus for the first `flipAfter` hits, then 200. With
// flipAfter=0 every hit returns firstStatus.
type fakeArchiveReceiver struct {
	firstStatus int
	flipAfter   int32
	hits        atomic.Int32
}

func (f *fakeArchiveReceiver) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.hits.Add(1)
		if f.flipAfter > 0 && n > f.flipAfter {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"secretPath":"secret/catalyst/tofu-phase0-archive"}`))
			return
		}
		if f.firstStatus == 0 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(f.firstStatus)
		_, _ = w.Write([]byte(`{"ok":false,"error":"not handover target"}`))
	})
}

func TestPostTofuArchive_DeliversOnFirstTry(t *testing.T) {
	recv := &fakeArchiveReceiver{}
	srv := httptest.NewServer(recv.handler())
	defer srv.Close()

	h := NewWithPDM(silentLogger(), &fakePDM{})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	h.postTofuArchiveWithRetry(client, "https://api.x.example/api/v1/handover/tofu-archive", []byte(`{"deploymentId":"d","sovereignFqdn":"f","files":{"main.tf":"YQ=="}}`), "d")

	if got := recv.hits.Load(); got != 1 {
		t.Fatalf("receiver hit count = %d, want 1 (clean single delivery)", got)
	}
}

func TestPostTofuArchive_Retries503ThenSucceeds(t *testing.T) {
	// The child returns 503 ("not handover target") for the first 2 hits
	// — its OpenBao k8s-auth login is still settling — then flips to 200.
	// The export MUST retry the 5xx and land the archive, not give up.
	recv := &fakeArchiveReceiver{firstStatus: http.StatusServiceUnavailable, flipAfter: 2}
	srv := httptest.NewServer(recv.handler())
	defer srv.Close()

	h := NewWithPDM(silentLogger(), &fakePDM{})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	// Shrink the first backoff implicitly by relying on the 5s starting
	// sleep — bound the whole call so a regression (giving up on 503)
	// surfaces as a fast failure rather than a 5-minute hang.
	done := make(chan struct{})
	go func() {
		h.postTofuArchiveWithRetry(client, "https://api.x.example/api/v1/handover/tofu-archive", []byte(`{"files":{"a":"YQ=="}}`), "d")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("postTofuArchiveWithRetry did not converge within 30s (3 hits: 503,503,200)")
	}
	if got := recv.hits.Load(); got < 3 {
		t.Fatalf("receiver hit count = %d, want >=3 (503,503,200 — proves 503 is retried)", got)
	}
}

func TestPostTofuArchive_GivesUpOn4xx(t *testing.T) {
	recv := &fakeArchiveReceiver{firstStatus: http.StatusBadRequest}
	srv := httptest.NewServer(recv.handler())
	defer srv.Close()

	h := NewWithPDM(silentLogger(), &fakePDM{})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	done := make(chan struct{})
	go func() {
		h.postTofuArchiveWithRetry(client, "https://api.x.example/api/v1/handover/tofu-archive", []byte(`{}`), "d")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("postTofuArchiveWithRetry kept retrying a 4xx — must give up immediately")
	}
	if got := recv.hits.Load(); got != 1 {
		t.Errorf("receiver hit count = %d, want 1 (no retry on 4xx)", got)
	}
}
