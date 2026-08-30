package huawei

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Op is a lifecycle operation derived from a CTS trace name.
type Op string

// Lifecycle operations the collector corrects window boundaries for.
const (
	OpCreate Op = "create"
	OpDelete Op = "delete"
	OpResize Op = "resize"
	OpStop   Op = "stop"
	OpStart  Op = "start"
)

// Event is one lifecycle change taken from the CTS audit trail.
type Event struct {
	TraceID      string
	TraceName    string
	Op           Op
	ResourceID   string
	ResourceType string
	ResourceName string
	At           time.Time
}

// Trace is the subset of a CTS v3 trace this collector reads.
type Trace struct {
	TraceID      string `json:"trace_id"`
	TraceName    string `json:"trace_name"`
	ResourceID   string `json:"resource_id"`
	ResourceType string `json:"resource_type"`
	ResourceName string `json:"resource_name"`
	ServiceType  string `json:"service_type"`
	TraceStatus  string `json:"trace_status"`
	Code         string `json:"code"`
	Time         int64  `json:"time"` // epoch milliseconds
}

// ListTraces pages GET cts /v3/{pid}/traces?trace_type=system&from&to&limit=200
// and returns the raw traces in [from, to).
func (c *Client) ListTraces(ctx context.Context, creds Credentials, region string, from, to time.Time) ([]Trace, error) {
	var out []Trace
	next := ""
	for {
		q := url.Values{
			"trace_type": {"system"},
			"from":       {strconv.FormatInt(from.UnixMilli(), 10)},
			"to":         {strconv.FormatInt(to.UnixMilli(), 10)},
			"limit":      {strconv.Itoa(pageLimit)},
		}
		if next != "" {
			q.Set("next", next)
		}
		var resp struct {
			Traces   []Trace `json:"traces"`
			MetaData struct {
				Count  int    `json:"count"`
				Marker string `json:"marker"`
			} `json:"meta_data"`
		}
		if err := c.Get(ctx, creds, "cts", region, "/v3/"+creds.ProjectID+"/traces", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Traces...)
		if resp.MetaData.Marker == "" || len(resp.Traces) < pageLimit || resp.MetaData.Marker == next {
			break
		}
		next = resp.MetaData.Marker
	}
	return out, nil
}

// ClassifyTrace maps a trace name (createServer, deleteVolume, resizeServer,
// stopServer, startServer, ...) to a lifecycle operation. Traces that did not
// succeed (trace_status other than normal, non-2xx code) are ignored.
func ClassifyTrace(t Trace) (Event, bool) {
	if t.ResourceID == "" {
		return Event{}, false
	}
	if t.TraceStatus != "" && !strings.EqualFold(t.TraceStatus, "normal") {
		return Event{}, false
	}
	if t.Code != "" && !strings.HasPrefix(t.Code, "2") {
		return Event{}, false
	}
	name := strings.ToLower(t.TraceName)
	var op Op
	switch {
	case strings.HasPrefix(name, "create"):
		op = OpCreate
	case strings.HasPrefix(name, "delete"):
		op = OpDelete
	case strings.HasPrefix(name, "resize"):
		op = OpResize
	case strings.HasPrefix(name, "stop"), strings.HasPrefix(name, "shutdown"), strings.HasPrefix(name, "poweroff"):
		op = OpStop
	case strings.HasPrefix(name, "start"), strings.HasPrefix(name, "poweron"):
		op = OpStart
	default:
		return Event{}, false
	}
	// Batch actions on ECS carry the action in the name and the servers in
	// the request; a resource_id is still present on Kom4DC traces, so the
	// classification above applies unchanged.
	return Event{
		TraceID:      t.TraceID,
		TraceName:    t.TraceName,
		Op:           op,
		ResourceID:   t.ResourceID,
		ResourceType: strings.ToLower(t.ResourceType),
		ResourceName: t.ResourceName,
		At:           time.UnixMilli(t.Time).UTC(),
	}, true
}
