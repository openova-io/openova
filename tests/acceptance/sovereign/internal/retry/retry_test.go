package retry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The two errors of gate run 3 on hw307 (15 Nov 2026), as kubectl printed them
// from a runner in another cloud: step D0.10's exec stream and step D0.11's
// TLS handshake. The same API answered in 4 ms from the operator host.
const (
	run3ExecStream = `E1115 19:35:12.419799    4495 v2.go:167] "Unhandled Error" err="read message: %!w(<nil>)"
E1115 19:35:12.419833    4495 v2.go:150] "Unhandled Error" err="read message: %!w(<nil>)"
error: error reading from error stream: read message: %!w(<nil>)`
	run3Handshake = "Unable to connect to the server: net/http: TLS handshake timeout"
)

func TestClassify(t *testing.T) {
	for text, want := range map[string]Kind{
		run3ExecStream: Unanswered,
		run3Handshake:  Unanswered,
		"Unable to connect to the server: dial tcp 5.37.79.43:6443: i/o timeout":                                            Unanswered,
		"Unable to connect to the server: dial tcp 5.37.79.43:6443: connect: connection refused":                            Unanswered,
		"Unable to connect to the server: EOF":                                                                              Unanswered,
		`Get "https://console.x/api/v1/whoami": read tcp 10.0.0.1:5->10.0.0.2:443: read: connection reset by peer`:          Unanswered,
		`Get "https://console.x/api/v1/whoami": EOF`:                                                                        Unanswered,
		`Get "https://console.x/api/v1/whoami": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`: Unanswered,
		"http2: server sent GOAWAY and closed the connection; LastStreamID=1":                                               Unanswered,
		"websocket: close 1006 (abnormal closure): unexpected EOF":                                                          Unanswered,
		// The server answered "try again".
		"Error from server (ServiceUnavailable): the server is currently unable to handle the request": Answered,
		"Error from server (InternalError): an error on the server (\"\") has prevented the request":   Answered,
		"Error from server (TooManyRequests): the server has received too many requests":               Answered,
		"Error from server: error dialing backend: dial tcp 10.52.1.40:10250: i/o timeout":             Answered,
		"Error from server: etcdserver: request timed out":                                             Answered,
		// Real answers: never retried.
		"Error from server (NotFound): configmaps \"sovereign-variables\" not found":                 Final,
		"Error from server (Forbidden): pods is forbidden: User \"x\" cannot list resource \"pods\"": Final,
		"error: the server doesn't have a resource type \"sovereigns\"":                              Final,
		"error: You must be logged in to the server (Unauthorized)":                                  Final,
		"Unable to connect to the server: dial tcp: lookup api.x: no such host":                      Final,
		"Unable to connect to the server: x509: certificate signed by unknown authority":             Final,
		// The command in the container ran and failed, even if what it printed names a transport error.
		"Error: cannot get ipcache: dial unix /var/run/cilium/cilium.sock: connect: connection refused\ncommand terminated with exit code 1": Final,
		"": Final,
		"error: scenario discover cannot reach \"a\" for \"pods\"": Final,
		"signal: killed": Final,
	} {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%q) = %d, want %d", text, got, want)
		}
	}
}

// fakeClock records the waits of a Policy and moves its clock by each wait and
// by the time each attempt takes.
type fakeClock struct {
	now     time.Time
	perCall time.Duration
	waits   []time.Duration
}

func (c *fakeClock) policy() *Policy {
	p := Default()
	p.Now = func() time.Time { return c.now }
	p.Sleep = func(_ context.Context, d time.Duration) error {
		c.waits = append(c.waits, d)
		c.now = c.now.Add(d)
		return nil
	}
	return p
}

// attempts fails with text for the first failures calls, then succeeds. It
// counts the calls.
func (c *fakeClock) attempts(failures int, text string, calls *int) func() (Kind, error) {
	return func() (Kind, error) {
		*calls++
		c.now = c.now.Add(c.perCall)
		if failures < 0 || *calls <= failures {
			return Classify(text), errors.New(text)
		}
		return Final, nil
	}
}

func secs(n ...int) []time.Duration {
	out := []time.Duration{}
	for _, x := range n {
		out = append(out, time.Duration(x)*time.Second)
	}
	return out
}

func TestDoRetriesTransientFailuresThenSucceeds(t *testing.T) {
	c := &fakeClock{}
	calls := 0
	if err := c.policy().Do(context.Background(), c.attempts(2, run3Handshake, &calls)); err != nil || calls != 3 {
		t.Fatalf("two handshake timeouts then an answer: err=%v after %d calls, want nil after 3", err, calls)
	}
	if !slices.Equal(c.waits, secs(2, 4)) {
		t.Errorf("waits %v, want [2s 4s]", c.waits)
	}
}

