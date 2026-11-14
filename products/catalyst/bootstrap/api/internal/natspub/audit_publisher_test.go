package natspub

// The tests of the audit publisher run against a real NATS server with
// JetStream: the release that the chart of bp-nats-jetstream runs. The test
// binary builds it once with `go install <module>@<version>` into a folder of
// its own, so the module of catalyst-api gains no dependency. A build that
// fails, for example without a network, fails the tests: they are never
// skipped.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
)

// natsServerModule is the server the tests run: the appVersion of the nats
// chart 1.2.0 that platform/nats-jetstream/chart/Chart.yaml depends on.
const natsServerModule = "github.com/nats-io/nats-server/v2@v2.10.16"

const testSource = "catalyst-api/sov.example/region-a"

var (
	natsBuild    sync.Once
	natsBuildDir string
	natsBuildErr error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if natsBuildDir != "" {
		_ = os.RemoveAll(natsBuildDir)
	}
	os.Exit(code)
}

// natsServerBinary builds the server once per test binary and returns its path.
func natsServerBinary(t *testing.T) string {
	t.Helper()
	natsBuild.Do(func() {
		dir, err := os.MkdirTemp("", "natspub-nats-server-")
		if err != nil {
			natsBuildErr = err
			return
		}
		natsBuildDir = dir
		cmd := exec.Command("go", "install", natsServerModule)
		cmd.Dir = dir // outside every module
		cmd.Env = append(os.Environ(), "GOBIN="+dir, "GOFLAGS=", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			natsBuildErr = fmt.Errorf("go install %s: %v\n%s", natsServerModule, err, out)
		}
	})
	if natsBuildErr != nil {
		t.Fatalf("the NATS server could not be built: %v", natsBuildErr)
	}
	return filepath.Join(natsBuildDir, "nats-server")
}

// natsServer is one running server with JetStream.
type natsServer struct {
	URL  string
	stop func()
}

// startNATS starts a server on a free local port with a store of its own.
// With stream set it creates CATALYST_ORG as the chart declares it.
func startNATS(t *testing.T, stream bool) *natsServer {
	t.Helper()
	bin := natsServerBinary(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	var logs bytes.Buffer
	cmd := &exec.Cmd{Path: bin, Args: []string{bin, "-js", "-a", "127.0.0.1", "-p", fmt.Sprint(port), "-sd", t.TempDir()},
		Stdout: &logs, Stderr: &logs}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	var once sync.Once
	s := &natsServer{URL: fmt.Sprintf("nats://127.0.0.1:%d", port)}
	s.stop = func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(s.stop)

	var nc *nats.Conn
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if nc, err = nats.Connect(s.URL, nats.Timeout(time.Second)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the NATS server did not accept a connection: %v\n%s", err, logs.String())
		}
	}
	defer nc.Close()
	if stream {
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
			Name:      natsbus.StreamCatalystOrg,
			Subjects:  []string{"catalyst.>"},
			Storage:   jetstream.FileStorage,
			Retention: jetstream.LimitsPolicy,
			MaxAge:    336 * time.Hour,
			Replicas:  1,
			Discard:   jetstream.DiscardOld,
		}); err != nil {
			t.Fatalf("create stream: %v", err)
		}
	}
	return s
}

