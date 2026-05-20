package harness

import (
	"strings"
	"testing"
)

func TestDetectGaps_ContiguousSequence_NoGaps(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5}
	gaps := DetectGaps(ids)
	if len(gaps) != 0 {
		t.Fatalf("expected zero gaps for contiguous sequence, got %v", gaps)
	}
}

func TestDetectGaps_SingleMissingID(t *testing.T) {
	// Simulates "tx 3 was committed on old primary but lost in failover."
	ids := []int64{1, 2, 4, 5}
	gaps := DetectGaps(ids)
	if len(gaps) != 1 || gaps[0] != 3 {
		t.Fatalf("expected [3], got %v", gaps)
	}
}

func TestDetectGaps_MultipleMissingIDs(t *testing.T) {
	ids := []int64{1, 5, 6, 10}
	gaps := DetectGaps(ids)
	want := []int64{2, 3, 4, 7, 8, 9}
	if len(gaps) != len(want) {
		t.Fatalf("expected %v, got %v", want, gaps)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, gaps)
		}
	}
}

func TestDetectGaps_UnsortedInput(t *testing.T) {
	// Postgres SELECT may not guarantee order without ORDER BY; the
	// detector must sort first.
	ids := []int64{5, 1, 3, 2, 4}
	gaps := DetectGaps(ids)
	if len(gaps) != 0 {
		t.Fatalf("expected zero gaps for unsorted contiguous input, got %v", gaps)
	}
}

func TestDetectGaps_EmptyInput(t *testing.T) {
	if gaps := DetectGaps(nil); gaps != nil {
		t.Fatalf("expected nil for empty input, got %v", gaps)
	}
	if gaps := DetectGaps([]int64{}); gaps != nil {
		t.Fatalf("expected nil for empty slice, got %v", gaps)
	}
}

func TestDetectGaps_StartsAboveOne(t *testing.T) {
	// If the DB started its BIGSERIAL above 1 for any reason, the
	// detector should flag IDs 1..first-1 as missing too. This is the
	// "first N rows lost" edge case.
	ids := []int64{3, 4, 5}
	gaps := DetectGaps(ids)
	want := []int64{1, 2}
	if len(gaps) != len(want) || gaps[0] != want[0] || gaps[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, gaps)
	}
}

func TestAssertZeroTxLoss_PassPath(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5}
	if err := AssertZeroTxLoss(ids, 5); err != nil {
		t.Fatalf("expected PASS, got %v", err)
	}
}

func TestAssertZeroTxLoss_FloorFailure(t *testing.T) {
	// Writer ACK'd 100 rows but new primary only has 95 → 5 txs lost.
	ids := make([]int64, 95)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	err := AssertZeroTxLoss(ids, 100)
	if err == nil {
		t.Fatal("expected FAIL when writerAcked > count")
	}
	if !strings.Contains(err.Error(), "floor check") {
		t.Fatalf("expected floor-check error, got %v", err)
	}
}

func TestAssertZeroTxLoss_GapFailure(t *testing.T) {
	// 5 rows visible, no floor mismatch, but ID 3 is missing → fail.
	ids := []int64{1, 2, 4, 5, 6}
	err := AssertZeroTxLoss(ids, 5)
	if err == nil {
		t.Fatal("expected FAIL when a gap exists")
	}
	if !strings.Contains(err.Error(), "gap check") {
		t.Fatalf("expected gap-check error, got %v", err)
	}
}

func TestAssertZeroTxLoss_GapErrorTruncates(t *testing.T) {
	// 20 missing IDs — the error should preview at most 10.
	var ids []int64
	for i := int64(21); i <= 30; i++ {
		ids = append(ids, i)
	}
	err := AssertZeroTxLoss(ids, 10)
	if err == nil {
		t.Fatal("expected FAIL")
	}
	// Two assertions: 20 missing, preview shows first 10.
	if !strings.Contains(err.Error(), "20 missing") {
		t.Fatalf("expected '20 missing' in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "first up to 10") {
		t.Fatalf("expected truncation hint, got %v", err)
	}
}

func TestMaxID(t *testing.T) {
	if MaxID(nil) != 0 {
		t.Fatal("expected 0 for nil")
	}
	if MaxID([]int64{5, 1, 3, 7, 2}) != 7 {
		t.Fatal("expected 7")
	}
}
