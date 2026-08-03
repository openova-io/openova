// pprof_loopback.go — always-on, loopback-ONLY pprof listener (#5642).
//
// Why this exists, and why the pre-existing CATALYST_PPROF_ENABLED flag
// (#5352, main.go) is not enough:
//
// catalyst-api OOMKills on a ~60-minute metronome on hw292 (dep
// 1c56518035a83e03) — RSS climbs from ~200Mi at start to the 4Gi limit,
// exit 137, restart, repeat. The 2026-08-03 investigation could rule out
// goroutine growth, file-descriptor growth, Prometheus label-cardinality
// growth, informer Indexer growth and SSE-subscriber growth from
// /metrics alone, but could NOT name the retaining allocation — because
// there is no way to read a heap profile out of the leaking process.
//
// CATALYST_PPROF_ENABLED mounts chi's profiler onto the PUBLIC router,
// and it is read at process start. Turning it on therefore means
// `kubectl set env` / a values override → a new Pod → the leaked heap
// that was about to be profiled is destroyed by the very act of
// arranging to profile it. The flag is unusable for the defect class it
// was added for. Every future occurrence of this class inherits the same
// dead end unless the profile surface is present *before* the process
// starts leaking.
//
// The listener below is therefore ALWAYS ON and bound to the loopback
// interface only:
//
//   - It is a SEPARATE http.ServeMux on its own net.Listener. It is not
//     reachable through the public chi router, so no request that
//     arrives on :8080 can ever route to it.
//   - It binds 127.0.0.1 (guarded — see requireLoopback). It is not
//     reachable from another Pod, from the Service, from the gateway, or
//     from the network. No Service port, no ingress, no NodePort is
//     added or required.
//   - The only way in is `kubectl port-forward`, which attaches to the
//     Pod's own network namespace and dials 127.0.0.1 from inside it.
//     That already requires pods/portforward RBAC in catalyst-system —
//     a caller who holds it can equally exec into the Pod, so this
//     grants no capability that did not already exist.
//
// Capturing a profile from a live, leaking Pod is then one command, with
// no restart and no state loss:
//
//	kubectl -n catalyst-system port-forward pod/<catalyst-api-pod> 6060:6060
//	go tool pprof -top -sample_index=inuse_space http://127.0.0.1:6060/debug/pprof/heap
//
// CATALYST_PPROF_LOOPBACK_ADDR overrides the bind address; setting it to
// the empty string disables the listener entirely. A non-loopback value
// is REFUSED rather than honoured — see requireLoopback.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// defaultPprofLoopbackAddr — loopback-only bind address for the
// diagnostics listener. 6060 is the Go ecosystem's conventional pprof
// port; nothing else in the container binds it.
const defaultPprofLoopbackAddr = "127.0.0.1:6060"

// errPprofNotLoopback is returned when the configured bind address does
// not resolve to a loopback IP. Deliberately fatal-to-the-listener
// rather than best-effort: a diagnostics surface that silently binds
// 0.0.0.0 because someone typed ":6060" would expose process internals
// (goroutine stacks, heap contents, command line) to every Pod in the
// cluster. Refusing keeps the blast radius at "no profiler" instead of
// "profiler on the pod network".
var errPprofNotLoopback = errors.New("pprof loopback listener: bind address is not a loopback address")

// pprofMux builds the diagnostics mux. Only the pprof handlers are
// registered — no catalyst routes, no metrics, no auth surface.
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// requireLoopback verifies that addr's host half is a loopback address.
//
// An empty host ("::6060" / ":6060") is REJECTED: net.Listen treats it
// as "all interfaces", which is precisely the exposure this listener
// must never have. A hostname is rejected too — resolution can change
// underneath the process, so only literal loopback IPs are accepted.
func requireLoopback(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("pprof loopback listener: parse %q: %w", addr, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("pprof loopback listener: %q has no port", addr)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("%w: %q binds every interface", errPprofNotLoopback, addr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not a literal IP", errPprofNotLoopback, addr)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q", errPprofNotLoopback, addr)
	}
	return nil
}

// startLoopbackPprof binds the diagnostics listener and serves it in the
// background. Returns the bound address so callers (and tests) can dial
// it; returns a nil listener with a nil error when addr is empty
// (explicitly disabled).
//
// Errors are returned, not fatal: catalyst-api must keep serving even if
// port 6060 is somehow taken. main() logs and continues.
func startLoopbackPprof(log *slog.Logger, addr string) (net.Listener, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof loopback listener: listen %q: %w", addr, err)
	}
	srv := &http.Server{
		Handler: pprofMux(),
		// A CPU profile / trace request is long-lived by design
		// (?seconds=30 is the common default), so no write timeout.
		// The read timeout only bounds header reads.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Warn("pprof loopback listener stopped", "addr", ln.Addr().String(), "err", serveErr)
		}
	}()
	return ln, nil
}
