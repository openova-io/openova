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
// it must reach a port that dnsdist listens on. The overlay
// clusters/_template/bootstrap-kit/11-powerdns.yaml set targetPort 5353, the
// port of the authoritative server behind dnsdist, so every public query to
// the Sovereign's name servers went to a port nothing listens on: UDP timed
// out and TCP was reset (check NET-G19 on hw307, 23 November 2026).

// dnsdistContainerPortRE finds one port of the dnsdist container in
// templates/dnsdist.yaml: its name and its containerPort.
var dnsdistContainerPortRE = regexp.MustCompile(`(?m)^\s*- name: (dns-[a-z]+)\s*\n\s*containerPort: ([0-9]+)\s*$`)

// dnsdistPorts reads the named container ports of dnsdist from the chart.
func dnsdistPorts(t *testing.T, root string) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "platform", "powerdns", "chart", "templates", "dnsdist.yaml")))
	if err != nil {
		t.Fatalf("read the dnsdist template: %v", err)
	}
	ports := map[string]int{}
	for _, m := range dnsdistContainerPortRE.FindAllStringSubmatch(string(raw), -1) {
		n, _ := strconv.Atoi(m[2])
		ports[m[1]] = n
	}
	if len(ports) == 0 {
		t.Fatal("the dnsdist template names no container port dns-*: the guard reads nothing")
	}
	return ports
}

// anycastTargetFaults returns, for the anycast ports of one bp-powerdns
// HelmRelease values block, each port whose targetPort is not a port that
// dnsdist listens on (by name or by number).
func anycastTargetFaults(values map[string]any, dnsdist map[string]int) (int, []string) {
	anycast, _ := values["anycast"].(map[string]any)
	ports, _ := anycast["ports"].([]any)
	listens := map[int]bool{}
	for _, n := range dnsdist {
		listens[n] = true
	}
	var faults []string
	for _, p := range ports {
		port, _ := p.(map[string]any)
		switch tp := port["targetPort"].(type) {
		case string:
			if _, ok := dnsdist[tp]; !ok {
				faults = append(faults, port["name"].(string)+": targetPort "+tp+" is no container port of dnsdist")
			}
		case int:
			if !listens[tp] {
				faults = append(faults, port["name"].(string)+": targetPort "+strconv.Itoa(tp)+" is no port dnsdist listens on")
			}
		default:
			faults = append(faults, "a port without a targetPort")
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
				t.Errorf("%s: powerdns-anycast %s — the Service selects the dnsdist Pods, so a query to the name servers reaches nothing", f, fault)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no bp-powerdns HelmRelease with anycast values found: the guard checked nothing")
	}

	// The defect this guards against must fail it.
	old := map[string]any{"anycast": map[string]any{"ports": []any{
		map[string]any{"name": "dns-udp", "port": 53, "targetPort": 5353, "protocol": "UDP"},
		map[string]any{"name": "dns-tcp", "port": 53, "targetPort": 5353, "protocol": "TCP"},
	}}}
	if _, faults := anycastTargetFaults(old, dnsdist); len(faults) != 2 {
		t.Errorf("targetPort 5353 on both ports must give two faults, got %v", faults)
	}
}
