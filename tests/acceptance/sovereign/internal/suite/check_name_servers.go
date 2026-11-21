package suite

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Check NET-G19, the name servers answer on public port 53 in every region.
//
// The name-server address of a region is the public address of its primary
// control-plane node: the output control_plane_ips_per_region of every
// provider module, which the node registers as its ExternalIP
// (--node-external-ip). The addresses are the ExternalIP addresses of the
// nodes with the label catalyst.openova.io/role=control-plane, as checks
// NET-G01 and NET-G02 read them. The queries leave the gate run's machine,
// which is outside every network of the Sovereign. For each address:
//
//   - an SOA query for the Sovereign's domain (the sovereignFQDN of step
//     D0.1) gets a reply over UDP and over TCP, each within 5 seconds;
//   - no reply sets RA (recursion available), and no reply to the SOA query
//     is SERVFAIL (a front door with no name server behind it);
//   - a query for a name outside the Sovereign's zones (example.com.) gets
//     no answer record, over UDP and over TCP: the server is no open
//     resolver. A query of that name that gets no reply passes this rule.
//
// A reply is a DNS response to the query: the same ID, the QR bit, the same
// question. Its rcode is recorded and not judged further: the check proves
// that a name server answers at the address, not what the zone holds. The
// evidence holds, per query, the address, the transport, the rcode, the
// flags and the milliseconds. A region whose control-plane nodes have no
// ExternalIP fails as not measured, and so does a run that did not learn the
// Sovereign's name. The check creates nothing.
const nameServersID = "NET-G19"

const (
	nameServersPort        = 53
	nameServersOutsideName = "example.com."
	dnsTypeA               = 1
	dnsTypeSOA             = 6
)

// nameServersTimeout bounds each query, the dial included. The tests shorten it.
var nameServersTimeout = 5 * time.Second

// nameServersDial opens the connection of one query. The tests replace it.
var nameServersDial = func(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// nameServersQuery is one query of the check, as the evidence records it.
type nameServersQuery struct {
	Address   string `json:"address"`
	Transport string `json:"transport"`
	Name      string `json:"name"`
	Rcode     string `json:"rcode,omitempty"`
	Flags     string `json:"flags,omitempty"`
	Answers   int    `json:"answers"`
	Ms        int64  `json:"ms"`
	Error     string `json:"error,omitempty"`
}

type nameServersCheck struct{}

func init() { RegisterCheck(nameServersCheck{}) }

func (nameServersCheck) ID() string { return nameServersID }

func (nameServersCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	fqdn := strings.TrimSuffix(sovereignFQDN(cc.Prior), ".")
	if fqdn == "" {
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	zone := fqdn + "."
	for i, region := range cc.Regions {
		name := "the name servers of region " + region.ID + " answer on public port 53 over UDP and TCP"
		rec.Step(nameServersID+"."+strconv.Itoa(i+1), name, func(ev Evidence) error {
			nodes, err := region.GetList(ctx, "nodes", "")
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			addresses := []string{}
			for _, node := range nodes {
				if node.Str("metadata", "labels", peerPortsRoleLabel) != "control-plane" {
					continue
				}
				for _, a := range node.Objects("status", "addresses") {
					if ip := a.Str("address"); a.Str("type") == "ExternalIP" && ip != "" && !seen[ip] {
						seen[ip] = true
						addresses = append(addresses, ip)
					}
				}
			}
			sort.Strings(addresses)
			ev["addresses"] = addresses
			if len(addresses) == 0 {
				return fmt.Errorf("no control-plane node of region %s has an ExternalIP address: not measured", region.ID)
			}
			queries := []nameServersQuery{}
			failures := []string{}
			for _, ip := range addresses {
				target := net.JoinHostPort(ip, strconv.Itoa(nameServersPort))
				for _, transport := range []string{"udp", "tcp"} {
					for _, q := range []struct {
						name  string
						qtype uint16
					}{{zone, dnsTypeSOA}, {nameServersOutsideName, dnsTypeA}} {
						got := askNameServer(ctx, transport, target, q.name, q.qtype)
						got.Address = ip
						queries = append(queries, got)
						where := target + " over " + transport
						switch {
						case got.Error != "" && q.name == zone:
							failures = append(failures, fmt.Sprintf("%s: no answer within %s: %s", where, nameServersTimeout, got.Error))
						case got.Error != "":
							// no reply to a name outside the zones: no open resolver
						case strings.Contains(" "+got.Flags+" ", " ra "):
							failures = append(failures, fmt.Sprintf("%s: the answer to %s offers recursion (RA)", where, q.name))
						case q.name == zone && got.Rcode == "SERVFAIL":
							failures = append(failures, fmt.Sprintf("%s: the answer to %s is SERVFAIL: no name server answers behind the address", where, q.name))
						case q.name != zone && got.Answers > 0:
							failures = append(failures, fmt.Sprintf("%s: %s got %d answer record(s): the server resolves names outside the Sovereign's zones", where, q.name, got.Answers))
						}
					}
				}
			}
			ev["queries"] = queries
			if len(failures) > 0 {
				return fmt.Errorf("region %s: %v", region.ID, failures)
			}
			return nil
		})
	}
	return nil
}

// askNameServer sends one query of class IN over transport ("udp" or "tcp")
// to target and reads the reply. A failure is recorded in Error.
// The result is named so that the deferred write of Ms reaches it.
func askNameServer(ctx context.Context, transport, target, name string, qtype uint16) (out nameServersQuery) {
	out = nameServersQuery{Transport: transport, Name: name}
	start := time.Now()
	defer func() { out.Ms = time.Since(start).Milliseconds() }()
	msg, err := dnsQueryMessage(name, qtype)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, nameServersTimeout)
	defer cancel()
	conn, err := nameServersDial(ctx, transport, target)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	reply, err := exchangeDNS(conn, transport, msg)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	flags := binary.BigEndian.Uint16(reply[2:4])
	out.Rcode, out.Flags = dnsRcodeName(int(flags&0x000f)), dnsFlagNames(flags)
	out.Answers = int(binary.BigEndian.Uint16(reply[6:8]))
	return out
}

// exchangeDNS writes msg on conn and returns the first reply to it. Over UDP a
// datagram that is no reply to msg is skipped; over TCP each message carries
// its two-byte length.
func exchangeDNS(conn net.Conn, transport string, msg []byte) ([]byte, error) {
	if transport == "tcp" {
		framed, err := dnsLength(msg)
		if err != nil {
			return nil, err
		}
		if _, err := conn.Write(append(framed, msg...)); err != nil {
			return nil, err
		}
		var size [2]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return nil, err
		}
		reply := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(conn, reply); err != nil {
			return nil, err
		}
		return reply, checkDNSReply(reply, msg)
	}
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if checkDNSReply(buf[:n], msg) == nil {
			return buf[:n], nil
		}
	}
}

