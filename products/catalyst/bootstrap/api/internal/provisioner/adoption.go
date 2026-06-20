package provisioner

// adoption.go — the OpenTofu→Crossplane adoption-claim GENERATOR
// (ADR-0011, Refs #4002). This is the producer that was MISSING: before
// this file, provisioner.go only *commented* "// Crossplane adoption
// (Phase 1 hand-off)" and nothing ever handed the OpenTofu-created cloud
// resources to Crossplane — so Crossplane owned ZERO cloud resources on
// every Sovereign and the day-2 IaC layer was inert.
//
// After `tofu apply`, GenerateAdoptionClaims reads the OpenTofu STATE
// (terraform.tfstate — which carries every resource's real cloud id +
// attributes, unlike `tofu output` which only surfaces a couple of IPs)
// and emits one `CloudAdoption` (compose.openova.io/v1alpha1) per adopted
// resource, each annotated `crossplane.io/external-name=<cloud id>` and
// Observe-first. Crossplane's provider-opentofu then OBSERVES the live
// resource by id — binding Crossplane to the real infra WITHOUT
// re-provisioning it.
//
// The function is pure (tfstate bytes in, YAML out) so it is unit-tested
// and runnable standalone to backfill an already-provisioned Sovereign
// (used for the hw173 live proof). Provision() calls it post-readOutputs
// and writes adoption-claims.yaml into the deploy workdir.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// adoptableResource is one cloud resource we emit a CloudAdoption for.
type adoptableResource struct {
	Name         string // CloudAdoption metadata.name (k8s-safe)
	ResourceKind string // CloudAdoption spec.parameters.resourceKind
	ResourceID   string // the real cloud id (→ crossplane.io/external-name)
	Address      string // public IP/EIP if any (for the comment + console)
}

// tfStateResource is the slice of terraform.tfstate we care about.
type tfStateResource struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Instances []struct {
		IndexKey   any            `json:"index_key"`
		Attributes map[string]any `json:"attributes"`
	} `json:"instances"`
}

type tfState struct {
	Resources []tfStateResource `json:"resources"`
}

// tfTypeToAdoptionKind maps a Terraform/OpenTofu resource type to the
// CloudAdoption resourceKind enum. Only the resource families a Sovereign
// adopts day-2 are listed; everything else in state (data sources, NAT
// rules, route tables, OBS buckets …) is intentionally skipped — those are
// either not cloud-owned day-2 surfaces or are managed by their own path.
var tfTypeToAdoptionKind = map[string]string{
	// Huawei (HCS)
	"huaweicloud_elb_loadbalancer": "loadbalancer",
	"huaweicloud_compute_instance": "server",
	"huaweicloud_vpc":              "network",
	"huaweicloud_vpc_subnet":       "subnet",
	"huaweicloud_vpc_eip":          "eip",
	// Hetzner
	"hcloud_load_balancer": "loadbalancer",
	"hcloud_server":        "server",
	"hcloud_network":       "network",
	"hcloud_firewall":      "firewall",
	"hcloud_primary_ip":    "eip",
}

