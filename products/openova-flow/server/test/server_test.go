// HTTP-level tests — every endpoint with a happy path and a sad path.
package test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/openova-flow/server/internal/api"
	"github.com/openova-io/openova/products/openova-flow/server/internal/store"
	"github.com/openova-io/openova/products/openova-flow/server/internal/types"
)

func newServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	s := store.NewStore(64)
	ts := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(ts.Close)
	return ts, s
}

func post(t *testing.T, ts *httptest.Server, flowID string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		ts.URL+"/v1/flows/"+flowID+"/events",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestHealth(t *testing.T) {
	ts, _ := newServer(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatalf("get %s: %v", p, err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s status %d", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestIngestAndSnapshot(t *testing.T) {
	ts, _ := newServer(t)

	resp := post(t, ts, "f1", `{
		"type":"upsert-flow",
		"flow":{"id":"f1","status":"running","startedAt":1}
	}`)
	if resp.StatusCode != 200 {
		t.Fatalf("upsert-flow status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, ts, "f1", `{
		"type":"upsert-nodes",
		"nodes":[{"id":"n1","flowId":"f1","label":"hi","status":"running"}]
	}`)
	if resp.StatusCode != 200 {
		t.Fatalf("upsert-nodes status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = post(t, ts, "f1", `{
		"type":"upsert-rels",
		"relationships":[{"fromId":"n1","toId":"n2","type":"finish-to-start"}]
	}`)
	if resp.StatusCode != 200 {
		t.Fatalf("upsert-rels status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Snapshot must aggregate all three.
	resp, err := http.Get(ts.URL + "/v1/flows/f1/snapshot")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("snapshot status %d", resp.StatusCode)
	}
	var msg types.FlowMessage
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != types.TypeSnapshot {
		t.Fatalf("type %s", msg.Type)
	}
	if msg.Flow == nil || msg.Flow.ID != "f1" {
		t.Fatalf("flow missing: %+v", msg.Flow)
	}
	if len(msg.Nodes) != 1 || msg.Nodes[0].ID != "n1" {
		t.Fatalf("nodes wrong: %+v", msg.Nodes)
	}
	if len(msg.Relationships) != 1 || msg.Relationships[0].Type != "finish-to-start" {
		t.Fatalf("rels wrong: %+v", msg.Relationships)
	}
}

func TestSnapshot404(t *testing.T) {
	ts, _ := newServer(t)
	resp, err := http.Get(ts.URL + "/v1/flows/nope/snapshot")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestIngest400OnBadJSON(t *testing.T) {
	ts, _ := newServer(t)
	resp := post(t, ts, "f1", `{not json}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestIngest400OnUnknownType(t *testing.T) {
	ts, _ := newServer(t)
	resp := post(t, ts, "f1", `{"type":"unknown-thing"}`)
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestDelete(t *testing.T) {
	ts, _ := newServer(t)
	post(t, ts, "f1", `{"type":"upsert-flow","flow":{"id":"f1","status":"running","startedAt":1}}`).Body.Close()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/flows/f1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Subsequent snapshot must 404.
	resp, _ = http.Get(ts.URL + "/v1/flows/f1/snapshot")
	if resp.StatusCode != 404 {
		t.Fatalf("after delete status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestStreamReplayAndTail — connect to /stream, expect snapshot
// frame, then post a new event and expect it on the wire.
func TestStreamReplayAndTail(t *testing.T) {
	ts, _ := newServer(t)
	post(t, ts, "f2", `{"type":"upsert-flow","flow":{"id":"f2","status":"running","startedAt":1}}`).Body.Close()

	resp, err := http.Get(ts.URL + "/v1/flows/f2/stream")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream status %d", resp.StatusCode)
	}

	// Read the snapshot frame.
	frame := readSSEFrame(t, resp.Body, 2*time.Second)
	if frame.event != "snapshot" {
		t.Fatalf("first frame event=%s", frame.event)
	}
	if !strings.Contains(frame.data, `"f2"`) {
		t.Fatalf("snapshot missing flowId: %s", frame.data)
	}

	// Post a follow-up event in a goroutine.
	go func() {
		time.Sleep(100 * time.Millisecond)
		post(t, ts, "f2", `{"type":"upsert-nodes","nodes":[{"id":"n5","flowId":"f2","label":"x","status":"running"}]}`).Body.Close()
	}()

	frame = readSSEFrame(t, resp.Body, 2*time.Second)
	if frame.event != "upsert-nodes" {
		t.Fatalf("second frame event=%s data=%s", frame.event, frame.data)
	}
	if !strings.Contains(frame.data, `"n5"`) {
		t.Fatalf("upsert-nodes frame missing id: %s", frame.data)
	}
}

type sseFrame struct {
	event string
	id    string
	data  string
}

// readSSEFrame consumes lines until a blank line, returning the
// parsed event/id/data triple. Times out cleanly.
func readSSEFrame(t *testing.T, body io.Reader, timeout time.Duration) sseFrame {
	t.Helper()
	type result struct {
		f   sseFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var f sseFrame
		var dataBuf bytes.Buffer
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				f.data = strings.TrimRight(dataBuf.String(), "\n")
				// Skip heartbeats — caller usually wants real frames.
				if f.event == "heartbeat" {
					f = sseFrame{}
					dataBuf.Reset()
					continue
				}
				ch <- result{f: f}
				return
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				f.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "id:"):
				f.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "data:"):
				if dataBuf.Len() > 0 {
					dataBuf.WriteByte('\n')
				}
				dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		ch <- result{err: sc.Err()}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("sse scan: %v", r.err)
		}
		return r.f
	case <-time.After(timeout):
		t.Fatalf("sse read timeout")
	}
	return sseFrame{}
}
