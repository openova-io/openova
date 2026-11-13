// Command probe is the NATS side of lab test CMP-S2. It is not product code.
//
//	probe ensure -url nats://127.0.0.1:4222
//	    Creates the stream CATALYST_ORG with the platform's own function
//	    (events.ConnectNATS + EnsureCatalystOrgStream) and prints its
//	    configuration and state as JSON.
//
//	probe read -url ... -subject S [-want-rule R] [-want-pod P] [-timeout D] -out F
//	    Reads every message on subject S from CATALYST_ORG with an ordered
//	    ephemeral consumer that starts at now minus -since (the consumer shape
//	    the design names), until a message matches -want-rule (and -want-pod)
//	    or -timeout passes. Writes every message to F (JSON lines) and prints a
//	    summary: stream state, counts by rule and priority, the first match and
//	    its delay, and the shape of the payload (keys and JSON types).
//
//	probe schema -in F
//	    Prints a draft JSON Schema derived from the detection messages in F
//	    (every message whose source is not "internal").
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/openova-io/openova/core/services/shared/events"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe ensure|read|schema [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ensure":
		err = ensure(os.Args[2:])
	case "read":
		err = read(os.Args[2:])
	case "schema":
		err = schema(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func ensure(args []string) error {
	fs := flag.NewFlagSet("ensure", flag.ExitOnError)
	url := fs.String("url", "nats://127.0.0.1:4222", "NATS URL")
	_ = fs.Parse(args)

	conn, err := events.ConnectNATS(*url)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.EnsureCatalystOrgStream(ctx); err != nil {
		return err
	}
	conn.Close()

	nc, err := nats.Connect(*url)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	s, err := js.Stream(ctx, events.StreamCatalystOrg)
	if err != nil {
		return err
	}
	info, err := s.Info(ctx)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{
		"server_version": nc.ConnectedServerVersion(),
		"config":         info.Config,
		"state":          info.State,
		"cluster":        info.Cluster,
	})
}

type record struct {
	Seq        uint64          `json:"seq"`
	StreamTime time.Time       `json:"stream_time"`
	Subject    string          `json:"subject"`
	Headers    nats.Header     `json:"headers"`
	Bytes      int             `json:"bytes"`
	Data       json.RawMessage `json:"data"`
}

