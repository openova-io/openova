package huawei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

const (
	maxBackoff      = 6 * time.Hour
	maxBackfill     = 31 * 24 * time.Hour
	usageBatchSize  = 500
	maxTransitions  = 200
	attrTransitions = "transitions"
	attrCreated     = "created"
)

// Repository is the persistence the collector needs; *store.Store implements
// it, and tests substitute an in-memory fake.
type Repository interface {
	ListVerifiedSources(ctx context.Context) ([]store.CostSource, error)
	GetCredentialSecret(ctx context.Context, id string) (accessKey string, secretEnc []byte, err error)
	SetSourceError(ctx context.Context, sourceID, lastError string) error
	SetSourceFailed(ctx context.Context, sourceID, lastError string) error
	SetSourceCollected(ctx context.Context, sourceID string, at time.Time) error
	CustomerStartDate(ctx context.Context, id string) (time.Time, error)
	UpsertInventory(ctx context.Context, sourceID string, items []store.InventoryUpsert) (map[string]json.RawMessage, error)
	SetInventoryAttrs(ctx context.Context, sourceID, resourceID string, attrs any) error
	MarkInventoryDeleted(ctx context.Context, sourceID string, kinds []string, seenIDs []string, at time.Time) (int64, error)
	ListInventory(ctx context.Context, sourceID string) ([]store.InventoryItem, error)
	GetInventoryItem(ctx context.Context, sourceID, resourceID string) (store.InventoryItem, error)
	SetInventoryBounds(ctx context.Context, sourceID, resourceID string, firstSeen, deletedAt *time.Time) error
	UpsertUsage(ctx context.Context, recs []store.UsageRecord) (int, error)
	DeleteUsageInRange(ctx context.Context, sourceID, resourceID string, from, to time.Time) (int64, error)
}

var _ Repository = (*store.Store)(nil)

// ErrCredentialUndecryptable means the stored secret cannot be opened with
// the current APP_ENCRYPTION_KEY. The source is flipped to failed and left
// alone until a new credential is entered (POST /sources/{id}/credential).
var ErrCredentialUndecryptable = errors.New("credential cannot be decrypted with the current APP_ENCRYPTION_KEY; re-enter it via POST /sources/{id}/credential")

// TickResult summarises one pass over all collectable sources.
type TickResult struct {
	Sources int              // sources considered this tick
	Skipped int              // sources skipped because they are in backoff
	Failed  int              // sources that errored (isolated; the tick continued)
	Errors  map[string]error // source id → error
	Result  string           // "ok" when nothing failed, "partial" otherwise
}

// Collector runs the per-source collection loops: inventory snapshot +
// usage emission every CollectInterval, the CTS change-log poll every
// CTSInterval, and the CES utilisation sample every CESInterval.
type Collector struct {
	Store           Repository
	Client          *Client
	Keys            *crypto.Keyring
	Metrics         *metrics.Registry
	CollectInterval time.Duration
	CTSInterval     time.Duration
	CESInterval     time.Duration
	Now             func() time.Time

	mu      sync.Mutex
	backoff map[string]backoffState
	lastCTS map[string]time.Time
}

type backoffState struct {
	failures int
	next     time.Time
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Collector) metricsReg() *metrics.Registry {
	if c.Metrics != nil {
		return c.Metrics
	}
	return metrics.Default
}

// Run blocks until ctx is done, driving the three loops.
func (c *Collector) Run(ctx context.Context) {
	collect := time.NewTicker(c.CollectInterval)
	cts := time.NewTicker(c.CTSInterval)
	ces := time.NewTicker(c.CESInterval)
	defer collect.Stop()
	defer cts.Stop()
	defer ces.Stop()
	// First pass shortly after start so a fresh deployment shows data within
	// one interval of activation.
	first := time.NewTimer(5 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			c.CollectAll(ctx)
			c.PollCTSAll(ctx)
		case <-collect.C:
			c.CollectAll(ctx)
		case <-cts.C:
			c.PollCTSAll(ctx)
		case <-ces.C:
			c.SampleCESAll(ctx)
		}
	}
}

// CollectAll runs one inventory+usage pass over every collectable source.
// Each source is isolated: one failure marks that source only and the tick
// continues; the result is "partial" when any source failed.
func (c *Collector) CollectAll(ctx context.Context) TickResult {
	return c.forEachSource(ctx, "collect", c.CollectSource)
}

