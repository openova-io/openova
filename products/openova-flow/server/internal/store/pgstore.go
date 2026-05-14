// Package store — Postgres-backed FlowMessage store via CNPG.
//
// Replaces the in-memory map+RingBuffer that lost all state on pod
// restart. Schema in migrations/001_initial.sql:
//
//   - flow_instances     — one row per flow_id, current FlowInstance
//   - flow_nodes         — (flow_id, node_id) keyed FlowNode rows
//   - flow_relationships — (flow_id, from_id, to_id, rel_type) edges
//   - flow_events        — append-only event log used for SSE replay
//                           (bounded retention via trigger, last 4096)
//   - flow_log_lines     — per-execution log lines
//   - flow_executions    — per-Job-run rows (status, started_at, etc.)
//
// SSE fan-out uses Postgres LISTEN/NOTIFY. Each Append() issues
// `NOTIFY flow_<short_id>, '<seq>'` where short_id is the SHA-256
// suffix of flow_id (Postgres channel names are limited to 63 bytes
// of identifier).
//
// Concurrency: pgxpool handles connection sharing. All public methods
// are safe for concurrent callers — Postgres provides the serialisation.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// PGStore — CNPG-backed Store. Implements the same surface as the
// legacy in-memory Store (Append, Snapshot, Subscribe, Drop, etc.)
// so api/*.go consumers swap with a single field-type change.
type PGStore struct {
	pool *pgxpool.Pool

	// In-process subscriber multiplexer. Each call to Subscribe(flowID)
	// registers a Subscriber on the in-process map; a single goroutine
	// per process LISTENs on every channel and fans out to subscribers.
	subMu sync.Mutex
	subs  map[string]map[int64]*Subscriber
	next  int64

	// listener — single dedicated pgx conn used for LISTEN/NOTIFY
	// across all flows. Reconnects on error with exponential backoff.
	listenerCtx    context.Context
	listenerCancel context.CancelFunc
}

// NewPGStore opens the pgxpool against dsn, runs migrations, and
// starts the LISTEN dispatcher goroutine. Caller must Close() on
// shutdown.
func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pg DSN: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pgxpool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping pg: %w", err)
	}

	listenerCtx, listenerCancel := context.WithCancel(context.Background())
	s := &PGStore{
		pool:           pool,
		subs:           map[string]map[int64]*Subscriber{},
		listenerCtx:    listenerCtx,
		listenerCancel: listenerCancel,
	}
	if err := s.applyMigrations(ctx); err != nil {
		pool.Close()
		listenerCancel()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	go s.runListener()
	return s, nil
}

// Close drains the pool and stops the listener goroutine.
func (s *PGStore) Close() {
	s.listenerCancel()
	s.pool.Close()
}

// flowChannel returns a stable, 63-byte-safe Postgres channel name
// derived from flow_id. We use sha256[:8] + "flow_" prefix.
func flowChannel(flowID string) string {
	sum := sha256.Sum256([]byte(flowID))
	return "flow_" + hex.EncodeToString(sum[:8])
}

