package events

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// fakeJS embeds the interface; only the three methods the publisher uses
// are implemented. hasStream models whether any stream binds catalyst.audit.
type fakeJS struct {
	jetstream.JetStream
	hasStream bool
	created   []jetstream.StreamConfig
	published int
	createErr error
}

func (f *fakeJS) PublishMsg(_ context.Context, m *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	if !f.hasStream {
		return nil, jetstream.ErrNoStreamResponse
	}
	f.published++
	return &jetstream.PubAck{Stream: StreamCatalystOrg}, nil
}

func (f *fakeJS) StreamNameBySubject(_ context.Context, _ string) (string, error) {
	if f.hasStream {
		return StreamCatalystOrg, nil
	}
	return "", jetstream.ErrStreamNotFound
}

func (f *fakeJS) CreateStream(_ context.Context, c jetstream.StreamConfig) (jetstream.Stream, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, c)
	f.hasStream = true
	return nil, nil
}

func auditEvent() Event {
	return Event{Type: TypeSwitchover, ContinuumName: "cnpg/dr-cnpg-pair", ApplicationName: "cnpg-pair"}
}

// hw307 2026-11-07: no stream bound to catalyst.audit -> step-7 failed and
// rolled back a successful promotion. The publisher must create the
// canonical stream and deliver.
func TestPublish_CreatesCanonicalStreamWhenNoneBound(t *testing.T) {
	f := &fakeJS{}
	p := NewJetStreamPublisherWith(f)
	if err := p.Publish(context.Background(), auditEvent()); err != nil {
		t.Fatalf("publish with no stream: %v", err)
	}
	if len(f.created) != 1 || f.created[0].Name != StreamCatalystOrg || len(f.created[0].Subjects) != 1 || f.created[0].Subjects[0] != "catalyst.>" {
		t.Fatalf("expected the canonical CATALYST_ORG catalyst.> stream, got %+v", f.created)
	}
	if f.published != 1 {
		t.Fatalf("published %d, want 1", f.published)
	}
}

func TestPublish_ExistingStreamUntouched(t *testing.T) {
	f := &fakeJS{hasStream: true}
	if err := NewJetStreamPublisherWith(f).Publish(context.Background(), auditEvent()); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 {
		t.Fatalf("must not create a stream when one is bound: %+v", f.created)
	}
}

func TestPublish_ConcurrentCreatorWinsIsSuccess(t *testing.T) {
	f := &fakeJS{createErr: jetstream.ErrStreamNameAlreadyInUse}
	err := EnsureCatalystStream(context.Background(), f)
	if err != nil {
		t.Fatalf("a concurrent creator must count as success: %v", err)
	}
}

func TestPublish_EnsureFailureSurfaces(t *testing.T) {
	f := &fakeJS{createErr: errors.New("boom")}
	if err := NewJetStreamPublisherWith(f).Publish(context.Background(), auditEvent()); err == nil {
		t.Fatal("expected an error when the stream cannot be ensured")
	}
}