func (c *Collector) inBackoff(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.backoff[id]
	return ok && c.now().Before(b.next)
}

func (c *Collector) fail(id string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.backoff == nil {
		c.backoff = map[string]backoffState{}
	}
	b := c.backoff[id]
	b.failures++
	delay := c.CollectInterval
	if delay <= 0 {
		delay = 15 * time.Minute
	}
	for i := 1; i < b.failures && delay < maxBackoff; i++ {
		delay *= 2
	}
	if delay > maxBackoff {
		delay = maxBackoff
	}
	b.next = now.Add(delay)
	c.backoff[id] = b
}

func (c *Collector) succeed(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.backoff, id)
}

// credentials decrypts the source's AK/SK. The secret lives only in the
// returned struct for the duration of one pass.
func (c *Collector) credentials(ctx context.Context, src store.CostSource) (Credentials, error) {
	if src.CredentialID == nil {
		return Credentials{}, errors.New("source has no credential")
	}
	ak, enc, err := c.Store.GetCredentialSecret(ctx, *src.CredentialID)
	if err != nil {
		return Credentials{}, fmt.Errorf("credential: %w", err)
	}
	sk, err := c.Keys.Open(enc)
	if err != nil {
		return Credentials{}, ErrCredentialUndecryptable
	}
	return Credentials{AccessKey: ak, SecretKey: string(sk), ProjectID: src.ProjectID}, nil
}

// guarded runs one per-source step, converting a panic into an error so a
// single source can never take the tick (or the process) down with it.
func guarded(step string, src store.CostSource, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("collector: panic isolated", "step", step, "source", src.ID, "project", src.ProjectID, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic in %s: %v", step, r)
		}
	}()
	return fn()
}

// handleSourceError records one source's failure without touching the
// others: an undecryptable credential flips the source to failed so it is
// not retried every tick; any other error keeps the source verified, stores
// last_error and applies per-source backoff.
func (c *Collector) handleSourceError(ctx context.Context, step string, src store.CostSource, err error, now time.Time) {
	c.metricsReg().Inc("chargeback_collect_sources_total", "Per-source collection steps by result", map[string]string{"step": step, "result": "error"}, 1)
	if errors.Is(err, ErrCredentialUndecryptable) {
		if serr := c.Store.SetSourceFailed(ctx, src.ID, err.Error()); serr != nil {
			slog.Error("collector: record credential failure", "source", src.ID, "error", serr)
		}
		c.succeed(src.ID) // nothing to back off: the source is no longer collectable
		slog.Warn("collector: source disabled until its credential is re-entered", "step", step, "source", src.ID, "project", src.ProjectID, "error", err)
		return
	}
	c.fail(src.ID, now)
	if serr := c.Store.SetSourceError(ctx, src.ID, err.Error()); serr != nil {
		slog.Error("collector: record source error", "source", src.ID, "error", serr)
	}
	slog.Warn("collector: source step failed; continuing with the next source", "step", step, "source", src.ID, "project", src.ProjectID, "error", err)
}

// forEachSource applies one guarded step to every collectable source and
// returns the tick summary. A failing source is isolated: it is marked, the
// loop moves on, and the tick result is "partial".
func (c *Collector) forEachSource(ctx context.Context, step string, fn func(context.Context, store.CostSource, time.Time) error) TickResult {
	res := TickResult{Errors: map[string]error{}, Result: "ok"}
	sources, err := c.Store.ListVerifiedSources(ctx)
	if err != nil {
		slog.Error("collector: list sources", "step", step, "error", err)
		res.Result = "partial"
		res.Errors[""] = err
		return res
	}
	for _, src := range sources {
		if ctx.Err() != nil {
			return res
		}
		res.Sources++
		if c.inBackoff(src.ID) {
			res.Skipped++
			c.metricsReg().Inc("chargeback_collect_sources_total", "Per-source collection steps by result", map[string]string{"step": step, "result": "skipped"}, 1)
			continue
		}
		now := c.now()
		if err := guarded(step, src, func() error { return fn(ctx, src, now) }); err != nil {
			res.Failed++
			res.Errors[src.ID] = err
			c.handleSourceError(ctx, step, src, err, now)
			continue
		}
		c.succeed(src.ID)
		c.metricsReg().Inc("chargeback_collect_sources_total", "Per-source collection steps by result", map[string]string{"step": step, "result": "ok"}, 1)
	}
	if res.Failed > 0 {
		res.Result = "partial"
	}
	c.metricsReg().Inc("chargeback_collect_ticks_total", "Collection ticks by result (ok = every source succeeded, partial = at least one source failed)", map[string]string{"step": step, "result": res.Result}, 1)
	slog.Info("collector: tick complete", "step", step, "sources", res.Sources, "skipped", res.Skipped, "failed", res.Failed, "result", res.Result)
	return res
}

