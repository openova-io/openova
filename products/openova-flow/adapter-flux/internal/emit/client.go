// Package emit — POST FlowMessage envelopes to the configured
// openova-flow-server with exponential backoff + jitter.
package emit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/openova-flow/adapter-flux/internal/types"
)

// Client posts FlowMessage envelopes. Construct one per process.
type Client struct {
	baseURL  string
	flowID   string
	http     *http.Client
	maxRetry int
	log      *slog.Logger
}

// NewClient — baseURL like "https://openova-flow.<sov-fqdn>". The
// client appends "/v1/flows/<flowID>/events" on every POST.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the baseURL is operator-driven
// (env-supplied at boot); the FlowID is similarly env-driven.
func NewClient(baseURL, flowID string, timeout time.Duration, log *slog.Logger) *Client {
	return &Client{
		baseURL:  baseURL,
		flowID:   flowID,
		http:     &http.Client{Timeout: timeout},
		maxRetry: 5,
		log:      log,
	}
}

// Emit POSTs one envelope, retrying with exponential backoff on
// 5xx / network error. Returns the final error (nil on success).
func (c *Client) Emit(ctx context.Context, m types.FlowMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("emit: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/v1/flows/%s/events", c.baseURL, c.flowID)
	var lastErr error
	for attempt := 0; attempt < c.maxRetry; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("emit: build req: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			c.log.Warn("emit: POST failed; will retry", "url", url, "attempt", attempt, "err", err)
		} else {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				// Schema error — retrying won't help.
				return fmt.Errorf("emit: %s -> %d (no retry)", url, resp.StatusCode)
			}
			lastErr = fmt.Errorf("emit: %s -> %d", url, resp.StatusCode)
			c.log.Warn("emit: POST 5xx; will retry", "url", url, "attempt", attempt, "status", resp.StatusCode)
		}
		// Backoff: 200ms, 400ms, 800ms, 1.6s, 3.2s with ±25% jitter.
		base := time.Duration(200*(1<<attempt)) * time.Millisecond
		jitter := time.Duration(rand.Int63n(int64(base / 4)))
		sleep := base + jitter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
	return fmt.Errorf("emit: gave up after %d attempts: %w", c.maxRetry, lastErr)
}
