// dns.go — the kind dns_records of the sweep report. It asks the
// authoritative servers of the parent domain, over DNS, about every name the
// platform writes for a Sovereign, and names those that still answer. It only
// reads.
//
// The names: the apex <fqdn>; <sub>.<fqdn> for every sub of
// handler.CanonicalSovereignSubdomains (the record set the provisioning API
// writes into the parent zone and the wipe deletes); *.<fqdn> and
// admin.<fqdn> (written by cmd/catalyst-dns for a pool domain). The apex also
// answers while any other name below <fqdn> is left.
// The parent domain is <fqdn> without its first label. Its servers are the
// hosts of its NS records and their addresses, looked up with the system
// resolver; each is asked on port 53 over UDP, without recursion.
//
// A name still answers when a server gives an authoritative answer that is
// not NXDOMAIN, or a referral (the name is delegated). A name is gone on an
// authoritative NXDOMAIN. Any other outcome (no reply in time, a reply code
// other than these, an answer that is not authoritative, a malformed reply)
// means that server could not be asked, and the next one is tried. Fail
// closed: a name that no server can be asked about, or a parent domain whose
// servers cannot be found, ends the count with an error and no result.

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
)

// dnsKind is the entry dns_records of the report's list kinds. It has no used
// count: queries cannot count a zone. Asked is how many names were asked.
type dnsKind struct {
	Kind      string   `json:"kind"`
	Asked     int      `json:"asked"`
	Leftovers []string `json:"leftovers"`
}

// dnsAsker reaches DNS. Tests replace the look-ups, the port and the timeout.
type dnsAsker struct {
	lookupNS   func(ctx context.Context, domain string) ([]string, error)
	lookupHost func(ctx context.Context, host string) ([]string, error)
	port       string
	timeout    time.Duration
}

func systemAsker() dnsAsker {
	r := net.DefaultResolver
	return dnsAsker{
		lookupNS: func(ctx context.Context, domain string) ([]string, error) {
			ns, err := r.LookupNS(ctx, domain)
			hosts := make([]string, 0, len(ns))
			for _, n := range ns {
				hosts = append(hosts, n.Host)
			}
			return hosts, err
		},
		lookupHost: r.LookupHost,
		port:       "53",
		timeout:    3 * time.Second,
	}
}

// sweepNames is the sorted list of names asked for one Sovereign FQDN.
func sweepNames(fqdn string) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, n := range append([]string{fqdn, "*." + fqdn, "admin." + fqdn}, prefixed(fqdn)...) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func prefixed(fqdn string) []string {
	out := make([]string, 0, len(handler.CanonicalSovereignSubdomains))
	for _, sub := range handler.CanonicalSovereignSubdomains {
		out = append(out, sub+"."+fqdn)
	}
	return out
}

// countDNS returns the kind dns_records for the Sovereign FQDN, or an error
// when it cannot be measured completely.
func (a dnsAsker) countDNS(ctx context.Context, sovereignFQDN string) (*dnsKind, error) {
	fqdn := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(sovereignFQDN)), ".")
	i := strings.IndexByte(fqdn, '.')
	if i <= 0 || i == len(fqdn)-1 {
		return nil, fmt.Errorf("%q has no parent domain", sovereignFQDN)
	}
	parent := fqdn[i+1:]
	hosts, err := a.lookupNS(ctx, parent)
	if err != nil {
		return nil, fmt.Errorf("look up the name servers of %s: %w", parent, err)
	}
	sort.Strings(hosts)
	servers := []string{}
	for _, h := range hosts {
		addrs, err := a.lookupHost(ctx, strings.TrimSuffix(h, "."))
		if err != nil {
			continue
		}
		sort.Slice(addrs, func(x, y int) bool { // IPv4 first, then by text
			vx, vy := net.ParseIP(addrs[x]).To4() != nil, net.ParseIP(addrs[y]).To4() != nil
			if vx != vy {
				return vx
			}
			return addrs[x] < addrs[y]
		})
		for _, ad := range addrs {
			servers = append(servers, net.JoinHostPort(ad, a.port))
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no address of a name server of %s (name servers: %v)", parent, hosts)
	}
	kind := &dnsKind{Kind: "dns_records", Leftovers: []string{}}
	for _, name := range sweepNames(fqdn) {
		answers, err := a.ask(ctx, servers, name)
		if err != nil {
			return nil, err
		}
		kind.Asked++
		if answers {
			kind.Leftovers = append(kind.Leftovers, name)
		}
	}
	return kind, nil
}

// ask asks the servers in order until one can answer about the name.
func (a dnsAsker) ask(ctx context.Context, servers []string, name string) (bool, error) {
	why := []string{}
	for _, s := range servers {
		answers, err := a.askOne(ctx, s, name)
		if err == nil {
			return answers, nil
		}
		why = append(why, s+": "+err.Error())
	}
	return false, fmt.Errorf("%s could not be asked: %s", name, strings.Join(why, "; "))
}

func (a dnsAsker) askOne(ctx context.Context, server, name string) (bool, error) {
	var idb [2]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return false, err
	}
	id := binary.BigEndian.Uint16(idb[:])
	query, err := dnsQuery(id, name)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write(query); err != nil {
		return false, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return false, err
		}
		if n >= 12 && binary.BigEndian.Uint16(buf) == id {
			return judge(buf[:n])
		}
	}
}

// dnsQuery encodes a query for the A records of name, without recursion.
func dnsQuery(id uint16, name string) ([]byte, error) {
	if len(name) > 253 {
		return nil, fmt.Errorf("%q is longer than 253 characters", name)
	}
	msg := binary.BigEndian.AppendUint16(nil, id)
	msg = append(msg, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0) // no flags; one question
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("%q has a label that is empty or longer than 63 characters", name)
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	return append(msg, 0, 0, 1, 0, 1), nil // root; type A; class IN
}

// judge reads the header of a reply: true when the name still answers.
func judge(msg []byte) (bool, error) {
	flags := binary.BigEndian.Uint16(msg[2:4])
	authoritative := flags&0x0400 != 0
	rcode := flags & 0x000f
	answers, authority := binary.BigEndian.Uint16(msg[6:8]), binary.BigEndian.Uint16(msg[8:10])
	switch {
	case flags&0x8000 == 0 || flags&0x7800 != 0:
		return false, errors.New("the reply is not the answer to a query")
	case rcode == 3 && authoritative:
		return false, nil
	case rcode == 0 && authoritative:
		return true, nil
	case rcode == 0 && answers == 0 && authority > 0:
		return true, nil
	case rcode == 0 || rcode == 3:
		return false, errors.New("the answer is not authoritative")
	default:
		return false, fmt.Errorf("the reply code is %d", rcode)
	}
}
