package meter

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// The usage event (docs/contracts/model-contract.md, section 4). Its subject is registered in
// docs/contracts/C3-event-subjects.yaml. It carries quantities only: no price, no amount, no
// user and no agent name.
const (
	Subject         = "catalyst.usage.recorded.v1"
	EventType       = "io.openova.catalyst.usage.recorded.v1"
	ResourceKind    = "llm-model"
	MeterInput      = "llm.tokens.input"
	MeterCacheRead  = "llm.tokens.cache_read"
	MeterCacheWrite = "llm.tokens.cache_write"
	MeterOutput     = "llm.tokens.output"
	MeterRequests   = "llm.requests"
)

// UsageEvent is the data block of one usage event.
type UsageEvent struct {
	ID           string // ULID, also the envelope id and the JetStream message identifier
	Organization string
	Alias        string // resource.id
	OccurredAt   time.Time
	Measures     []Measure // built by MeasuresFrom
}

// Measure is one quantity of the event.
type Measure struct {
	Meter    string `json:"meter"`
	Quantity int64  `json:"quantity"`
}

// MeasuresFrom maps the usage block of the back end to the measures of the event:
// input is prompt tokens less cached and cache-write tokens, never below 0; cache_read is the
// cached tokens; cache_write is the cache-write tokens; output is the completion tokens, which
// include the reasoning tokens. A token measure of 0 is left out; llm.requests is always 1.
func MeasuresFrom(u Usage) []Measure {
	var out []Measure
	add := func(meter string, q int64) {
		if q > 0 {
			out = append(out, Measure{Meter: meter, Quantity: q})
		}
	}
	add(MeterInput, u.PromptTokens-u.CachedTokens-u.CacheWriteTokens)
	add(MeterCacheRead, u.CachedTokens)
	add(MeterCacheWrite, u.CacheWriteTokens)
	add(MeterOutput, u.CompletionTokens)
	return append(out, Measure{Meter: MeterRequests, Quantity: 1})
}

type eventResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type eventData struct {
	Organization string        `json:"organization"`
	Resource     eventResource `json:"resource"`
	OccurredAt   string        `json:"occurredAt"`
	Measures     []Measure     `json:"measures"`
}

// envelope is a CloudEvents 1.0 message in structured JSON mode, with exactly eight attributes.
type envelope struct {
	SpecVersion     string    `json:"specversion"`
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	Source          string    `json:"source"`
	Subject         string    `json:"subject"`
	Time            string    `json:"time"`
	DataContentType string    `json:"datacontenttype"`
	Data            eventData `json:"data"`
}

// CloudEvent returns the message body of the event for the given source
// (model-gateway/<sovereign-fqdn>/<region-id>). The envelope subject is the Organization.
func (e UsageEvent) CloudEvent(source string) ([]byte, error) {
	switch {
	case e.ID == "":
		return nil, errors.New("meter: the event has no id")
	case e.Organization == "":
		return nil, errors.New("meter: the event has no organization")
	case e.Alias == "":
		return nil, errors.New("meter: the event has no alias")
	case source == "":
		return nil, errors.New("meter: the event has no source")
	case len(e.Measures) < 1 || len(e.Measures) > 5:
		return nil, fmt.Errorf("meter: the event has %d measures, not 1 to 5", len(e.Measures))
	}
	at := e.OccurredAt.UTC().Format(time.RFC3339)
	return json.Marshal(envelope{
		SpecVersion:     "1.0",
		ID:              e.ID,
		Type:            EventType,
		Source:          source,
		Subject:         e.Organization,
		Time:            at,
		DataContentType: "application/json",
		Data: eventData{
			Organization: e.Organization,
			Resource:     eventResource{Kind: ResourceKind, ID: e.Alias},
			OccurredAt:   at,
			Measures:     e.Measures,
		},
	})
}

// crockford is the alphabet of a ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// maxMillis is the largest time a ULID can hold: 2^48 - 1 milliseconds.
const maxMillis = 1<<48 - 1

// NewEventID returns a new ULID: 26 characters of Crockford base 32, the first 10 the time in
// milliseconds, the last 16 eighty random bits.
func NewEventID() (string, error) {
	return newULID(time.Now(), rand.Reader)
}

func newULID(now time.Time, entropy io.Reader) (string, error) {
	ms := now.UnixMilli()
	if ms < 0 || ms > maxMillis {
		return "", errors.New("meter: the time does not fit a ULID")
	}
	var random [10]byte
	if _, err := io.ReadFull(entropy, random[:]); err != nil {
		return "", fmt.Errorf("meter: read random bits: %w", err)
	}
	var out [26]byte
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms%32]
		ms /= 32
	}
	for i := 0; i < 16; i++ {
		v := 0
		for k := 0; k < 5; k++ {
			bit := i*5 + k
			v = v<<1 | int(random[bit/8]>>(7-bit%8)&1)
		}
		out[10+i] = crockford[v]
	}
	return string(out[:]), nil
}