// CollectSource snapshots the inventory of one source and emits usage for
// the elapsed window.
func (c *Collector) CollectSource(ctx context.Context, src store.CostSource, now time.Time) error {
	creds, err := c.credentials(ctx, src)
	if err != nil {
		return err
	}
	resources, failed := c.Client.ListAll(ctx, creds, src.Region)
	if len(failed) > 0 {
		for kind, ferr := range failed {
			slog.Warn("collector: list failed", "source", src.ID, "kind", kind, "error", ferr)
		}
		if len(failed) == 5 {
			// Nothing listed: surface the first error and back off.
			for _, ferr := range failed {
				return ferr
			}
		}
	}
	if err := c.reconcileInventory(ctx, src, resources, failed, now); err != nil {
		return err
	}
	from := c.windowStart(ctx, src, now)
	written, err := c.emitUsage(ctx, src, from, now, "", "")
	if err != nil {
		return err
	}
	c.metricsReg().Inc("chargeback_usage_records_written_total", "Usage records upserted", nil, float64(written))
	slog.Info("collector: pass complete", "source", src.ID, "project", src.ProjectID, "resources", len(resources), "usage_records", written, "window_from", from, "window_to", now)
	return c.Store.SetSourceCollected(ctx, src.ID, now)
}

// windowStart is the previous tick, or a bounded backfill from the
// customer's start date on the first pass.
func (c *Collector) windowStart(ctx context.Context, src store.CostSource, now time.Time) time.Time {
	if src.LastCollectedAt != nil && !src.LastCollectedAt.IsZero() {
		return src.LastCollectedAt.UTC()
	}
	from := now.Add(-maxBackfill)
	if start, err := c.Store.CustomerStartDate(ctx, src.CustomerID); err == nil && !start.IsZero() && start.After(from) {
		from = start
	}
	if src.VerifiedAt != nil && src.VerifiedAt.Before(from) {
		from = src.VerifiedAt.UTC()
	}
	return from
}