// dnsLength is the two-byte length that frames msg over TCP.
func dnsLength(msg []byte) ([]byte, error) {
	n := len(msg)
	if n > math.MaxUint16 {
		return nil, fmt.Errorf("a DNS message of %d bytes is too long", n)
	}
	return binary.BigEndian.AppendUint16(nil, uint16(n)), nil
}

// checkDNSReply reports whether reply is a DNS response to the query msg: the
// same ID, the QR bit, one question equal to that of msg: the name without
// regard to case, the type and class byte for byte.
func checkDNSReply(reply, msg []byte) error {
	question := msg[12:]
	switch {
	case len(reply) < 12+len(question):
		return fmt.Errorf("a reply of %d bytes is too short", len(reply))
	case !bytes.Equal(reply[0:2], msg[0:2]):
		return errors.New("the reply carries another ID")
	case reply[2]&0x80 == 0:
		return errors.New("the reply has no QR bit")
	case binary.BigEndian.Uint16(reply[4:6]) != 1 || !bytes.EqualFold(reply[12:8+len(question)], question[:len(question)-4]) || !bytes.Equal(reply[8+len(question):12+len(question)], question[len(question)-4:]):
		return errors.New("the reply answers another question")
	}
	return nil
}

// dnsQueryMessage is a query for name of type qtype and class IN, with a
// random ID and RD set: a server that recurses would show it.
func dnsQueryMessage(name string, qtype uint16) ([]byte, error) {
	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	msg := append([]byte{}, id[0], id[1], 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("%q is no DNS name", name)
		}
		msg = append(append(msg, byte(len(label))), label...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	return binary.BigEndian.AppendUint16(msg, 1), nil
}

func dnsRcodeName(rcode int) string {
	names := []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}
	if rcode < len(names) {
		return names[rcode]
	}
	return "RCODE" + strconv.Itoa(rcode)
}

// dnsFlagNames lists the header flags of a reply that are set, as dig does.
func dnsFlagNames(flags uint16) string {
	set := []string{}
	for _, f := range []struct {
		bit  uint16
		name string
	}{{0x8000, "qr"}, {0x0400, "aa"}, {0x0200, "tc"}, {0x0100, "rd"}, {0x0080, "ra"}} {
		if flags&f.bit != 0 {
			set = append(set, f.name)
		}
	}
	return strings.Join(set, " ")
}
