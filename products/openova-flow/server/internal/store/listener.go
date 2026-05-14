package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

// runListener — single goroutine that holds a dedicated pgx conn and
// LISTENs on per-flow Postgres channels. On NOTIFY, looks up the
// referenced flow_events row and fans out to in-process subscribers.
//
// The dispatcher reconnects on conn errors with exponential backoff
// (1s → 30s capped). LISTEN is re-issued for every active flow on
// each reconnect.
func (s *PGStore) runListener() {
	backoff := time.Second
	for {
		select {
		case <-s.listenerCtx.Done():
			return
		default:
		}
		err := s.listenerLoopOnce()
		if err != nil && !errors.Is(err, context.Canceled) {
			select {
			case <-s.listenerCtx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		backoff = time.Second
	}
}

func (s *PGStore) listenerLoopOnce() error {
	conn, err := s.pool.Acquire(s.listenerCtx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if err := s.relistenAll(conn.Conn()); err != nil {
		return err
	}

	relistenTicker := time.NewTicker(5 * time.Second)
	defer relistenTicker.Stop()

	for {
		select {
		case <-s.listenerCtx.Done():
			return nil
		case <-relistenTicker.C:
			if err := s.relistenAll(conn.Conn()); err != nil {
				return err
			}
		default:
		}
		// Block on next notification with a 5s window so the
		// relisten ticker gets cycles.
		ctx, cancel := context.WithTimeout(s.listenerCtx, 5*time.Second)
		notif, err := conn.Conn().WaitForNotification(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if errors.Is(err, context.Canceled) {
				return err
			}
			return err
		}
		if notif == nil {
			continue
		}
		seq, perr := strconv.ParseInt(notif.Payload, 10, 64)
		if perr != nil {
			continue
		}
		s.fanoutFromEvent(uint64(seq))
	}
}

// relistenAll issues `LISTEN flow_<channel>` for every flow_id known
// to flow_instances. Idempotent — LISTEN on an already-listened
// channel is a no-op.
func (s *PGStore) relistenAll(conn *pgx.Conn) error {
	ctx, cancel := context.WithTimeout(s.listenerCtx, 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, `LISTEN flow_global`); err != nil {
		return err
	}
	rows, err := conn.Query(ctx, `SELECT flow_id FROM flow_instances`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		ch := flowChannel(id)
		if _, err := conn.Exec(ctx, "LISTEN "+pgIdent(ch)); err != nil {
			return err
		}
	}
	return nil
}

// fanoutFromEvent reads the event row referenced by seq and dispatches
// it to in-process Subscribers for the matching flow_id.
func (s *PGStore) fanoutFromEvent(seq uint64) {
	ctx, cancel := context.WithTimeout(s.listenerCtx, 2*time.Second)
	defer cancel()
	var flowID string
	var payload []byte
	if err := s.pool.QueryRow(ctx, `SELECT flow_id, payload FROM flow_events WHERE seq = $1`, int64(seq)).Scan(&flowID, &payload); err != nil {
		return
	}
	m := &types.FlowMessage{}
	if err := json.Unmarshal(payload, m); err != nil {
		return
	}
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

// pgIdent quotes a Postgres identifier safely.
func pgIdent(name string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '"' {
			b.WriteString(`""`)
			continue
		}
		b.WriteByte(c)
	}
	b.WriteByte('"')
	return b.String()
}
