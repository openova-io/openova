package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A handler that waits for a condition answers "not yet" with an error, and
// JetStream redelivers until MaxDeliver. On the last delivery that error drops
// the message, so the handler must be able to tell that delivery apart.

type deliveredMsg struct {
	fakeMsg
	data []byte
}

func (m deliveredMsg) Data() []byte                       { return m.data }
func (m deliveredMsg) Ack() error                         { return nil }
func (m deliveredMsg) NakWithDelay(_ time.Duration) error { return nil }

func finalSeenOn(t *testing.T, numDelivered uint64, maxDeliver int, envelopeMeta map[string]string) bool {
	t.Helper()
	body, err := json.Marshal(Event{ID: "e1", Type: "tenant.created", Metadata: envelopeMeta})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg := deliveredMsg{fakeMsg: msgWithDelivered(numDelivered), data: body}
	var seen *bool
	handleNATSMsg(context.Background(), msg, func(e *Event) error {
		v := IsFinalDelivery(e)
		seen = &v
		return nil
	}, "provisioning", "catalyst.tenant.created", maxDeliver)
	if seen == nil {
		t.Fatalf("the handler was never called")
	}
	return *seen
}

func TestHandleNATSMsg_MarksOnlyTheLastAllowedDelivery(t *testing.T) {
	if finalSeenOn(t, 9, 10, nil) {
		t.Fatalf("delivery 9 of 10 was marked final: the handler would give up while a redelivery is still coming")
	}
	if !finalSeenOn(t, 10, 10, nil) {
		t.Fatalf("delivery 10 of 10 was not marked final: a handler returning an error on it drops the message with nothing reported")
	}
}

func TestHandleNATSMsg_FinalMarkIsTheSubscribersAlone(t *testing.T) {
	if finalSeenOn(t, 1, 10, map[string]string{MetaFinalDelivery: "true"}) {
		t.Fatalf("a final-delivery mark carried in by the published envelope reached the handler on delivery 1")
	}
}

func TestHandleNATSMsg_UnreadableMetadataIsNotFinal(t *testing.T) {
	body, _ := json.Marshal(Event{ID: "e1", Type: "tenant.created"})
	msg := deliveredMsg{fakeMsg: fakeMsg{err: errors.New("no metadata")}, data: body}
	handleNATSMsg(context.Background(), msg, func(e *Event) error {
		if IsFinalDelivery(e) {
			t.Fatalf("a delivery whose count could not be read was marked final")
		}
		return nil
	}, "provisioning", "catalyst.tenant.created", 10)
}
