package server

import "crypto/subtle"

// constantTimeEqual compares two secrets without leaking their contents
// through timing. The length difference is unavoidable and harmless: the
// token is a fixed-length value the operator mints.
func constantTimeEqual(got, want string) bool {
	if len(got) != len(want) {
		// Still burn the comparison so a length probe is not measurably
		// faster than a content probe on same-length input.
		subtle.ConstantTimeCompare([]byte(want), []byte(want))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
