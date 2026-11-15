// Package retry decides which failed reads of the suite are worth another
// try, and makes them with a bounded exponential backoff (#7922).
//
// The discovery of a run (suite/discover.go, suite/provision.go) reads the
// Sovereign over two kinds of path: the console API and the Kubernetes API of
// each region. A read fails in one of three ways (Classify):
//
//   - Unanswered: no answer reached the suite. The connection was refused,
//     reset or closed, a TLS handshake or a read timed out, an exec stream
//     broke, the answer ended early (EOF). The path between the runner and the
//     Sovereign may have failed, not the Sovereign. It is retried. When the
//     retries are used up the read returns an *Unreachable: the target could
//     not be reached, so nothing was measured.
//   - Answered: the server answered with an error that means "try again": HTTP
//     429 or 5xx, kubectl's "Error from server" with the reason
//     ServiceUnavailable, InternalError, Timeout, ServerTimeout or
//     TooManyRequests, an etcd timeout, or an API server that could not dial a
//     node's kubelet. It is retried. When the retries are used up the last
//     answer stands: the server was reached and said that it failed, and that
//     is a measurement.
//   - Final: every other answer, such as 403, 404, a kind the cluster does not
//     serve, a command in a container that exited non-zero, a body that does
//     not decode, or a check's own verdict. It is never retried.
//
// The bound (Default): at most 5 attempts, with waits of 2, 4, 8 and 16
// seconds between them, and no attempt starts later than 2 minutes after the
// first. A Policy that gave up on an unanswered read makes one attempt only
// for every later read until one succeeds, so a target that is down costs
// one bound in a run, not one per read.
package retry

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Kind is how a failed read failed.
type Kind int

const (
	// Final is an answer that another try does not change. Never retried.
	Final Kind = iota
	// Answered is an answer of the server that means "try again".
	Answered
	// Unanswered is a read that got no answer.
	Unanswered
)

// The texts of kubectl, client-go and net/http that Classify looks for.
var (
	// The command in the container ran and exited non-zero: its verdict.
	ranInContainer = "command terminated with exit code"
	// The server answered and said "try again".
	tryAgain = []string{
		"Error from server (ServiceUnavailable)",
		"Error from server (InternalError)",
		"Error from server (Timeout)",
		"Error from server (ServerTimeout)",
		"Error from server (TooManyRequests)",
		"the server is currently unable to handle the request",
		"the server was unable to return a response in the time allotted",
		"the server has received too many requests",
		"etcdserver: request timed out",
		"etcdserver: leader changed",
		"error dialing backend",
	}
	// Any other answer of the server.
	answered = []string{"Error from server", "etcdserver:"}
	// No answer reached the suite.
	noAnswer = []string{
		"TLS handshake timeout",
		"i/o timeout",
		"connection reset by peer",
		"connection refused",
		"broken pipe",
		"use of closed network connection",
		"unexpected EOF",
		"no route to host",
		"network is unreachable",
		"host is down",
		"connection timed out",
		"error reading from error stream",
		"error sending request",
		"http2: client connection lost",
		"http2: server sent GOAWAY",
		"stream error:",
		"websocket: close",
		"Client.Timeout exceeded",
		"server misbehaving",
		"Temporary failure in name resolution",
	}
	eof = regexp.MustCompile(`\bEOF\b`)
)

// Classify says how a read failed from the text it printed or returned: the
// standard error of kubectl, or the error of an HTTP request. A text that names
// no known cause is Final, so that an unknown failure is never retried.
func Classify(text string) Kind {
	switch {
	case strings.Contains(text, ranInContainer):
		return Final
	case containsAny(text, tryAgain):
		return Answered
	case containsAny(text, answered):
		return Final
	case containsAny(text, noAnswer) || eof.MatchString(text):
		return Unanswered
	}
	return Final
}

func containsAny(text string, parts []string) bool {
	for _, p := range parts {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// Policy is the retry rule of one target: a region or the console.
type Policy struct {
	Attempts  int           // the most attempts of one read
	FirstWait time.Duration // the wait after the first attempt; it doubles after each further one
	Budget    time.Duration // no attempt starts later than this after the first attempt
	// Sleep waits d or until ctx ends; nil is a real wait. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock; nil is time.Now. Tests replace it.
	Now func() time.Time

	down atomic.Bool // a read gave up unanswered, and none succeeded since
}

// Default is the bound of the package comment: 5 attempts, waits of 2, 4, 8
// and 16 seconds, no attempt later than 2 minutes after the first.
func Default() *Policy {
	return &Policy{Attempts: 5, FirstWait: 2 * time.Second, Budget: 2 * time.Minute}
}

// Unreachable is the error of a read that got no answer in any attempt.
type Unreachable struct {
	Attempts int
	Elapsed  time.Duration
	Err      error // the error of the last attempt
}

func (u *Unreachable) Error() string {
	return fmt.Sprintf("%v (no answer after %d attempt(s) in %s)", u.Err, u.Attempts, u.Elapsed.Round(time.Second))
}

func (u *Unreachable) Unwrap() error { return u.Err }

// Do calls attempt until it succeeds or fails Final, the attempts are used up,
// the budget would be passed by the next wait, or ctx ends. It returns nil on
// success and else the error of the last attempt: as an *Unreachable when that
// attempt got no answer, with the number of attempts added when the server
// answered "try again" more than once. attempt returns how it failed, and nil
// when it succeeded. A nil Policy makes one attempt.
func (p *Policy) Do(ctx context.Context, attempt func() (Kind, error)) error {
	if p == nil {
		_, err := attempt()
		return err
	}
	start, wait, most := p.now(), p.FirstWait, p.Attempts
	if p.down.Load() {
		most = 1
	}
	for n := 1; ; n++ {
		kind, err := attempt()
		switch {
		case err == nil:
			p.down.Store(false)
			return nil
		case kind == Final:
			return err
		}
		if n < most && ctx.Err() == nil && p.now().Sub(start)+wait <= p.Budget && p.sleep(ctx, wait) == nil {
			wait *= 2
			continue
		}
		elapsed := p.now().Sub(start)
		switch {
		case kind == Unanswered:
			p.down.Store(true)
			return &Unreachable{Attempts: n, Elapsed: elapsed, Err: err}
		case n > 1:
			return fmt.Errorf("%w (still failing after %d attempts in %s)", err, n, elapsed.Round(time.Second))
		}
		return err
	}
}

func (p *Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Policy) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// OnlyUnreachable says whether err failed only because a target could not be
// reached: err is an *Unreachable, or wraps only errors of which each failed
// only so. An error that also holds an answer, such as a step that read one
// value and could not reach the other, is not.
func OnlyUnreachable(err error) bool {
	switch e := err.(type) {
	case nil:
		return false
	case *Unreachable:
		return true
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		for _, c := range children {
			if !OnlyUnreachable(c) {
				return false
			}
		}
		return len(children) > 0
	case interface{ Unwrap() error }:
		return OnlyUnreachable(e.Unwrap())
	}
	return false
}