// Append ingests a FlowMessage, persisting its side-effects to the
// graph tables AND appending the raw envelope to flow_events for SSE
// replay. Returns the seq number from flow_events.seq (BIGSERIAL).
//
// Idempotency: upsert-* messages use INSERT ... ON CONFLICT DO UPDATE
// against the natural key; delete-* messages issue a DELETE. A
// retransmit therefore converges on the same final state.
//
// Concurrency: each Append() runs in its own pgx transaction.
// Subscribers receive events via LISTEN/NOTIFY (decoupled).
func (s *PGStore) Append(flowID string, m *types.FlowMessage) (uint64, error) {
	if flowID == "" {
		return 0, errors.New("pgstore: Append: flowID empty")
	}
	if m == nil {
		return 0, errors.New("pgstore: Append: nil message")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Ensure flow_instances row exists (FK target for nodes/rels/events).
	if _, err := tx.Exec(ctx, `
		INSERT INTO flow_instances (flow_id, status)
		VALUES ($1, 'open')
		ON CONFLICT (flow_id) DO NOTHING
	`, flowID); err != nil {
		return 0, fmt.Errorf("upsert flow_instance: %w", err)
	}

	switch m.Type {
	case types.TypeSnapshot:
		// Snapshot semantics: replace ALL nodes/rels for this flow.
		// Equivalent to delete-then-bulk-upsert, atomic in this tx.
		if _, err := tx.Exec(ctx, `DELETE FROM flow_nodes WHERE flow_id = $1`, flowID); err != nil {
			return 0, fmt.Errorf("snapshot delete nodes: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM flow_relationships WHERE flow_id = $1`, flowID); err != nil {
			return 0, fmt.Errorf("snapshot delete rels: %w", err)
		}
		if m.Flow != nil {
			if err := upsertFlowInstance(ctx, tx, m.Flow); err != nil {
				return 0, err
			}
		}
		for _, n := range m.Nodes {
			if err := upsertNode(ctx, tx, flowID, n); err != nil {
				return 0, err
			}
		}
		for _, r := range m.Relationships {
			if err := upsertRel(ctx, tx, flowID, r); err != nil {
				return 0, err
			}
		}
	case types.TypeUpsertFlow:
		if m.Flow != nil {
			if err := upsertFlowInstance(ctx, tx, m.Flow); err != nil {
				return 0, err
			}
		}
	case types.TypeUpsertNodes:
		for _, n := range m.Nodes {
			if err := upsertNode(ctx, tx, flowID, n); err != nil {
				return 0, err
			}
		}
	case types.TypeUpsertRels:
		for _, r := range m.Relationships {
			if err := upsertRel(ctx, tx, flowID, r); err != nil {
				return 0, err
			}
		}
	case types.TypeDeleteNodes:
		for _, id := range m.IDs {
			if _, err := tx.Exec(ctx, `DELETE FROM flow_nodes WHERE flow_id = $1 AND node_id = $2`, flowID, id); err != nil {
				return 0, fmt.Errorf("delete node: %w", err)
			}
		}
	case types.TypeDeleteRels:
		for _, p := range m.Pairs {
			if _, err := tx.Exec(ctx, `DELETE FROM flow_relationships WHERE flow_id = $1 AND from_id = $2 AND to_id = $3 AND rel_type = $4`, flowID, p.FromID, p.ToID, p.Type); err != nil {
				return 0, fmt.Errorf("delete rel: %w", err)
			}
		}
	default:
		return 0, fmt.Errorf("pgstore: Append: unknown message type %q", m.Type)
	}

	// Append the raw envelope to flow_events for SSE replay. The
	// trigger flow_events_retention_trg keeps the last 4096 events
	// per flow_id.
	payload, err := json.Marshal(m)
	if err != nil {
		return 0, fmt.Errorf("marshal envelope: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO flow_events (flow_id, event_type, payload)
		VALUES ($1, $2, $3)
		RETURNING seq
	`, flowID, string(m.Type), payload).Scan(&seq); err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}

	// pg_notify the per-flow channel with the new seq so subscribers
	// can react. Postgres NOTIFY runs at commit time so subscribers
	// only see committed events.
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, flowChannel(flowID), fmt.Sprintf("%d", seq)); err != nil {
		return 0, fmt.Errorf("pg_notify: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return uint64(seq), nil
}

// Snapshot returns the current FlowInstance + nodes + relationships
// from the graph tables. Replaces the buffer-folding approach with
// direct SELECTs — O(1) joins, no event replay.
func (s *PGStore) Snapshot(flowID string) (*types.FlowInstance, []types.FlowNode, []types.Relationship, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var flow *types.FlowInstance
	{
		row := s.pool.QueryRow(ctx, `
			SELECT flow_id, definition_id, parent_flow_id, triggered_by,
			       status, started_at, ended_at, meta
			FROM flow_instances WHERE flow_id = $1
		`, flowID)
		var (
			id, status                   string
			defID, parentID              *string
			startedAt, endedAt           *int64
			triggeredByRaw, metaRaw      []byte
		)
		if err := row.Scan(&id, &defID, &parentID, &triggeredByRaw, &status, &startedAt, &endedAt, &metaRaw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, nil, nil
			}
			return nil, nil, nil, fmt.Errorf("select flow_instance: %w", err)
		}
		fi := &types.FlowInstance{
			ID:           id,
			DefinitionID: defID,
			ParentFlowID: parentID,
			Status:       status,
		}
		if startedAt != nil {
			fi.StartedAt = *startedAt
		}
		if endedAt != nil {
			fi.EndedAt = endedAt
		}
		_ = json.Unmarshal(triggeredByRaw, &fi.TriggeredBy)
		_ = json.Unmarshal(metaRaw, &fi.Meta)
		flow = fi
	}

	// Nodes
	rows, err := s.pool.Query(ctx, `
		SELECT node_id, label, status, family, region, started_at, ended_at, meta
		FROM flow_nodes WHERE flow_id = $1
	`, flowID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("select flow_nodes: %w", err)
	}
	var nodes []types.FlowNode
	for rows.Next() {
		var (
			nid, label, status   string
			family, region       *string
			startedAt, endedAt   *int64
			metaRaw              []byte
		)
		if err := rows.Scan(&nid, &label, &status, &family, &region, &startedAt, &endedAt, &metaRaw); err != nil {
			rows.Close()
			return nil, nil, nil, fmt.Errorf("scan node: %w", err)
		}
		n := types.FlowNode{
			ID:        nid,
			FlowID:    flowID,
			Label:     label,
			Status:    status,
			Family:    family,
			Region:    region,
			StartedAt: startedAt,
			EndedAt:   endedAt,
		}
		_ = json.Unmarshal(metaRaw, &n.Meta)
		nodes = append(nodes, n)
	}
	rows.Close()

	// Relationships
	relRows, err := s.pool.Query(ctx, `
		SELECT from_id, to_id, rel_type, from_flow_id, to_flow_id, condition, lag_seconds
		FROM flow_relationships WHERE flow_id = $1
	`, flowID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("select flow_rels: %w", err)
	}
	var rels []types.Relationship
	for relRows.Next() {
		var (
			fromID, toID, relType, condition string
			fromFlow, toFlow                 *string
			lag                              int64
		)
		if err := relRows.Scan(&fromID, &toID, &relType, &fromFlow, &toFlow, &condition, &lag); err != nil {
			relRows.Close()
			return nil, nil, nil, fmt.Errorf("scan rel: %w", err)
		}
		rels = append(rels, types.Relationship{
			FromID:     fromID,
			ToID:       toID,
			Type:       relType,
			FromFlowID: fromFlow,
			ToFlowID:   toFlow,
			Condition:  condition,
			Lag:        lag,
		})
	}
	relRows.Close()
	return flow, nodes, rels, nil
}

// Drop removes a flow's state entirely. CASCADE FK takes nodes,
// relationships, events, log_lines, executions with it.
func (s *PGStore) Drop(flowID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM flow_instances WHERE flow_id = $1`, flowID); err != nil {
		return fmt.Errorf("drop flow: %w", err)
	}
	// Tear down every subscriber on the flow so SSE clients see EOF.
	s.subMu.Lock()
	if subs, ok := s.subs[flowID]; ok {
		for _, sub := range subs {
			close(sub.Ch)
		}
		delete(s.subs, flowID)
	}
	s.subMu.Unlock()
	return nil
}

// SeqForFlow returns the most-recently-assigned seq in flow_events
// for the given flowId, or 0 when the flow has never been ingested.
// Used by the SSE handler to stamp the initial snapshot's `id:` line.
func (s *PGStore) SeqForFlow(flowID string) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var seq int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(seq), 0) FROM flow_events WHERE flow_id = $1
	`, flowID).Scan(&seq); err != nil {
		return 0, err
	}
	return uint64(seq), nil
}

// EventsAfter returns events with seq > lastSeq in ascending order,
// up to `limit` rows. Used for SSE catch-up replay when a consumer
// resumes via Last-Event-ID.
func (s *PGStore) EventsAfter(flowID string, lastSeq uint64, limit int) ([]uint64, []*types.FlowMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if limit <= 0 || limit > 4096 {
		limit = 4096
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, payload FROM flow_events
		WHERE flow_id = $1 AND seq > $2
		ORDER BY seq ASC
		LIMIT $3
	`, flowID, int64(lastSeq), limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var seqs []uint64
	var msgs []*types.FlowMessage
	for rows.Next() {
		var seq int64
		var payload []byte
		if err := rows.Scan(&seq, &payload); err != nil {
			return nil, nil, err
		}
		m := &types.FlowMessage{}
		if err := json.Unmarshal(payload, m); err != nil {
			return nil, nil, err
		}
		seqs = append(seqs, uint64(seq))
		msgs = append(msgs, m)
	}
	return seqs, msgs, nil
}

// Subscribe registers an in-process subscriber that receives events
// emitted by the LISTEN goroutine via runListener(). Returns the
// subscriber + a cancel func.
func (s *PGStore) Subscribe(flowID string) (*Subscriber, func()) {
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

// FlowIDs — debug accessor; returns currently-tracked flow ids.
func (s *PGStore) FlowIDs() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT flow_id FROM flow_instances`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// AppendLogLines bulk-inserts execution log lines. Returns the
// number of rows written.
type LogLineInput struct {
	NodeID  string
	ExecID  string
	Level   string
	Message string
}

func (s *PGStore) AppendLogLines(flowID string, lines []LogLineInput) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Ensure flow_instances row exists so FK passes.
	if _, err := tx.Exec(ctx, `
		INSERT INTO flow_instances (flow_id, status)
		VALUES ($1, 'open') ON CONFLICT (flow_id) DO NOTHING
	`, flowID); err != nil {
		return 0, err
	}
	count := 0
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO flow_log_lines (flow_id, node_id, exec_id, level, message)
			VALUES ($1, $2, $3, $4, $5)
		`, flowID, l.NodeID, l.ExecID, l.Level, l.Message); err != nil {
			return count, err
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return count, err
	}
	return count, nil
}

