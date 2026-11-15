package kube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/retry"
)

// The fake kubectl of these tests is the test binary itself, as in package
// suite. It appends one line to the file named in fakeCalls for each call,
// fails with the text of fakeStderr (exit code 1) for the first fakeFailures
// calls (-1: every call), and then prints an empty list.
const (
	fakeCalls    = "KUBE_FAKE_CALLS"
	fakeFailures = "KUBE_FAKE_FAILURES"
	fakeStderr   = "KUBE_FAKE_STDERR"
)

func TestMain(m *testing.M) {
	if calls := os.Getenv(fakeCalls); calls != "" && len(os.Args) > 1 && os.Args[1] == "--kubeconfig" {
		os.Exit(fake(calls))
	}
	os.Exit(m.Run())
}

func fake(calls string) int {
	f, err := os.OpenFile(filepath.Clean(calls), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	_, _ = f.WriteString("call\n")
	_ = f.Close()
	b, _ := os.ReadFile(filepath.Clean(calls))
	n := strings.Count(string(b), "\n")
	failures, _ := strconv.Atoi(os.Getenv(fakeFailures))
	if failures < 0 || n <= failures {
		fmt.Fprintln(os.Stderr, os.Getenv(fakeStderr))
		return 1
	}
	fmt.Println(`{"items":[]}`)
	return 0
}

// region is a region whose kubectl is the fake. It fails failures times with
// stderr; calls reports how often it was started.
func region(t *testing.T, policy *retry.Policy, failures int, stderr string) (Region, func() int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "calls")
	t.Setenv(fakeCalls, counter)
	t.Setenv(fakeFailures, strconv.Itoa(failures))
	t.Setenv(fakeStderr, stderr)
	return Region{ID: "region-a", Kubeconfig: "a", Kubectl: self, Retry: policy}, func() int {
		b, _ := os.ReadFile(filepath.Clean(counter))
		return strings.Count(string(b), "\n")
	}
}

// fast is the bound of the discovery without its waits.
func fast() *retry.Policy {
	p := retry.Default()
	p.Sleep = func(context.Context, time.Duration) error { return nil }
	return p
}

const handshake = "Unable to connect to the server: net/http: TLS handshake timeout"

func TestRunRetriesAHandshakeTimeoutThenReads(t *testing.T) {
	reg, calls := region(t, fast(), 2, handshake)
	items, err := reg.GetList(context.Background(), "configmaps", "flux-system")
	if err != nil || items == nil || calls() != 3 {
		t.Fatalf("two handshake timeouts, then a list: err=%v items=%v after %d calls, want the list after 3", err, items, calls())
	}
}

func TestRunGivesUpUnreachable(t *testing.T) {
	reg, calls := region(t, fast(), -1, "error: error reading from error stream: read message: %!w(<nil>)")
	_, err := reg.Exec(context.Background(), "kube-system", "cilium-crdb5", "cilium-agent", nil, "cilium-dbg", "bpf", "ipcache", "list")
	var u *retry.Unreachable
	if !errors.As(err, &u) || calls() != 5 || !retry.OnlyUnreachable(err) {
		t.Fatalf("a stream that keeps breaking: err=%v after %d calls, want unreachable after 5", err, calls())
	}
	if !strings.Contains(err.Error(), "[region-a] kubectl exec -n kube-system cilium-crdb5 -c cilium-agent -- <redacted>") {
		t.Errorf("the error does not name the call: %v", err)
	}
}

func TestRunNeverRetriesARealAnswer(t *testing.T) {
	reg, calls := region(t, fast(), -1, `Error from server (Forbidden): configmaps is forbidden: User "gate" cannot list resource "configmaps"`)
	_, err := reg.GetList(context.Background(), "configmaps", "flux-system")
	if err == nil || calls() != 1 || retry.OnlyUnreachable(err) || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("a Forbidden: err=%v after %d calls, want the answer after one call", err, calls())
	}
}

// Without a policy, as in every journey and check, a call is made once and its
// error is what it was before the policy existed.
func TestRunWithoutAPolicyMakesOneCall(t *testing.T) {
	reg, calls := region(t, nil, -1, handshake)
	_, err := reg.GetList(context.Background(), "configmaps", "flux-system")
	want := "[region-a] kubectl get configmaps -o json -n flux-system: exit status 1: " + handshake
	if err == nil || err.Error() != want || calls() != 1 || retry.OnlyUnreachable(err) {
		t.Fatalf("no policy: err=%v after %d calls, want %q after one call", err, calls(), want)
	}
}

// A call with standard input is never retried: the input is read once.
func TestRunWithStdinMakesOneCall(t *testing.T) {
	reg, calls := region(t, fast(), -1, handshake)
	_, err := reg.Exec(context.Background(), "ns", "pod", "c", strings.NewReader("SELECT 1;"), "psql")
	if err == nil || calls() != 1 {
		t.Fatalf("a call with stdin: err=%v after %d calls, want one call", err, calls())
	}
}