func read(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	url := fs.String("url", "nats://127.0.0.1:4222", "NATS URL")
	subject := fs.String("subject", "catalyst.security.runtime_detected.v1", "subject to read")
	since := fs.Duration("since", 7*24*time.Hour, "consumer start: now minus this")
	wantRule := fs.String("want-rule", "", "stop when a message with this rule is read")
	wantPod := fs.String("want-pod", "", "and output_fields k8s.pod.name equals this")
	afterS := fs.String("after", "", "RFC 3339 time: only messages stored after it count for the match and the after_* counts")
	timeout := fs.Duration("timeout", 0, "how long to wait for the wanted message")
	out := fs.String("out", "", "write every message here (JSON lines)")
	label := fs.String("label", "", "label printed in the summary")
	_ = fs.Parse(args)

	nc, err := nats.Connect(*url)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	var after time.Time
	if *afterS != "" {
		t, err := time.Parse(time.RFC3339Nano, *afterS)
		if err != nil {
			return fmt.Errorf("-after: %w", err)
		}
		after = t
	}

	began := time.Now()
	deadline := began.Add(*timeout)
	var recs []record
	var state jetstream.StreamState
	var match *record
	polls := 0
	for {
		polls++
		recs, state, err = readAll(js, *subject, *since)
		if err != nil {
			return err
		}
		match = findMatch(afterOnly(recs, after), *wantRule, *wantPod)
		waiting := *wantRule != "" || *wantPod != ""
		if !waiting || match != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if *out != "" {
		var buf bytes.Buffer
		for _, r := range recs {
			b, _ := json.Marshal(r)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}

	summary := map[string]any{
		"label":              *label,
		"subject":            *subject,
		"consumer":           fmt.Sprintf("ordered ephemeral, filter %s, deliver by start time now-%s", *subject, since.String()),
		"stream_messages":    state.Msgs,
		"stream_bytes":       state.Bytes,
		"stream_subjects":    state.Subjects,
		"messages_read":      len(recs),
		"polls":              polls,
		"waited_seconds":     round(time.Since(began).Seconds()),
		"by_rule":            countBy(recs, "rule"),
		"by_priority":        countBy(recs, "priority"),
		"by_source":          countBy(recs, "source"),
		"by_hostname":        countBy(recs, "hostname"),
		"after":              *afterS,
		"after_by_rule":      countBy(afterOnly(recs, after), "rule"),
		"after_by_priority":  countBy(afterOnly(recs, after), "priority"),
		"header_keys_seen":   headerKeys(recs),
		"payload_shape":      shape(recs),
		"time_field_rfc3339": timeCheck(recs),
	}
	if *wantRule != "" || *wantPod != "" {
		summary["want_rule"] = *wantRule
		summary["want_pod"] = *wantPod
		summary["found"] = match != nil
		if match != nil {
			var p map[string]any
			_ = json.Unmarshal(match.Data, &p)
			m := map[string]any{"seq": match.Seq, "stream_time": match.StreamTime, "bytes": match.Bytes}
			if ts, ok := p["time"].(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					m["event_time"] = t
					m["event_to_stream_seconds"] = round(match.StreamTime.Sub(t).Seconds())
				}
			}
			summary["first_match"] = m
			summary["first_match_payload"] = json.RawMessage(match.Data)
		}
	}
	samples := map[string]json.RawMessage{}
	for _, r := range recs {
		var p map[string]any
		if json.Unmarshal(r.Data, &p) != nil {
			continue
		}
		rule, _ := p["rule"].(string)
		if _, ok := samples[rule]; !ok {
			samples[rule] = r.Data
		}
	}
	summary["first_payload_per_rule"] = samples
	return printJSON(summary)
}

func readAll(js jetstream.JetStream, subject string, since time.Duration) ([]record, jetstream.StreamState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := js.Stream(ctx, events.StreamCatalystOrg)
	if err != nil {
		return nil, jetstream.StreamState{}, err
	}
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(">"))
	if err != nil {
		return nil, jetstream.StreamState{}, err
	}
	n := int(info.State.Subjects[subject])
	if n == 0 {
		return nil, info.State, nil
	}
	start := time.Now().Add(-since)
	cons, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverByStartTimePolicy,
		OptStartTime:   &start,
	})
	if err != nil {
		return nil, info.State, err
	}
	var recs []record
	for len(recs) < n {
		batch, err := cons.Fetch(n-len(recs), jetstream.FetchMaxWait(10*time.Second))
		if err != nil {
			return recs, info.State, err
		}
		got := 0
		for m := range batch.Messages() {
			got++
			md, _ := m.Metadata()
			r := record{Subject: m.Subject(), Headers: m.Headers(), Bytes: len(m.Data()), Data: json.RawMessage(append([]byte(nil), m.Data()...))}
			if md != nil {
				r.Seq = md.Sequence.Stream
				r.StreamTime = md.Timestamp
			}
			recs = append(recs, r)
		}
		if got == 0 {
			break
		}
	}
	return recs, info.State, nil
}

// afterOnly keeps the messages the stream stored after t (all when t is zero).
func afterOnly(recs []record, t time.Time) []record {
	if t.IsZero() {
		return recs
	}
	var out []record
	for _, r := range recs {
		if r.StreamTime.After(t) {
			out = append(out, r)
		}
	}
	return out
}

func findMatch(recs []record, rule, pod string) *record {
	if rule == "" && pod == "" {
		return nil
	}
	for i := range recs {
		var p map[string]any
		if json.Unmarshal(recs[i].Data, &p) != nil {
			continue
		}
		if rule != "" && p["rule"] != rule {
			continue
		}
		if pod != "" {
			of, _ := p["output_fields"].(map[string]any)
			if of == nil || of["k8s.pod.name"] != pod {
				continue
			}
		}
		return &recs[i]
	}
	return nil
}

func countBy(recs []record, key string) map[string]int {
	c := map[string]int{}
	for _, r := range recs {
		var p map[string]any
		if json.Unmarshal(r.Data, &p) != nil {
			c["<not JSON>"]++
			continue
		}
		c[fmt.Sprint(p[key])]++
	}
	return c
}

func headerKeys(recs []record) []string {
	seen := map[string]bool{}
	for _, r := range recs {
		for k := range r.Headers {
			seen[k] = true
		}
	}
	return sortedKeys(seen)
}

// jsonType names the JSON type of a value decoded with UseNumber.
func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if strings.ContainsAny(t.String(), ".eE") {
			return "number"
		}
		return "integer"
	case []any:
		el := map[string]bool{}
		for _, e := range t {
			el[jsonType(e)] = true
		}
		if len(el) == 0 {
			return "array<empty>"
		}
		return "array<" + strings.Join(sortedKeys(el), "|") + ">"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func decode(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var p map[string]any
	err := d.Decode(&p)
	return p, err
}

