package suite

import (
	"context"
	"io"
	"net"
	"reflect"
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

// peerClosed is what a dial from outside gives when every peer port of the
// fixture's control-plane addresses is closed.
func peerClosed() map[string]string {
	return map[string]string{"203.0.113.1:2379": "timeout", "203.0.113.1:12379": "timeout",
		"203.0.113.2:2379": "timeout", "203.0.113.2:12379": "timeout"}
}

func peerWith(target, outcome string) map[string]string {
	out := peerClosed()
	out[target] = outcome
	return out
}

func peerWithout(target string) map[string]string {
	out := peerClosed()
	delete(out, target)
	return out
}

func TestPeerPortsCheck(t *testing.T) {
	if _, ok := registry[peerPortsID]; !ok || peerPortsID != "NET-G01" {
		t.Fatalf("check %s is not registered", peerPortsID)
	}
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errRefused()}
	for name, c := range map[string]struct {
		scenario string
		before   []*Step
		outside  map[string]string
		override map[string]error // what the dial from here answers
		want     string           // "" = PASS, else a part of the reason of the FAIL
	}{
		"every peer port times out from outside": {"peer-ports", []*Step{d010(Pass, "")}, peerClosed(), nil, ""},
		"a worker's address is not judged":       {"peer-ports", []*Step{d010(Pass, "")}, peerWith("198.51.100.9:2379", "open"), nil, ""},
		"the mesh endpoint is open from outside": {"peer-ports", []*Step{d010(Pass, "")}, peerWith("203.0.113.2:12379", "open"), nil, "203.0.113.2:12379 is open from outside"},
		"etcd refuses from outside":              {"peer-ports", []*Step{d010(Pass, "")}, peerWith("203.0.113.1:2379", "refused"), nil, "203.0.113.1:2379 is refused from outside"},
		"the dial from outside went wrong":       {"peer-ports", []*Step{d010(Pass, "")}, peerWith("203.0.113.1:12379", "error"), nil, "203.0.113.1:12379 is error from outside"},
		"a port was not dialled from outside":    {"peer-ports", []*Step{d010(Pass, "")}, peerWithout("203.0.113.2:12379"), nil, "203.0.113.2:12379 was not dialled from outside"},
		"no port was dialled from outside":       {"peer-ports", []*Step{d010(Pass, "")}, map[string]string{}, nil, "203.0.113.1:2379 was not dialled from outside"},
		"the run did not say":                    {"peer-ports", []*Step{d010(Pass, "")}, nil, nil, "the run was not given the outcome of the dial from outside (--peer-ports-outside)"},
		"the control port times out from here":   {"peer-ports", []*Step{d010(Pass, "")}, peerClosed(), map[string]error{"203.0.113.1:6443": timeoutError{}}, "203.0.113.1:6443 is timeout from here, so a time-out from outside proves nothing"},
		// Gate run 44455908137 from the parent, which the firewall lets
		// through: 2379 and 12379 refused from there. That is no outside
		// dial, and the check does not judge it.
		"a refusal from here is not judged": {"peer-ports", []*Step{d010(Pass, "")}, peerClosed(), map[string]error{"203.0.113.1:2379": refused, "203.0.113.1:12379": refused}, ""},
		"a region has no public address":    {"peer-ports-no-address", []*Step{d010(Pass, "")}, peerClosed(), nil, "no control-plane node of region region-b has an ExternalIP address"},
		"a region cannot be read":           {"peer-ports-region-b-unreachable", []*Step{d010(Pass, "")}, peerClosed(), nil, "[region-b] kubectl get nodes"},
		"the mesh step failed":              {"peer-ports", []*Step{d010(Fail, "2 agent(s) lack peer gateway identities")}, peerClosed(), nil, "mesh step D0.10 is FAIL: 2 agent(s) lack peer gateway identities"},
		"the mesh step did not run":         {"peer-ports", nil, peerClosed(), nil, "mesh step D0.10 was not run in this run"},
	} {
		fakePeerDial(t, c.override)
		got := runCheck(t, c.scenario, peerPortsCheck{}, &CheckContext{Deploy: &DeploymentInfo{PeerPortsOutside: c.outside}}, c.before...).run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// A run without the facts of the deployment fails too.
	fakePeerDial(t, nil)
	if got := runCheck(t, "peer-ports", peerPortsCheck{}, &CheckContext{}, d010(Pass, "")).run.Checks[0]; got.Status != Fail ||
		!strings.Contains(got.Reason, "--peer-ports-outside") {
		t.Errorf("no deployment facts: want FAIL, got %+v", got)
	}
	// From here only port 6443 of the control-plane addresses is dialled,
	// once each: never 2379 or 12379, and never a worker's address. The
	// evidence holds both outcomes.
	dialled := fakePeerDial(t, nil)
	r := runCheck(t, "peer-ports", peerPortsCheck{}, &CheckContext{Deploy: &DeploymentInfo{PeerPortsOutside: peerClosed()}}, d010(Pass, ""))
	if got, want := strings.Join(*dialled, " "), "203.0.113.1:6443 203.0.113.2:6443"; got != want {
		t.Errorf("dialled %q, want %q", got, want)
	}
	var ev Evidence
	for _, s := range r.run.Steps {
		if s.ID == "NET-G01.3" {
			ev = s.Evidence
		}
	}
	if !reflect.DeepEqual(ev["from_here"], map[string]string{"203.0.113.2:6443": "open"}) ||
		!reflect.DeepEqual(ev["from_outside"], map[string]string{"203.0.113.2:2379": "timeout", "203.0.113.2:12379": "timeout"}) ||
		!reflect.DeepEqual(ev["addresses"], []string{"203.0.113.2"}) {
		t.Errorf("the evidence of region b is %v", ev)
	}
}

func TestParsePeerPortsOutside(t *testing.T) {
	if got, err := parsePeerPortsOutside("  "); got != nil || err != nil {
		t.Errorf("an empty value: want nil and no error, got %v %v", got, err)
	}
	got, err := parsePeerPortsOutside(" 203.0.113.1:2379=timeout, 203.0.113.1:12379 = open ,[2001:DB8::1]:2379=refused,192.0.2.7:12379=error")
	want := map[string]string{"203.0.113.1:2379": "timeout", "203.0.113.1:12379": "open", "[2001:db8::1]:2379": "refused", "192.0.2.7:12379": "error"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("a good value: got %v %v, want %v", got, err, want)
	}
	for _, bad := range []string{
		"203.0.113.1:2379",                                   // no outcome
		"203.0.113.1:2379=",                                  // an empty outcome
		"203.0.113.1:2379=closed",                            // an outcome outside the list
		"203.0.113.1=timeout",                                // no port
		"203.0.113.1:0=timeout",                              // port 0
		"203.0.113.1:65536=timeout",                          // a port above 65535
		"203.0.113.1:02379=timeout",                          // a port in another spelling
		"203.0.113.1:etcd=timeout",                           // a port by name
		"node-1:2379=timeout",                                // a name, not an address
		"2001:db8::1:2379=timeout",                           // IPv6 without brackets
		"=timeout",                                           // nothing
		"203.0.113.1:2379=timeout,203.0.113.1:2379=open",     // a pair named twice
		"[2001:db8::1]:2379=timeout,[2001:DB8::1]:2379=open", // the same pair in two spellings
		"203.0.113.1:2379=timeout;203.0.113.2:2379=timeout",  // another separator
	} {
		if got, err := parsePeerPortsOutside(bad); err == nil || got != nil || !strings.HasPrefix(err.Error(), "--peer-ports-outside: ") {
			t.Errorf("%q: want an error that starts with --peer-ports-outside: , got %v %v", bad, got, err)
		}
	}
}

// TestExecuteRefusesPeerPortsOutside: a value in another form stops the run
// before anything runs, as the other values of a run do.
func TestExecuteRefusesPeerPortsOutside(t *testing.T) {
	cfg := Config{Env: "x", Console: "http://127.0.0.1:9", Session: "x", KubeconfigA: "a", KubeconfigB: "b",
		RunIn: string(RunInLocal), RepoRoot: ".", PeerPortsOutside: "203.0.113.1:2379=closed"}
	if _, err := Execute(context.Background(), cfg, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "--peer-ports-outside: ") {
		t.Errorf("a value in another form: want the run refused, got %v", err)
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
