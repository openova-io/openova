package suite

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// peerNode is a fixture node with a role label and its addresses.
func peerNode(name, role string, addresses ...string) obj {
	list := []obj{}
	for i := 0; i+1 < len(addresses); i += 2 {
		list = append(list, obj{"type": addresses[i], "address": addresses[i+1]})
	}
	labels := obj{}
	if role != "" {
		labels[peerPortsRoleLabel] = role
	}
	return obj{"metadata": obj{"name": name, "labels": labels}, "status": obj{"addresses": list}}
}

func init() {
	nodesA := kubeList(
		peerNode("a-cp1", "control-plane", "InternalIP", "10.20.0.5", "ExternalIP", "203.0.113.1"),
		peerNode("a-w1", "", "InternalIP", "10.20.0.6", "ExternalIP", "198.51.100.9"),
	)
	nodesB := kubeList(
		peerNode("b-cp1", "control-plane", "InternalIP", "10.30.0.5", "ExternalIP", "203.0.113.2"),
		peerNode("b-cp2", "control-plane", "InternalIP", "10.30.0.7", "ExternalIP", "203.0.113.2"),
	)
	fakeAnswers["peer-ports"] = map[string]string{"a nodes": nodesA, "b nodes": nodesB}
	fakeAnswers["peer-ports-no-address"] = map[string]string{
		"a nodes": nodesA,
		"b nodes": kubeList(peerNode("b-cp1", "control-plane", "InternalIP", "10.30.0.5")),
	}
	fakeAnswers["peer-ports-region-b-unreachable"] = map[string]string{"a nodes": nodesA}
}

// timeoutError is a dial error that timed out.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// fakePeerDial answers 6443 as open and every other port as a time-out,
// except the addresses named in override. It records what was dialled.
func fakePeerDial(t *testing.T, override map[string]error) *[]string {
	t.Helper()
	dialled := []string{}
	saved := peerPortsDial
	t.Cleanup(func() { peerPortsDial = saved })
	peerPortsDial = func(_ context.Context, address string) error {
		dialled = append(dialled, address)
		if err, ok := override[address]; ok {
			return err
		}
		if strings.HasSuffix(address, ":6443") {
			return nil
		}
		return &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}
	}
	return &dialled
}

func d010(status, reason string) *Step {
	return &Step{Journey: "D0", ID: "D0.10", Status: status, Error: reason}
}

func TestPeerPortsCheck(t *testing.T) {
	if _, ok := registry[peerPortsID]; !ok || peerPortsID != "NET-G01" {
		t.Fatalf("check %s is not registered", peerPortsID)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errRefused()}
	for name, c := range map[string]struct {
		scenario string
		before   []*Step
		override map[string]error
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"every peer port times out":      {"peer-ports", []*Step{d010(Pass, "")}, nil, ""},
		"the mesh endpoint is open":      {"peer-ports", []*Step{d010(Pass, "")}, map[string]error{"203.0.113.2:12379": nil}, "203.0.113.2:12379 is open"},
		"etcd refuses":                   {"peer-ports", []*Step{d010(Pass, "")}, map[string]error{"203.0.113.1:2379": refused}, "203.0.113.1:2379 is refused"},
		"the control port times out":     {"peer-ports", []*Step{d010(Pass, "")}, map[string]error{"203.0.113.1:6443": timeoutError{}}, "203.0.113.1:6443 is timeout, so a time-out proves nothing"},
		"a region has no public address": {"peer-ports-no-address", []*Step{d010(Pass, "")}, nil, "no control-plane node of region region-b has an ExternalIP address"},
		"a region cannot be read":        {"peer-ports-region-b-unreachable", []*Step{d010(Pass, "")}, nil, "[region-b] kubectl get nodes"},
		"the mesh step failed":           {"peer-ports", []*Step{d010(Fail, "2 agent(s) lack peer gateway identities")}, nil, "mesh step D0.10 is FAIL: 2 agent(s) lack peer gateway identities"},
		"the mesh step did not run":      {"peer-ports", nil, nil, "mesh step D0.10 was not run in this run"},
	} {
		fakePeerDial(t, c.override)
		got := runCheck(t, c.scenario, peerPortsCheck{}, &CheckContext{}, c.before...).run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// Only the control-plane addresses are dialled, each once per port; a
	// worker's address is not.
	dialled := fakePeerDial(t, nil)
	runCheck(t, "peer-ports", peerPortsCheck{}, &CheckContext{}, d010(Pass, ""))
	want := "203.0.113.1:6443 203.0.113.1:2379 203.0.113.1:12379 203.0.113.2:6443 203.0.113.2:2379 203.0.113.2:12379"
	if got := strings.Join(*dialled, " "); got != want {
		t.Errorf("dialled %q, want %q", got, want)
	}
}

func errRefused() error {
	// A port on which nothing listens: the kernel refuses the connection.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	_, err = net.Dial("tcp", address)
	if err == nil {
		panic("a closed port accepted a connection")
	}
	return err
}

func TestPeerPortsOutcome(t *testing.T) {
	// The real dialer: an accepted, a refused and a timed-out connection.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if got := peerPortsOutcome(peerPortsDial(context.Background(), l.Addr().String())); got != "open" {
		t.Errorf("a listening port: want open, got %s", got)
	}
	if got := peerPortsOutcome(errRefused()); got != "refused" {
		t.Errorf("a closed port: want refused, got %s", got)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := peerPortsOutcome(peerPortsDial(expired, l.Addr().String())); got != "timeout" {
		t.Errorf("a dial past its deadline: want timeout, got %s", got)
	}
	if got := peerPortsOutcome(&net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}); got != "timeout" {
		t.Errorf("a dial that timed out: want timeout, got %s", got)
	}
	if got := peerPortsOutcome(&net.AddrError{Err: "missing port in address", Addr: "x"}); !strings.HasPrefix(got, "error: ") {
		t.Errorf("another error: want error: ..., got %s", got)
	}
}
