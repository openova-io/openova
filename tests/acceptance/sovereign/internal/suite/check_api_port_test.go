package suite

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The fixtures are those of check NET-G01 (check_peer_ports_test.go): region
// a has the control-plane address 203.0.113.1 and a worker with 198.51.100.9;
// region b has two control-plane nodes behind 203.0.113.2. fakePeerDial
// answers port 6443 as open from here.

func TestAPIPortCheck(t *testing.T) {
	if _, ok := registry[apiPortID]; !ok || apiPortID != "NET-G02" {
		t.Fatalf("check %s is not registered", apiPortID)
	}
	closed := map[string]string{"203.0.113.1": "timeout", "203.0.113.2": "timeout"}
	with := func(address, outcome string) map[string]string {
		out := map[string]string{address: outcome}
		for k, v := range closed {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
		return out
	}
	for name, c := range map[string]struct {
		scenario string
		outside  map[string]string
		override map[string]error // what the dial from here answers
		want     string           // "" = PASS, else a part of the reason of the FAIL
	}{
		"every address times out from outside": {"peer-ports", closed, nil, ""},
		"a worker's address is not judged":     {"peer-ports", with("198.51.100.9", "open"), nil, ""},
		"the port is open from outside":        {"peer-ports", with("203.0.113.2", "open"), nil, "203.0.113.2:6443 is open from outside"},
		"the port refuses from outside":        {"peer-ports", with("203.0.113.1", "refused"), nil, "203.0.113.1:6443 is refused from outside"},
		"the dial from outside went wrong":     {"peer-ports", with("203.0.113.1", "error"), nil, "203.0.113.1:6443 is error from outside"},
		"an address was not dialled":           {"peer-ports", map[string]string{"203.0.113.1": "timeout"}, nil, "203.0.113.2:6443 was not dialled from outside"},
		"no address was dialled":               {"peer-ports", map[string]string{}, nil, "203.0.113.1:6443 was not dialled from outside"},
		"the run did not say":                  {"peer-ports", nil, nil, "the run was not given the outcome of the dial from outside (--api-port-outside)"},
		"the port times out from here":         {"peer-ports", closed, map[string]error{"203.0.113.1:6443": timeoutError{}}, "203.0.113.1:6443 is timeout from here, so a time-out from outside proves nothing"},
		"a region has no public address":       {"peer-ports-no-address", closed, nil, "no control-plane node of region region-b has an ExternalIP address"},
		"a region cannot be read":              {"peer-ports-region-b-unreachable", closed, nil, "[region-b] kubectl get nodes"},
	} {
		fakePeerDial(t, c.override)
		r := runCheck(t, c.scenario, apiPortCheck{}, &CheckContext{Deploy: &DeploymentInfo{APIPortOutside: c.outside}})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		if n := len(r.run.Steps); n != 2 {
			t.Errorf("%s: want one step per region, got %d steps", name, n)
		}
	}
	// A run without the facts of the deployment fails too.
	fakePeerDial(t, nil)
	if got := runCheck(t, "peer-ports", apiPortCheck{}, &CheckContext{}).run.Checks[0]; got.Status != Fail {
		t.Errorf("no deployment facts: want FAIL, got %+v", got)
	}
	// From here only port 6443 of the control-plane addresses is dialled, once
	// each, and the evidence holds both outcomes.
	dialled := fakePeerDial(t, nil)
	r := runCheck(t, "peer-ports", apiPortCheck{}, &CheckContext{Deploy: &DeploymentInfo{APIPortOutside: closed}})
	if got, want := strings.Join(*dialled, " "), "203.0.113.1:6443 203.0.113.2:6443"; got != want {
		t.Errorf("dialled %q, want %q", got, want)
	}
	ev := r.run.Steps[1].Evidence
	if !reflect.DeepEqual(ev["from_here"], map[string]string{"203.0.113.2": "open"}) ||
		!reflect.DeepEqual(ev["from_outside"], map[string]string{"203.0.113.2": "timeout"}) ||
		!reflect.DeepEqual(ev["addresses"], []string{"203.0.113.2"}) {
		t.Errorf("the evidence of region b is %v", ev)
	}
}

func TestParseAPIPortOutside(t *testing.T) {
	if got, err := parseAPIPortOutside("  "); got != nil || err != nil {
		t.Errorf("an empty value: want nil and no error, got %v %v", got, err)
	}
	got, err := parseAPIPortOutside(" 203.0.113.1=timeout, 203.0.113.2 = open ,2001:DB8::1=refused,192.0.2.7=error")
	want := map[string]string{"203.0.113.1": "timeout", "203.0.113.2": "open", "2001:db8::1": "refused", "192.0.2.7": "error"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("a good value: got %v %v, want %v", got, err, want)
	}
	for _, bad := range []string{
		"203.0.113.1",                             // no outcome
		"203.0.113.1=",                            // an empty outcome
		"203.0.113.1=closed",                      // an outcome outside the list
		"203.0.113.1=TIMEOUT",                     // the outcome in another case
		"=timeout",                                // no address
		"node-1=timeout",                          // a name, not an address
		"203.0.113.1:6443=timeout",                // an address with a port
		"203.0.113.1=timeout,203.0.113.1=open",    // an address named twice
		"2001:db8::1=timeout,2001:DB8::1=timeout", // the same address in two spellings
		"203.0.113.1=timeout;203.0.113.2=timeout", // another separator
	} {
		if got, err := parseAPIPortOutside(bad); err == nil || got != nil || !strings.HasPrefix(err.Error(), "--api-port-outside: ") {
			t.Errorf("%q: want an error that starts with --api-port-outside: , got %v %v", bad, got, err)
		}
	}
}

// TestExecuteRefusesAPIPortOutside: a value in another form stops the run
// before anything runs, as the other values of a run do.
func TestExecuteRefusesAPIPortOutside(t *testing.T) {
	cfg := Config{Env: "x", Console: "http://127.0.0.1:9", Session: "x", KubeconfigA: "a", KubeconfigB: "b",
		RunIn: string(RunInLocal), RepoRoot: ".", APIPortOutside: "203.0.113.1=closed"}
	if _, err := Execute(context.Background(), cfg, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "--api-port-outside: ") {
		t.Errorf("a value in another form: want the run refused, got %v", err)
	}
}
