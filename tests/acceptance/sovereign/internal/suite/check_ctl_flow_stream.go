package suite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
)

// Check CTL-G12, Flow stream answers from the API's own store. The check
// opens GET /api/v1/flows/{deploymentId}/stream of the deployment under test
// with the run's console session, the request the flow canvas of the console
// makes. It passes when the answer is 200 with Content-Type
// text/event-stream, the first event of the stream is named snapshot, and
// its data is a JSON object whose type is "snapshot", whose flow (when
// present) has the deployment's id, and whose every node has the
// deployment's id as its flowId. The API builds that frame from its own jobs
// store; it has no flow server to ask. A deployment the store holds no row
// for answers {"type":"snapshot"}, which passes. The stream stays open; the
// check closes it once it has read the first event. The check reads only.
const flowStreamID = "CTL-G12"

// flowStreamTimeout is the time the check waits for the first event.
const flowStreamTimeout = 30 * time.Second

// flowStreamMaxBytes is the most the check reads before it gives up.
const flowStreamMaxBytes = 64 << 20

type flowStreamCheck struct {
	timeout time.Duration // 0: flowStreamTimeout
}

func init() { RegisterCheck(flowStreamCheck{}) }

func (flowStreamCheck) ID() string { return flowStreamID }

func (c flowStreamCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if cc.API == nil {
		return errors.New("the run has no console session")
	}
	if cc.Deploy == nil || cc.Deploy.ID == "" {
		return errors.New("the run did not learn the deployment under test")
	}
	timeout := c.timeout
	if timeout == 0 {
		timeout = flowStreamTimeout
	}
	rec.Step(flowStreamID+".1", "the flow stream of the deployment answers with a snapshot frame", func(ev Evidence) error {
		return flowStreamJudge(ctx, cc.API, cc.Deploy.ID, timeout, ev)
	})
	return nil
}

// flowStreamEvent is one event of a text/event-stream.
type flowStreamEvent struct {
	name string // "message" when the event has no event line
	data string
}

// flowStreamJudge opens the stream, reads its first event and judges it.
func flowStreamJudge(parent context.Context, c *api.Client, dep string, timeout time.Duration, ev Evidence) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	path := "/api/v1/flows/" + url.PathEscape(dep) + "/stream"
	ev["path"] = path
	res, err := c.Stream(ctx, nil, path) // an answer HTTP 401 renews the session once
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	ev["status"], ev["content_type"] = res.StatusCode, res.Header.Get("Content-Type")
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("GET %s: HTTP %d %s; want 200", path, res.StatusCode, api.Snippet(b))
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("GET %s: Content-Type %q; want text/event-stream", path, res.Header.Get("Content-Type"))
	}
	first, err := flowStreamFirstEvent(bufio.NewReader(io.LimitReader(res.Body, flowStreamMaxBytes)))
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("GET %s: no event within %v", path, timeout)
		}
		return fmt.Errorf("GET %s: %w", path, err)
	}
	ev["event"] = first.name
	if first.name != "snapshot" {
		return fmt.Errorf("GET %s: the first event is %q; want snapshot", path, first.name)
	}
	var msg struct {
		Type string `json:"type"`
		Flow *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"flow"`
		Nodes []struct {
			ID     string `json:"id"`
			FlowID string `json:"flowId"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(first.data), &msg); err != nil {
		return fmt.Errorf("GET %s: the data of the snapshot event is not a JSON object: %s", path, api.Snippet([]byte(first.data)))
	}
	ev["type"], ev["nodes"], ev["has_flow"] = msg.Type, len(msg.Nodes), msg.Flow != nil
	if msg.Type != "snapshot" {
		return fmt.Errorf("GET %s: the snapshot event has type %q; want snapshot", path, msg.Type)
	}
	if msg.Flow != nil {
		ev["flow_status"] = msg.Flow.Status
		if msg.Flow.ID != dep {
			return fmt.Errorf("GET %s: the snapshot is of flow %q; want the deployment %q", path, msg.Flow.ID, dep)
		}
	}
	for _, n := range msg.Nodes {
		if n.FlowID != dep {
			return fmt.Errorf("GET %s: node %q has flowId %q; want the deployment %q", path, n.ID, n.FlowID, dep)
		}
	}
	return nil
}

// flowStreamFirstEvent returns the first event of the stream that carries
// data. Comment lines (": ...") and fields other than event and data are
// skipped; several data lines are joined with a newline.
func flowStreamFirstEvent(r *bufio.Reader) (flowStreamEvent, error) {
	name, data, hasData := "", []string{}, false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return flowStreamEvent{}, errors.New("the stream ended before its first event")
			}
			return flowStreamEvent{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if hasData {
				if name == "" {
					name = "message"
				}
				return flowStreamEvent{name: name, data: strings.Join(data, "\n")}, nil
			}
			name = ""
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data, hasData = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")), true
		}
	}
}
