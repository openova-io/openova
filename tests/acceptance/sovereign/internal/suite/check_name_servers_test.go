package suite

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures are those of check NET-G01 (check_peer_ports_test.go): region
// a has the control-plane address 203.0.113.1 and a worker with 198.51.100.9;
// region b has two control-plane nodes behind 203.0.113.2. discovered() names
// the Sovereign sov.test. fakeNameServers answers every query on 127.0.0.1.

// nsBehaviour is how a fake name server answers.
type nsBehaviour struct {
	silent bool   // reads each query and never answers
	drop   bool   // never answers a name outside sov.test
	ra     bool   // sets RA in every reply
	open   bool   // answers a name outside sov.test with an A record
	rcode  uint16 // the rcode of the reply to a name of sov.test
}

// fakeNameServers replaces nameServersDial. A dial of "<network> <address>"
// reaches a fake name server of its own on 127.0.0.1 that answers as
// behaviours names it (the zero value answers as an authoritative server of
// sov.test that does not recurse). It returns the list of the dials.
func fakeNameServers(t *testing.T, behaviours map[string]nsBehaviour) *[]string {
	t.Helper()
	var mu sync.Mutex
	dialled := &[]string{}
	servers := map[string]string{}
	saved := nameServersDial
	t.Cleanup(func() { nameServersDial = saved })
	nameServersDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		key := network + " " + address
		mu.Lock()
		*dialled = append(*dialled, key)
		local, ok := servers[key]
		if !ok {
			local = startFakeNameServer(t, network, behaviours[key])
			servers[key] = local
		}
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, local)
	}
	return dialled
}

func startFakeNameServer(t *testing.T, network string, b nsBehaviour) string {
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		go func() {
			buf := make([]byte, 65535)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				if r := fakeDNSReply(buf[:n], b); r != nil {
					_, _ = pc.WriteTo(r, from)
				}
			}
		}()
		return pc.LocalAddr().String()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				for {
					var size [2]byte
					if _, err := io.ReadFull(c, size[:]); err != nil {
						return
					}
					q := make([]byte, binary.BigEndian.Uint16(size[:]))
					if _, err := io.ReadFull(c, q); err != nil {
						return
					}
					r := fakeDNSReply(q, b)
					if r == nil {
						continue
					}
					framed, _ := dnsLength(r)
					_, _ = c.Write(append(framed, r...))
				}
			}()
		}
	}()
	return l.Addr().String()
}

// fakeDNSReply answers the query q: the question echoed, AA set, and one answer
// record (SOA for sov.test, A for another name when the server is open). It
// returns nil for a query the server does not answer.
func fakeDNSReply(q []byte, b nsBehaviour) []byte {
	end := 12
	labels := []string{}
	for q[end] != 0 {
		labels = append(labels, string(q[end+1:end+1+int(q[end])]))
		end += 1 + int(q[end])
	}
	question := q[12 : end+5]
	inZone := strings.HasSuffix(strings.Join(labels, ".")+".", "sov.test.")
	if b.silent || (b.drop && !inZone) {
		return nil
	}
	flags := uint16(0x8000|0x0400) | binary.BigEndian.Uint16(q[2:4])&0x0100
	if b.ra {
		flags |= 0x0080
	}
	answer := []byte(nil)
	switch {
	case inZone && b.rcode == 0:
		answer = append([]byte{0xc0, 0x0c, 0, dnsTypeSOA, 0, 1, 0, 0, 0x0e, 0x10, 0, 26, 0xc0, 0x0c, 0xc0, 0x0c}, make([]byte, 20)...)
	case inZone:
		flags |= b.rcode
	case b.open:
		answer = []byte{0xc0, 0x0c, 0, dnsTypeA, 0, 1, 0, 0, 0x0e, 0x10, 0, 4, 192, 0, 2, 1}
	default:
		flags |= 5 // REFUSED: the name is in no zone of the server
	}
	r := append([]byte{q[0], q[1]}, binary.BigEndian.AppendUint16(nil, flags)...)
	ancount := byte(0)
	if answer != nil {
		ancount = 1
	}
	r = append(r, 0, 1, 0, ancount, 0, 0, 0, 0)
	return append(append(r, question...), answer...)
}

