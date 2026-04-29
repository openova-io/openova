package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// integrationDSN — set CI/local env to a writable Postgres for these tests.
// When unset, the integration tests skip; the rest of the package gets
// covered by allocator/handler tests with a thin in-memory shim. We
// deliberately don't pull testcontainers into the build path — Catalyst-
// Zero CI already runs Postgres as a service for other suites and the same
// container can host PDM's tests via PDM_TEST_DSN.
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PDM_TEST_DSN")
	if dsn == "" {
		t.Skip("PDM_TEST_DSN not set — skipping integration test")
	}
	return dsn
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := New(ctx, integrationDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		// Truncate after each test so subsequent runs start clean.
		_, _ = s.pool.Exec(context.Background(), `TRUNCATE pool_allocations`)
		s.Close()
	})
	return s
}

func TestReserveHappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if a.State != StateReserved {
		t.Errorf("state=%s want reserved", a.State)
	}
	if _, err := uuid.Parse(a.ReservationToken); err != nil {
		t.Errorf("reservation token not a UUID: %v", err)
	}
	if a.ExpiresAt == nil || a.ExpiresAt.Before(time.Now().UTC()) {
		t.Errorf("expiresAt must be in the future, got %v", a.ExpiresAt)
	}
}

func TestReserveConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test"); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	_, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second reserve: want ErrConflict, got %v", err)
	}
}

func TestExpiryFreesName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "omani.works", "tenant1", 1*time.Millisecond, "test"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Reserve again — should succeed because the previous reservation has
	// expired (the Reserve path prunes the expired row in the same tx).
	a, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test")
	if err != nil {
		t.Fatalf("re-reserve after expiry: %v", err)
	}
	if a.State != StateReserved {
		t.Errorf("state=%s want reserved", a.State)
	}
}

func TestCommitFlipsState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	committed, err := s.Commit(ctx, "omani.works", "tenant1", CommitInput{
		ReservationToken: r.ReservationToken,
		SovereignFQDN:    "tenant1.omani.works",
		LoadBalancerIP:   "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if committed.State != StateActive {
		t.Errorf("state=%s want active", committed.State)
	}
	if committed.LoadBalancerIP != "1.2.3.4" {
		t.Errorf("lbIP=%s", committed.LoadBalancerIP)
	}
}

func TestCommitTokenMismatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	_, err := s.Commit(ctx, "omani.works", "tenant1", CommitInput{
		ReservationToken: uuid.NewString(),
		SovereignFQDN:    "tenant1.omani.works",
		LoadBalancerIP:   "1.2.3.4",
	})
	if !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("commit: want ErrTokenMismatch, got %v", err)
	}
}

func TestReleaseRemovesRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r, err := s.Reserve(ctx, "omani.works", "tenant1", 10*time.Minute, "test")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := s.Commit(ctx, "omani.works", "tenant1", CommitInput{
		ReservationToken: r.ReservationToken,
		SovereignFQDN:    "tenant1.omani.works",
		LoadBalancerIP:   "1.2.3.4",
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	freed, err := s.Release(ctx, "omani.works", "tenant1")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if freed.State != StateActive {
		t.Errorf("freed.State=%s want active (the previous state)", freed.State)
	}
	// Now the row is gone — Get must return ErrNotFound.
	if _, err := s.Get(ctx, "omani.works", "tenant1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after release: want ErrNotFound, got %v", err)
	}
}

func TestExpireReservationsSweeper(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Reserve(ctx, "omani.works", "x", 1*time.Millisecond, "test"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	deleted, err := s.ExpireReservations(ctx)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted=%d want 1", deleted)
	}
}
