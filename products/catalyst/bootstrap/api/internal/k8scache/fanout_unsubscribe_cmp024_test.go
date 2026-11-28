package k8scache

import (
	"sync"
	"testing"
	"time"
)

// TestFanoutNeverSendsOnAClosedChannel (CMP-024): four publishers fan events
// out without pause while subscribers come and go, each unsubscribed the
// moment it has received an event, so publishers are sending to it while it
// leaves. Before CMP-024 the fanout sent after it had released subMu and an
// unsubscribe in between closed the channel: the send panicked ("send on
// closed channel") and took the process down, and under -race the close and
// the send are reported as a race. Each subscriber must receive an event (the
// fanout reached it while the publishers ran), and its channel must be closed
// after the unsubscribe (a consumer ranging over it ends).
func TestFanoutNeverSendsOnAClosedChannel(t *testing.T) {
	f, err := NewFactory(Config{Logger: quietLogger(), EventBufferSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var publishers sync.WaitGroup
	for range 4 {
		publishers.Add(1)
		go func() {
			defer publishers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				f.Publish(Event{Cluster: "acme", Kind: "pod", Type: EventAdded, Object: newPod("ns", "p"), At: time.Now()})
			}
		}()
	}
	defer func() {
		close(stop)
		publishers.Wait()
	}()

	const subscribers = 2000
	for i := range subscribers {
		ch, unsub := f.Subscribe("", map[string]struct{}{"pod": {}})
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("subscriber %d of %d received no event in 5s: the fanout never reached it", i+1, subscribers)
		}
		unsub()
		closed := make(chan struct{})
		go func() {
			for range ch { // the events still buffered, then the close ends the loop
			}
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("subscriber %d: its channel is still open after the unsubscribe", i+1)
		}
	}
}
