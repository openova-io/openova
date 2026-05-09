// Package valkey writes K8s resource snapshots into a Valkey KV under
// the canonical key shape:
//
//	cluster:{cluster-id}:kind:{kind}:{namespace}/{name}
//
// Cluster-scoped resources (Node, Namespace, ClusterRole, etc.) use
// the empty namespace token, producing keys like:
//
//	cluster:{cluster-id}:kind:node:/{node-name}
//
// The value is the redacted unstructured JSON the projector receives
// from the upstream NATS Stream (catalyst.events). Future SSE
// consumers in catalyst-api read the same key shape — DO NOT change
// the format without bumping the schema version + adding a migration.
//
// TTL: every PUT carries a per-key TTL (defaults to 24h, matching
// the Stream's retention). On DELETE events we explicitly DEL the
// key so the cache reflects the upstream state immediately.
//
// Idempotency: the projector may run with N replicas. Writes are
// last-write-wins on namespacedName key. JetStream's
// AckExplicitPolicy + DeliverAll consumer ensures every message is
// processed at least once across the consumer group.
package valkey

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// KV is the small surface the projector needs from a Valkey client.
// It exists so tests can drive the projector with an in-memory map
// instead of a live Valkey instance.
type KV interface {
	// Set writes value at key with the given TTL. TTL=0 means no expiry.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Del removes key. Idempotent — deleting an absent key is a no-op.
	Del(ctx context.Context, key string) error
	// Close releases the underlying connection.
	Close()
}

// EventType mirrors the K8s watch.EventType shape carried in the
// NATS event payload.
type EventType string

const (
	EventAdded    EventType = "ADDED"
	EventModified EventType = "MODIFIED"
	EventDeleted  EventType = "DELETED"
)

// Event is the payload shape consumed off the NATS catalyst.events
// JetStream. Mirrors `internal/k8scache/factory.go:Event` field-for-
// field so the projector's serialization is byte-stable across
// catalyst-api versions. Public fields ARE the wire contract.
type Event struct {
	Cluster string    `json:"cluster"`
	Kind    string    `json:"kind"`
	Type    EventType `json:"type"`
	// Object is the redacted unstructured.Unstructured serialized as
	// `{"object":{...}}` per k8scache's wire shape. The projector
	// does not unmarshal Object — it stores the bytes verbatim, both
	// for fidelity and because re-marshaling a CRD with unknown
	// custom fields can drop them silently.
	Object   ObjectMeta `json:"object"`
	RawBytes []byte     `json:"-"` // populated by the consumer for verbatim PUT
	At       time.Time  `json:"at"`
}

// ObjectMeta is the minimal subset of unstructured.Unstructured the
// projector needs to compute the namespaced key. The full object body
// stays in RawBytes so the value Valkey receives is byte-identical to
// the upstream message (no re-marshal drift).
type ObjectMeta struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

// Projector writes Events into a Valkey KV. Construct via NewProjector.
type Projector struct {
	kv  KV
	ttl time.Duration
}

// NewProjector wires a KV + a per-key TTL. ttl<=0 falls back to 24h.
func NewProjector(kv KV, ttl time.Duration) *Projector {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Projector{kv: kv, ttl: ttl}
}

// Apply projects one Event into the KV. Returns the resolved key for
// metric labels. Errors propagate up to the consumer so it can
// nack/retry.
func (p *Projector) Apply(ctx context.Context, ev Event) (string, error) {
	key := Key(ev.Cluster, ev.Kind, ev.Object.Metadata.Namespace, ev.Object.Metadata.Name)
	if ev.Object.Metadata.Name == "" {
		return key, errors.New("projector: event missing metadata.name")
	}
	if ev.Cluster == "" || ev.Kind == "" {
		return key, errors.New("projector: event missing cluster or kind")
	}
	switch ev.Type {
	case EventDeleted:
		if err := p.kv.Del(ctx, key); err != nil {
			return key, fmt.Errorf("kv del %s: %w", key, err)
		}
	case EventAdded, EventModified:
		body := ev.RawBytes
		if len(body) == 0 {
			return key, errors.New("projector: PUT requires RawBytes (verbatim message body)")
		}
		if err := p.kv.Set(ctx, key, body, p.ttl); err != nil {
			return key, fmt.Errorf("kv set %s: %w", key, err)
		}
	default:
		return key, fmt.Errorf("projector: unknown event type %q", ev.Type)
	}
	return key, nil
}

// Key returns the canonical Valkey key for (cluster, kind, namespace,
// name). Cluster-scoped resources use the empty namespace token,
// producing `cluster:{c}:kind:{k}:/{name}`.
func Key(cluster, kind, namespace, name string) string {
	return fmt.Sprintf("cluster:%s:kind:%s:%s/%s", cluster, kind, namespace, name)
}

// MemKV is an in-memory KV implementation used by tests + a
// development mode where the projector runs without a real Valkey.
// Safe for concurrent use.
type MemKV struct {
	store map[string][]byte
}

// NewMemKV constructs an empty MemKV. Test-only.
func NewMemKV() *MemKV {
	return &MemKV{store: map[string][]byte{}}
}

// Set implements KV. TTL is ignored in MemKV.
func (m *MemKV) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.store[key] = append([]byte(nil), value...)
	return nil
}

// Del implements KV.
func (m *MemKV) Del(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

// Close implements KV. No-op for MemKV.
func (m *MemKV) Close() {}

// Get is a test-only accessor that returns the bytes stored at key.
// Returns nil + ok=false when the key is absent.
func (m *MemKV) Get(key string) ([]byte, bool) {
	v, ok := m.store[key]
	return v, ok
}

// Len returns the number of keys currently stored. Test-only.
func (m *MemKV) Len() int { return len(m.store) }
