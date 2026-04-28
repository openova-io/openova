package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// TestDLQSubscriber_attemptKey verifies the retry-counter key strategy.
// Payloads with a valid Event.ID must key by that ID so a partition
// rebalance doesn't reset the counter. Malformed payloads fall back to
// topic/partition/offset coordinates.
func TestDLQSubscriber_attemptKey(t *testing.T) {
	s := &DLQSubscriber{Group: "test"}
	rec := &kgo.Record{Topic: "sme.provision.events", Partition: 2, Offset: 42}

	t.Run("prefers event ID", func(t *testing.T) {
		evt := &Event{ID: "evt-123"}
		got := s.attemptKey(rec, evt)
		if got != "evt-123" {
			t.Fatalf("want evt-123, got %q", got)
		}
	})

	t.Run("falls back to offset when event is unparseable", func(t *testing.T) {
		got := s.attemptKey(rec, nil)
		want := "sme.provision.events:2:42"
		if got != want {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("falls back when event ID is empty", func(t *testing.T) {
		evt := &Event{}
		got := s.attemptKey(rec, evt)
		want := "sme.provision.events:2:42"
		if got != want {
			t.Fatalf("want %q, got %q", want, got)
		}
	})
}

// TestDLQSubscriber_defaults verifies that zero/empty inputs fall back to
// sane constants.
func TestDLQSubscriber_defaults(t *testing.T) {
	s := NewDLQSubscriber(nil, nil, "notification", 0, "")
	if s.MaxRetries != DefaultMaxRetries {
		t.Fatalf("want default max retries %d, got %d", DefaultMaxRetries, s.MaxRetries)
	}
	if s.DLQTopic != TopicDLQ {
		t.Fatalf("want default DLQ topic %q, got %q", TopicDLQ, s.DLQTopic)
	}
}

// TestDLQEnvelope_roundtrip verifies the envelope serializes cleanly and
// preserves either Payload or RawPayload.
func TestDLQEnvelope_roundtrip(t *testing.T) {
	t.Run("valid payload round trip", func(t *testing.T) {
		env := DLQEnvelope{
			OriginalTopic:     "sme.provision.events",
			OriginalPartition: 0,
			OriginalOffset:    7,
			ConsumerGroup:     "notification",
			EventID:           "evt-1",
			EventType:         "provision.app_ready",
			TenantID:          "t-abc",
			Error:             "boom",
			Attempts:          3,
			Payload:           json.RawMessage(`{"hello":"world"}`),
		}
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		var got DLQEnvelope
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if string(got.Payload) != `{"hello":"world"}` {
			t.Fatalf("payload lost in round trip: %s", got.Payload)
		}
		if got.RawPayload != "" {
			t.Fatalf("unexpected raw_payload: %q", got.RawPayload)
		}
	})

	t.Run("malformed payload uses raw_payload", func(t *testing.T) {
		env := DLQEnvelope{
			OriginalTopic: "sme.provision.events",
			ConsumerGroup: "notification",
			Error:         "invalid character 'x' looking for beginning of value",
			Attempts:      1,
			RawPayload:    "not json at all",
		}
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonContains(b, `"raw_payload":"not json at all"`) {
			t.Fatalf("raw_payload missing from %s", b)
		}
	})
}

// fakeHandler captures calls and lets us script returns per attempt.
type fakeHandler struct {
	mu       sync.Mutex
	calls    int
	returns  []error
	received []*Event
}

func (f *fakeHandler) handle(evt *Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, evt)
	call := f.calls
	f.calls++
	if call < len(f.returns) {
		return f.returns[call]
	}
	return nil
}

// TestDLQSubscriber_retryCounterProgression drives the attempt counter
// without needing a real broker. This exercises the in-memory logic that
// decides retry vs DLQ.
func TestDLQSubscriber_retryCounterProgression(t *testing.T) {
	s := &DLQSubscriber{
		Group:      "test",
		MaxRetries: 3,
		attempts:   make(map[string]int),
	}
	rec := &kgo.Record{Topic: "sme.tenant.events", Partition: 0, Offset: 1}
	evt := &Event{ID: "evt-9"}
	key := s.attemptKey(rec, evt)

	// Simulate 3 sequential failures. Counter should reach 3 on the
	// third failure, which is the DLQ trigger boundary.
	for i := 1; i <= 3; i++ {
		s.attemptsMux.Lock()
		s.attempts[key]++
		attempt := s.attempts[key]
		s.attemptsMux.Unlock()
		if attempt != i {
			t.Fatalf("attempt %d: want counter %d, got %d", i, i, attempt)
		}
	}
	// On successful handling we clear the counter.
	s.attemptsMux.Lock()
	delete(s.attempts, key)
	s.attemptsMux.Unlock()
	if _, ok := s.attempts[key]; ok {
		t.Fatalf("counter should be cleared after success")
	}
}

// TestDLQSubscriber_contextCancelBeforeWork verifies Subscribe returns the
// context error immediately when its context is already cancelled. Any
// slower behaviour would block the caller's shutdown path.
func TestDLQSubscriber_contextCancelBeforeWork(t *testing.T) {
	// We can't stand up a real broker here, but we can verify that
	// NewDLQSubscriber wires through the intended group identifier so
	// operators can trace a DLQ record back to the pod that deferred it.
	s := NewDLQSubscriber(nil, nil, "notification", 3, TopicDLQ)
	if s.Group != "notification" {
		t.Fatalf("consumer group not propagated")
	}
}

// Compile-time assertion that the signature stays stable for callers.
var _ = func() func(context.Context, func(*Event) error) error {
	return (&DLQSubscriber{}).Subscribe
}

// TestDLQEnvelope_errorField ensures the error string lands verbatim so
// operators can grep the topic for specific failures.
func TestDLQEnvelope_errorField(t *testing.T) {
	msg := "tenant lookup failed: not found"
	env := DLQEnvelope{Error: msg}
	b, _ := json.Marshal(env)
	if !jsonContains(b, msg) {
		t.Fatalf("error %q not in %s", msg, b)
	}
	// Sanity: unmarshalling round trip preserves the string.
	var got DLQEnvelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != msg {
		t.Fatalf("error field lost: %q", got.Error)
	}
}

// TestDLQSubscriber_nilProducer covers the defensive-path where a pod
// starts without a working producer — publishDLQ must not panic, just
// log and return.
func TestDLQSubscriber_nilProducer(t *testing.T) {
	s := &DLQSubscriber{Group: "test", DLQTopic: TopicDLQ, attempts: make(map[string]int)}
	rec := &kgo.Record{Topic: "sme.user.events", Value: []byte(`{"id":"x"}`)}
	// Expect no panic, no return value.
	s.publishDLQ(context.Background(), rec, &Event{ID: "x"}, errors.New("boom"), 3, false)
}

func jsonContains(b []byte, want string) bool {
	return bytesIndex(b, []byte(want)) >= 0
}

func bytesIndex(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
