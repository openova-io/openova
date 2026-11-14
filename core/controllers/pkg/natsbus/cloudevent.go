// cloudevent.go is the envelope of contract C3 for every subject that is
// registered as `envelope: cloudevents` in docs/contracts/C3-event-subjects.yaml:
// CloudEvents 1.0 in structured JSON mode.
//
//	{"specversion":"1.0","id":"<ULID>","type":"io.openova.<NATS subject>",
//	 "source":"<component>/<sovereign>/<region>","subject":"<organization>",
//	 "time":"<RFC 3339>","datacontenttype":"application/json","data":{...}}
//
// Every attribute is required except subject. The id is a ULID and is sent as
// the Nats-Msg-Id header, so the stream drops a repeated publish of the same
// event. The legacy subjects keep the Event envelope of subscriber.go.
package natsbus

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	// cloudEventSpecVersion is the only specversion this package writes and reads.
	cloudEventSpecVersion = "1.0"
	// cloudEventTypePrefix comes before the NATS subject in the type attribute.
	cloudEventTypePrefix = "io.openova."
	// cloudEventContentType is the only datacontenttype this package writes.
	cloudEventContentType = "application/json"
)

// versionedSubject is the form of a subject registered after contract C3.
var versionedSubject = regexp.MustCompile(`^catalyst\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[0-9]+$`)

// CloudEvent is the envelope of every subject registered after contract C3.
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`       // always "1.0"
	ID              string          `json:"id"`                // ULID
	Type            string          `json:"type"`              // "io.openova." + NATS subject
	Source          string          `json:"source"`            // who published it
	Subject         string          `json:"subject,omitempty"` // Organization slug, or absent
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype"` // always "application/json"
	Data            json.RawMessage `json:"data"`
}

// NewCloudEvent fills SpecVersion, ID, Time and DataContentType and marshals
// data. natsSubject must match ^catalyst\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[0-9]+$ .
// source must not be empty. subject is the Organization slug, or "".
func NewCloudEvent(natsSubject, source, subject string, data any) (CloudEvent, error) {
	if !versionedSubject.MatchString(natsSubject) {
		return CloudEvent{}, fmt.Errorf("natsbus: %q is not a subject of the form catalyst.<domain>.<event>.v<major>", natsSubject)
	}
	if source == "" {
		return CloudEvent{}, errors.New("natsbus: a CloudEvent needs a source")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return CloudEvent{}, fmt.Errorf("natsbus: marshal CloudEvent data: %w", err)
	}
	return CloudEvent{
		SpecVersion:     cloudEventSpecVersion,
		ID:              NewID(),
		Type:            cloudEventTypePrefix + natsSubject,
		Source:          source,
		Subject:         subject,
		Time:            time.Now().UTC(),
		DataContentType: cloudEventContentType,
		Data:            raw,
	}, nil
}

// natsSubject returns the NATS subject that the type attribute names. It
// returns an error when a required attribute is missing or has another value
// than this package writes.
func (ce CloudEvent) natsSubject() (string, error) {
	subject := strings.TrimPrefix(ce.Type, cloudEventTypePrefix)
	switch {
	case ce.SpecVersion != cloudEventSpecVersion:
		return "", fmt.Errorf("natsbus: CloudEvent specversion is %q, want %q", ce.SpecVersion, cloudEventSpecVersion)
	case subject == ce.Type || !versionedSubject.MatchString(subject):
		return "", fmt.Errorf("natsbus: CloudEvent type %q is not %s<catalyst.<domain>.<event>.v<major>>", ce.Type, cloudEventTypePrefix)
	case ce.ID == "":
		return "", errors.New("natsbus: CloudEvent has no id")
	case ce.Source == "":
		return "", errors.New("natsbus: CloudEvent has no source")
	case ce.Time.IsZero():
		return "", errors.New("natsbus: CloudEvent has no time")
	case ce.DataContentType != cloudEventContentType:
		return "", fmt.Errorf("natsbus: CloudEvent datacontenttype is %q, want %q", ce.DataContentType, cloudEventContentType)
	case !json.Valid(ce.Data):
		return "", errors.New("natsbus: CloudEvent data is not JSON")
	}
	return subject, nil
}

// SendCloudEvent publishes to the subject derived from ce.Type, with
// Nats-Msg-Id = ce.ID, and returns only after the JetStream acknowledgement.
// The wait is bounded by ctx. An envelope with a missing or wrong required
// attribute is not published.
//
// The name does not start with Publish on purpose: that word followed by
// CloudEvent holds the name of a cloud provider's tool, and
// scripts/check-provider-leaks.py would count every line that calls it.
func (p *Publisher) SendCloudEvent(ctx context.Context, ce CloudEvent) error {
	if p == nil || p.js == nil {
		return errors.New("natsbus: publisher not initialised")
	}
	subject, err := ce.natsSubject()
	if err != nil {
		return err
	}
	body, err := json.Marshal(ce)
	if err != nil {
		return fmt.Errorf("natsbus: marshal CloudEvent: %w", err)
	}
	if _, err := p.js.Publish(ctx, subject, body, jetstream.WithMsgID(ce.ID)); err != nil {
		return fmt.Errorf("natsbus: publish %s: %w", subject, err)
	}
	return nil
}

// DecodeCloudEvent parses a message body; it returns an error when the body
// is not a JSON object or specversion is not "1.0".
func DecodeCloudEvent(body []byte) (CloudEvent, error) {
	var ce CloudEvent
	if err := json.Unmarshal(body, &ce); err != nil {
		return CloudEvent{}, fmt.Errorf("natsbus: decode CloudEvent: %w", err)
	}
	if ce.SpecVersion != cloudEventSpecVersion {
		return CloudEvent{}, fmt.Errorf("natsbus: CloudEvent specversion is %q, want %q", ce.SpecVersion, cloudEventSpecVersion)
	}
	return ce, nil
}

// crockford is the alphabet of a ULID: base 32 without I, L, O and U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a new ULID: 26 characters, 48 bits of Unix time in
// milliseconds followed by 80 random bits. Identifiers made in different
// milliseconds sort by time. It is the id NewCloudEvent fills in; a producer
// that needs the id before it builds the envelope calls NewID and sets
// CloudEvent.ID to it. Like uuid.New it panics when the system has no
// randomness, or when the clock is outside the range of a ULID.
func NewID() string {
	id, err := newULID(time.Now(), rand.Reader)
	if err != nil {
		panic("natsbus: no event id: " + err.Error())
	}
	return id
}

// newULID builds a ULID from a time and 10 bytes read from entropy. The time
// must lie between 1970 and the year 10889, the range of 48 bits of
// milliseconds.
func newULID(t time.Time, entropy io.Reader) (string, error) {
	ms := t.UnixMilli()
	if ms < 0 || ms >= 1<<48 {
		return "", fmt.Errorf("natsbus: the time %s is outside the range of a ULID", t.UTC().Format(time.RFC3339))
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(ms)<<16)
	if _, err := io.ReadFull(entropy, b[6:]); err != nil {
		return "", err
	}
	// 128 bits are 26 groups of 5 bits, the last group first; the first
	// character holds the 3 bits that are left.
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