func TestNameServersCheck(t *testing.T) {
	if _, ok := registry[nameServersID]; !ok || nameServersID != "NET-G19" {
		t.Fatalf("check %s is not registered", nameServersID)
	}
	if nameServersTimeout != 5*time.Second {
		t.Fatalf("each query must be bounded by 5 seconds, it is %s", nameServersTimeout)
	}
	// fails runs the check on scenario with the fake name servers answering as
	// behaviours says, and wants a FAIL whose reason holds each part of want.
	fails := func(t *testing.T, scenario string, behaviours map[string]nsBehaviour, want ...string) *runner {
		t.Helper()
		fakeNameServers(t, behaviours)
		r := runCheck(t, scenario, nameServersCheck{}, &CheckContext{}, discovered()...)
		got := r.run.Checks[0]
		if got.Status != Fail {
			t.Fatalf("want FAIL, got %+v", got)
		}
		for _, w := range want {
			if !strings.Contains(got.Reason, w) {
				t.Errorf("want the reason to hold %q, got %q", w, got.Reason)
			}
		}
		return r
	}
	short := func(t *testing.T) {
		saved := nameServersTimeout
		nameServersTimeout = 300 * time.Millisecond
		t.Cleanup(func() { nameServersTimeout = saved })
	}

	t.Run("the external address of each regions control plane is asked", func(t *testing.T) {
		dialled := fakeNameServers(t, nil)
		runCheck(t, "peer-ports", nameServersCheck{}, &CheckContext{}, discovered()...)
		got := append([]string{}, *dialled...)
		sort.Strings(got)
		want := []string{"tcp 203.0.113.1:53", "tcp 203.0.113.1:53", "tcp 203.0.113.2:53", "tcp 203.0.113.2:53",
			"udp 203.0.113.1:53", "udp 203.0.113.1:53", "udp 203.0.113.2:53", "udp 203.0.113.2:53"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("dialled %q, want %q: two queries per address and transport, no worker, a shared address once", got, want)
		}
	})

	t.Run("answers over udp and tcp pass", func(t *testing.T) {
		fakeNameServers(t, nil)
		r := runCheck(t, "peer-ports", nameServersCheck{}, &CheckContext{}, discovered()...)
		if got := r.run.Checks[0]; got.Status != Pass {
			t.Fatalf("want PASS, got %+v", got)
		}
		if n := len(r.run.Steps); n != 3 {
			t.Fatalf("want the discovery step and one step per region, got %d steps", n)
		}
		ev := r.run.Steps[2].Evidence
		queries, _ := ev["queries"].([]nameServersQuery)
		if !reflect.DeepEqual(ev["addresses"], []string{"203.0.113.2"}) || len(queries) != 4 {
			t.Fatalf("the evidence of region b is %v", ev)
		}
		want := []string{"udp sov.test. NOERROR qr aa rd 1", "udp example.com. REFUSED qr aa rd 0",
			"tcp sov.test. NOERROR qr aa rd 1", "tcp example.com. REFUSED qr aa rd 0"}
		for i, q := range queries {
			line := strings.Join([]string{q.Transport, q.Name, q.Rcode, q.Flags}, " ") + " " + string(rune('0'+q.Answers))
			if q.Address != "203.0.113.2" || q.Error != "" || line != want[i] {
				t.Errorf("query %d is %+v, want %q", i, q, want[i])
			}
			// An answered query records how long it took: within the bound.
			if q.Ms < 0 || q.Ms >= nameServersTimeout.Milliseconds() {
				t.Errorf("query %d records ms %d, want 0 <= ms < %d", i, q.Ms, nameServersTimeout.Milliseconds())
			}
		}
	})

	t.Run("no answer within 5 seconds fails", func(t *testing.T) {
		short(t)
		r := fails(t, "peer-ports", map[string]nsBehaviour{"udp 203.0.113.1:53": {silent: true}},
			"region region-a: [203.0.113.1:53 over udp: no answer within 300ms")
		// The evidence records how long each query waited, a timeout included.
		queries, _ := r.run.Steps[1].Evidence["queries"].([]nameServersQuery)
		if len(queries) != 4 {
			t.Fatalf("the evidence of region a is %v", r.run.Steps[1].Evidence)
		}
		for _, q := range queries[:2] {
			if q.Transport != "udp" || q.Error == "" || q.Ms < 300 {
				t.Errorf("query %+v: want a udp timeout that records ms >= 300", q)
			}
		}
	})

	t.Run("an answer over udp only fails", func(t *testing.T) {
		short(t)
		fails(t, "peer-ports", map[string]nsBehaviour{"tcp 203.0.113.2:53": {silent: true}},
			"region region-b: [203.0.113.2:53 over tcp: no answer within 300ms")
	})

	t.Run("an answer that offers recursion fails", func(t *testing.T) {
		fails(t, "peer-ports", map[string]nsBehaviour{"udp 203.0.113.2:53": {ra: true}},
			"203.0.113.2:53 over udp: the answer to sov.test. offers recursion (RA)")
	})

	t.Run("an outside name that gets an answer fails", func(t *testing.T) {
		fails(t, "peer-ports", map[string]nsBehaviour{"tcp 203.0.113.1:53": {open: true}},
			"203.0.113.1:53 over tcp: example.com. got 1 answer record(s): the server resolves names outside the Sovereign's zones")
	})

	t.Run("a region without an external address fails as not measured", func(t *testing.T) {
		fakeNameServers(t, nil)
		r := runCheck(t, "peer-ports-no-address", nameServersCheck{}, &CheckContext{}, discovered()...)
		want := []string{"no control-plane node of region region-b has an ExternalIP address: not measured"}
		if got := failedSteps(r); !reflect.DeepEqual(got, want) || r.run.Checks[0].Status != Fail {
			t.Errorf("want only region b failed as not measured, got %q (%+v)", got, r.run.Checks[0])
		}
	})

	t.Run("a reply of servfail fails", func(t *testing.T) {
		fails(t, "peer-ports", map[string]nsBehaviour{"tcp 203.0.113.2:53": {rcode: 2}},
			"203.0.113.2:53 over tcp: the answer to sov.test. is SERVFAIL")
	})

	t.Run("no reply to an outside name passes", func(t *testing.T) {
		short(t)
		// A server that drops an outside name is no open resolver: only the
		// query of the Sovereign's own domain must be answered.
		fakeNameServers(t, map[string]nsBehaviour{"udp 203.0.113.1:53": {drop: true}, "tcp 203.0.113.2:53": {drop: true}})
		if got := runCheck(t, "peer-ports", nameServersCheck{}, &CheckContext{}, discovered()...).run.Checks[0]; got.Status != Pass {
			t.Errorf("want PASS, got %+v", got)
		}
	})

	t.Run("a region that cannot be read fails", func(t *testing.T) {
		fails(t, "peer-ports-region-b-unreachable", nil, "[region-b] kubectl get nodes")
	})

	t.Run("a run that did not learn the name fails", func(t *testing.T) {
		fakeNameServers(t, nil)
		got := runCheck(t, "peer-ports", nameServersCheck{}, &CheckContext{}, priorStep("D0", "D0.1", Pass, "")).run.Checks[0]
		if got.Status != Fail || !strings.Contains(got.Reason, "step D0.1 recorded no sovereignFQDN") {
			t.Errorf("want FAIL for a run without the name, got %+v", got)
		}
	})
}

