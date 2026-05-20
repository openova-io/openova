// Package harness contains the testable pieces of the D31 acceptance
// run: the counter-gap detector, the RTO timer, and the kubectl/psql
// shell drivers. Split off `cmd/d31-acceptance/main.go` so the unit
// tests in counter_test.go can exercise the gap-detection logic
// without spinning up a real CNPG pair.
//
// Per CLAUDE.md §0 Pillar 3 the zero-tx-loss bar is "every COMMIT the
// writer goroutine observed as ACK'd must be present on the promoted
// replica, and the IDs must form a contiguous monotonic sequence from
// 1 to N (no gaps)." Two distinct signals get asserted post-failover:
//
//  1. Gap-free: the row IDs read from the new primary form 1..maxID
//     with no missing value. A missing value means a tx was committed
//     locally on the OLD primary and lost when the region was killed
//     — exactly the failure mode synchronous_commit=remote_apply is
//     designed to prevent.
//  2. Floor: the count read from the new primary is >= the count the
//     writer goroutine had ACK'd at region-kill time. The writer's
//     own running ACK counter is the contract — anything the writer
//     received OK on must survive.
//
// Both checks must pass for D31 to be GREEN.
package harness

import (
	"fmt"
	"sort"
)

// DetectGaps takes a list of row IDs read from the new primary
// post-promotion and returns the list of missing IDs (gaps) in the
// 1..max range. Zero-length return == zero-tx-loss.
//
// The contract is "IDs form a contiguous monotonic sequence" — the
// canonical D31 writer schema is `(id BIGSERIAL PRIMARY KEY, ...)` so
// the IDs are guaranteed to be allocated densely from 1; any gap is
// a tx that was committed (BIGSERIAL bumped) but not replicated
// before the primary died.
func DetectGaps(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	sorted := make([]int64, len(ids))
	copy(sorted, ids)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var gaps []int64
	prev := int64(0)
	for _, id := range sorted {
		// Each step from prev to id should be exactly +1. If it
		// jumps further, every id in (prev, id) is a gap.
		for missing := prev + 1; missing < id; missing++ {
			gaps = append(gaps, missing)
		}
		prev = id
	}
	return gaps
}

// AssertZeroTxLoss returns nil iff the post-failover state passes
// both the floor check (count >= writerAcked) and the gap check
// (no missing IDs in 1..max). Otherwise it returns a structured
// error naming the specific failure mode.
//
// `ids` is the slice of IDs SELECTed from the new primary after
// promotion completed. `writerAcked` is the writer goroutine's own
// running tally of rows it observed as COMMIT-OK against the OLD
// primary just before the region-kill (i.e. the floor we MUST meet).
func AssertZeroTxLoss(ids []int64, writerAcked int64) error {
	count := int64(len(ids))
	if count < writerAcked {
		return fmt.Errorf("FAIL floor check: writer ACK'd %d rows pre-kill but new primary has only %d (%d txs lost)", writerAcked, count, writerAcked-count)
	}
	gaps := DetectGaps(ids)
	if len(gaps) > 0 {
		// Truncate the gap list in the error message — for a 1M-row
		// run with even one lost tx, the operator wants to SEE that
		// it's one gap not a thousand. Show up to 10 IDs.
		preview := gaps
		if len(preview) > 10 {
			preview = preview[:10]
		}
		return fmt.Errorf("FAIL gap check: %d missing row IDs in 1..%d (first up to 10: %v)", len(gaps), ids[len(ids)-1], preview)
	}
	return nil
}

// MaxID returns the largest ID in `ids`, or 0 for an empty slice.
// Pure helper, exposed so cmd/d31-acceptance can include the max ID
// in the human-readable PASS report.
func MaxID(ids []int64) int64 {
	var m int64
	for _, id := range ids {
		if id > m {
			m = id
		}
	}
	return m
}
