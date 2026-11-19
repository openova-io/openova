package modelcontract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Measure is one quantity of a usage event (section 4).
type Measure struct {
	Meter    string `json:"meter"`
	Quantity int64  `json:"quantity"`
}

// Measures maps a usage block to the measures of section 4, in the meter's order: input is the
// prompt less the cached and cache-write tokens, never below 0, then cache_read, cache_write and
// output; a token measure of 0 is left out; llm.requests is always 1.
func Measures(u Usage) []Measure {
	var cached int64
	if u.Details.Cached != nil {
		cached = *u.Details.Cached
	}
	out := []Measure{}
	for _, m := range []Measure{{"llm.tokens.input", u.Prompt - cached - u.Details.CacheWrite},
		{"llm.tokens.cache_read", cached}, {"llm.tokens.cache_write", u.Details.CacheWrite}, {"llm.tokens.output", u.Completion}} {
		if m.Quantity > 0 {
			out = append(out, m)
		}
	}
	return append(out, Measure{"llm.requests", 1})
}

// Entry is the meter's record of one request: GET /internal/events/{requestId} on the admin port
// of a meter pod. Event is the usage event as published, or empty.
type Entry struct {
	RequestID    string `json:"requestId"`
	Organization string `json:"organization"`
	Alias        string `json:"alias"`
	Stream       bool   `json:"stream"`
	Status       string `json:"status"`
	Published    bool   `json:"published"`
	Event        struct {
		Subject string `json:"subject"`
		Data    struct {
			Organization string `json:"organization"`
			Resource     struct {
				ID string `json:"id"`
			} `json:"resource"`
			Measures []Measure `json:"measures"`
		} `json:"data"`
	} `json:"event"`
}

// ReadEntry asks one meter pod, at its admin address base, for the entry of requestID. found
// is false when the pod answers 404.
func ReadEntry(ctx context.Context, base, requestID string) (e Entry, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/internal/events/"+url.PathEscape(requestID), nil)
	if err != nil {
		return e, false, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return e, false, fmt.Errorf("GET /internal/events/%s on %s got no answer", requestID, base)
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return e, false, nil
	case res.StatusCode != http.StatusOK:
		return e, false, fmt.Errorf("GET /internal/events/%s on %s answered %d", requestID, base, res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&e); err != nil {
		return e, false, fmt.Errorf("GET /internal/events/%s on %s answered no entry: %v", requestID, base, err)
	}
	return e, true, nil
}

// Metered judges the entry of a call to alias that was answered with usage u: status ok and
// published, streamed or not as the call was, one Organization in the entry, the event and its
// subject (section 3), the alias as the resource, and the measures of u (section 4).
func (e Entry) Metered(alias string, stream bool, u Usage) error {
	d := e.Event.Data
	switch {
	case e.Status != "ok" || !e.Published || e.Stream != stream:
		return fmt.Errorf("4: the entry has status %q, published %v, stream %v; want ok, true, %v", e.Status, e.Published, e.Stream, stream)
	case e.Organization != d.Organization || e.Event.Subject != d.Organization:
		return fmt.Errorf("3: the entry names Organization %q, the event %q, its subject %q", e.Organization, d.Organization, e.Event.Subject)
	case e.Alias != alias || d.Resource.ID != alias:
		return fmt.Errorf("4: the entry names alias %q, the event %q; want %s", e.Alias, d.Resource.ID, alias)
	case !slices.Equal(d.Measures, Measures(u)):
		return fmt.Errorf("4: the event measures %v, the usage of the answer gives %v", d.Measures, Measures(u))
	}
	return nil
}
