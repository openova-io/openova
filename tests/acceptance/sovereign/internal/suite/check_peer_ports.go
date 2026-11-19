package suite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Check NET-G01, Peer ports closed: the ports that only the regions of the
// Sovereign may reach do not answer from outside.
//
// The machine that runs the suite is in a list of the firewall: it reads the
// clusters (the gate runner, or the Job on the parent in parent execution). A
// dial from it is not a dial from outside: the firewall lets it through, and a
// port answers refused where an outside machine sees a time-out (gate run
// 44455908137, from the parent: 5.37.79.43:2379 is refused). So the gate
// workflow dials ports 2379 and 12379 of every control-plane address from a
// runner hosted by GitHub whose address is in no list, and gives the outcome
// to the run as --peer-ports-outside: "<address>:<port>=<outcome>,...", where
// an outcome is open, timeout, refused or error (scripts/gate-outside-dial.py
// dial-peer writes it). The check never dials those ports itself.
//
// The addresses are the ExternalIP addresses of the control-plane nodes of
// every region: the nodes with the label catalyst.openova.io/role=control-plane.
// For each address:
//
//   - a TCP connection to port 6443 from here is accepted. This is the
//     control: it proves the address is alive, so that a time-out from
//     outside is the firewall and not an address that cannot be reached;
//   - from outside, port 2379 (the embedded etcd) and port 12379 (the mesh
//     endpoint) time out. An accepted or a refused connection fails (the
//     packet got through the firewall), and so does a port that was not
//     dialled from outside.
//
// The mesh between the regions must still work: discovery step D0.10 passed
// in this run. A run that was not given --peer-ports-outside fails the check,
// and so does a region without a control-plane address; it never passes on
// nothing.
const peerPortsID = "NET-G01"

const (
	peerPortsRoleLabel   = "catalyst.openova.io/role"
	peerPortsControlPort = 6443
	peerPortsDialTimeout = 5 * time.Second
)

// peerPortsClosed are the ports that must time out from outside.
var peerPortsClosed = []int{2379, 12379}

// peerPortsDial opens and closes one TCP connection. The tests replace it.
var peerPortsDial = func(ctx context.Context, address string) error {
	d := net.Dialer{Timeout: peerPortsDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

type peerPortsCheck struct{}

func init() { RegisterCheck(peerPortsCheck{}) }

func (peerPortsCheck) ID() string { return peerPortsID }

func (peerPortsCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return peerPortsID + "." + strconv.Itoa(n) }
	rec.Step(next(), "mesh step D0.10 passed in this run", func(ev Evidence) error {
		for _, s := range cc.Prior {
			if s.Journey == "D0" && s.ID == "D0.10" {
				ev["d0_10"] = s.Status
				if s.Status != Pass {
					return fmt.Errorf("mesh step D0.10 is %s: %s", s.Status, s.Error)
				}
				return nil
			}
		}
		return errors.New("mesh step D0.10 was not run in this run")
	})
	for _, region := range cc.Regions {
		rec.Step(next(), "the control-plane addresses of region "+region.ID+" answer on 6443 here and time out on 2379 and 12379 from outside", func(ev Evidence) error {
			if cc.Deploy == nil || cc.Deploy.PeerPortsOutside == nil {
				return errors.New("the run was not given the outcome of the dial from outside (--peer-ports-outside)")
			}
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
				return fmt.Errorf("no control-plane node of region %s has an ExternalIP address", region.ID)
			}
			here, outside := map[string]string{}, map[string]string{}
			failures := []string{}
			for _, ip := range addresses {
				key := ip
				if parsed := net.ParseIP(ip); parsed != nil {
					key = parsed.String() // the form parsePeerPortsOutside keeps
				}
				control := net.JoinHostPort(ip, strconv.Itoa(peerPortsControlPort))
				here[control] = peerPortsOutcome(peerPortsDial(ctx, control))
				if here[control] != "open" {
					failures = append(failures, fmt.Sprintf("%s is %s from here, so a time-out from outside proves nothing", control, here[control]))
				}
				for _, port := range peerPortsClosed {
					target := net.JoinHostPort(ip, strconv.Itoa(port))
					outcome, dialled := cc.Deploy.PeerPortsOutside[net.JoinHostPort(key, strconv.Itoa(port))]
					if !dialled {
						failures = append(failures, target+" was not dialled from outside")
						continue
					}
					outside[target] = outcome
					if outcome != "timeout" {
						failures = append(failures, fmt.Sprintf("%s is %s from outside", target, outcome))
					}
				}
			}
			ev["from_here"], ev["from_outside"] = here, outside
			if len(failures) > 0 {
				return fmt.Errorf("region %s: %v", region.ID, failures)
			}
			return nil
		})
	}
	return nil
}

// peerPortsOutcome names the result of one dial: open, timeout, refused, or
// "error: <text>" when it cannot tell.
func peerPortsOutcome(err error) string {
	var netErr net.Error
	switch {
	case err == nil:
		return "open"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	}
	return "error: " + err.Error()
}

// parsePeerPortsOutside reads the value of --peer-ports-outside:
// "<address>:<port>=<outcome>,<address>:<port>=<outcome>", with an IPv6
// address in brackets. An address is an IP address, a port a number from 1 to
// 65535, each pair is given once, and an outcome is one of apiPortOutcomes.
// The pairs are kept in the form net.JoinHostPort writes, with the address in
// the form net.IP prints. It returns nil for an empty value: the run did not
// say.
func parsePeerPortsOutside(s string) (map[string]string, error) {
	parts := splitList(s)
	if len(parts) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range parts {
		target, outcome, found := strings.Cut(part, "=")
		outcome = strings.TrimSpace(outcome)
		host, port, err := net.SplitHostPort(strings.TrimSpace(target))
		ip := net.ParseIP(host)
		n, perr := strconv.Atoi(port)
		if !found || err != nil || ip == nil || perr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || !apiPortOutcomes[outcome] {
			return nil, fmt.Errorf("--peer-ports-outside: %q is not <address>:<port>=<outcome> with an outcome of open, timeout, refused or error", part)
		}
		key := net.JoinHostPort(ip.String(), port)
		if _, twice := out[key]; twice {
			return nil, fmt.Errorf("--peer-ports-outside: %s is named twice", key)
		}
		out[key] = outcome
	}
	return out, nil
}