// reconcileInventory upserts observed resources (tracking status/flavor
// transitions) and marks unseen ones deleted for the kinds that listed.
func (c *Collector) reconcileInventory(ctx context.Context, src store.CostSource, resources []Resource, failed map[string]error, now time.Time) error {
	items := make([]store.InventoryUpsert, 0, len(resources))
	for _, r := range resources {
		items = append(items, store.InventoryUpsert{ResourceID: r.ID, Kind: r.Kind, Name: r.Name, Attrs: r.Attrs, Created: r.Created, SeenAt: now})
	}
	prev, err := c.Store.UpsertInventory(ctx, src.ID, items)
	if err != nil {
		return fmt.Errorf("inventory upsert: %w", err)
	}
	tolerance := 2 * c.CollectInterval
	if tolerance <= 0 {
		tolerance = 30 * time.Minute
	}
	kindCounts := map[string]int{}
	for _, r := range resources {
		kindCounts[r.Kind]++
		attrs := map[string]any{}
		for k, v := range r.Attrs {
			attrs[k] = v
		}
		if !r.Created.IsZero() {
			attrs[attrCreated] = r.Created.UTC().Format(time.RFC3339)
		}
		var trs []Transition
		if old, ok := prev[r.ID]; ok {
			oldAttrs := map[string]any{}
			_ = json.Unmarshal(old, &oldAttrs)
			trs = transitionsFrom(oldAttrs)
			if created, ok := oldAttrs[attrCreated].(string); ok && attrs[attrCreated] == nil {
				attrs[attrCreated] = created
			}
			if r.Kind == KindECS {
				// Compare against the state the transitions already put in
				// force (a CTS stop read earlier already explains a SHUTOFF
				// seen now), not against the raw previous snapshot.
				curStatus, curFlavor := stateAt(now, Lifecycle{Status: str(oldAttrs["status"]), Flavor: str(oldAttrs["flavor"])}, trs)
				if !strings.EqualFold(curStatus, r.Status) {
					trs = AdoptObserved(trs, Transition{At: now, Status: r.Status}, tolerance)
				}
				if newFlavor := str(r.Attrs["flavor"]); curFlavor != "" && newFlavor != "" && curFlavor != newFlavor {
					trs = AdoptObserved(trs, Transition{At: now, Flavor: newFlavor}, tolerance)
				}
			}
		}
		if len(trs) == 0 {
			at := now
			if !r.Created.IsZero() {
				at = r.Created.UTC()
			}
			trs = []Transition{{At: at, Status: r.Status, Flavor: str(r.Attrs["flavor"]), Source: "created"}}
		}
		if len(trs) > maxTransitions {
			trs = trs[len(trs)-maxTransitions:]
		}
		attrs[attrTransitions] = trs
		if err := c.Store.SetInventoryAttrs(ctx, src.ID, r.ID, attrs); err != nil {
			return err
		}
	}
	for kind, n := range kindCounts {
		c.metricsReg().Set("chargeback_inventory_resources", "Live resources by kind (last pass)", map[string]string{"kind": kind, "source": src.ID}, float64(n))
	}
	var okKinds, seen []string
	for _, kind := range []string{KindECS, KindEVS, KindEIP, KindELB, KindNAT} {
		if _, bad := failed[kind]; !bad {
			okKinds = append(okKinds, kind)
		}
	}
	for _, r := range resources {
		seen = append(seen, r.ID)
	}
	if n, err := c.Store.MarkInventoryDeleted(ctx, src.ID, okKinds, seen, now); err != nil {
		return fmt.Errorf("mark deleted: %w", err)
	} else if n > 0 {
		slog.Info("collector: resources deleted since last pass", "source", src.ID, "count", n)
	}
	return nil
}

func transitionsFrom(attrs map[string]any) []Transition {
	raw, ok := attrs[attrTransitions]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var trs []Transition
	if err := json.Unmarshal(b, &trs); err != nil {
		return nil
	}
	return trs
}

// lifecycleOf builds the window-math input for one inventory row.
func lifecycleOf(it store.InventoryItem) (Lifecycle, map[string]any) {
	attrs := map[string]any{}
	_ = json.Unmarshal(it.Attrs, &attrs)
	lc := Lifecycle{Created: it.FirstSeen, Status: str(attrs["status"]), Flavor: str(attrs["flavor"]), Transitions: transitionsFrom(attrs)}
	if s, ok := attrs[attrCreated].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.Before(lc.Created) {
			lc.Created = t.UTC()
		}
	}
	if it.DeletedAt != nil {
		lc.Deleted = it.DeletedAt.UTC()
	}
	return lc, attrs
}

