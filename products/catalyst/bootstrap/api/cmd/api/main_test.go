// main_test.go — coverage for the bake-time owner UserAccess seed
// goroutine wired into main() (TBD-A34 / issue #1891). The goroutine
// converges D21 (owner UserAccess CR) at catalyst-api boot WITHOUT
// requiring an operator PIN-login + /auth/handover.
//
// The runBakeTimeOwnerSeed function is exercised directly here — the
// goroutine wrapper in main() is just `go runBakeTimeOwnerSeed(...)`
// so a synchronous unit test against the function covers the same
// code path without the goroutine-scheduling surface.
package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// newBakeTimeSeedFakeClient — mirrors the fake-dynamic-client pattern
// in handler/user_access_owner_seed_test.go::newOwnerSeedFakeClient
// (UserAccess GVR + UserAccessList list-kind on a fresh Scheme). Lives
// here so this _test.go file is self-contained and doesn't depend on
// any unexported handler-package helpers.
func newBakeTimeSeedFakeClient(seed ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		handler.UserAccessGVR(): "UserAccessList",
	}, seed...)
}

// captureLogger returns a slog.Logger that writes JSON lines into the
// returned buffer. Tests assert on the buffer's content to confirm the
// goroutine logged the expected skip / converged / error message.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, &buf
}

// TestRunBakeTimeOwnerSeed_SeedsWhenChrootEnvsSet proves the canonical
// happy path: chroot mode (SOVEREIGN_FQDN set) + OPERATOR_EMAIL set +
// in-cluster dynamic client present → an owner UserAccess CR is
// created in catalyst-system within the first attempt.
func TestRunBakeTimeOwnerSeed_SeedsWhenChrootEnvsSet(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.omani.works")
	t.Setenv("OPERATOR_EMAIL", "emrah.baysal@openova.io")

	client := newBakeTimeSeedFakeClient()
	log, buf := captureLogger()

	runBakeTimeOwnerSeed(context.Background(), log, client)

	// Assert: a UserAccess CR exists in catalyst-system with the
	// canonical owner-seed name. We re-use the same lookup path the
	// handler tests do — the CR shape is covered exhaustively over
	// there; this test only proves the boot wiring fires.
	const wantName = "useraccess-owner-emrah-baysal-at-openova-io"
	got, err := client.Resource(handler.UserAccessGVR()).
		Namespace("catalyst-system").
		Get(context.Background(), wantName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected owner UserAccess %q after bake-time seed; got err %v", wantName, err)
	}
	if got.GetName() != wantName {
		t.Errorf("CR name: got %q want %q", got.GetName(), wantName)
	}

	// Log should carry the converged Info line so an operator can
	// confirm bake-time seeding fired without scraping the CR.
	if !strings.Contains(buf.String(), "owner auto-seeded at bake-time") {
		t.Errorf("expected converged Info log; got %s", buf.String())
	}
}

// TestRunBakeTimeOwnerSeed_SkipsOnNilDynamicClient proves the
// out-of-cluster path (CI / smoke / local dev) is a clean no-op.
func TestRunBakeTimeOwnerSeed_SkipsOnNilDynamicClient(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.omani.works")
	t.Setenv("OPERATOR_EMAIL", "emrah.baysal@openova.io")

	log, buf := captureLogger()
	runBakeTimeOwnerSeed(context.Background(), log, nil)

	if !strings.Contains(buf.String(), "out-of-cluster") {
		t.Errorf("expected out-of-cluster skip log; got %s", buf.String())
	}
}

// TestRunBakeTimeOwnerSeed_SkipsOnMotherMode proves the contabo
// (Catalyst-Zero) catalyst-api boot path — SOVEREIGN_FQDN unset —
// surfaces a structured skip and does NOT attempt the seed.
func TestRunBakeTimeOwnerSeed_SkipsOnMotherMode(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("OPERATOR_EMAIL", "emrah.baysal@openova.io")

	client := newBakeTimeSeedFakeClient()
	log, buf := captureLogger()
	runBakeTimeOwnerSeed(context.Background(), log, client)

	// No CR should have been created.
	list, err := client.Resource(handler.UserAccessGVR()).
		Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected 0 CRs in mother mode; got %d", len(list.Items))
	}

	if !strings.Contains(buf.String(), "SOVEREIGN_FQDN unset") {
		t.Errorf("expected mother-mode skip log; got %s", buf.String())
	}
}

// TestRunBakeTimeOwnerSeed_SkipsOnEmptyOperatorEmail proves the
// chroot-but-orgEmail-not-yet-stamped path is a clean no-op (the
// orchestrator overlay writer can stamp orgEmail later — the next
// Pod restart picks it up).
func TestRunBakeTimeOwnerSeed_SkipsOnEmptyOperatorEmail(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.omani.works")
	t.Setenv("OPERATOR_EMAIL", "")

	client := newBakeTimeSeedFakeClient()
	log, buf := captureLogger()
	runBakeTimeOwnerSeed(context.Background(), log, client)

	list, err := client.Resource(handler.UserAccessGVR()).
		Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected 0 CRs when OPERATOR_EMAIL empty; got %d", len(list.Items))
	}

	if !strings.Contains(buf.String(), "OPERATOR_EMAIL unset") {
		t.Errorf("expected OPERATOR_EMAIL-unset skip log; got %s", buf.String())
	}
}

// TestRunBakeTimeOwnerSeed_IdempotentReRun proves a second invocation
// (simulating Pod restart) returns cleanly without error or duplicate
// CR. Covers the production case where catalyst-api rolls and the
// goroutine fires a second time over an already-seeded chroot.
func TestRunBakeTimeOwnerSeed_IdempotentReRun(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.omani.works")
	t.Setenv("OPERATOR_EMAIL", "emrah.baysal@openova.io")

	client := newBakeTimeSeedFakeClient()
	logA, _ := captureLogger()
	runBakeTimeOwnerSeed(context.Background(), logA, client)

	// Second run — must be a no-op (AlreadyExists folded to nil).
	logB, bufB := captureLogger()
	runBakeTimeOwnerSeed(context.Background(), logB, client)

	list, err := client.Resource(handler.UserAccessGVR()).
		Namespace("").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected exactly 1 CR after idempotent re-run; got %d", len(list.Items))
	}
	// Second run still logs the Info converged line (because
	// EnsureOwnerUserAccess folds AlreadyExists to nil and returns
	// success — the second goroutine reports it auto-seeded too).
	if !strings.Contains(bufB.String(), "owner auto-seeded at bake-time") {
		t.Errorf("expected converged log on second run; got %s", bufB.String())
	}
}

// silentLogger is a convenience for tests that don't care about log
// output (e.g. the import-only ensure-the-helpers-compile sanity).
//
//lint:ignore U1000 helper kept for future tests that should suppress log noise
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
