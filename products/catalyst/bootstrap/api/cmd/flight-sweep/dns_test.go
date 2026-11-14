package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// fakeServer is an authoritative name server on 127.0.0.1. Each name gets one
// behaviour; a name without one gets an authoritative NXDOMAIN.
//
//	"a"        authoritative answer with one record
//	"nodata"   authoritative answer without records
//	"referral" no answer, one record in the authority section, not authoritative
//	"servfail" reply code 2
//	"cache"    an answer that is not authoritative
//	"wrong-id" a reply with another id
//	"silent"   no reply
func fakeServer(t *testing.T, behaviour map[string]string) (port string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			labels, i := []string{}, 12
			for i < n && q[i] != 0 {
				l := int(q[i])
				labels = append(labels, string(q[i+1:i+1+l]))
				i += 1 + l
			}
			reply := append([]byte{}, q...)
			set := func(flags, an, ns uint16) {
				binary.BigEndian.PutUint16(reply[2:], flags)
				binary.BigEndian.PutUint16(reply[6:], an)
				binary.BigEndian.PutUint16(reply[8:], ns)
			}
			switch behaviour[strings.Join(labels, ".")] {
			case "a":
				set(0x8400, 1, 0)
			case "nodata":
				set(0x8400, 0, 1)
			case "referral":
				set(0x8000, 0, 1)
			case "servfail":
				set(0x8402, 0, 0)
			case "cache":
				set(0x8080, 1, 0)
			case "wrong-id":
				set(0x8403, 0, 1)
				reply[0] ^= 0xff
			case "silent":
				continue
			default:
				set(0x8403, 0, 1)
			}
			_, _ = pc.WriteTo(reply, from)
		}
	}()
	_, port, _ = net.SplitHostPort(pc.LocalAddr().String())
	return port
}

// asker points the count at name servers on 127.0.0.1, one per port, in order.
func asker(ports ...string) dnsAsker {
	return dnsAsker{
		lookupNS: func(_ context.Context, domain string) ([]string, error) {
			if domain != "omani.works" {
				return nil, errors.New("asked about " + domain)
			}
			hosts := []string{}
			for i := range ports {
				hosts = append(hosts, "ns"+string(rune('1'+i))+".omani.works.")
			}
			return hosts, nil
		},
		lookupHost: func(_ context.Context, host string) ([]string, error) { return []string{"127.0.0.1"}, nil },
		port:       ports[0],
		timeout:    300 * time.Millisecond,
	}
}

func TestCountDNSClean(t *testing.T) {
	k, err := asker(fakeServer(t, nil)).countDNS(context.Background(), "T1.omani.works.")
	if err != nil {
		t.Fatal(err)
	}
	if k.Kind != "dns_records" || k.Asked != len(sweepNames("t1.omani.works")) || k.Leftovers == nil || len(k.Leftovers) != 0 {
		t.Fatalf("got %+v, want every name asked and none left", k)
	}
}

func TestCountDNSNamesWhatAnswers(t *testing.T) {
	port := fakeServer(t, map[string]string{
		"t1.omani.works": "referral", "console.t1.omani.works": "a", "*.t1.omani.works": "a", "mcp.t1.omani.works": "nodata",
	})
	k, err := asker(port).countDNS(context.Background(), "t1.omani.works")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.t1.omani.works", "console.t1.omani.works", "mcp.t1.omani.works", "t1.omani.works"}
	if !slices.Equal(k.Leftovers, want) {
		t.Fatalf("leftovers %v, want %v", k.Leftovers, want)
	}
}

func TestCountDNSFailsClosed(t *testing.T) {
	for _, b := range []string{"servfail", "cache", "wrong-id", "silent"} {
		k, err := asker(fakeServer(t, map[string]string{"api.t1.omani.works": b})).countDNS(context.Background(), "t1.omani.works")
		if err == nil || k != nil || !strings.Contains(err.Error(), "api.t1.omani.works could not be asked") {
			t.Errorf("%s: got %+v, %v; want no result and an error naming api.t1.omani.works", b, k, err)
		}
	}
	a := asker(fakeServer(t, nil))
	a.lookupNS = func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") }
	if k, err := a.countDNS(context.Background(), "t1.omani.works"); err == nil || k != nil {
		t.Errorf("name servers not found: got %+v, %v; want an error", k, err)
	}
	a = asker(fakeServer(t, nil))
	a.lookupHost = func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") }
	if k, err := a.countDNS(context.Background(), "t1.omani.works"); err == nil || k != nil {
		t.Errorf("no address of a name server: got %+v, %v; want an error", k, err)
	}
	if k, err := asker(fakeServer(t, nil)).countDNS(context.Background(), "works"); err == nil || k != nil {
		t.Errorf("a name without a parent domain: got %+v, %v; want an error", k, err)
	}
}

func TestCountDNSAsksTheNextServer(t *testing.T) {
	silent := net.JoinHostPort("127.0.0.1", fakeServer(t, map[string]string{"api.t1.omani.works": "silent"}))
	answering := net.JoinHostPort("127.0.0.1", fakeServer(t, map[string]string{"api.t1.omani.works": "a"}))
	a := asker("53")
	if answers, err := a.ask(context.Background(), []string{silent, answering}, "api.t1.omani.works"); err != nil || !answers {
		t.Errorf("the second server answers: got %v, %v; want true", answers, err)
	}
	if _, err := a.ask(context.Background(), []string{silent, silent}, "api.t1.omani.works"); err == nil {
		t.Errorf("no server answers: no error")
	}
}

func TestSweepNamesCoverTheWriters(t *testing.T) {
	names := sweepNames("t1.omani.works")
	want := []string{"t1.omani.works", "*.t1.omani.works", "admin.t1.omani.works"}
	for _, sub := range handler.CanonicalSovereignSubdomains {
		want = append(want, sub+".t1.omani.works")
	}
	for _, w := range want {
		if !slices.Contains(names, w) {
			t.Errorf("the names asked lack %s", w)
		}
	}
	if !slices.IsSorted(names) || len(names) != len(slices.Compact(slices.Clone(names))) {
		t.Errorf("the names are not sorted and distinct: %v", names)
	}
}

func TestDNSQuery(t *testing.T) {
	q, err := dnsQuery(0x1234, "*.t1.omani.works")
	want := []byte{0x12, 0x34, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, '*', 2, 't', '1', 5, 'o', 'm', 'a', 'n', 'i', 5, 'w', 'o', 'r', 'k', 's', 0, 0, 1, 0, 1}
	if err != nil || !slices.Equal(q, want) {
		t.Fatalf("got %v, %v; want %v", q, err, want)
	}
	for _, bad := range []string{"a..b", strings.Repeat("x", 64) + ".works", strings.Repeat("abcdefghi.", 26)} {
		if _, err := dnsQuery(1, bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
