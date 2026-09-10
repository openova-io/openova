package commercial

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Deliverer pushes queued documents to the operator's billing system.
//
// It is the second half of the outbox: issuing writes the row, this drains
// it. Delivery is at-least-once — a row is marked delivered only after the
// Exporter returns — and failures are retried with exponential backoff, so a
// billing system that is down for an afternoon costs nothing but a delay.
type Deliverer struct {
	Store    *store.Store
	Exporter Exporter
	// Interval is how often the loop looks for due rows (default 30s).
	Interval time.Duration
	// Batch is how many rows one pass takes (default 20).
	Batch int
	// Now is injectable for tests.
	Now func() time.Time
}

func (d *Deliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d *Deliverer) interval() time.Duration {
	if d.Interval > 0 {
		return d.Interval
	}
	return 30 * time.Second
}

func (d *Deliverer) batch() int {
	if d.Batch > 0 {
		return d.Batch
	}
	return 20
}

// Run drains the outbox on a ticker until ctx is done. It starts with one
// pass a few seconds in, so a Sovereign that restarts with a backlog does not
// sit on it for a whole interval.
func (d *Deliverer) Run(ctx context.Context) {
	if d == nil || d.Store == nil {
		return
	}
	if d.Exporter == nil {
		slog.Info("commercial outbox: no exporter configured; queued documents wait until one is")
		return
	}
	first := time.NewTimer(5 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	for {
		if n, err := d.DeliverDue(ctx); err != nil {
			slog.Warn("commercial outbox: delivery pass failed", "error", err)
		} else if n > 0 {
			slog.Info("commercial outbox: delivered", "documents", n)
		}
		t := time.NewTimer(d.interval())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// DeliverDue makes one pass and returns how many documents were delivered.
func (d *Deliverer) DeliverDue(ctx context.Context) (int, error) {
	due, err := d.Store.DueOutbox(ctx, d.now(), d.batch())
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, e := range due {
		if err := d.deliver(ctx, e); err != nil {
			continue
		}
		delivered++
	}
	return delivered, nil
}

// DeliverOne pushes a single entry now, which is what the operator's Retry
// does. It reports the entry as it stands afterwards.
func (d *Deliverer) DeliverOne(ctx context.Context, id int64) (store.OutboxEntry, error) {
	e, err := d.Store.GetOutboxEntry(ctx, id)
	if err != nil {
		return store.OutboxEntry{}, err
	}
	if e.DeliveredAt != nil {
		return e, nil
	}
	if d.Exporter == nil {
		return e, errors.New("no exporter is configured for this Sovereign")
	}
	if err := d.deliver(ctx, e); err != nil {
		// The failure is recorded on the row; the caller shows it rather
		// than treating a far-end outage as a request failure.
		return d.Store.GetOutboxEntry(ctx, id)
	}
	return d.Store.GetOutboxEntry(ctx, id)
}

// deliver pushes one entry and records the outcome on it. The TMF678 bill
// goes through Exporter.Deliver as lane 1 wired it; every other document
// type (DESIGN.md §9.1) goes through the SAME exporter's DeliverDocument —
// one transport, and an exporter that has none for a type fails the row
// with a message the operator can read rather than dropping it.
func (d *Deliverer) deliver(ctx context.Context, e store.OutboxEntry) error {
	var ref string
	var err error
	if e.DocType == "" || e.DocType == store.OutboxInvoice {
		var doc InvoiceDocument
		if uerr := json.Unmarshal(e.Document, &doc); uerr != nil {
			// A document that cannot be read will never deliver; record it
			// so an operator sees it rather than retrying forever in silence.
			_ = d.Store.MarkOutboxFailed(ctx, e.ID, uerr)
			return uerr
		}
		if doc.IdempotencyKey == "" {
			doc.IdempotencyKey = e.IdempotencyKey
		}
		ref, err = d.Exporter.Deliver(ctx, doc)
	} else {
		de, ok := d.Exporter.(external.Exporter)
		if !ok {
			err = errors.New("the configured exporter delivers invoices only; it has no transport for " + e.DocType + " documents")
		} else {
			ref, err = de.DeliverDocument(ctx, external.Envelope{Type: e.DocType, IdempotencyKey: e.IdempotencyKey, Document: e.Document})
		}
	}
	if err != nil {
		slog.Warn("commercial outbox: delivery failed; it will be retried", "entry", e.ID, "attempts", e.Attempts+1, "error", err)
		if merr := d.Store.MarkOutboxFailed(ctx, e.ID, err); merr != nil {
			slog.Error("commercial outbox: recording the failure failed", "entry", e.ID, "error", merr)
		}
		return err
	}
	if merr := d.Store.MarkOutboxDelivered(ctx, e.ID, ref); merr != nil {
		slog.Error("commercial outbox: recording the delivery failed", "entry", e.ID, "error", merr)
		return merr
	}
	return nil
}
