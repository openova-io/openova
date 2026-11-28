// Package shipper talks to the access controller's internal port (ADR-0033):
// it posts ready and closed, and puts the chunks of the recording.
package shipper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Client calls the routes under the base address of the controller's
// internal port.
type Client struct {
	Base  string // the configuration's ingest, without a final slash
	UID   string
	HTTP  *http.Client
	Pause time.Duration // the pause after the first failed attempt; it doubles up to 5 seconds
}

// Refused is an answer in the 4xx range: another attempt cannot change it.
type Refused struct {
	Status int
	Body   string
}

func (e *Refused) Error() string {
	return fmt.Sprintf("the controller refused with %d: %s", e.Status, e.Body)
}

// Ready posts ready with the credential identifier until the controller takes
// it, refuses it, or ctx ends.
func (c *Client) Ready(ctx context.Context, credentialID string) error {
	return c.retry(ctx, func() error { return c.post(ctx, "ready", map[string]string{"credentialID": credentialID}) })
}

// Closed posts closed with an end reason until the controller takes it,
// refuses it, or ctx ends.
func (c *Client) Closed(ctx context.Context, reason string) error {
	return c.retry(ctx, func() error { return c.post(ctx, "closed", map[string]string{"reason": reason}) })
}

// Chunk puts chunk seq (from 1) of a stream with its SHA-256 until the
// controller takes it, refuses it, or giveUp has passed.
func (c *Client) Chunk(ctx context.Context, stream string, seq int, b []byte, giveUp time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, giveUp)
	defer cancel()
	sum := sha256.Sum256(b)
	path := "/internal/v1/sessions/" + c.UID + "/streams/" + stream + "/chunks/" + strconv.Itoa(seq)
	return c.retry(ctx, func() error {
		return c.send(ctx, http.MethodPut, path, b, map[string]string{"X-Chunk-SHA256": hex.EncodeToString(sum[:])})
	})
}

func (c *Client) post(ctx context.Context, route string, body map[string]string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.send(ctx, http.MethodPost, "/internal/v1/sessions/"+c.UID+"/"+route, b, map[string]string{"Content-Type": "application/json"})
}

// send makes one attempt.
func (c *Client) send(ctx context.Context, method, path string, body []byte, header map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return &Refused{Status: resp.StatusCode, Body: string(text)}
	}
	return fmt.Errorf("%s %s answered %d", method, path, resp.StatusCode)
}

// retry runs attempt until it succeeds, it is refused, or ctx ends.
func (c *Client) retry(ctx context.Context, attempt func() error) error {
	pause := c.Pause
	for {
		err := attempt()
		var refused *Refused
		if err == nil || errors.As(err, &refused) {
			return err
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w; the last attempt: %v", ctx.Err(), err)
		case <-t.C:
		}
		pause = min(2*pause, 5*time.Second)
	}
}
