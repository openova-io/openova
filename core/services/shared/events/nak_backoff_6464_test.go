package events

import (
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// #6464 — a FIXED 5s nak, combined with MaxDeliver:-1, is an unbounded
// hot loop with a 5-second period.
//
// MEASURED on the mothership 2026-08-18: Stalwart logged
// smtp.rate-limit-exceeded (id="sender", limit=[25, 3600000ms]) for
// noreply@openova.io continuously, send attempts landing every 2-3s. A
// 25/hour budget dies in about a minute and stays dead, so PIN sign-in
// was gone FLEET-WIDE — no customer could get a code on any Sovereign.
//
// The retry rate must DECAY when a downstream keeps refusing. It must
// not decay so eagerly that a genuine transient blip is punished.

type fakeMsg struct {
	jetstream.Msg
	md  *jetstream.MsgMetadata
	err error
}

func (f fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.md, nil
}

func msgWithDelivered(n uint64) fakeMsg {
	return fakeMsg{md: &jetstream.MsgMetadata{NumDelivered: n}}
}

func TestNakBackoff_GrowsExponentially(t *testing.T) {
	want := map[uint64]time.Duration{
		1: 5 * time.Second,
		2: 10 * time.Second,
		3: 20 * time.Second,
		4: 40 * time.Second,
		5: 80 * time.Second,
	}
	for n, exp := range want {
		if got := nakBackoff(msgWithDelivered(n)); got != exp {
			t.Errorf("delivery %d: got %v want %v — a retry rate that does not decay is what pinned the noreply@ quota at zero (#6464)", n, got, exp)
		}
	}
}

// CONTROL 1 — the FIRST retry must still be 5s. If the fix made the very
// first retry slow, a genuine transient blip (a one-second downstream
// hiccup) would take minutes to recover, trading one outage shape for
// another.
func TestNakBackoff_FirstRetryUnchanged(t *testing.T) {
	if got := nakBackoff(msgWithDelivered(1)); got != 5*time.Second {
		t.Fatalf("first retry is %v, want 5s — transient blips must still recover fast", got)
	}
}

// CONTROL 2 — the delay must be CAPPED. Unbounded doubling would park a
// message for hours and look indistinguishable from a lost event.
func TestNakBackoff_Capped(t *testing.T) {
	for _, n := range []uint64{10, 50, 1000, 1 << 20} {
		got := nakBackoff(msgWithDelivered(n))
		if got != 5*time.Minute {
			t.Errorf("delivery %d: got %v want the 5m cap", n, got)
		}
	}
}

// CONTROL 3 — unreadable metadata must fall back to the BASE delay, never
// to zero. A zero delay would be a TIGHTER loop than the constant this fix
// removes, turning a safety fallback into a worse storm.
func TestNakBackoff_UnreadableMetadataFallsBackToBase(t *testing.T) {
	for _, m := range []fakeMsg{
		{err: errors.New("no metadata: not a jetstream message")},
		{md: nil},
		{md: &jetstream.MsgMetadata{NumDelivered: 0}},
	} {
		got := nakBackoff(m)
		if got != 5*time.Second {
			t.Errorf("unreadable metadata: got %v want the 5s base (never 0 — that is a tighter loop than the bug)", got)
		}
		if got <= 0 {
			t.Fatalf("fallback produced a non-positive delay %v — hot loop", got)
		}
	}
}

// #6464 RESIDUAL — decaying the nak RATE (nakBackoff, #6469) is only half
// the fix. With MaxDeliver:-1 (infinite) a message that can NEVER succeed
// still redelivers forever: an "app is ready" notification whose recipient
// quota is spent naks, backs off to the 5m cap, and retries every 5m for
// the life of the stream. Worse, that 5m spacing keeps the mailer's
// circuit breaker (3 refusals / 90s) from ever tripping, so every
// redelivery reaches the wire and the noreply@ 25/hour budget stays pinned
// at zero — PIN sign-in dead fleet-wide. The redelivery COUNT must be
// bounded, not just its rate.
func TestResolveMaxDeliver_DefaultIsBoundedNeverInfinite(t *testing.T) {
	t.Setenv(maxDeliverEnv, "") // force the default path
	got := resolveMaxDeliver()
	if got != defaultNATSMaxDeliver {
		t.Fatalf("default MaxDeliver is %d, want %d", got, defaultNATSMaxDeliver)
	}
	if got <= 0 {
		t.Fatalf("default MaxDeliver is %d — a non-positive value is JetStream 'infinite', the exact unbounded loop that killed PIN sign-in (#6464)", got)
	}
}

// CONTROL 1 — a valid positive override is honoured (Inviolable Principle
// #4: the knob is runtime-configurable at the Deployment level).
func TestResolveMaxDeliver_PositiveOverrideHonoured(t *testing.T) {
	t.Setenv(maxDeliverEnv, "25")
	if got := resolveMaxDeliver(); got != 25 {
		t.Fatalf("override 25 gave %d — a valid positive ceiling must win", got)
	}
}

// CONTROL 2 — the escape hatch must NOT let a stray value reinstate the
// unbounded loop. -1 (JetStream infinite), 0, and garbage all fall back to
// the safe bounded default; an explicit infinite retry is not supported.
func TestResolveMaxDeliver_NonPositiveAndGarbageFallBackToDefault(t *testing.T) {
	for _, raw := range []string{"-1", "0", "-1000", "abc", "  ", "1.5", "12x"} {
		t.Setenv(maxDeliverEnv, raw)
		got := resolveMaxDeliver()
		if got != defaultNATSMaxDeliver {
			t.Errorf("EVENTS_NATS_MAX_DELIVER=%q gave %d, want the bounded default %d — a stray value must never re-enable infinite redelivery (#6464)", raw, got, defaultNATSMaxDeliver)
		}
		if got <= 0 {
			t.Fatalf("EVENTS_NATS_MAX_DELIVER=%q produced non-positive %d — that IS the infinite loop", raw, got)
		}
	}
}

// The bounded ceiling paired with nakBackoff must give a real transient
// (e.g. a 1-2 minute Stalwart restart) room to recover before the message
// is abandoned. Sum the nakBackoff delays across the default attempt
// budget and assert the retry window comfortably exceeds a couple of
// minutes — otherwise the cap trades the storm for dropped notifications
// on every routine mail-server bounce.
func TestMaxDeliver_RetryWindowSurvivesRoutineTransient(t *testing.T) {
	var window time.Duration
	for n := uint64(1); n < uint64(defaultNATSMaxDeliver); n++ {
		window += nakBackoff(msgWithDelivered(n))
	}
	if window < 10*time.Minute {
		t.Fatalf("default retry window is %v — too short to ride out a routine Stalwart restart without dropping the notification", window)
	}
}
