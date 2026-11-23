package bootstrapkit

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The public name-server Service powerdns-anycast (bp-powerdns
// templates/anycast-endpoint.yaml) selects the dnsdist Pods. Every port of
// it must reach a port that dnsdist listens on, with the same protocol:
// Kubernetes resolves a named targetPort by name AND protocol (FindPort), so
// a UDP port named after the TCP container port gets no endpoint port. The
// overlay clusters/_template/bootstrap-kit/11-powerdns.yaml set targetPort
// 5353, the port of the authoritative server behind dnsdist, which no
// selected Pod listens on. Where dnsdist runs in hostPortMode (Hetzner, and
// Huawei since NET-185) the public queries reach dnsdist on node:53 and do
// not pass this Service, so this guard does not cover check NET-G19.

// dnsdistContainerPortRE finds one port of the dnsdist container in
// templates/dnsdist.yaml: its name, its containerPort and its protocol. Only
// template directives and a hostPort line may stand between containerPort
// and protocol, so a match never runs into the next port.
var dnsdistContainerPortRE = regexp.MustCompile(`(?m)^[ \t]*- name: (dns-[a-z]+)[ \t]*\n[ \t]*containerPort: ([0-9]+)[ \t]*\n(?:[ \t]*(?:\{\{[^\n]*|hostPort:[^\n]*)\n)*[ \t]*protocol: ([A-Z]+)[ \t]*$`)

// containerPort is one named port of the dnsdist container.
type containerPort struct {
	number   int
	protocol string
}

// dnsdistPorts reads the named container ports of dnsdist from the chart.
func dnsdistPorts(t *testing.T, root string) map[string]containerPort {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "platform", "powerdns", "chart", "templates", "dnsdist.yaml")))
	if err != nil {
		t.Fatalf("read the dnsdist template: %v", err)
	}
	ports := map[string]containerPort{}
	for _, m := range dnsdistContainerPortRE.FindAllStringSubmatch(string(raw), -1) {
		n, _ := strconv.Atoi(m[2])
		ports[m[1]] = containerPort{number: n, protocol: m[3]}
	}
	if len(ports) == 0 {
		t.Fatal("the dnsdist template names no container port dns-* with a protocol: the guard reads nothing")
	}
	return ports
}

// anycastTargetFaults returns, for the anycast ports of one bp-powerdns
// HelmRelease values block, each port whose targetPort is not a port that
// dnsdist listens on with the port's protocol (by name or by number).
func anycastTargetFaults(values map[string]any, dnsdist map[string]containerPort) (int, []string) {
	anycast, _ := values["anycast"].(map[string]any)
	ports, _ := anycast["ports"].([]any)
	var faults []string
	for _, p := range ports {
		port, _ := p.(map[string]any)
		name, _ := port["name"].(string)
		if name == "" {
			faults = append(faults, "a port without a name")
			continue
		}
		protocol, _ := port["protocol"].(string)
		if protocol == "" {
			protocol = "TCP" // the Kubernetes default
		}
		switch tp := port["targetPort"].(type) {
		case string:
			cp, ok := dnsdist[tp]
			if !ok {
				faults = append(faults, name+": targetPort "+tp+" is no container port of dnsdist")
			} else if cp.protocol != protocol {
				faults = append(faults, name+": targetPort "+tp+" is a "+cp.protocol+" container port of dnsdist, the port is "+protocol)
			}
		case int:
			listens := false
			for _, cp := range dnsdist {
				if cp.number == tp && cp.protocol == protocol {
					listens = true
				}
			}
			if !listens {
				faults = append(faults, name+": targetPort "+strconv.Itoa(tp)+" is no "+protocol+" port dnsdist listens on")
			}
		default:
			faults = append(faults, name+": no targetPort")
		}
	}
	return len(ports), faults
}

func TestBootstrapKit_PowerDNSAnycastReachesDnsdist(t *testing.T) {
	root := repoRoot(t)
	dnsdist := dnsdistPorts(t, root)

	files, err := filepath.Glob(filepath.Join(root, "clusters", "*", "bootstrap-kit", "11-powerdns.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no bootstrap-kit 11-powerdns.yaml found (%v)", err)
	}
	checked := 0
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc["kind"] != "HelmRelease" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			values, _ := spec["values"].(map[string]any)
			if _, ok := values["anycast"]; !ok {
				continue
			}
			n, faults := anycastTargetFaults(values, dnsdist)
			if n == 0 {
				t.Errorf("%s: anycast names no port", f)
			}
			for _, fault := range faults {
				t.Errorf("%s: powerdns-anycast %s — the Service selects the dnsdist Pods, so this port has no endpoint", f, fault)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no bp-powerdns HelmRelease with anycast values found: the guard checked nothing")
	}

	// Each defect this guards against must fail it.
	bad := map[string][]any{
		"targetPort 5353": {
			map[string]any{"name": "dns-udp", "port": 53, "targetPort": 5353, "protocol": "UDP"},
			map[string]any{"name": "dns-tcp", "port": 53, "targetPort": 5353, "protocol": "TCP"},
		},
		"swapped named ports": {
			map[string]any{"name": "dns-udp", "port": 53, "targetPort": "dns-tcp", "protocol": "UDP"},
			map[string]any{"name": "dns-tcp", "port": 53, "targetPort": "dns-udp", "protocol": "TCP"},
		},
		"no name": {
			map[string]any{"port": 53, "targetPort": "dns-udp", "protocol": "UDP"},
			map[string]any{"port": 53, "targetPort": "dns-tcp", "protocol": "TCP"},
		},
	}
	for what, ports := range bad {
		if _, faults := anycastTargetFaults(map[string]any{"anycast": map[string]any{"ports": ports}}, dnsdist); len(faults) != 2 {
			t.Errorf("%s on both ports must give two faults, got %v", what, faults)
		}
	}
	good := []any{
		map[string]any{"name": "dns-udp", "port": 53, "targetPort": "dns-udp", "protocol": "UDP"},
		map[string]any{"name": "dns-tcp", "port": 53, "targetPort": 53, "protocol": "TCP"},
	}
	if _, faults := anycastTargetFaults(map[string]any{"anycast": map[string]any{"ports": good}}, dnsdist); len(faults) != 0 {
		t.Errorf("a named and a numeric target on dnsdist's ports must give no fault, got %v", faults)
	}
}
