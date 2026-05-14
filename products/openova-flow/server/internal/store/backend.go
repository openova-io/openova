package store

import "github.com/openova-io/openova/products/openova-flow/server/internal/types"

// Backend — abstraction over the persistence layer so api/ can swap
// between the in-memory Store (tests/dev) and PGStore (production).
// All implementations are concurrency-safe.
type Backend interface {
	Append(flowID string, m *types.FlowMessage) (uint64, error)
	Snapshot(flowID string) (*types.FlowInstance, []types.FlowNode, []types.Relationship, error)
	SeqForFlow(flowID string) (uint64, error)
	Subscribe(flowID string) (*Subscriber, func())
	Drop(flowID string) error
	FlowIDs() ([]string, error)
}

// MemBackend wraps the legacy in-memory Store so it satisfies the
// Backend interface. Used by tests and CI dev environments where
// spinning up Postgres would be overkill.
type MemBackend struct{ S *Store }

// NewMemBackend wraps an in-memory Store. bufCap is the ring size.
func NewMemBackend(bufCap int) *MemBackend {
	return &MemBackend{S: NewStore(bufCap)}
}

func (m *MemBackend) Append(flowID string, msg *types.FlowMessage) (uint64, error) {
	return m.S.Append(flowID, msg), nil
}

func (m *MemBackend) Snapshot(flowID string) (*types.FlowInstance, []types.FlowNode, []types.Relationship, error) {
	flow, nodes, rels := m.S.Snapshot(flowID)
	return flow, nodes, rels, nil
}

func (m *MemBackend) SeqForFlow(flowID string) (uint64, error) {
	return m.S.SeqForFlow(flowID), nil
}

func (m *MemBackend) Subscribe(flowID string) (*Subscriber, func()) {
	return m.S.Subscribe(flowID)
}

func (m *MemBackend) Drop(flowID string) error {
	m.S.Drop(flowID)
	return nil
}

func (m *MemBackend) FlowIDs() ([]string, error) {
	return m.S.FlowIDs(), nil
}

// Compile-time assertions.
var _ Backend = (*PGStore)(nil)
var _ Backend = (*MemBackend)(nil)