func TestDoGivesUpUnreachableAfterTheBound(t *testing.T) {
	c := &fakeClock{}
	calls := 0
	p := c.policy()
	err := p.Do(context.Background(), c.attempts(-1, run3ExecStream, &calls))
	var u *Unreachable
	if !errors.As(err, &u) || calls != 5 || u.Attempts != 5 || !OnlyUnreachable(err) {
		t.Fatalf("a stream that keeps breaking: err=%v after %d calls, want *Unreachable after 5", err, calls)
	}
	if !slices.Equal(c.waits, secs(2, 4, 8, 16)) {
		t.Errorf("waits %v, want [2s 4s 8s 16s]", c.waits)
	}
	if !strings.Contains(err.Error(), "error reading from error stream") || !strings.Contains(err.Error(), "no answer after 5 attempt(s) in 30s") {
		t.Errorf("the error does not say what failed and how often: %v", err)
	}
	// The target is down now: the next read makes one attempt only.
	calls = 0
	if err := p.Do(context.Background(), c.attempts(-1, run3Handshake, &calls)); !OnlyUnreachable(err) || calls != 1 {
		t.Errorf("a read after the target gave up: err=%v after %d calls, want one attempt", err, calls)
	}
	// A read that succeeds brings the full bound back.
	calls = 0
	if err := p.Do(context.Background(), c.attempts(0, "", &calls)); err != nil || calls != 1 {
		t.Fatalf("a read that succeeds: %v after %d calls", err, calls)
	}
	calls = 0
	if err := p.Do(context.Background(), c.attempts(1, run3Handshake, &calls)); err != nil || calls != 2 {
		t.Errorf("after a success: err=%v after %d calls, want a retry", err, calls)
	}
}

func TestDoStopsAtTheBudget(t *testing.T) {
	// Each attempt takes 30 s, as a dial that times out does: after the fourth
	// attempt (134 s with the waits) a wait of 16 s would start a fifth past 2 minutes.
	c := &fakeClock{perCall: 30 * time.Second}
	calls := 0
	err := c.policy().Do(context.Background(), c.attempts(-1, "dial tcp 5.37.79.43:6443: i/o timeout", &calls))
	var u *Unreachable
	if !errors.As(err, &u) || calls != 4 || !slices.Equal(c.waits, secs(2, 4, 8)) {
		t.Fatalf("slow attempts: err=%v after %d calls, waits %v; want *Unreachable after 4 calls and waits [2s 4s 8s]", err, calls, c.waits)
	}
}

func TestDoNeverRetriesARealAnswer(t *testing.T) {
	c := &fakeClock{}
	calls := 0
	text := `Error from server (NotFound): configmaps "sovereign-variables" not found`
	err := c.policy().Do(context.Background(), c.attempts(-1, text, &calls))
	if err == nil || err.Error() != text || calls != 1 || len(c.waits) != 0 || OnlyUnreachable(err) {
		t.Fatalf("a NotFound: err=%v after %d calls and waits %v; want the answer itself after one call", err, calls, c.waits)
	}
}

func TestDoRetriesAnAnswerToTryAgainButItStands(t *testing.T) {
	c := &fakeClock{}
	calls := 0
	text := "Error from server (ServiceUnavailable): the server is currently unable to handle the request"
	err := c.policy().Do(context.Background(), c.attempts(-1, text, &calls))
	if err == nil || calls != 5 || OnlyUnreachable(err) || !strings.Contains(err.Error(), "still failing after 5 attempts") {
		t.Fatalf("a server that keeps answering 503: err=%v after %d calls; want its answer, not unreachable, after 5", err, calls)
	}
}

func TestDoStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p := Default()
	p.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	err := p.Do(ctx, func() (Kind, error) { calls++; return Unanswered, errors.New(run3Handshake) })
	if calls != 1 || !OnlyUnreachable(err) {
		t.Fatalf("a cancelled run: err=%v after %d calls, want one call", err, calls)
	}
}

func TestANilPolicyMakesOneAttempt(t *testing.T) {
	var p *Policy
	calls := 0
	err := p.Do(context.Background(), func() (Kind, error) { calls++; return Unanswered, errors.New(run3Handshake) })
	if calls != 1 || err == nil || OnlyUnreachable(err) {
		t.Fatalf("a nil policy: err=%v after %d calls, want the plain error after one call", err, calls)
	}
}

func TestOnlyUnreachable(t *testing.T) {
	down := &Unreachable{Attempts: 5, Err: errors.New(run3Handshake)}
	answer := errors.New("the bootstrap variables of region b have no PROVIDER")
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                        {nil, false},
		"unreachable":                {down, true},
		"wrapped once":               {fmt.Errorf("[a] cilium-x: ipcache: %w", down), true},
		"joined, every part":         {errors.Join(fmt.Errorf("provider: %w", down), fmt.Errorf("nodes_ready: %w", down)), true},
		"joined with an answer":      {errors.Join(fmt.Errorf("provider: %w", down), answer), false},
		"an answer":                  {answer, false},
		"an answer that only quotes": {errors.New(down.Error()), false},
		"an empty join":              {errors.Join(), false},
	} {
		if got := OnlyUnreachable(c.err); got != c.want {
			t.Errorf("%s: OnlyUnreachable = %v, want %v", name, got, c.want)
		}
	}
}
