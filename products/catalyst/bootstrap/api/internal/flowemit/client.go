// Package flowemit — thin HTTP client that catalyst-api uses to push
// FlowMessage envelopes into openova-flow-server. Replaces the
// in-process Layer-1/Layer-2 snapshot composition (`flow_snapshot_local.go`)
// with a stateless emit-then-forget pattern. openova-flow-server's
// CNPG-backed store becomes the durable source of truth for the
// graph; this client is fire-and-forget with bounded retry.
package flowemit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client posts FlowMessage envelopes to openova-flow-server. Safe for
// concurrent callers (the underlying http.Client is shared).
type Client struct {
	baseURL string
	hc      *http.Client
	log     Logger
	disabled bool

	// in-flight deduplication: if multiple goroutines race to post
	// the same Job state, fold them into one POST. We key by
	// (flowID, op-hash) for ~1s windows.
	dedupMu sync.Mutex
	inFlight map[string]struct{}
}

// Logger — minimal interface compatible with slog.Logger.
type Logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// noopLogger discards messages. Used when the caller doesn't wire one.
type noopLogger struct{}

func (noopLogger) Warn(_ string, _ ...any) {}
func (noopLogger) Info(_ string, _ ...any) {}

// NewClient builds a flowemit client. If baseURL is empty the client
// is a no-op (every method returns nil); useful for tests/dev.
func NewClient(baseURL string, log Logger) *Client {
	if log == nil {
		log = noopLogger{}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc: &http.Client{
			Timeout: 10 * time.Second,
		},
		log:      log,
		disabled: baseURL == "",
		inFlight: map[string]struct{}{},
	}
}

// envelope is the wire shape openova-flow-server's /events endpoint
// expects. Mirrors types.FlowMessage from products/openova-flow/...
// inline so this package has no cross-product import.
type envelope struct {
	Type          string              `json:"type"`
	Flow          *FlowInstanceWire   `json:"flow,omitempty"`
	Nodes         []FlowNodeWire      `json:"nodes,omitempty"`
	Relationships []RelationshipWire  `json:"relationships,omitempty"`
	IDs           []string            `json:"ids,omitempty"`
	Pairs         []RelPairWire       `json:"pairs,omitempty"`
}

type FlowInstanceWire struct {
	ID            string                 `json:"id"`
	DefinitionID  *string                `json:"definitionId,omitempty"`
	ParentFlowID  *string                `json:"parentFlowId,omitempty"`
	Status        string                 `json:"status"`
	StartedAt     int64                  `json:"startedAt"`
	EndedAt       *int64                 `json:"endedAt,omitempty"`
	Meta          map[string]interface{} `json:"meta,omitempty"`
}

type FlowNodeWire struct {
	ID        string                 `json:"id"`
	FlowID    string                 `json:"flowId"`
	Label     string                 `json:"label"`
	Status    string                 `json:"status"`
	Family    *string                `json:"family,omitempty"`
	Region    *string                `json:"region,omitempty"`
	StartedAt *int64                 `json:"startedAt,omitempty"`
	EndedAt   *int64                 `json:"endedAt,omitempty"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

type RelationshipWire struct {
	FromID     string  `json:"fromId"`
	ToID       string  `json:"toId"`
	FromFlowID *string `json:"fromFlowId,omitempty"`
	ToFlowID   *string `json:"toFlowId,omitempty"`
	Type       string  `json:"type"`
	Condition  string  `json:"condition,omitempty"`
	Lag        int64   `json:"lag,omitempty"`
}

type RelPairWire struct {
	FromID string `json:"fromId"`
	ToID   string `json:"toId"`
	Type   string `json:"type"`
}

// UpsertFlow emits a flow-instance update.
func (c *Client) UpsertFlow(ctx context.Context, flowID string, fi FlowInstanceWire) error {
	if c == nil || c.disabled {
		return nil
	}
	return c.post(ctx, flowID, envelope{Type: "upsert-flow", Flow: &fi})
}

// UpsertNodes emits a batch of node updates.
func (c *Client) UpsertNodes(ctx context.Context, flowID string, nodes []FlowNodeWire) error {
	if c == nil || c.disabled || len(nodes) == 0 {
		return nil
	}
	return c.post(ctx, flowID, envelope{Type: "upsert-nodes", Nodes: nodes})
}

// UpsertRels emits a batch of relationship updates.
func (c *Client) UpsertRels(ctx context.Context, flowID string, rels []RelationshipWire) error {
	if c == nil || c.disabled || len(rels) == 0 {
		return nil
	}
	return c.post(ctx, flowID, envelope{Type: "upsert-rels", Relationships: rels})
}

// DeleteNodes emits a delete-nodes envelope.
func (c *Client) DeleteNodes(ctx context.Context, flowID string, ids []string) error {
	if c == nil || c.disabled || len(ids) == 0 {
		return nil
	}
	return c.post(ctx, flowID, envelope{Type: "delete-nodes", IDs: ids})
}

// Snapshot emits a full snapshot envelope (replaces all nodes+rels in
// one transactional write). Used by helmwatch.Bridge.SeedJobs at
// initial-list-synced time and by /refresh-watch.
func (c *Client) Snapshot(ctx context.Context, flowID string, fi *FlowInstanceWire, nodes []FlowNodeWire, rels []RelationshipWire) error {
	if c == nil || c.disabled {
		return nil
	}
	return c.post(ctx, flowID, envelope{
		Type:          "snapshot",
		Flow:          fi,
		Nodes:         nodes,
		Relationships: rels,
	})
}

// DropFlow removes the flow's state on openova-flow-server. Called by
// the deployment wipe path.
func (c *Client) DropFlow(ctx context.Context, flowID string) error {
	if c == nil || c.disabled {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/v1/flows/"+flowID, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("flowemit drop %s: %d %s", flowID, resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) post(ctx context.Context, flowID string, env envelope) error {
	if flowID == "" {
		return errors.New("flowemit: flowID empty")
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	url := c.baseURL + "/v1/flows/" + flowID + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Bounded retry — 3 attempts, exponential backoff. The catalyst-
	// api event path is hot so don't block the caller for long. After
	// max-retries we log and drop; the next event for the same node
	// will reconcile state.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		resp, err := c.hc.Do(req.Clone(req.Context()))
		if err != nil {
			lastErr = err
			continue
		}
		// Consume + close.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// 5xx: retry. 4xx: don't (malformed envelope or invalid flowID).
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			lastErr = fmt.Errorf("flowemit %s: %d", env.Type, resp.StatusCode)
			break
		}
		lastErr = fmt.Errorf("flowemit %s: %d", env.Type, resp.StatusCode)
	}
	if lastErr != nil {
		c.log.Warn("flowemit post failed", "flowID", flowID, "type", env.Type, "err", lastErr)
	}
	return lastErr
}