// emitUsage recomputes usage for the source's resources (all of them, or the
// one named by onlyResource) over [from, to) and upserts it. rawRef tags the
// records (a CTS trace id when the recompute was triggered by the audit trail).
func (c *Collector) emitUsage(ctx context.Context, src store.CostSource, from, to time.Time, onlyResource, rawRef string) (int, error) {
	items, err := c.Store.ListInventory(ctx, src.ID)
	if err != nil {
		return 0, err
	}
	servers := map[string]Lifecycle{}
	for _, it := range items {
		if it.Kind == KindECS {
			lc, _ := lifecycleOf(it)
			servers[it.ResourceID] = lc
		}
	}
	var batch []store.UsageRecord
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := c.Store.UpsertUsage(ctx, batch)
		written += n
		batch = batch[:0]
		return err
	}
	for _, it := range items {
		if onlyResource != "" && it.ResourceID != onlyResource {
			continue
		}
		if it.DeletedAt != nil && it.DeletedAt.Before(from.Truncate(time.Hour)) {
			continue
		}
		lc, attrs := lifecycleOf(it)
		labelsBase := map[string]any{"name": it.Name}
		if it.Kind == KindEVS {
			if sid := str(attrs["attached_to"]); sid != "" {
				labelsBase["attached_to"] = sid
				if slc, ok := servers[sid]; ok {
					// A volume follows its server's power state for the
					// stopped-instance policy; its own life bounds stay.
					lc.Status = slc.Status
					lc.Transitions = nil
					for _, t := range slc.Transitions {
						if t.Status != "" {
							lc.Transitions = append(lc.Transitions, Transition{At: t.At, Status: t.Status, Source: t.Source})
						}
					}
				}
			}
		}
		for _, sl := range HourSlices(from, to, lc) {
			for _, sku := range SKUsFor(it.Kind, attrs, sl.Flavor) {
				labels := map[string]any{}
				for k, v := range labelsBase {
					labels[k] = v
				}
				switch it.Kind {
				case KindECS:
					labels["status"] = sl.Status
					labels["flavor"] = strings.TrimPrefix(sku.Name, "ecs.")
				case KindEVS:
					if _, attached := labelsBase["attached_to"]; attached {
						labels["server_status"] = sl.Status
					}
					labels["volume_type"] = str(attrs["volume_type"])
				default:
					labels["status"] = sl.Status
				}
				lb, _ := json.Marshal(labels)
				batch = append(batch, store.UsageRecord{
					CustomerID:   src.CustomerID,
					SourceID:     src.ID,
					ResourceID:   it.ResourceID,
					ResourceKind: it.Kind,
					SKU:          sku.Name,
					Quantity:     store.Decimal(strconv.FormatFloat(Quantity(sl.Hours(), sku.Multiplier), 'f', 6, 64)),
					Unit:         sku.Unit,
					WindowStart:  sl.Start,
					WindowEnd:    sl.End,
					Region:       src.Region,
					Labels:       lb,
					RawRef:       rawRef,
				})
				if len(batch) >= usageBatchSize {
					if err := flush(); err != nil {
						return written, err
					}
				}
			}
		}
	}
	return written, flush()
}

// PollCTSAll runs the change-log poll over every collectable source, each
// one isolated like CollectAll.
func (c *Collector) PollCTSAll(ctx context.Context) TickResult {
	return c.forEachSource(ctx, "cts", c.PollCTS)
}

// PollCTS fetches lifecycle traces since the last poll and corrects the
// affected resources' boundaries, then recomputes their usage from the hour
// of the event.
func (c *Collector) PollCTS(ctx context.Context, src store.CostSource, now time.Time) error {
	creds, err := c.credentials(ctx, src)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.lastCTS == nil {
		c.lastCTS = map[string]time.Time{}
	}
	from, ok := c.lastCTS[src.ID]
	c.mu.Unlock()
	if !ok {
		look := c.CollectInterval
		if look < time.Hour {
			look = time.Hour
		}
		from = now.Add(-look)
	}
	traces, err := c.Client.ListTraces(ctx, creds, src.Region, from, now)
	if err != nil {
		var ge *GatewayError
		if errors.As(err, &ge) && ge.NotPublished() {
			slog.Info("collector: CTS not published on this gateway; inventory snapshots remain the only lifecycle signal", "source", src.ID)
			c.mu.Lock()
			c.lastCTS[src.ID] = now
			c.mu.Unlock()
			return nil
		}
		return err
	}
	c.metricsReg().Inc("chargeback_cts_traces_total", "CTS traces fetched", nil, float64(len(traces)))
	tolerance := 2 * c.CollectInterval
	if tolerance <= 0 {
		tolerance = 30 * time.Minute
	}
	applied := 0
	for _, t := range traces {
		ev, ok := ClassifyTrace(t)
		if !ok {
			continue
		}
		changed, err := c.applyEvent(ctx, src, ev, tolerance)
		if err != nil {
			slog.Warn("collector: apply cts event", "source", src.ID, "trace", ev.TraceID, "error", err)
			continue
		}
		if !changed {
			continue
		}
		applied++
		start := ev.At.Truncate(time.Hour)
		if _, err := c.Store.DeleteUsageInRange(ctx, src.ID, ev.ResourceID, start, now); err != nil {
			return err
		}
		if _, err := c.emitUsage(ctx, src, start, now, ev.ResourceID, ev.TraceID); err != nil {
			return err
		}
	}
	if applied > 0 {
		slog.Info("collector: cts corrections applied", "source", src.ID, "events", applied)
	}
	c.mu.Lock()
	c.lastCTS[src.ID] = now
	c.mu.Unlock()
	return nil
}

