package store

import (
	"sync"

	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// Store — Map<flowId, *flowState>. Each flowState owns:
//   - a RingBuffer of raw FlowMessages (for SSE replay),
//   - a folded current FlowInstance + nodes + relationships (built
//     lazily from the buffer when /snapshot is hit).
//
// Concurrency: per-flow lock; no global lock across flows so multiple
// flows are mutated in parallel. Map mutations (Add/Drop) guarded by
// the top-level RWMutex.
type Store struct {
	mu     sync.RWMutex
	flows  map[string]*flowState
	bufCap int

	// fanout — per-flow set of subscriber channels. Each channel
	// receives the assigned sequence + envelope after append. Backed
	// by a 16-slot buffer per subscriber per the SSE spec.
	subMu sync.Mutex
	subs  map[string]map[int64]*Subscriber
	next  int64
}

type flowState struct {
	mu  sync.RWMutex
	buf *RingBuffer
}

// Subscriber — one SSE client. The handler reads from Ch.
type Subscriber struct {
	ID     int64
	FlowID string
	Ch     chan SubEvent
	// LastSeq the subscriber has acked (its cursor through the
	// ring).
	LastSeq uint64
}

// SubEvent — what flows down a Subscriber's channel: the assigned
// sequence + the envelope. The SSE handler renders both as the
// `id: <seq>` line + the JSON data payload.
type SubEvent struct {
	Seq uint64
	Msg *types.FlowMessage
}

// NewStore — empty store with the per-flow ring capacity.
func NewStore(bufCap int) *Store {
	if bufCap <= 0 {
		bufCap = 4096
	}
	return &Store{
		flows:  map[string]*flowState{},
		bufCap: bufCap,
		subs:   map[string]map[int64]*Subscriber{},
	}
}

// Append ingests a FlowMessage for the named flow. Lazily creates the
// per-flow state on first ingest. Returns the assigned sequence number.
func (s *Store) Append(flowID string, m *types.FlowMessage) uint64 {
	if flowID == "" {
		return 0
	}
	s.mu.Lock()
	fs, ok := s.flows[flowID]
	if !ok {
		fs = &flowState{buf: NewRingBuffer(s.bufCap)}
		s.flows[flowID] = fs
	}
	s.mu.Unlock()

	seq := fs.buf.Append(m)
	s.fanout(flowID, seq, m)
	return seq
}

// BufferSlice — copy of the per-flow ring (oldest first). Empty when
// the flow id has never been ingested.
func (s *Store) BufferSlice(flowID string) []*types.FlowMessage {
	s.mu.RLock()
	fs, ok := s.flows[flowID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return fs.buf.Slice()
}

// Snapshot folds the per-flow ring into the current FlowInstance + the
// current set of FlowNodes (keyed by (flowId,id)) + Relationships
// (keyed by (fromId,toId,type)). Mirrors @openova/flow-core's
// reducer — the wire contract is the same.
//
// Returns nils when the flow id has never been ingested.
func (s *Store) Snapshot(flowID string) (*types.FlowInstance, []types.FlowNode, []types.Relationship) {
	msgs := s.BufferSlice(flowID)
	if len(msgs) == 0 {
		return nil, nil, nil
	}
	var flow *types.FlowInstance
	nodes := map[string]types.FlowNode{}
	rels := map[string]types.Relationship{}

	for _, m := range msgs {
		switch m.Type {
		case types.TypeSnapshot:
			// A snapshot resets state — drop any prior state, then
			// seed from this envelope.
			if m.Flow != nil {
				f := *m.Flow
				flow = &f
			}
			nodes = map[string]types.FlowNode{}
			for _, n := range m.Nodes {
				nodes[nodeKey(n)] = n
			}
			rels = map[string]types.Relationship{}
			for _, r := range m.Relationships {
				rels[relKey(r)] = r
			}
		case types.TypeUpsertFlow:
			if m.Flow != nil {
				f := *m.Flow
				flow = &f
			}
		case types.TypeUpsertNodes:
			for _, n := range m.Nodes {
				nodes[nodeKey(n)] = n
			}
		case types.TypeUpsertRels:
			for _, r := range m.Relationships {
				rels[relKey(r)] = r
			}
		case types.TypeDeleteNodes:
			for _, id := range m.IDs {
				// Delete by id-suffix match across flowIds; the
				// adapter typically scopes ids to a single flow but
				// we honour cross-flow deletes too.
				for k, n := range nodes {
					if n.ID == id {
						delete(nodes, k)
					}
				}
			}
		case types.TypeDeleteRels:
			for _, p := range m.Pairs {
				k := relKeyFromPair(p)
				delete(rels, k)
			}
		}
	}

	nodeSlice := make([]types.FlowNode, 0, len(nodes))
	for _, n := range nodes {
		nodeSlice = append(nodeSlice, n)
	}
	relSlice := make([]types.Relationship, 0, len(rels))
	for _, r := range rels {
		relSlice = append(relSlice, r)
	}
	return flow, nodeSlice, relSlice
}

// Drop removes a flow's state entirely. Called by DELETE /v1/flows/{id}.
func (s *Store) Drop(flowID string) {
	s.mu.Lock()
	delete(s.flows, flowID)
	s.mu.Unlock()
	// Tear down every subscriber on the flow as well so SSE clients
	// see EOF on their stream.
	s.subMu.Lock()
	subs, ok := s.subs[flowID]
	if ok {
		for _, sub := range subs {
			close(sub.Ch)
		}
		delete(s.subs, flowID)
	}
	s.subMu.Unlock()
}

// Subscribe registers a new SSE consumer for the flow. The returned
// channel emits SubEvent values whose Seq is monotonic. The cancel
// func tears down the registration. The fanout is non-blocking with
// drop-oldest semantics — slow consumers lose events but the ingest
// path never stalls.
func (s *Store) Subscribe(flowID string) (*Subscriber, func()) {
	s.subMu.Lock()
	s.next++
	sub := &Subscriber{
		ID:     s.next,
		FlowID: flowID,
		Ch:     make(chan SubEvent, 16),
	}
	if _, ok := s.subs[flowID]; !ok {
		s.subs[flowID] = map[int64]*Subscriber{}
	}
	s.subs[flowID][sub.ID] = sub
	s.subMu.Unlock()
	return sub, func() {
		s.subMu.Lock()
		if m, ok := s.subs[flowID]; ok {
			if _, ok2 := m[sub.ID]; ok2 {
				delete(m, sub.ID)
				close(sub.Ch)
			}
			if len(m) == 0 {
				delete(s.subs, flowID)
			}
		}
		s.subMu.Unlock()
	}
}

// fanout — non-blocking deliver to every subscriber on the flow.
// Slowest subscriber drops the oldest event to make room (16-slot
// buffer per client + drop-oldest). This mirrors the catalyst-api
// k8scache pattern.
func (s *Store) fanout(flowID string, seq uint64, m *types.FlowMessage) {
	s.subMu.Lock()
	subs := s.subs[flowID]
	out := make([]*Subscriber, 0, len(subs))
	for _, sub := range subs {
		out = append(out, sub)
	}
	s.subMu.Unlock()
	ev := SubEvent{Seq: seq, Msg: m}
	for _, sub := range out {
		select {
		case sub.Ch <- ev:
		default:
			// Drop oldest, push new — never block the writer.
			select {
			case <-sub.Ch:
			default:
			}
			select {
			case sub.Ch <- ev:
			default:
			}
		}
	}
}

// SeqForFlow returns the most-recently-assigned sequence number for
// the given flowId, or 0 when the flow has never been ingested. Used
// by the SSE handler to stamp the initial snapshot's `id:` line.
func (s *Store) SeqForFlow(flowID string) uint64 {
	s.mu.RLock()
	fs, ok := s.flows[flowID]
	s.mu.RUnlock()
	if !ok {
		return 0
	}
	return fs.buf.Seq()
}

// FlowIDs — debug accessor; returns the currently-known flow ids.
func (s *Store) FlowIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.flows))
	for id := range s.flows {
		out = append(out, id)
	}
	return out
}

func nodeKey(n types.FlowNode) string {
	return n.FlowID + "\x00" + n.ID
}

func relKey(r types.Relationship) string {
	return r.FromID + "\x00" + r.ToID + "\x00" + r.Type
}

func relKeyFromPair(p types.RelPair) string {
	return p.FromID + "\x00" + p.ToID + "\x00" + p.Type
}
