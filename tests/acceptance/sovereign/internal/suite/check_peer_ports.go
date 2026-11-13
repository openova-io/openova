package suite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Check NET-G01, Peer ports closed: the ports that only the regions of the
// Sovereign may reach do not answer from the machine that runs the suite.
//
// The addresses are the ExternalIP addresses of the control-plane nodes of
// every region: the nodes with the label catalyst.openova.io/role=control-plane.
// For each address:
//
//   - a TCP connection to port 6443 is accepted. This is the control: it
//     proves the address answers from here, so that a time-out on the other
//     ports is the firewall and not an address that cannot be reached;
//   - a TCP connection to port 2379 (the embedded etcd) and to port 12379
//     (the mesh endpoint) times out. An accepted or a refused connection
//     fails: the packet got through the firewall.
//
// The mesh between the regions must still work: discovery step D0.10 passed
// in this run. A region without a control-plane address fails the check; it
// never passes on nothing.
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
		rec.Step(next(), "the control-plane addresses of region "+region.ID+" answer on 6443 and time out on 2379 and 12379", func(ev Evidence) error {
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
			results := map[string]string{}
			failures := []string{}
			for _, ip := range addresses {
				control := net.JoinHostPort(ip, strconv.Itoa(peerPortsControlPort))
				results[control] = peerPortsOutcome(peerPortsDial(ctx, control))
				if results[control] != "open" {
					failures = append(failures, fmt.Sprintf("%s is %s, so a time-out proves nothing", control, results[control]))
				}
				for _, port := range peerPortsClosed {
					target := net.JoinHostPort(ip, strconv.Itoa(port))
					results[target] = peerPortsOutcome(peerPortsDial(ctx, target))
					if results[target] != "timeout" {
						failures = append(failures, fmt.Sprintf("%s is %s", target, results[target]))
					}
				}
			}
			ev["results"] = results
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