// applyEvent updates one resource's bounds/transitions from an event.
func (c *Collector) applyEvent(ctx context.Context, src store.CostSource, ev Event, tolerance time.Duration) (bool, error) {
	it, err := c.Store.GetInventoryItem(ctx, src.ID, ev.ResourceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil // not (yet) in inventory; the next snapshot will add it
		}
		return false, err
	}
	attrs := map[string]any{}
	_ = json.Unmarshal(it.Attrs, &attrs)
	trs := transitionsFrom(attrs)
	switch ev.Op {
	case OpCreate:
		if !ev.At.Before(it.FirstSeen) {
			return false, nil
		}
		at := ev.At
		if err := c.Store.SetInventoryBounds(ctx, src.ID, ev.ResourceID, &at, nil); err != nil {
			return false, err
		}
		attrs[attrCreated] = ev.At.Format(time.RFC3339)
		if len(trs) > 0 && trs[0].Source == "created" {
			trs[0].At = ev.At
		}
	case OpDelete:
		if it.DeletedAt != nil && !ev.At.Before(*it.DeletedAt) {
			return false, nil
		}
		at := ev.At
		if err := c.Store.SetInventoryBounds(ctx, src.ID, ev.ResourceID, nil, &at); err != nil {
			return false, err
		}
	case OpStop:
		trs = MergeTransition(trs, Transition{At: ev.At, Status: "SHUTOFF"}, tolerance)
	case OpStart:
		trs = MergeTransition(trs, Transition{At: ev.At, Status: "ACTIVE"}, tolerance)
	case OpResize:
		trs = MergeTransition(trs, Transition{At: ev.At}, tolerance)
	default:
		return false, nil
	}
	attrs[attrTransitions] = trs
	if err := c.Store.SetInventoryAttrs(ctx, src.ID, ev.ResourceID, attrs); err != nil {
		return false, err
	}
	return true, nil
}

// SampleCESAll samples CPU utilisation for every live ECS of every source,
// each one isolated like CollectAll.
func (c *Collector) SampleCESAll(ctx context.Context) TickResult {
	return c.forEachSource(ctx, "ces", c.SampleCES)
}

// SampleCES writes informational ecs.cpu_util records for the last two hours
// (idempotent on the hour).
func (c *Collector) SampleCES(ctx context.Context, src store.CostSource, now time.Time) error {
	creds, err := c.credentials(ctx, src)
	if err != nil {
		return err
	}
	items, err := c.Store.ListInventory(ctx, src.ID)
	if err != nil {
		return err
	}
	from := now.Add(-2 * time.Hour).Truncate(time.Hour)
	var batch []store.UsageRecord
	for _, it := range items {
		if it.Kind != KindECS || it.DeletedAt != nil {
			continue
		}
		pts, err := c.Client.CPUUtilHourly(ctx, creds, src.Region, it.ResourceID, from, now)
		if err != nil {
			var ge *GatewayError
			if errors.As(err, &ge) && ge.NotPublished() {
				slog.Info("collector: CES not published on this gateway; utilisation sampling disabled", "source", src.ID)
				return nil
			}
			return err
		}
		for _, p := range pts {
			start := time.UnixMilli(p.Timestamp).UTC().Truncate(time.Hour)
			labels, _ := json.Marshal(map[string]any{"name": it.Name, "unit": p.Unit})
			batch = append(batch, store.UsageRecord{
				CustomerID: src.CustomerID, SourceID: src.ID, ResourceID: it.ResourceID, ResourceKind: KindECS,
				SKU: SKUCPUUtil, Unit: UnitCPUUtil, Quantity: store.Decimal(strconv.FormatFloat(p.Average, 'f', 6, 64)),
				WindowStart: start, WindowEnd: start.Add(time.Hour), Region: src.Region, Labels: labels,
			})
		}
	}
	n, err := c.Store.UpsertUsage(ctx, batch)
	if err != nil {
		return err
	}
	c.metricsReg().Inc("chargeback_ces_points_total", "CES datapoints stored", nil, float64(n))
	return nil
}

// VerifyProject implements api.Verifier: one signed ECS list call.
func (c *Collector) VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error {
	return c.Client.Verify(ctx, Credentials{AccessKey: accessKey, SecretKey: secretKey, ProjectID: projectID}, region)
}