// LogLines returns log lines for one execution, in seq order.
type LogLineRow struct {
	Seq        int64
	Level      string
	Message    string
	OccurredAt time.Time
}

func (s *PGStore) LogLines(flowID, execID string, limit int) ([]LogLineRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, level, message, occurred_at
		FROM flow_log_lines
		WHERE flow_id = $1 AND exec_id = $2
		ORDER BY seq ASC
		LIMIT $3
	`, flowID, execID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLineRow
	for rows.Next() {
		var r LogLineRow
		if err := rows.Scan(&r.Seq, &r.Level, &r.Message, &r.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// upsertFlowInstance: idempotent INSERT ... ON CONFLICT for the
// flow_instances row (covers TypeSnapshot + TypeUpsertFlow).
func upsertFlowInstance(ctx context.Context, tx pgx.Tx, fi *types.FlowInstance) error {
	triggeredBy, _ := json.Marshal(fi.TriggeredBy)
	meta, _ := json.Marshal(fi.Meta)
	_, err := tx.Exec(ctx, `
		INSERT INTO flow_instances (flow_id, definition_id, parent_flow_id, triggered_by, status, started_at, ended_at, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (flow_id) DO UPDATE SET
			definition_id  = EXCLUDED.definition_id,
			parent_flow_id = EXCLUDED.parent_flow_id,
			triggered_by   = EXCLUDED.triggered_by,
			status         = EXCLUDED.status,
			started_at     = COALESCE(EXCLUDED.started_at, flow_instances.started_at),
			ended_at       = COALESCE(EXCLUDED.ended_at, flow_instances.ended_at),
			meta           = EXCLUDED.meta
	`, fi.ID, fi.DefinitionID, fi.ParentFlowID, triggeredBy, fi.Status, nullableInt64Ptr(&fi.StartedAt), fi.EndedAt, meta)
	if err != nil {
		return fmt.Errorf("upsert flow_instance: %w", err)
	}
	return nil
}

func upsertNode(ctx context.Context, tx pgx.Tx, flowID string, n types.FlowNode) error {
	meta, _ := json.Marshal(n.Meta)
	_, err := tx.Exec(ctx, `
		INSERT INTO flow_nodes (flow_id, node_id, label, status, family, region, started_at, ended_at, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (flow_id, node_id) DO UPDATE SET
			label      = EXCLUDED.label,
			status     = EXCLUDED.status,
			family     = COALESCE(EXCLUDED.family, flow_nodes.family),
			region     = COALESCE(EXCLUDED.region, flow_nodes.region),
			started_at = COALESCE(EXCLUDED.started_at, flow_nodes.started_at),
			ended_at   = COALESCE(EXCLUDED.ended_at, flow_nodes.ended_at),
			meta       = EXCLUDED.meta
	`, flowID, n.ID, n.Label, n.Status, n.Family, n.Region, n.StartedAt, n.EndedAt, meta)
	if err != nil {
		return fmt.Errorf("upsert node %s/%s: %w", flowID, n.ID, err)
	}
	return nil
}

func upsertRel(ctx context.Context, tx pgx.Tx, flowID string, r types.Relationship) error {
	relType := r.Type
	cond := r.Condition
	if cond == "" {
		cond = "always"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO flow_relationships (flow_id, from_id, to_id, rel_type, from_flow_id, to_flow_id, condition, lag_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (flow_id, from_id, to_id, rel_type) DO UPDATE SET
			from_flow_id = EXCLUDED.from_flow_id,
			to_flow_id   = EXCLUDED.to_flow_id,
			condition    = EXCLUDED.condition,
			lag_seconds  = EXCLUDED.lag_seconds
	`, flowID, r.FromID, r.ToID, relType, r.FromFlowID, r.ToFlowID, cond, r.Lag)
	if err != nil {
		return fmt.Errorf("upsert rel %s/%s->%s/%s: %w", flowID, r.FromID, r.ToID, relType, err)
	}
	return nil
}

func nullableInt64Ptr(v *int64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}