// TestDNSQueryMessage: the query is the RFC 1035 wire form, and a reply is
// taken only when it answers that query.
func TestDNSQueryMessage(t *testing.T) {
	msg, err := dnsQueryMessage("sov.test.", dnsTypeSOA)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 3, 's', 'o', 'v', 4, 't', 'e', 's', 't', 0, 0, 6, 0, 1}
	if !reflect.DeepEqual(msg[2:], want) {
		t.Errorf("the query is % x, want % x after the ID", msg[2:], want)
	}
	for _, bad := range []string{"", "a..b", strings.Repeat("x", 64) + ".test"} {
		if _, err := dnsQueryMessage(bad, dnsTypeA); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	reply := fakeDNSReply(msg, nsBehaviour{})
	if err := checkDNSReply(reply, msg); err != nil {
		t.Errorf("a reply to the query: %v", err)
	}
	other := append([]byte{}, reply...)
	other[0] ^= 0xff
	if err := checkDNSReply(other, msg); err == nil || err.Error() != "the reply carries another ID" {
		t.Errorf("a reply with another ID: got %v", err)
	}
	if err := checkDNSReply(msg, msg); err == nil || err.Error() != "the reply has no QR bit" {
		t.Errorf("the query itself: got %v", err)
	}
	asked, _ := dnsQueryMessage("other.test.", dnsTypeSOA)
	asked[0], asked[1] = msg[0], msg[1]
	if err := checkDNSReply(fakeDNSReply(asked, nsBehaviour{}), msg); err == nil {
		t.Error("a reply to another question: want an error")
	}
	// The name is compared without regard to case, the type and class byte
	// for byte: type 0x61 is no reply to a query of type 0x41.
	capitals := append([]byte{}, reply...)
	copy(capitals[13:16], "SOV")
	if err := checkDNSReply(capitals, msg); err != nil {
		t.Errorf("a reply that writes the name in capitals: %v", err)
	}
	typed, _ := dnsQueryMessage("sov.test.", 0x41)
	retyped := fakeDNSReply(typed, nsBehaviour{})
	retyped[len(typed)-3] = 0x61
	if err := checkDNSReply(retyped, typed); err == nil || err.Error() != "the reply answers another question" {
		t.Errorf("a reply of type 0x61 to a query of type 0x41: got %v", err)
	}
	if got := dnsFlagNames(0x8580); got != "qr aa rd ra" {
		t.Errorf("flags 0x8580 are %q", got)
	}
	if got := dnsRcodeName(3) + " " + dnsRcodeName(9); got != "NXDOMAIN RCODE9" {
		t.Errorf("rcodes 3 and 9 are %q", got)
	}
}