// adoptionResourceAddress pulls the public address out of an instance's
// attributes, trying the per-cloud/per-kind field names in priority order.
func adoptionResourceAddress(attrs map[string]any) string {
	if s, ok := attrs["ipv4_address"].(string); ok && s != "" { // hcloud_server
		return s
	}
	if s, ok := attrs["ipv4"].(string); ok && s != "" { // hcloud_load_balancer
		return s
	}
	if s, ok := attrs["public_ip"].(string); ok && s != "" { // huawei ECS
		return s
	}
	if s, ok := attrs["address"].(string); ok && s != "" { // huawei EIP
		return s
	}
	// huaweicloud_vpc_eip nests the address under publicip[0].ip_address
	if pub, ok := attrs["publicip"].([]any); ok && len(pub) > 0 {
		if m, ok := pub[0].(map[string]any); ok {
			if s, ok := m["ip_address"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// indexKeyString renders a tfstate index_key (string for for_each, number
// for count, nil for singleton) into a short, k8s-name-safe suffix.
func indexKeyString(k any) string {
	switch v := k.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int(v))
	default:
		return fmt.Sprintf("%v", v)
	}
}

// k8sName sanitises an arbitrary string into a DNS-1123 label fragment.
func k8sName(parts ...string) string {
	s := strings.ToLower(strings.Join(parts, "-"))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

// collectAdoptableResources walks the parsed tfstate and returns the
// resources we emit CloudAdoptions for, deterministically ordered.
func collectAdoptableResources(st *tfState) []adoptableResource {
	var res []adoptableResource
	for _, r := range st.Resources {
		kind, ok := tfTypeToAdoptionKind[r.Type]
		if !ok {
			continue
		}
		for _, inst := range r.Instances {
			id, _ := inst.Attributes["id"].(string)
			if id == "" {
				continue
			}
			suffix := indexKeyString(inst.IndexKey)
			nameParts := []string{kind, r.Name}
			if suffix != "" {
				nameParts = append(nameParts, suffix)
			}
			res = append(res, adoptableResource{
				Name:         k8sName(append([]string{"adopt"}, nameParts...)...),
				ResourceKind: kind,
				ResourceID:   id,
				Address:      adoptionResourceAddress(inst.Attributes),
			})
		}
	}
	// Deterministic order: by kind, then id. Makes the generated file
	// stable across re-runs (no spurious GitOps diffs) and the unit test
	// assertable.
	sort.Slice(res, func(i, j int) bool {
		if res[i].ResourceKind != res[j].ResourceKind {
			return res[i].ResourceKind < res[j].ResourceKind
		}
		return res[i].ResourceID < res[j].ResourceID
	})
	return res
}

// GenerateAdoptionClaims renders the multi-document adoption-claims.yaml
// for one Sovereign from its OpenTofu state.
//
//   - tfstateJSON: the raw bytes of the deploy workdir's terraform.tfstate
//   - sovereignFQDN: stamped as the catalyst.openova.io/sovereign label
//   - cloud: "huawei" | "hetzner" — selects the CloudAdoption spec.cloud
//   - region: cloud region/location code (may be "")
//
// Every emitted CloudAdoption is Observe-first (manage:false) so applying
// the file can NEVER re-provision or delete the live infra. Returns an
// error only on unparseable state; an empty/no-adoptable-resources state
// yields a valid header-only document (not an error) so the GitOps path
// stays green.
func GenerateAdoptionClaims(tfstateJSON []byte, sovereignFQDN, cloud, region string) ([]byte, error) {
	var st tfState
	if err := json.Unmarshal(tfstateJSON, &st); err != nil {
		return nil, fmt.Errorf("parse terraform.tfstate: %w", err)
	}
	if cloud == "" {
		cloud = "huawei"
	}
	resources := collectAdoptableResources(&st)

	var b strings.Builder
	fmt.Fprintf(&b, "# adoption-claims.yaml — GENERATED by internal/provisioner/adoption.go\n")
	fmt.Fprintf(&b, "# (ADR-0011, Refs #4002). DO NOT hand-edit — re-generated from\n")
	fmt.Fprintf(&b, "# terraform.tfstate at every Phase-1 hand-off.\n")
	fmt.Fprintf(&b, "#\n")
	fmt.Fprintf(&b, "# Sovereign: %s   cloud: %s   region: %s\n", sovereignFQDN, cloud, region)
	fmt.Fprintf(&b, "# Adopted resources: %d. Each CloudAdoption is Observe-first —\n", len(resources))
	fmt.Fprintf(&b, "# Crossplane observes the live cloud resource by external-name and\n")
	fmt.Fprintf(&b, "# never re-provisions it (managementPolicies:[Observe]).\n")

	if len(resources) == 0 {
		// No adoptable resources (e.g. an empty/destroyed state). Emit a
		// valid, applies-cleanly document with no CloudAdoption objects.
		fmt.Fprintf(&b, "#\n# (no adoptable resources found in state)\n")
		return []byte(b.String()), nil
	}

	for _, r := range resources {
		addr := r.Address
		if addr == "" {
			addr = "(no public address)"
		}
		fmt.Fprintf(&b, "---\n")
		fmt.Fprintf(&b, "# %s %s — %s\n", cloud, r.ResourceKind, addr)
		fmt.Fprintf(&b, "apiVersion: compose.openova.io/v1alpha1\n")
		fmt.Fprintf(&b, "kind: CloudAdoption\n")
		fmt.Fprintf(&b, "metadata:\n")
		fmt.Fprintf(&b, "  name: %s\n", r.Name)
		fmt.Fprintf(&b, "  namespace: crossplane-system\n")
		fmt.Fprintf(&b, "  labels:\n")
		fmt.Fprintf(&b, "    catalyst.openova.io/sovereign: %s\n", labelSafe(sovereignFQDN))
		fmt.Fprintf(&b, "    catalyst.openova.io/adoption-seam: \"true\"\n")
		fmt.Fprintf(&b, "    catalyst.openova.io/resource-kind: %s\n", r.ResourceKind)
		fmt.Fprintf(&b, "  annotations:\n")
		fmt.Fprintf(&b, "    crossplane.io/external-name: %s\n", r.ResourceID)
		fmt.Fprintf(&b, "spec:\n")
		fmt.Fprintf(&b, "  parameters:\n")
		fmt.Fprintf(&b, "    cloud: %s\n", cloud)
		fmt.Fprintf(&b, "    resourceKind: %s\n", r.ResourceKind)
		fmt.Fprintf(&b, "    resourceId: %s\n", r.ResourceID)
		if region != "" {
			fmt.Fprintf(&b, "    region: %s\n", region)
		}
		fmt.Fprintf(&b, "    sovereignFQDN: %s\n", sovereignFQDN)
		fmt.Fprintf(&b, "    manage: false\n")
		fmt.Fprintf(&b, "  providerConfigRef:\n")
		fmt.Fprintf(&b, "    name: default\n")
	}
	return []byte(b.String()), nil
}

// writeAdoptionClaims is the Provision()-side wiring: it reads the deploy
// workdir's terraform.tfstate, generates the adoption-claims.yaml, and
// writes it back into the workdir. The catalyst-api's GitOps mirror (the
// same per-Sovereign aggregator path the catalog-edit flow uses) picks the
// file up into clusters/<fqdn>/infrastructure/ where Flux reconciles it.
//
// Best-effort: returns an error for the caller to log + swallow. A failure
// here must never fail an otherwise-successful provision — the seam
// degrades to "Crossplane adopts on the next reconcile" rather than
// breaking Phase 1.
func (p *Provisioner) writeAdoptionClaims(deployDir string, req Request, emit func(string, string, string)) error {
	statePath := filepath.Join(deployDir, "terraform.tfstate")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", statePath, err)
	}
	region := strings.TrimSpace(req.HuaweiRegion)
	if region == "" && len(req.Regions) > 0 {
		region = strings.TrimSpace(req.Regions[0].CloudRegion)
	}
	cloud := strings.TrimSpace(req.Provider)
	if cloud == "" {
		cloud = "huawei"
	}
	yaml, err := GenerateAdoptionClaims(raw, req.SovereignFQDN, cloud, region)
	if err != nil {
		return err
	}
	outPath := filepath.Join(deployDir, "adoption-claims.yaml")
	if err := os.WriteFile(outPath, yaml, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	emit("crossplane-adoption", "info", fmt.Sprintf(
		"Generated Observe-first Crossplane adoption claims from OpenTofu state → %s. "+
			"Crossplane (provider-opentofu) will observe the live cloud resources by external-name (ADR-0011, #4002).",
		outPath))
	return nil
}

// labelSafe truncates/sanitises a value for use as a k8s label value
// (max 63 chars, must start/end alphanumeric). FQDNs contain dots which
// are legal in label VALUES, so we only enforce length + edge chars.
func labelSafe(s string) string {
	if len(s) > 63 {
		s = s[:63]
	}
	return strings.Trim(s, "-._")
}
