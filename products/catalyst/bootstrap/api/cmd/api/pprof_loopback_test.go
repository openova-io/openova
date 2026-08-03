// pprof_loopback_test.go — #5642 guards for the always-on diagnostics
// listener.
//
// The two properties that matter are opposites, so both are asserted
// (a one-directional test here would pass on a listener that binds
// nothing at all, and equally on one that binds every interface):
//
//  1. A loopback address DOES serve a usable heap profile. If this
//     regresses, the next OOM cycle is undiagnosable again — which is
//     the whole defect #5642 exists to close.
//  2. A non-loopback address is REFUSED and binds nothing. If this
//     regresses, goroutine stacks / heap contents / the process command
//     line become readable by every Pod on the cluster network.
package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func quietPprofLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPprofLoopback_DefaultAddrIsLoopback pins the shipped default. A
// change to ":6060" or "0.0.0.0:6060" fails here rather than silently
// widening exposure on the next deploy.
func TestPprofLoopback_DefaultAddrIsLoopback(t *testing.T) {
	if err := requireLoopback(defaultPprofLoopbackAddr); err != nil {
		t.Fatalf("defaultPprofLoopbackAddr %q must be loopback: %v", defaultPprofLoopbackAddr, err)
	}
	host, _, err := net.SplitHostPort(defaultPprofLoopbackAddr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", defaultPprofLoopbackAddr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("defaultPprofLoopbackAddr host %q is not a literal loopback IP", host)
	}
}

// TestPprofLoopback_RejectsNonLoopback is the negative half. Each input
// is a real way this could be widened by accident.
func TestPprofLoopback_RejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{
		":6060",          // every interface — the classic mistake
		"0.0.0.0:6060",   // every interface, explicit
		"[::]:6060",      // every interface, v6
		"10.42.2.7:6060", // this Pod's routable IP
		"localhost:6060", // resolvable name, not a literal IP
	} {
		t.Run(addr, func(t *testing.T) {
			if err := requireLoopback(addr); err == nil {
				t.Fatalf("requireLoopback(%q) = nil; want rejection", addr)
			}
			ln, err := startLoopbackPprof(quietPprofLogger(), addr)
			if err == nil {
				if ln != nil {
					_ = ln.Close()
				}
				t.Fatalf("startLoopbackPprof(%q) started a listener; want refusal", addr)
			}
			if ln != nil {
				_ = ln.Close()
				t.Fatalf("startLoopbackPprof(%q) returned a listener alongside its error", addr)
			}
			if !errors.Is(err, errPprofNotLoopback) && !strings.Contains(err.Error(), "loopback") {
				t.Fatalf("startLoopbackPprof(%q) err = %v; want a loopback refusal", addr, err)
			}
		})
	}
}

// TestPprofLoopback_EmptyAddrDisables — the documented off switch.
func TestPprofLoopback_EmptyAddrDisables(t *testing.T) {
	ln, err := startLoopbackPprof(quietPprofLogger(), "   ")
	if err != nil {
		t.Fatalf("empty addr should disable cleanly, got err: %v", err)
	}
	if ln != nil {
		_ = ln.Close()
		t.Fatal("empty addr started a listener; want none")
	}
}

// TestPprofLoopback_ServesHeapProfile is the positive half: the
// listener must actually hand back a heap profile, not merely accept a
// connection. Binds :0 on loopback so the test never collides with a
// developer's own pprof server.
func TestPprofLoopback_ServesHeapProfile(t *testing.T) {
	ln, err := startLoopbackPprof(quietPprofLogger(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startLoopbackPprof: %v", err)
	}
	if ln == nil {
		t.Fatal("startLoopbackPprof returned no listener")
	}
	defer ln.Close()

	client := &http.Client{Timeout: 20 * time.Second}
	base := "http://" + ln.Addr().String()

	resp, err := client.Get(base + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatalf("GET heap: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /debug/pprof/heap status = %d; want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read heap body: %v", err)
	}
	// debug=1 renders the textual profile; its header is the marker that
	// this is a real profile rather than an empty 200.
	if !strings.Contains(string(body), "heap profile:") {
		t.Fatalf("heap response is not a profile (len=%d, head=%q)", len(body), string(body[:min(120, len(body))]))
	}

	idx, err := client.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET index: %v", err)
	}
	defer idx.Body.Close()
	if idx.StatusCode != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ status = %d; want 200", idx.StatusCode)
	}
}

// TestPprofLoopback_NotOnPublicMux — the diagnostics handlers must live
// on their own mux. A request the public router would forward (any path
// that is not /debug/pprof/*) must 404 here, proving the mux carries
// nothing but the profiler.
func TestPprofLoopback_NotOnPublicMux(t *testing.T) {
	ln, err := startLoopbackPprof(quietPprofLogger(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startLoopbackPprof: %v", err)
	}
	defer ln.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	for _, path := range []string{"/healthz", "/metrics", "/api/v1/deployments"} {
		resp, err := client.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code != http.StatusNotFound {
			t.Fatalf("diagnostics mux served %s with %d; it must carry pprof ONLY", path, code)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