func newTestAuditPublisher(t *testing.T, url string) *AuditPublisher {
	t.Helper()
	p, err := NewAuditPublisher(url, testSource, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// rawMessages returns every message of the stream on a subject: header
// Nats-Msg-Id and body, in order.
func rawMessages(t *testing.T, url, subject string) (ids []string, bodies [][]byte) {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := js.Stream(ctx, natsbus.StreamCatalystOrg)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		t.Fatal(err)
	}
	n := info.State.Subjects[subject]
	if n == 0 {
		return nil, nil
	}
	cons, err := js.OrderedConsumer(ctx, natsbus.StreamCatalystOrg, jetstream.OrderedConsumerConfig{FilterSubjects: []string{subject}})
	if err != nil {
		t.Fatal(err)
	}
	for ; n > 0; n-- {
		msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
		if err != nil {
			t.Fatalf("%d messages on %s were not read: %v", n, subject, err)
		}
		ids = append(ids, msg.Headers().Get(jetstream.MsgIDHeader))
		bodies = append(bodies, msg.Data())
	}
	return ids, bodies
}

func TestAuditEventIsPublishedAsACloudEvent(t *testing.T) {
	srv := startNATS(t, true)
	bus := audit.NewBus(audit.BusConfig{Publisher: newTestAuditPublisher(t, srv.URL)})
	when := time.Date(2026, 11, 12, 9, 0, 4, 0, time.UTC)
	if err := bus.PublishStrict(context.Background(), audit.Event{
		AuditType: audit.AuditTypeRBACGrantCreated, SovereignID: "dep-1", Actor: "admin@sov.example",
		Tier: "viewer", UserAccessRef: "rbac-abc", Timestamp: when, Phase: audit.PhaseIntent,
	}); err != nil {
		t.Fatal(err)
	}
	listed := bus.List("dep-1", nil, 10)
	if len(listed) != 1 || len(listed[0].ID) != 26 {
		t.Fatalf("the ring holds %+v", listed)
	}
	ids, bodies := rawMessages(t, srv.URL, AuditSubject)
	if len(bodies) != 1 {
		t.Fatalf("the stream holds %d messages on %s, want 1", len(bodies), AuditSubject)
	}
	if ids[0] != listed[0].ID {
		t.Errorf("header Nats-Msg-Id %q, want the event id %q", ids[0], listed[0].ID)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(bodies[0], &env); err != nil {
		t.Fatal(err)
	}
	for attr, want := range map[string]string{
		"specversion": `"1.0"`, "id": `"` + listed[0].ID + `"`, "type": `"io.openova.catalyst.audit.recorded.v1"`,
		"source": `"` + testSource + `"`, "time": `"2026-11-12T09:00:04Z"`, "datacontenttype": `"application/json"`,
	} {
		if string(env[attr]) != want {
			t.Errorf("envelope attribute %s is %s, want %s", attr, env[attr], want)
		}
	}
	if _, has := env["subject"]; has {
		t.Errorf("an audit event names no Organization: the envelope must have no subject: %s", bodies[0])
	}
	var data audit.Event
	if err := json.Unmarshal(env["data"], &data); err != nil {
		t.Fatal(err)
	}
	if data.ID != listed[0].ID || data.Phase != "intent" || data.AuditType != audit.AuditTypeRBACGrantCreated ||
		data.SovereignID != "dep-1" || data.Actor != "admin@sov.example" || data.Result != "ok" || !data.Timestamp.Equal(when) {
		t.Errorf("data is %s", env["data"])
	}
}

// The acceptance of the durable audit: an event published through one Bus is
// listed by a new Bus that was filled from the same server.
func TestAuditSurvivesANewBus(t *testing.T) {
	srv := startNATS(t, true)
	first := audit.NewBus(audit.BusConfig{Publisher: newTestAuditPublisher(t, srv.URL)})
	first.Publish(context.Background(), audit.Event{AuditType: audit.AuditTypeRBACGrantCreated, SovereignID: "dep-1", TargetUser: "u1"})
	if err := first.PublishStrict(context.Background(), audit.Event{AuditType: audit.AuditTypeRBACTierChanged, SovereignID: "dep-1", TargetUser: "u2"}); err != nil {
		t.Fatal(err)
	}
	before := first.List("dep-1", audit.IsRBACAuditType, 10)
	if len(before) != 2 {
		t.Fatalf("the first Bus lists %d events, want 2", len(before))
	}

	// The restart: a new publisher and a new Bus, built the way main.go
	// builds them. A Bus without the replay would be empty.
	if got := audit.NewBus(audit.BusConfig{}).List("dep-1", nil, 10); len(got) != 0 {
		t.Fatalf("a new Bus is not empty: %+v", got)
	}
	var logged bytes.Buffer
	second := NewAuditBus(newTestAuditPublisher(t, srv.URL), 0, slog.New(slog.NewJSONHandler(&logged, nil)))
	if second.Transport() != audit.TransportJetStream {
		t.Errorf("transport %q, want %q", second.Transport(), audit.TransportJetStream)
	}
	if !strings.Contains(logged.String(), `"replayed":2`) {
		t.Errorf("the start line does not say that 2 events were replayed: %s", logged.String())
	}
	after := second.List("dep-1", audit.IsRBACAuditType, 10)
	if len(after) != 2 {
		t.Fatalf("the new Bus lists %d events, want 2", len(after))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].AuditType != before[i].AuditType ||
			after[i].TargetUser != before[i].TargetUser || !after[i].Timestamp.Equal(before[i].Timestamp) {
			t.Errorf("event %d: before %+v, after %+v", i, before[i], after[i])
		}
	}
	// The replay published nothing again.
	if _, bodies := rawMessages(t, srv.URL, AuditSubject); len(bodies) != 2 {
		t.Errorf("the stream holds %d messages after the replay, want 2", len(bodies))
	}
}

func TestAuditPublishStrictFailsWhenTheServerIsStopped(t *testing.T) {
	srv := startNATS(t, true)
	bus := audit.NewBus(audit.BusConfig{Publisher: newTestAuditPublisher(t, srv.URL)})
	if err := bus.PublishStrict(context.Background(), audit.Event{AuditType: "break-glass", SovereignID: "dep-1"}); err != nil {
		t.Fatalf("with the server up: %v", err)
	}
	srv.stop()
	start := time.Now()
	err := bus.PublishStrict(context.Background(), audit.Event{AuditType: "break-glass", SovereignID: "dep-1", Detail: "second"})
	if err == nil {
		t.Fatal("PublishStrict returned no error with the server stopped")
	}
	if errors.Is(err, audit.ErrNoPublisher) {
		t.Errorf("the error must be the publisher's, got %v", err)
	}
	if waited := time.Since(start); waited > auditPublishTimeout+5*time.Second {
		t.Errorf("PublishStrict waited %s", waited)
	}
	if got := bus.List("dep-1", nil, 10); len(got) != 1 || got[0].Detail == "second" {
		t.Errorf("an event the stream does not hold is in the ring: %+v", got)
	}
	// Publish keeps the event in the ring and does not fail the caller.
	bus.Publish(context.Background(), audit.Event{AuditType: audit.AuditTypeRBACGrantCreated, SovereignID: "dep-1", Detail: "third"})
	if got := bus.List("dep-1", nil, 10); len(got) != 2 || got[0].Detail != "third" {
		t.Errorf("Publish did not keep the event in the ring: %+v", got)
	}
}

func TestAuditReplayReturnsTheNewestInOrder(t *testing.T) {
	srv := startNATS(t, true)
	p := newTestAuditPublisher(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if got, err := p.Replay(ctx, 10); err != nil || len(got) != 0 {
		t.Fatalf("replay of an empty subject: %v, %v", got, err)
	}
	base := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	const total = 600 // more than two fetches of auditReplayBatch
	for i := 0; i < total; i++ {
		if err := p.Publish(ctx, audit.Event{ID: natsbus.NewID(), AuditType: audit.AuditTypeRBACGrantCreated,
			Detail: fmt.Sprint(i), Timestamp: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	// Messages that Replay must not return: another subject of the stream, and
	// two messages on the audit subject that are not audit events.
	nc, err := nats.Connect(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	for subject, body := range map[string]string{
		"catalyst.tenant.created": `{"id":"x","type":"tenant.created"}`,
	} {
		if _, err := js.Publish(ctx, subject, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`{"id":"legacy","type":"audit"}`, `{"specversion":"1.0","id":"01X","data":{"detail":"no audit type"}}`} {
		if _, err := js.Publish(ctx, AuditSubject, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Publish(ctx, audit.Event{ID: natsbus.NewID(), AuditType: audit.AuditTypeRBACGrantDeleted, Detail: "last",
		Timestamp: base.Add(total * time.Second)}); err != nil {
		t.Fatal(err)
	}

	got, err := p.Replay(ctx, 300)
	if err != nil {
		t.Fatal(err)
	}
	// The newest 300 messages of the subject are 297 numbered events, the two
	// that are skipped and the last one.
	if len(got) != 298 {
		t.Fatalf("replay returned %d events, want 298", len(got))
	}
	for i, ev := range got[:297] {
		if want := fmt.Sprint(total - 297 + i); ev.Detail != want {
			t.Fatalf("event %d has detail %q, want %q: the order or the tail is wrong", i, ev.Detail, want)
		}
	}
	if got[297].Detail != "last" {
		t.Errorf("the newest event is %+v", got[297])
	}
	all, err := p.Replay(ctx, 5000)
	if err != nil || len(all) != total+1 {
		t.Fatalf("replay with a limit above the count: %d events, %v", len(all), err)
	}
	if none, err := p.Replay(ctx, 0); err != nil || len(none) != 0 {
		t.Errorf("replay with limit 0: %v, %v", none, err)
	}
	// A Bus smaller than the replay keeps the newest events.
	small := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	small.Load(all)
	if l := small.List("", nil, 10); len(l) != 5 || l[0].Detail != "last" || l[4].Detail != fmt.Sprint(total-4) {
		t.Errorf("a ring of 5 holds %+v", l)
	}
}

func TestAuditEventPublishedTwiceIsStoredOnce(t *testing.T) {
	srv := startNATS(t, true)
	p := newTestAuditPublisher(t, srv.URL)
	ev := audit.Event{ID: natsbus.NewID(), AuditType: audit.AuditTypeRBACGrantCreated, Timestamp: time.Now().UTC()}
	for i := 0; i < 2; i++ {
		if err := p.Publish(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if ids, _ := rawMessages(t, srv.URL, AuditSubject); len(ids) != 1 || ids[0] != ev.ID {
		t.Errorf("the stream holds %v, want the one id %s", ids, ev.ID)
	}
}

func TestAuditPublisherCreatesNoStream(t *testing.T) {
	srv := startNATS(t, false) // JetStream without the stream
	p := newTestAuditPublisher(t, srv.URL)
	bus := audit.NewBus(audit.BusConfig{Publisher: p})
	if err := bus.PublishStrict(context.Background(), audit.Event{AuditType: "break-glass"}); err == nil {
		t.Error("PublishStrict succeeded without the stream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := p.Replay(ctx, 10); err == nil {
		t.Error("Replay succeeded without the stream")
	}
	nc, err := nats.Connect(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Stream(ctx, natsbus.StreamCatalystOrg); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Errorf("the stream exists after Publish and Replay: %v", err)
	}
}

func TestNewAuditBusWithoutAPublisherIsInProcess(t *testing.T) {
	bus := NewAuditBus(nil, 7, nil)
	if bus.Transport() != audit.TransportInProcess {
		t.Errorf("transport %q, want %q", bus.Transport(), audit.TransportInProcess)
	}
	if err := bus.PublishStrict(context.Background(), audit.Event{AuditType: "break-glass"}); !errors.Is(err, audit.ErrNoPublisher) {
		t.Errorf("PublishStrict returned %v, want ErrNoPublisher", err)
	}
	bus.Publish(context.Background(), audit.Event{AuditType: audit.AuditTypeRBACGrantCreated})
	if got := bus.List("", nil, 10); len(got) != 1 {
		t.Errorf("the ring holds %+v", got)
	}
}

// A replay that fails leaves a Bus that still records.
func TestNewAuditBusStartsEmptyWhenTheReplayFails(t *testing.T) {
	srv := startNATS(t, false) // no stream yet
	var logged bytes.Buffer
	bus := NewAuditBus(newTestAuditPublisher(t, srv.URL), 0, slog.New(slog.NewJSONHandler(&logged, nil)))
	if bus.Transport() != audit.TransportJetStream || len(bus.List("", nil, 10)) != 0 {
		t.Errorf("transport %q, ring %+v", bus.Transport(), bus.List("", nil, 10))
	}
	if !strings.Contains(logged.String(), "replay from the stream failed") {
		t.Errorf("the failed replay is not logged: %s", logged.String())
	}
}

func TestNewAuditPublisherRefusesWhatItCannotUse(t *testing.T) {
	if _, err := NewAuditPublisher("", testSource, nil); err == nil {
		t.Error("an empty URL was accepted")
	}
	if _, err := NewAuditPublisher("nats://127.0.0.1:4222", "", nil); err == nil {
		t.Error("an empty source was accepted")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "nats://" + l.Addr().String()
	_ = l.Close()
	if _, err := NewAuditPublisher(closed, testSource, nil); err == nil {
		t.Error("a server that does not answer was accepted")
	}
	var none *AuditPublisher
	if err := none.Publish(context.Background(), audit.Event{ID: "x"}); err == nil {
		t.Error("a nil publisher published")
	}
	if _, err := none.Replay(context.Background(), 1); err == nil {
		t.Error("a nil publisher replayed")
	}
	none.Close()
	srv := startNATS(t, true)
	p := newTestAuditPublisher(t, srv.URL)
	if err := p.Publish(context.Background(), audit.Event{AuditType: "x"}); err == nil || !strings.Contains(err.Error(), "needs an id") {
		t.Errorf("an event without an id: %v", err)
	}
}
