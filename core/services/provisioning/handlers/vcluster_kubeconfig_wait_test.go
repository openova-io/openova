package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The funnel waits for the Organization's vCluster kubeconfig and writes it
// nowhere: the Organization's boundary tree produces both copies (the vCluster
// writes `<slug>/tenant-<slug>-kubeconfig`, bp-reflector auto-reflects it into
// flux-system). waitForKubeconfigHeld takes a GET function only, so the funnel
// has no path to write either Secret.

// fakeSecrets answers GET /api/v1/namespaces/<ns>/secrets/<name> from a map of
// "<ns>/<name>" to the Secret's JSON; an absent key is a 404.
type fakeSecrets struct {
	objs  map[string]string
	gets  map[string]int
	after map[string]func(n int) string // per key: the body for the n-th GET (1-based)
}

func (f *fakeSecrets) get(path string) ([]byte, error) {
	rest, ok := strings.CutPrefix(path, "/api/v1/namespaces/")
	if !ok {
		return nil, errors.New("unexpected path " + path)
	}
	ns, name, _ := strings.Cut(rest, "/secrets/")
	key := ns + "/" + name
	f.gets[key]++
	if fn, ok := f.after[key]; ok {
		if body := fn(f.gets[key]); body != "" {
			return []byte(body), nil
		}
		return nil, errors.New("k8s GET " + path + ": status 404")
	}
	if body, ok := f.objs[key]; ok {
		return []byte(body), nil
	}
	return nil, errors.New("k8s GET " + path + ": status 404")
}

const (
	heldKubeconfig = `{"data":{"config":"YXBpVmVyc2lvbjogdjE="}}`
	// The boundary tree's metadata-only stub before the kubeconfig is written.
	emptyStub = `{"metadata":{"annotations":{"reflector.v1.k8s.emberstack.com/reflects":"acme/tenant-acme-kubeconfig"}}}`
)

// The order measured on the 2026-11-21 gate run: the copy in flux-system
// exists before it holds the kubeconfig. The wait returns once it does.
func TestWaitForKubeconfigHeld_ReturnsWhenTheFluxSystemCopyIsFilledLate(t *testing.T) {
	f := &fakeSecrets{
		objs: map[string]string{"acme/tenant-acme-kubeconfig": heldKubeconfig},
		gets: map[string]int{},
		after: map[string]func(int) string{
			"flux-system/tenant-acme-kubeconfig": func(n int) string {
				switch {
				case n == 1:
					return "" // not created yet
				case n < 4:
					return emptyStub
				default:
					return heldKubeconfig
				}
			},
		},
	}
	if err := waitForKubeconfigHeld(context.Background(), f.get, "acme", time.Minute, time.Millisecond); err != nil {
		t.Fatalf("waitForKubeconfigHeld: %v", err)
	}
	if got := f.gets["flux-system/tenant-acme-kubeconfig"]; got != 4 {
		t.Fatalf("GETs of the flux-system copy = %d, want 4 (returns on the first read that holds the kubeconfig)", got)
	}
}

// A stub without the data key `config` is not a kubeconfig: the wait ends at
// its bound and names both namespaces.
func TestWaitForKubeconfigHeld_StubsWithoutDataAreNotAKubeconfig(t *testing.T) {
	f := &fakeSecrets{
		objs: map[string]string{
			"acme/tenant-acme-kubeconfig":        emptyStub,
			"flux-system/tenant-acme-kubeconfig": `{"data":{"value":"eA=="}}`,
		},
		gets: map[string]int{},
	}
	err := waitForKubeconfigHeld(context.Background(), f.get, "acme", 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("waitForKubeconfigHeld returned nil for two Secrets without the key config")
	}
	want := "secret tenant-acme-kubeconfig holds no kubeconfig in namespace acme, flux-system after 20ms"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestWaitForKubeconfigHeld_StopsWithTheContext(t *testing.T) {
	f := &fakeSecrets{objs: map[string]string{}, gets: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForKubeconfigHeld(ctx, f.get, "acme", time.Hour, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
