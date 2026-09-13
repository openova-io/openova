package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// A 409 already says "conflict" in its status line. Repeating the sentinel in
// front of the sentence makes a readable message read like a machine:
// "conflict: that cost-centre code is already used by this customer".
//
// Seen live on hw307 after the store stopped leaking the raw Postgres detail —
// the sentence was right, the prefix was still there.
func TestConflictMessageDropsTheSentinelPrefix(t *testing.T) {
	err := fmt.Errorf("%w: that cost-centre code is already used by this customer", store.ErrConflict)
	if got := conflictMessage(err); got != "that cost-centre code is already used by this customer" {
		t.Fatalf("conflictMessage = %q, want the sentence alone", got)
	}
	// It must still BE a conflict — stripping the prefix is a display change,
	// never a reclassification.
	if !errors.Is(err, store.ErrConflict) {
		t.Fatal("the error stopped satisfying ErrConflict")
	}
}

// A message that never carried the prefix is returned untouched, so a
// caller-authored sentence is not trimmed by accident.
func TestConflictMessageLeavesAnUnprefixedSentenceAlone(t *testing.T) {
	for _, s := range []string{
		`"NC 2026" is already the public price book; withdraw it first`,
		"that statement belongs to another customer",
		"",
	} {
		if got := conflictMessage(errors.New(s)); got != s {
			t.Errorf("conflictMessage(%q) = %q, want it unchanged", s, got)
		}
	}
}
