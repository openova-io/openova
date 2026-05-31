// exec_stream_wire.go — G95.1 (Refs #2642) production wiring of the
// catalyst-api → kube-apiserver `pods/exec` SPDY bridge.
//
// Pre-G95.1 `Handler.SetExecStreamFactory` was only called from tests
// (internal/handler/k8s_exec_ws_test.go). In production main.go the
// factory stayed nil; the `/api/v1/sovereigns/{id}/k8s/exec/{ns}/
// {pod}/{container}` handler short-circuited with HTTP 503 "exec
// stream not wired" BEFORE the WebSocket upgrade ever happened. The
// browser's xterm.js client then opened on a dead WebSocket and
// rendered an empty terminal forever — the symptom founder reported
// on issue #2642: "WebSocket opens but renders empty terminal".
//
// This file plugs the gap: `newExecStreamFactory` returns the
// `handler.ExecStreamFactory` closure the handler layer expects. Each
// call resolves the Sovereign cluster's *rest.Config via the
// k8scache.Factory accessor introduced in the same PR, builds a
// `remotecommand.NewSPDYExecutor` against the apiserver's
// /api/v1/namespaces/{ns}/pods/{pod}/exec subresource, and wires the
// SPDY duplex stream into a single `io.ReadWriteCloser` the handler
// pumps bytes through.
//
// Wire-format note: the catalyst-api browser WebSocket is RAW BYTES
// in both directions — `k8s_exec_ws.go` does NOT do channel framing
// (it pumps bytes 1:1 to xterm.js). The channel framing happens INSIDE
// `remotecommand.NewSPDYExecutor`: the executor's stdin reader
// receives the bytes we write and forwards them as the v4 stdin
// channel (channel 0) to the apiserver; the executor's stdout writer
// receives v4 stdout channel (channel 1) bytes from the apiserver and
// hands them to us. Stderr (channel 2) is multiplexed into the same
// stdout pipe so xterm.js renders both streams as a single terminal —
// the same pattern the chepherd reference implementation uses (see
// chepherd/internal/runtime/pod_runner.go execStream).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"

	"k8s.io/client-go/tools/remotecommand"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// newExecStreamFactory returns a handler.ExecStreamFactory closure
// bound to the live k8scache.Factory. The closure runs synchronously
// on the request goroutine: it resolves the cluster's rest.Config,
// builds the SPDY executor URL, dials, and returns a streamPipe that
// bridges the executor's stdin/stdout to the handler's pump loop.
//
// A nil-tolerant logger lets the helper be wired regardless of
// whether the catalyst-api was started with full logging or a stub
// during unit tests.
func newExecStreamFactory(factory *k8scache.Factory, log *slog.Logger) handler.ExecStreamFactory {
	return func(ctx context.Context, ns, pod, container string, command []string) (io.ReadWriteCloser, error) {
		// The chi URL param resolver in HandleK8sExecWebSocket already
		// invoked resolveChrootClusterID; the factory is keyed by the
		// resolved ID. We re-resolve from the request context here so
		// the closure is safe to call from tests with a fake context.
		clusterID := handler.ClusterIDFromExecContext(ctx)
		if clusterID == "" {
			return nil, fmt.Errorf("exec stream: cluster id missing from request context")
		}

		restCfg, err := factory.RestConfigFor(clusterID)
		if err != nil {
			return nil, fmt.Errorf("exec stream: %w", err)
		}

		// Build the exec subresource URL the apiserver expects. Same
		// query shape chepherd uses (stdin/stdout/stderr/tty=true +
		// container + command[]) — these MUST match what the
		// apiserver's /pods/exec endpoint validates.
		q := url.Values{}
		q.Set("stdin", "true")
		q.Set("stdout", "true")
		q.Set("stderr", "true")
		q.Set("tty", "true")
		q.Set("container", container)
		for _, c := range command {
			q.Add("command", c)
		}
		execURL := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s/exec?%s",
			restCfg.Host, ns, pod, q.Encode())

		execu, err := remotecommand.NewSPDYExecutor(restCfg, "POST", mustParseURL(execURL))
		if err != nil {
			return nil, fmt.Errorf("exec stream: SPDY executor: %w", err)
		}

		// streamPipe is the bridge: handler pumps to/from this single
		// io.ReadWriteCloser; the goroutine below runs
		// executor.StreamWithContext which blocks until the exec
		// session ends. The pipes are sized 0 (synchronous) so a slow
		// xterm.js client back-pressures the apiserver-side read loop
		// naturally — no unbounded buffering in either direction.
		stdinR, stdinW := io.Pipe()
		stdoutR, stdoutW := io.Pipe()
		sp := &streamPipe{
			stdinW:  stdinW,
			stdoutR: stdoutR,
			done:    make(chan struct{}),
		}

		go func() {
			defer close(sp.done)
			defer stdoutW.Close()
			defer stdinR.Close()
			opts := remotecommand.StreamOptions{
				Stdin:  stdinR,
				Stdout: stdoutW,
				// Stderr multiplexed into stdout pipe — xterm.js
				// renders both streams in the same terminal (chepherd
				// pattern). The alternative — a second pipe — would
				// require the handler to merge two readers, doubling
				// the goroutine count for zero observable benefit.
				Stderr: stdoutW,
				Tty:    true,
			}
			if err := execu.StreamWithContext(ctx, opts); err != nil && log != nil {
				log.Info("k8s exec: SPDY stream ended",
					"cluster", clusterID,
					"ns", ns,
					"pod", pod,
					"container", container,
					"err", err,
				)
			}
		}()

		return sp, nil
	}
}

// streamPipe satisfies io.ReadWriteCloser by stitching the SPDY
// executor's stdin (we Write into stdinW which the executor reads
// from) and stdout (we Read from stdoutR which the executor writes
// to). Close shuts both halves so the SPDY goroutine unblocks.
type streamPipe struct {
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	done    chan struct{}
}

func (s *streamPipe) Read(p []byte) (int, error)  { return s.stdoutR.Read(p) }
func (s *streamPipe) Write(p []byte) (int, error) { return s.stdinW.Write(p) }
func (s *streamPipe) Close() error {
	_ = s.stdinW.Close()
	_ = s.stdoutR.Close()
	select {
	case <-s.done:
	default:
	}
	return nil
}

// mustParseURL panics on parse error. The exec URL is built from
// rest.Config.Host (validated at kubeconfig parse) + escaped query
// values, so parse failure indicates programmer error in this file,
// not an operator-input error.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("exec stream: invalid url %q: %v", s, err))
	}
	return u
}
