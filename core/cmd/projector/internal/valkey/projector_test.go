package valkey

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestKey_NamespaceScoped(t *testing.T) {
	got := Key("omantel", "pod", "default", "web-7d8b")
	want := "cluster:omantel:kind:pod:default/web-7d8b"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKey_ClusterScoped(t *testing.T) {
	got := Key("omantel", "node", "", "node-01")
	want := "cluster:omantel:kind:node:/node-01"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApply_AddedAndModified(t *testing.T) {
	mem := NewMemKV()
	p := NewProjector(mem, 0)
	body := []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"default"}}`)

	ev := Event{
		Cluster:  "omantel",
		Kind:     "pod",
		Type:     EventAdded,
		RawBytes: body,
		At:       time.Now(),
	}
	ev.Object.Metadata.Name = "web-1"
	ev.Object.Metadata.Namespace = "default"

	key, err := p.Apply(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if key != "cluster:omantel:kind:pod:default/web-1" {
		t.Fatalf("key: %q", key)
	}
	got, ok := mem.Get(key)
	if !ok {
		t.Fatal("key not stored")
	}
	if string(got) != string(body) {
		t.Fatalf("body: got %s want %s", got, body)
	}

	// MODIFIED overwrites.
	body2 := []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"default","resourceVersion":"42"}}`)
	ev.Type = EventModified
	ev.RawBytes = body2
	if _, err := p.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	got2, _ := mem.Get(key)
	if string(got2) != string(body2) {
		t.Fatal("MODIFIED did not overwrite")
	}
}

func TestApply_Deleted(t *testing.T) {
	mem := NewMemKV()
	p := NewProjector(mem, time.Hour)

	body := []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"default"}}`)
	ev := Event{
		Cluster:  "omantel",
		Kind:     "pod",
		Type:     EventAdded,
		RawBytes: body,
	}
	ev.Object.Metadata.Name = "web-1"
	ev.Object.Metadata.Namespace = "default"
	if _, err := p.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if mem.Len() != 1 {
		t.Fatalf("len after add: %d", mem.Len())
	}

	ev.Type = EventDeleted
	ev.RawBytes = nil // DELETE doesn't need bytes
	if _, err := p.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if mem.Len() != 0 {
		t.Fatalf("len after delete: %d", mem.Len())
	}
}

func TestApply_Validation(t *testing.T) {
	p := NewProjector(NewMemKV(), 0)
	cases := []struct {
		name   string
		ev     Event
		errSub string
	}{
		{
			name:   "missing-name",
			ev:     Event{Cluster: "c", Kind: "pod", Type: EventAdded, RawBytes: []byte("{}")},
			errSub: "metadata.name",
		},
		{
			name: "missing-cluster",
			ev: func() Event {
				e := Event{Kind: "pod", Type: EventAdded, RawBytes: []byte("{}")}
				e.Object.Metadata.Name = "x"
				return e
			}(),
			errSub: "cluster",
		},
		{
			name: "missing-rawbytes",
			ev: func() Event {
				e := Event{Cluster: "c", Kind: "pod", Type: EventAdded}
				e.Object.Metadata.Name = "x"
				return e
			}(),
			errSub: "RawBytes",
		},
		{
			name: "unknown-type",
			ev: func() Event {
				e := Event{Cluster: "c", Kind: "pod", Type: "BOGUS", RawBytes: []byte("{}")}
				e.Object.Metadata.Name = "x"
				return e
			}(),
			errSub: "unknown event type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Apply(context.Background(), tc.ev)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("err %q missing %q", err, tc.errSub)
			}
		})
	}
}

func TestNewProjector_DefaultTTL(t *testing.T) {
	p := NewProjector(NewMemKV(), 0)
	if p.ttl != 24*time.Hour {
		t.Fatalf("default ttl: %v", p.ttl)
	}
}
