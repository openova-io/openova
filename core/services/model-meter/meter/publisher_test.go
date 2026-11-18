package meter

// Tests of the spool-fallback publisher. A fake bus stands in for JetStream. They cover the
// spool and drain: write, read, the atomic rename, the file-name guard, the drain that leaves
// files while the bus is down, and the drain that publishes a spooled event with the same
// identifier once the bus is back.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBus keeps what it acknowledged; while down is true every publish fails.
type fakeBus struct {
	mu   sync.Mutex
	down bool
	msgs []busMsg
}

type busMsg struct {
	subject, msgID string
	body           []byte
}

func (b *fakeBus) PublishRawSeq(_ context.Context, subject, msgID string, body []byte) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return 0, errors.New("bus down")
	}
	b.msgs = append(b.msgs, busMsg{subject, msgID, append([]byte(nil), body...)})
	return uint64(len(b.msgs)), nil
}

func testEvent(id string) UsageEvent {
	return UsageEvent{ID: id, Organization: "acme", Alias: "agent-default",
		OccurredAt: time.Date(2026, 12, 1, 9, 0, 6, 0, time.UTC), Measures: []Measure{{MeterOutput, 5}, {MeterRequests, 1}}}
}

func TestSafeFilename_StripsPathChars(t *testing.T) {
	cases := []struct{ in, want string }{
		{"01JCF0R1K2M3N4P5Q6R7S8T9V0", "01JCF0R1K2M3N4P5Q6R7S8T9V0"},
		{"../etc/passwd", "___etc_passwd"},
		{"req/with/slashes", "req_with_slashes"},
		{".hidden", "_hidden"},
		{"", ""}, // anonymous: "anon-<unix-ns>"
	}
	for _, tc := range cases {
		got := safeFilename(tc.in)
		if tc.in == "" {
			if !strings.HasPrefix(got, "anon-") {
				t.Errorf("safeFilename(\"\") = %q, want anon- prefix", got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("safeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPublish_AcknowledgedIsNotSpooled(t *testing.T) {
	dir := t.TempDir()
	bus := &fakeBus{}
	p := &SpoolPublisher{Bus: bus, Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	if seq, err := p.Publish(context.Background(), testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V1")); err != nil || seq != 1 {
		t.Fatalf("Publish = %d, %v, want the sequence 1", seq, err)
	}
	if len(bus.msgs) != 1 || bus.msgs[0].subject != Subject || bus.msgs[0].msgID != "01JCF0R1K2M3N4P5Q6R7S8T9V1" {
		t.Fatalf("bus messages = %+v, want one on %s with the event id", bus.msgs, Subject)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d spool files, want 0", len(entries))
	}
	if p.MetricsSnapshot()["published_ok"] != 1 {
		t.Fatal("published_ok is not 1")
	}
}

func TestPublish_NoBusWritesTheEnvelopeToDisk(t *testing.T) {
	dir := t.TempDir()
	p := &SpoolPublisher{Bus: nil, Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	ev := testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V2")
	if _, err := p.Publish(context.Background(), ev); !errors.Is(err, ErrSpooled) {
		t.Fatalf("Publish = %v, want ErrSpooled", err)
	}
	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "01JCF0R1K2M3N4P5Q6R7S8T9V2.json")))
	if err != nil {
		t.Fatalf("expected the spool file: %v", err)
	}
	want, _ := ev.CloudEvent(testSource)
	if string(body) != string(want) {
		t.Fatalf("spool file\n got %s\nwant %s", body, want)
	}
	if p.MetricsSnapshot()["spooled"] != 1 {
		t.Errorf("spooled = %d, want 1", p.MetricsSnapshot()["spooled"])
	}
}

func TestPublish_FailureSpoolsAndALaterDrainPublishesTheSameID(t *testing.T) {
	dir := t.TempDir()
	bus := &fakeBus{down: true}
	p := &SpoolPublisher{Bus: bus, Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	ev := testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V3")
	if _, err := p.Publish(context.Background(), ev); !errors.Is(err, ErrSpooled) {
		t.Fatalf("Publish = %v, want ErrSpooled", err)
	}
	// Still down: the drain leaves the file.
	p.DrainSpoolOnce(context.Background())
	if _, err := os.Stat(filepath.Join(dir, ev.ID+".json")); err != nil {
		t.Fatalf("the spool file is gone while the bus is down: %v", err)
	}
	bus.mu.Lock()
	bus.down = false
	bus.mu.Unlock()
	p.DrainSpoolOnce(context.Background())
	want, _ := ev.CloudEvent(testSource)
	if len(bus.msgs) != 1 || bus.msgs[0].msgID != ev.ID || bus.msgs[0].subject != Subject || string(bus.msgs[0].body) != string(want) {
		t.Fatalf("bus messages = %+v, want the envelope once with id %s", bus.msgs, ev.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, ev.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("the spool file is still there after the drain: %v", err)
	}
	snap := p.MetricsSnapshot()
	if snap["publish_failed"] != 1 || snap["spooled"] != 1 || snap["spool_drained"] != 1 {
		t.Fatalf("counters = %v", snap)
	}
}

func TestPublish_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	p := &SpoolPublisher{Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	for _, id := range []string{"01JCF0R1K2M3N4P5Q6R7S8T9A0", "01JCF0R1K2M3N4P5Q6R7S8T9A1", "01JCF0R1K2M3N4P5Q6R7S8T9A2"} {
		if _, err := p.Publish(context.Background(), testEvent(id)); !errors.Is(err, ErrSpooled) {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found leftover .tmp: %s", e.Name())
		}
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 spool files, got %d", len(entries))
	}
}

func TestPublish_SpoolWriteFailureIsCountedAsDropped(t *testing.T) {
	p := &SpoolPublisher{Source: testSource, PublishTimeout: time.Second, SpoolDir: filepath.Join(t.TempDir(), "missing")}
	_, err := p.Publish(context.Background(), testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V4"))
	if err == nil || errors.Is(err, ErrSpooled) {
		t.Fatalf("Publish = %v, want a spool error", err)
	}
	if p.MetricsSnapshot()["spool_drop_fatal"] != 1 {
		t.Fatal("spool_drop_fatal is not 1")
	}
}

func TestDrainSpoolOnce_NilBusIsNoop(t *testing.T) {
	dir := t.TempDir()
	p := &SpoolPublisher{Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	_, _ = p.Publish(context.Background(), testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V5"))
	p.DrainSpoolOnce(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "01JCF0R1K2M3N4P5Q6R7S8T9V5.json")); err != nil {
		t.Errorf("file disappeared: %v", err)
	}
}

func TestDrainSpoolOnce_RemovesAFileThatIsNotAnEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := &fakeBus{}
	p := &SpoolPublisher{Bus: bus, Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	p.DrainSpoolOnce(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "broken.json")); !os.IsNotExist(err) {
		t.Fatal("the broken file is still there")
	}
	if len(bus.msgs) != 0 || p.MetricsSnapshot()["spool_drop_fatal"] != 1 {
		t.Fatalf("bus messages = %d, counters = %v", len(bus.msgs), p.MetricsSnapshot())
	}
}

func TestSpooledEnvelopeCarriesNoPrice(t *testing.T) {
	dir := t.TempDir()
	p := &SpoolPublisher{Source: testSource, PublishTimeout: time.Second, SpoolDir: dir}
	_, _ = p.Publish(context.Background(), testEvent("01JCF0R1K2M3N4P5Q6R7S8T9V6"))
	body, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "01JCF0R1K2M3N4P5Q6R7S8T9V6.json")))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"amount", "price", "customer", "tenant", "principal"} {
		if strings.Contains(strings.ToLower(string(body)), word) {
			t.Errorf("the envelope holds %q: %s", word, body)
		}
	}
}
