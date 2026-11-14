package source

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TypeNodes is the type of the source that lists the Nodes of the cluster.
const TypeNodes = "nodes"

// Nodes lists the Kubernetes Nodes of the cluster it runs in. Its reader is the
// controller manager's client, so the list comes from the shared Node informer.
type Nodes struct {
	Reader client.Reader
	Now    func() time.Time
}

// Type returns "nodes".
func (n *Nodes) Type() string { return TypeNodes }

// Enumerate lists every Node. The one scope is "nodes"; a failed list is an error and
// nothing is returned, so the listing is complete whenever it is returned.
func (n *Nodes) Enumerate(ctx context.Context) (Enumeration, error) {
	now := time.Now
	if n.Now != nil {
		now = n.Now
	}
	started := now()
	var list corev1.NodeList
	if err := n.Reader.List(ctx, &list); err != nil {
		return Enumeration{}, fmt.Errorf("nodes: list: %w", err)
	}
	sightings := make([]Sighting, 0, len(list.Items))
	for i := range list.Items {
		sightings = append(sightings, NodeSighting(&list.Items[i]))
	}
	sort.Slice(sightings, func(i, j int) bool { return sightings[i].ExternalID < sightings[j].ExternalID })
	return Enumeration{
		StartedAt:  started,
		FinishedAt: now(),
		Complete:   true,
		Scopes:     []ScopeResult{{Name: TypeNodes, Readable: true, Count: len(sightings)}},
		Sightings:  sightings,
	}, nil
}

// NodeSighting turns one Node into a Sighting of class node.
func NodeSighting(node *corev1.Node) Sighting {
	info := node.Status.NodeInfo
	family, version := ParseOSImage(info.OSImage)
	s := Sighting{
		ExternalID:  node.Name,
		DisplayName: node.Name,
		Class:       ClassNode,
		PowerState:  PowerUnknown,
		ProviderID:  node.Spec.ProviderID,
		Fingerprint: Fingerprint{FirmwareUUID: info.SystemUUID, MachineID: info.MachineID},
		OSFamily:    family,
		OSVersion:   version,
		Arch:        Arch(info.Architecture),
		Kernel:      info.KernelVersion,
	}
	for _, a := range node.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP:
			s.Addresses = append(s.Addresses, Address{Type: "Private", IP: a.Address})
		case corev1.NodeExternalIP:
			s.Addresses = append(s.Addresses, Address{Type: "Public", IP: a.Address})
		}
	}
	return s
}

// PrimaryAddress is the address a run connects to: the first Private address, or "".
func PrimaryAddress(addresses []Address) string {
	for _, a := range addresses {
		if a.Type == "Private" {
			return a.IP
		}
	}
	return ""
}

// osFamilies maps the start of a Node's osImage to the family names of the image
// catalogue. The first prefix that matches wins.
var osFamilies = []struct{ prefix, family string }{
	{"Ubuntu ", "ubuntu"},
	{"Debian GNU/Linux ", "debian"},
	{"Red Hat Enterprise Linux ", "rhel"},
	{"Rocky Linux ", "rocky"},
	{"AlmaLinux ", "almalinux"},
	{"Oracle Linux Server ", "oraclelinux"},
	{"SUSE Linux Enterprise Server ", "sles"},
	{"Windows Server ", "windows"},
}

var osVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?`)

// ParseOSImage reads a Node's osImage. "Ubuntu 22.04.4 LTS" gives "ubuntu", "22.04":
// the version is the first number, with its first dot part if it has one. An image
// whose start is not known, or that has no number after its name, gives "unknown", "".
func ParseOSImage(image string) (family, version string) {
	for _, f := range osFamilies {
		if strings.HasPrefix(image, f.prefix) {
			if v := osVersion.FindString(strings.TrimPrefix(image, f.prefix)); v != "" {
				return f.family, v
			}
			return "unknown", ""
		}
	}
	return "unknown", ""
}

// Arch maps a Node's architecture to the names of the image catalogue: amd64 is
// x86_64 and arm64 is aarch64. Any other value is kept as it is.
func Arch(nodeArch string) string {
	switch nodeArch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return nodeArch
}