// shape returns, per rule: for every top-level key and every output_fields
// key, the JSON types seen and in how many of that rule's messages it is.
func shape(recs []record) map[string]any {
	type acc struct {
		n    int
		top  map[string]map[string]bool
		topN map[string]int
		of   map[string]map[string]bool
		ofN  map[string]int
	}
	per := map[string]*acc{}
	for _, r := range recs {
		p, err := decode(r.Data)
		if err != nil {
			continue
		}
		rule := fmt.Sprint(p["rule"])
		a := per[rule]
		if a == nil {
			a = &acc{top: map[string]map[string]bool{}, topN: map[string]int{}, of: map[string]map[string]bool{}, ofN: map[string]int{}}
			per[rule] = a
		}
		a.n++
		for k, v := range p {
			if a.top[k] == nil {
				a.top[k] = map[string]bool{}
			}
			a.top[k][jsonType(v)] = true
			a.topN[k]++
		}
		if of, ok := p["output_fields"].(map[string]any); ok {
			for k, v := range of {
				if a.of[k] == nil {
					a.of[k] = map[string]bool{}
				}
				a.of[k][jsonType(v)] = true
				a.ofN[k]++
			}
		}
	}
	res := map[string]any{}
	for rule, a := range per {
		top := map[string]string{}
		for k, t := range a.top {
			top[k] = fmt.Sprintf("%s (in %d of %d)", strings.Join(sortedKeys(t), "|"), a.topN[k], a.n)
		}
		of := map[string]string{}
		for k, t := range a.of {
			of[k] = fmt.Sprintf("%s (in %d of %d)", strings.Join(sortedKeys(t), "|"), a.ofN[k], a.n)
		}
		res[rule] = map[string]any{"messages": a.n, "top_level": top, "output_fields": of}
	}
	return res
}

func timeCheck(recs []record) map[string]int {
	c := map[string]int{}
	for _, r := range recs {
		p, err := decode(r.Data)
		if err != nil {
			continue
		}
		s, ok := p["time"].(string)
		if !ok {
			c["time not a string"]++
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			c["does not parse as RFC 3339"]++
		} else {
			c["parses as RFC 3339"]++
		}
	}
	return c
}

func schema(args []string) error {
	fs := flag.NewFlagSet("schema", flag.ExitOnError)
	in := fs.String("in", "", "JSON lines written by read -out")
	_ = fs.Parse(args)
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()

	n := 0
	topTypes := map[string]map[string]bool{}
	topCount := map[string]int{}
	ofTypes := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return err
		}
		p, err := decode(r.Data)
		if err != nil {
			return err
		}
		if p["source"] == "internal" {
			continue
		}
		n++
		for k, v := range p {
			if topTypes[k] == nil {
				topTypes[k] = map[string]bool{}
			}
			topTypes[k][schemaType(v)] = true
			topCount[k]++
		}
		if of, ok := p["output_fields"].(map[string]any); ok {
			for _, v := range of {
				ofTypes[schemaType(v)] = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	props := map[string]any{}
	var required []string
	for k, ts := range topTypes {
		types := sortedKeys(ts)
		var prop map[string]any
		if len(types) == 1 {
			prop = map[string]any{"type": types[0]}
		} else {
			prop = map[string]any{"type": types}
		}
		switch k {
		case "time":
			prop["format"] = "date-time"
		case "tags":
			prop["items"] = map[string]any{"type": "string"}
		case "output_fields":
			prop["additionalProperties"] = map[string]any{"type": sortedKeys(ofTypes)}
		}
		props[k] = prop
		if topCount[k] == n {
			required = append(required, k)
		}
	}
	sort.Strings(required)
	return printJSON(map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                "catalyst.security.runtime_detected.v1 (draft derived by lab test CMP-S2)",
		"description":          fmt.Sprintf("Derived from %d captured detection messages; the internal metrics event is excluded.", n),
		"type":                 "object",
		"required":             required,
		"properties":           props,
		"additionalProperties": true,
	})
}

func schemaType(v any) string {
	t := jsonType(v)
	if strings.HasPrefix(t, "array") {
		return "array"
	}
	return t
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func round(f float64) float64 { return float64(int64(f*1000)) / 1000 }

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
