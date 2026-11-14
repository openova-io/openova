package natsbus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const testSubject = "catalyst.audit.recorded.v1"

func TestNewCloudEventFillsTheEnvelope(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	ce, err := NewCloudEvent(testSubject, "catalyst-api/sov.example/region-a", "acme", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if ce.SpecVersion != "1.0" || ce.DataContentType != "application/json" {
		t.Errorf("specversion %q, datacontenttype %q", ce.SpecVersion, ce.DataContentType)
	}
	if ce.Type != "io.openova."+testSubject {
		t.Errorf("type %q", ce.Type)
	}
	if ce.Source != "catalyst-api/sov.example/region-a" || ce.Subject != "acme" {
		t.Errorf("source %q, subject %q", ce.Source, ce.Subject)
	}
	if len(ce.ID) != 26 {
		t.Errorf("id %q is not 26 characters", ce.ID)
	}
	if ce.Time.Before(before) || ce.Time.Location() != time.UTC {
		t.Errorf("time %v is not now in UTC", ce.Time)
	}
	if string(ce.Data) != `{"k":"v"}` {
		t.Errorf("data %s", ce.Data)
	}
	if got, err := ce.natsSubject(); err != nil || got != testSubject {
		t.Errorf("natsSubject: %q, %v", got, err)
	}
}

func TestNewCloudEventRefusesASubjectWithoutAVersion(t *testing.T) {
	for _, s := range []string{"", "catalyst.audit.recorded", "catalyst.Audit.recorded.v1", "audit.recorded.v1",
		"catalyst.audit.recorded.v1.more", "catalyst.audit.v1", "catalyst.audit.recorded.vx", "catalyst.audit.>"} {
		if _, err := NewCloudEvent(s, "src", "", nil); err == nil {
			t.Errorf("subject %q was accepted", s)
		}
	}
}

func TestNewCloudEventRefusesAnEmptySourceAndUnmarshalableData(t *testing.T) {
	if _, err := NewCloudEvent(testSubject, "", "", nil); err == nil {
		t.Error("an empty source was accepted")
	}
	if _, err := NewCloudEvent(testSubject, "src", "", make(chan int)); err == nil {
		t.Error("data that cannot be marshalled was accepted")
	}
}

func TestCloudEventJSONHasTheEightAttributes(t *testing.T) {
	ce, err := NewCloudEvent(testSubject, "src", "", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ce)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"specversion", "id", "type", "source", "time", "datacontenttype", "data"} {
		if _, ok := m[k]; !ok {
			t.Errorf("attribute %q is missing in %s", k, body)
		}
	}
	if _, ok := m["subject"]; ok || len(m) != 7 {
		t.Errorf("an empty subject must be absent and nothing else added: %s", body)
	}
	ce.Subject = "acme"
	body, _ = json.Marshal(ce)
	if !bytes.Contains(body, []byte(`"subject":"acme"`)) {
		t.Errorf("subject is missing in %s", body)
	}
}

func TestDecodeCloudEvent(t *testing.T) {
	ce, err := NewCloudEvent(testSubject, "src", "acme", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ce)
	got, err := DecodeCloudEvent(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ce.ID || got.Type != ce.Type || got.Source != "src" || got.Subject != "acme" ||
		!got.Time.Equal(ce.Time) || string(got.Data) != `{"n":1}` {
		t.Errorf("round trip changed the envelope: %+v", got)
	}
	if _, err := DecodeCloudEvent([]byte(`{"specversion":"0.3","id":"x"}`)); err == nil || !strings.Contains(err.Error(), "specversion") {
		t.Errorf("specversion 0.3: %v", err)
	}
	if _, err := DecodeCloudEvent([]byte(`{"id":"x","type":"tenant.created"}`)); err == nil {
		t.Error("a legacy envelope without specversion was accepted")
	}
	if _, err := DecodeCloudEvent([]byte(`not json`)); err == nil {
		t.Error("a body that is not JSON was accepted")
	}
}

func TestSendCloudEventRefusesABrokenEnvelope(t *testing.T) {
	good, err := NewCloudEvent(testSubject, "src", "", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Publisher{}).SendCloudEvent(context.Background(), good); err == nil || !strings.Contains(err.Error(), "not initialised") {
		t.Errorf("a publisher without a connection: %v", err)
	}
	var nilPublisher *Publisher
	if err := nilPublisher.SendCloudEvent(context.Background(), good); err == nil {
		t.Error("a nil publisher published")
	}
	for name, breakIt := range map[string]func(*CloudEvent){
		"specversion":     func(ce *CloudEvent) { ce.SpecVersion = "0.3" },
		"type":            func(ce *CloudEvent) { ce.Type = testSubject },
		"type subject":    func(ce *CloudEvent) { ce.Type = "io.openova.catalyst.tenant.created" },
		"id":              func(ce *CloudEvent) { ce.ID = "" },
		"source":          func(ce *CloudEvent) { ce.Source = "" },
		"time":            func(ce *CloudEvent) { ce.Time = time.Time{} },
		"datacontenttype": func(ce *CloudEvent) { ce.DataContentType = "text/plain" },
		"data":            func(ce *CloudEvent) { ce.Data = nil },
	} {
		ce := good
		breakIt(&ce)
		if _, err := ce.natsSubject(); err == nil {
			t.Errorf("an envelope with a broken %s was accepted", name)
		}
	}
}

func TestNewULID(t *testing.T) {
	zero, err := newULID(time.UnixMilli(0), bytes.NewReader(make([]byte, 10)))
	if err != nil || zero != "00000000000000000000000000" {
		t.Errorf("time 0, entropy 0: %q, %v", zero, err)
	}
	last, err := newULID(time.UnixMilli(1<<48-1), bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil || last != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Errorf("largest time, entropy of ones: %q, %v", last, err)
	}
	// The ULID specification's example: 1469918176385 ms encodes as 01ARYZ6S41.
	spec, err := newULID(time.UnixMilli(1469918176385), bytes.NewReader(make([]byte, 10)))
	if err != nil || spec != "01ARYZ6S410000000000000000" {
		t.Errorf("the time of the specification's example: %q, %v", spec, err)
	}
	if _, err := newULID(time.Now(), errReader{}); err == nil {
		t.Error("an entropy source that fails gave an id")
	}
	for _, outside := range []time.Time{time.UnixMilli(-1), time.UnixMilli(1 << 48)} {
		if _, err := newULID(outside, bytes.NewReader(make([]byte, 10))); err == nil {
			t.Errorf("the time %v gave an id", outside)
		}
	}
	earlier, later := time.UnixMilli(1700000000000), time.UnixMilli(1700000000001)
	a, _ := newULID(earlier, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	b, _ := newULID(later, bytes.NewReader(make([]byte, 10)))
	if a >= b {
		t.Errorf("%q is not before %q", a, b)
	}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 26 || strings.Trim(id, crockford) != "" || id[0] > '7' || seen[id] {
			t.Fatalf("id %q is malformed or repeated", id)
		}
		seen[id] = true
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }
