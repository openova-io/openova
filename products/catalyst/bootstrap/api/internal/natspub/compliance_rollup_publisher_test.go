package natspub

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// fakeKV implements jsKV with controllable Put behavior + invocation
// recording so tests can verify Put-call shape + failure-path metric.
type fakeKV struct {
	puts      []fakeKVPut
	putErr    error
	putCalled int
}

type fakeKVPut struct {
	key   string
	value []byte
}

func (f *fakeKV) Put(key string, value []byte) (uint64, error) {
	f.putCalled++
	if f.putErr != nil {
		return 0, f.putErr
	}
	f.puts = append(f.puts, fakeKVPut{key: key, value: append([]byte(nil), value...)})
	return uint64(len(f.puts)), nil
}

func newTestPublisher(t *testing.T, kv jsKV, onFailure func()) *ComplianceRollupPublisher {
	t.Helper()
	return &ComplianceRollupPublisher{
		kv:        kv,
		log:       slog.Default(),
		bucket:    "policy-rollup",
		onFailure: onFailure,
		source:    "test",
	}
}

func TestPut_HappyPath_WritesToKV(t *testing.T) {
	kv := &fakeKV{}
	p := newTestPublisher(t, kv, nil)

	err := p.Put(context.Background(), "sovereign-hw01.omani.works",
		[]byte(`{"score":46,"numerator":274,"denominator":592}`))
	if err != nil {
		t.Fatalf("Put returned err: %v", err)
	}
	if kv.putCalled != 1 {
		t.Errorf("Put called %d times; want 1", kv.putCalled)
	}
	if got := kv.puts[0].key; got != "sovereign-hw01.omani.works" {
		t.Errorf("key=%q; want sovereign-hw01.omani.works", got)
	}
}

func TestPut_KVError_LogsAndBumpsFailureCounter(t *testing.T) {
	kv := &fakeKV{putErr: errors.New("nats: connection lost")}
	failures := 0
	p := newTestPublisher(t, kv, func() { failures++ })

	err := p.Put(context.Background(), "app:keycloak", []byte(`{"score":44}`))
	if err == nil {
		t.Fatal("Put returned nil err; want wrapped kv error")
	}
	if failures != 1 {
		t.Errorf("failure callback fired %d times; want 1", failures)
	}
}

func TestPut_NilCallback_SafeOnError(t *testing.T) {
	kv := &fakeKV{putErr: errors.New("broker stalled")}
	p := newTestPublisher(t, kv, nil) // no callback

	if err := p.Put(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("expected err propagation")
	}
	// no panic → pass
}

func TestPut_NilPublisher_ReturnsClosedError(t *testing.T) {
	var p *ComplianceRollupPublisher
	if err := p.Put(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("nil receiver Put should error")
	}
}

func TestNewComplianceRollupPublisher_EmptyURL_Errors(t *testing.T) {
	_, err := NewComplianceRollupPublisher("", "policy-rollup", "test",
		slog.Default(), nil)
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}
