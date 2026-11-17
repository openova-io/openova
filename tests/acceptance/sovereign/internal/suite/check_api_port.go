package suite

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Check NET-G02, API port limited: port 6443 of every control-plane address
// answers only the sources that the firewall of the Sovereign lists.
//
// The suite runs on the gate runner, and the firewall lists that machine: it
// could not read the clusters otherwise. So the suite cannot see a closed
// port by itself. The gate workflow dials port 6443 of every control-plane
// address from a second machine, a runner hosted by GitHub whose address is
// in no list, and gives the outcome to the run as --api-port-outside:
// "<address>=<outcome>,<address>=<outcome>", where an outcome is open,
// timeout, refused or error (scripts/gate-outside-dial.py writes it).
//
// The addresses are the ExternalIP addresses of the nodes with the label
// catalyst.openova.io/role=control-plane, as check NET-G01 reads them. For
// each address of each region:
//
//   - a TCP connection to port 6443 from here is accepted. This is the
//     control: the address is alive, so a time-out from outside is the
//     firewall and not an address that nothing listens on;
//   - the outcome from outside is timeout. An address that was not dialled
//     from outside fails, and so does every other outcome: open and refused
//     mean that the packet got through the firewall.
//
// A run that was not given --api-port-outside fails the check, and so does a
// region without a control-plane address. It never passes on nothing.
const apiPortID = "NET-G02"

// apiPortOutcomes are the outcomes that --api-port-outside may name.
var apiPortOutcomes = map[string]bool{"open": true, "timeout": true, "refused": true, "error": true}

type apiPortCheck struct{}

func init() { RegisterCheck(apiPortCheck{}) }

func (apiPortCheck) ID() string { return apiPortID }

func (apiPortCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, region := range cc.Regions {
		name := "port 6443 of the control-plane addresses of region " + region.ID + " answers here and times out from outside"
		rec.Step(apiPortID+"."+strconv.Itoa(i+1), name, func(ev Evidence) error {
			if cc.Deploy == nil || cc.Deploy.APIPortOutside == nil {
				return errors.New("the run was not given the outcome of the dial from outside (--api-port-outside)")
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
				target := net.JoinHostPort(ip, strconv.Itoa(peerPortsControlPort))
				here[ip] = peerPortsOutcome(peerPortsDial(ctx, target))
				if here[ip] != "open" {
					failures = append(failures, fmt.Sprintf("%s is %s from here, so a time-out from outside proves nothing", target, here[ip]))
				}
				key := ip
				if parsed := net.ParseIP(ip); parsed != nil {
					key = parsed.String() // the form parseAPIPortOutside keeps
				}
				outcome, dialled := cc.Deploy.APIPortOutside[key]
				if !dialled {
					failures = append(failures, target+" was not dialled from outside")
					continue
				}
				outside[ip] = outcome
				if outcome != "timeout" {
					failures = append(failures, fmt.Sprintf("%s is %s from outside", target, outcome))
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

// parseAPIPortOutside reads the value of --api-port-outside:
// "<address>=<outcome>,<address>=<outcome>". An address is an IP address and
// is given once; an outcome is one of apiPortOutcomes. The addresses are kept
// in the form net.IP prints. It returns nil for an empty value: the run did
// not say.
func parseAPIPortOutside(s string) (map[string]string, error) {
	parts := splitList(s)
	if len(parts) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range parts {
		address, outcome, found := strings.Cut(part, "=")
		ip := net.ParseIP(strings.TrimSpace(address))
		outcome = strings.TrimSpace(outcome)
		if !found || ip == nil || !apiPortOutcomes[outcome] {
			return nil, fmt.Errorf("--api-port-outside: %q is not <address>=<outcome> with an outcome of open, timeout, refused or error", part)
		}
		if _, twice := out[ip.String()]; twice {
			return nil, fmt.Errorf("--api-port-outside: address %s is named twice", ip)
		}
		out[ip.String()] = outcome
	}
	return out, nil
}
