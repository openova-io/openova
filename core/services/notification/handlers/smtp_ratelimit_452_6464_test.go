package handlers

import (
	"errors"
	"net/textproto"
	"testing"
)

// #6464 — a 452 rate-limit refusal bypassed backoff AND the circuit
// breaker, producing a 2-3 second send storm that held the noreply@
// 25/hour quota at zero and killed PIN sign-in fleet-wide.
//
// MEASURED on the mothership 2026-08-18T09:42Z. Stalwart logged:
//
//	Rate limit exceeded (smtp.rate-limit-exceeded)
//	  id = "sender"  limit = [25, 3600000ms]
//	  accountName = "noreply@openova.io"
//
// and refused with 452. isRateLimit matched only "503 5.5.1", so every
// 452 took the "non-rate-limit errors are not retried" branch and
// returned immediately — the consumer re-queued and re-sent at once.
// Attempts landed at :04 :08 :10 :13 :15 :19 :21 :23.
//
// This suite pins 452 FIRST. A suite that only ever feeds 503 cannot
// fail on this defect — the same blind spot that let the storm run.

func TestIsRateLimit_452_QuotaRefusalIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"textproto 452 sender quota", &textproto.Error{Code: 452, Msg: "4.2.1 Rate limit exceeded"}},
		{"textproto 452 bare", &textproto.Error{Code: 452, Msg: "insufficient system storage"}},
		{"wrapped plain 452", errors.New("smtp: 452 4.2.1 Rate limit exceeded")},
		{"multiline 452 continuation", errors.New("452-4.2.1 too many messages")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isRateLimit(tc.err) {
				t.Fatalf("452 refusal NOT classified as rate-limit (#6464): %v\n"+
					"It therefore skips exponential backoff AND the circuit breaker, and the\n"+
					"caller re-sends immediately — that is the 2-3s storm that pins the\n"+
					"noreply@ 25/hour quota at zero and kills PIN sign-in fleet-wide.", tc.err)
			}
		})
	}
}

// CONTROL 1 — the pre-existing 503 5.5.1 path must keep working, or the
// fix traded one missed code for another.
func TestIsRateLimit_503_StillDetected(t *testing.T) {
	for _, err := range []error{
		&textproto.Error{Code: 503, Msg: "5.5.1 You must authenticate first"},
		errors.New("503 5.5.1 You must authenticate first"),
		errors.New("503-5.5.1 continuation form"),
	} {
		if !isRateLimit(err) {
			t.Errorf("503 5.5.1 regressed — no longer detected: %v", err)
		}
	}
}

// CONTROL 2 — the guard must still say NO to genuinely fatal errors.
// Without this the fix could pass by classifying EVERYTHING as
// retryable, which would silently convert hard failures (bad
// credentials, unknown recipient) into long backoff loops instead of
// surfacing them to the consumer for NACK / dead-letter.
func TestIsRateLimit_RejectsNonRateLimitErrors(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("dial tcp: connection refused"),
		&textproto.Error{Code: 535, Msg: "5.7.8 Authentication credentials invalid"},
		&textproto.Error{Code: 550, Msg: "5.1.1 No such user here"},
		&textproto.Error{Code: 421, Msg: "4.3.2 Service shutting down"},
	} {
		if isRateLimit(err) {
			t.Errorf("non-rate-limit error wrongly classified as retryable: %v", err)
		}
	}
}
