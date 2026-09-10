package huawei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Resource kinds as stored in resource_inventory.kind.
const (
	KindECS = "ecs"
	KindEVS = "evs"
	KindEIP = "eip"
	KindELB = "elb"
	KindNAT = "nat"
	// KindBandwidth is a SHARED bandwidth object: a reserved pipe that
	// several Elastic IPs hang off. It is not listed on its own — ListEIP
	// produces it alongside the addresses — because the reservation must be
	// billed ONCE for the pipe, not once per attached address (#6867).
	KindBandwidth = "bandwidth"
)

// EIP billing shape, read from the bandwidth the address hangs off.
const (
	// attrBandwidthID joins an address to its bandwidth object.
	attrBandwidthID = "bandwidth_id"
	// attrChargeMode is how the cloud bills the pipe: ChargeModeBandwidth
	// (the reserved size, per hour) or ChargeModeTraffic (the bytes that
	// actually went out). Absent when the gateway did not report one.
	attrChargeMode = "bandwidth_charge_mode"
	// attrShareType is ShareTypeWhole when several addresses share one
	// pipe, ShareTypePer when the address has the pipe to itself.
	attrShareType = "bandwidth_share_type"

	// ChargeModeBandwidth bills the RESERVED size: eip.bandwidth_mbps.
	ChargeModeBandwidth = "bandwidth"
	// ChargeModeTraffic bills the OUTBOUND bytes: eip.traffic_gb. An
	// address on this mode reserves no pipe, so billing it one is not a
	// rounding error — it is a charge the cloud never made.
	ChargeModeTraffic = "traffic"

	// ShareTypeWhole is a shared bandwidth (Huawei's "WHOLE").
	ShareTypeWhole = "WHOLE"
	// ShareTypePer is a bandwidth dedicated to one address ("PER").
	ShareTypePer = "PER"
)

var pageLimit = 200

// listerFn lists one kind.
type listerFn func(*Client, context.Context, Credentials, string) ([]Resource, error)

// kindListers is THE registry of collectable kinds — one list, read by both
// ListAll and SupportedKinds. A second hand-written copy is how a newly added
// kind silently loses deletion tracking: its resources would never be marked
// deleted and would bill forever (#6853).
//
// `also` names the extra kinds a lister produces beyond its primary one.
// They belong in the registry for the same reason the primary does: the
// deletion sweep and the "nothing listed" check both derive from it, so a
// kind missing here is a kind whose resources are never marked deleted and
// bill forever.
var kindListers = []struct {
	kind string
	also []string
	fn   listerFn
}{
	{kind: KindECS, fn: (*Client).ListECS},
	{kind: KindEVS, fn: (*Client).ListEVS},
	{kind: KindEIP, also: []string{KindBandwidth}, fn: (*Client).ListEIP},
	{kind: KindELB, fn: (*Client).ListELB},
	{kind: KindNAT, fn: (*Client).ListNAT},
	// #6853 — every other service a customer can provision. An unmetered
	// resource emits no usage record at all, so it bills zero while nothing
	// looks wrong; that is worse than a mispriced one.
	{kind: KindRDS, fn: (*Client).ListRDS},
	{kind: KindDDS, fn: (*Client).ListDDS},
	{kind: KindGaussDB, fn: (*Client).ListGaussDB},
	{kind: KindCBR, fn: (*Client).ListCBR},
	{kind: KindCCE, fn: (*Client).ListCCE},
	{kind: KindVPC, fn: (*Client).ListVPC},
	{kind: KindDNS, fn: (*Client).ListDNS},
	{kind: KindWAF, fn: (*Client).ListWAF},
	{kind: KindIMS, fn: (*Client).ListIMS},
	{kind: KindAS, fn: (*Client).ListAS},
	{kind: KindVPCEP, fn: (*Client).ListVPCEP},
}

// SupportedKinds returns every resource kind the collector lists.
func SupportedKinds() []string {
	out := make([]string, 0, len(kindListers))
	for _, k := range kindListers {
		out = append(out, k.kind)
		out = append(out, k.also...)
	}
	return out
}

// Resource is one cloud resource as observed by a list call.
type Resource struct {
	ID      string
	Kind    string
	Name    string
	Status  string
	Created time.Time // zero when the API did not return one
	Attrs   map[string]any
}

// Verify performs the activation check: one signed ECS list call with
// limit=1. A 2xx means the credentials work for this project; the returned
// error is a *GatewayError for 401/403/404 so callers can store its code.
func (c *Client) Verify(ctx context.Context, creds Credentials, region string) error {
	q := url.Values{"limit": {"1"}}
	var out struct {
		Count int `json:"count"`
	}
	return c.Get(ctx, creds, "ecs", region, "/v1/"+creds.ProjectID+"/cloudservers/detail", q, &out)
}

// ListAll returns the inventory of every supported kind. Kinds whose list
// call failed are reported in failed (kind -> error) and omitted from the
// result so the caller does not mark their resources deleted.
func (c *Client) ListAll(ctx context.Context, creds Credentials, region string) (resources []Resource, failed map[string]error) {
	failed = map[string]error{}
	for _, k := range kindListers {
		rs, err := k.fn(c, ctx, creds, region)
		if err != nil {
			// Every kind this lister produces failed, not just its primary
			// one — otherwise the caller's "nothing listed" comparison
			// against SupportedKinds() could never reach equality, and a
			// source with dead credentials would report healthy (#6853).
			failed[k.kind] = err
			for _, also := range k.also {
				failed[also] = err
			}
			continue
		}
		resources = append(resources, rs...)
	}
	return resources, failed
}

// ListECS pages GET ecs /v1/{pid}/cloudservers/detail?limit=200&offset=N
// (offset is the 1-based page number on this API).
func (c *Client) ListECS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var out []Resource
	for page := 1; ; page++ {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}, "offset": {strconv.Itoa(page)}}
		var resp struct {
			Servers []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Status  string `json:"status"`
				Created string `json:"created"`
				Flavor  struct {
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					VCPUs json.RawMessage `json:"vcpus"`
					RAM   json.RawMessage `json:"ram"`
				} `json:"flavor"`
				// tags arrive as ["key=value", ...] on this API (see tags.go).
				Tags                json.RawMessage `json:"tags"`
				EnterpriseProjectID string          `json:"enterprise_project_id"`
			} `json:"servers"`
		}
		if err := c.Get(ctx, creds, "ecs", region, "/v1/"+creds.ProjectID+"/cloudservers/detail", q, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Servers {
			flavor := s.Flavor.Name
			if flavor == "" {
				flavor = s.Flavor.ID
			}
			attrs := map[string]any{
				"flavor": flavor,
				"vcpus":  rawNumber(s.Flavor.VCPUs),
				"ram_mb": rawNumber(s.Flavor.RAM),
				"status": s.Status,
			}
			putTagAttrs(attrs, s.Tags, s.EnterpriseProjectID)
			out = append(out, Resource{
				ID: s.ID, Kind: KindECS, Name: s.Name, Status: s.Status, Created: parseTime(s.Created),
				Attrs: attrs,
			})
		}
		if len(resp.Servers) < pageLimit {
			break
		}
	}
	return out, nil
}

// ListEVS pages GET evs /v2/{pid}/cloudvolumes/detail?limit=200&offset=N
// (offset is an item offset on this API).
func (c *Client) ListEVS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var out []Resource
	for offset := 0; ; offset += pageLimit {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}, "offset": {strconv.Itoa(offset)}}
		var resp struct {
			Volumes []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Size        int    `json:"size"`
				VolumeType  string `json:"volume_type"`
				Status      string `json:"status"`
				CreatedAt   string `json:"created_at"`
				Attachments []struct {
					ServerID string `json:"server_id"`
					Device   string `json:"device"`
				} `json:"attachments"`
				// tags arrive as an OBJECT {"key": "value"} on this API.
				Tags                json.RawMessage `json:"tags"`
				EnterpriseProjectID string          `json:"enterprise_project_id"`
			} `json:"volumes"`
		}
		if err := c.Get(ctx, creds, "evs", region, "/v2/"+creds.ProjectID+"/cloudvolumes/detail", q, &resp); err != nil {
			return nil, err
		}
		for _, v := range resp.Volumes {
			attrs := map[string]any{
				"size_gb":     v.Size,
				"volume_type": v.VolumeType,
				"status":      v.Status,
			}
			if len(v.Attachments) > 0 {
				attrs["attached_to"] = v.Attachments[0].ServerID
			}
			putTagAttrs(attrs, v.Tags, v.EnterpriseProjectID)
			out = append(out, Resource{ID: v.ID, Kind: KindEVS, Name: v.Name, Status: v.Status, Created: parseTime(v.CreatedAt), Attrs: attrs})
		}
		if len(resp.Volumes) < pageLimit {
			break
		}
	}
	return out, nil
}

// Bandwidth is one VPC bandwidth object — the pipe an Elastic IP hangs off.
// The BILLING SHAPE of an address lives here, not on the address: whether
// the pipe is charged by its reserved size or by the traffic that crosses
// it, and whether it is dedicated to one address or shared by several.
type Bandwidth struct {
	ID         string
	Name       string
	Size       int    // reserved Mbps
	ShareType  string // ShareTypeWhole (shared) | ShareTypePer (dedicated)
	ChargeMode string // ChargeModeBandwidth | ChargeModeTraffic
	Status     string
	PublicIPs  []string // ids of the addresses attached to it
	Addresses  []string // their IP addresses, for the operator to read
}

// Shared reports whether several addresses may hang off this pipe, in which
// case its reservation is billed once against the pipe itself.
func (b Bandwidth) Shared() bool { return strings.EqualFold(b.ShareType, ShareTypeWhole) }

// ListBandwidths pages GET vpc /v1/{pid}/bandwidths?limit=200&marker=<last id>.
// The charge mode and share type it returns are what decide whether an
// address bills its reservation or its traffic.
func (c *Client) ListBandwidths(ctx context.Context, creds Credentials, region string) ([]Bandwidth, error) {
	var out []Bandwidth
	marker := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
		if marker != "" {
			q.Set("marker", marker)
		}
		var resp struct {
			Bandwidths []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				Size         int    `json:"size"`
				ShareType    string `json:"share_type"`
				ChargeMode   string `json:"charge_mode"`
				Status       string `json:"status"`
				PublicIPInfo []struct {
					PublicIPID      string `json:"publicip_id"`
					PublicIPAddress string `json:"publicip_address"`
				} `json:"publicip_info"`
			} `json:"bandwidths"`
		}
		if err := c.Get(ctx, creds, "vpc", region, "/v1/"+creds.ProjectID+"/bandwidths", q, &resp); err != nil {
			return nil, err
		}
		for _, b := range resp.Bandwidths {
			bw := Bandwidth{ID: b.ID, Name: b.Name, Size: b.Size, Status: b.Status,
				ShareType:  strings.ToUpper(strings.TrimSpace(b.ShareType)),
				ChargeMode: strings.ToLower(strings.TrimSpace(b.ChargeMode))}
			for _, ip := range b.PublicIPInfo {
				bw.PublicIPs = append(bw.PublicIPs, ip.PublicIPID)
				bw.Addresses = append(bw.Addresses, ip.PublicIPAddress)
			}
			out = append(out, bw)
		}
		if len(resp.Bandwidths) < pageLimit {
			break
		}
		marker = resp.Bandwidths[len(resp.Bandwidths)-1].ID
	}
	return out, nil
}

// ListEIP pages GET vpc /v1/{pid}/publicips?limit=200&marker=<last id>, and
// joins each address to its bandwidth so the collector knows HOW the cloud
// bills it (#6867). It returns two kinds: the addresses, and one synthetic
// KindBandwidth resource per SHARED pipe, which is where that pipe's
// reservation is billed — once, however many addresses hang off it.
func (c *Client) ListEIP(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	bandwidths, err := c.ListBandwidths(ctx, creds, region)
	if err != nil {
		var ge *GatewayError
		if !errors.As(err, &ge) || !ge.NotPublished() {
			// A transient failure or a rejected credential must NOT be read
			// as "this project has no bandwidths": that would mark every
			// shared pipe deleted and stop billing it. Fail the whole kind
			// instead, which leaves the inventory exactly as it was.
			return nil, err
		}
		// The gateway does not publish the bandwidths API. Then no address
		// has a reported charge mode, and every one of them keeps billing
		// its reservation exactly as before — an older gateway is never
		// silently under-billed.
		bandwidths = nil
	}
	byID := make(map[string]Bandwidth, len(bandwidths))
	for _, b := range bandwidths {
		byID[b.ID] = b
	}

	var out []Resource
	marker := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
		if marker != "" {
			q.Set("marker", marker)
		}
		var resp struct {
			PublicIPs []struct {
				ID            string `json:"id"`
				Address       string `json:"public_ip_address"`
				BandwidthID   string `json:"bandwidth_id"`
				BandwidthSize int    `json:"bandwidth_size"`
				BandwidthName string `json:"bandwidth_name"`
				// Some gateway builds report the share type on the address
				// as well as on the bandwidth; either is accepted.
				BandwidthShareType string `json:"bandwidth_share_type"`
				Status             string `json:"status"`
				CreateTime         string `json:"create_time"`
				Type               string `json:"type"`
				// tags arrive as ["key=value", ...] on this API.
				Tags                json.RawMessage `json:"tags"`
				EnterpriseProjectID string          `json:"enterprise_project_id"`
			} `json:"publicips"`
		}
		if err := c.Get(ctx, creds, "vpc", region, "/v1/"+creds.ProjectID+"/publicips", q, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.PublicIPs {
			// #6859 — an EIP on this cloud has NO name of its own (the
			// Name field above is its IP address), so deployment
			// attribution runs through the bandwidth's name
			// ("catalyst-<sovereign>-<depid>-...-bw" vs
			// "bastion-openova-bw"). Without capturing it, ScopeMatcher
			// cannot attribute ANY EIP and excludes every one of them —
			// silently under-billing, which is as wrong as over-billing.
			bw := byID[e.BandwidthID]
			size := e.BandwidthSize
			if size == 0 {
				size = bw.Size
			}
			name := e.BandwidthName
			if name == "" {
				name = bw.Name
			}
			attrs := map[string]any{"public_ip_address": e.Address, "bandwidth_mbps": size, "bandwidth_name": name, "status": e.Status, "type": e.Type}
			if e.BandwidthID != "" {
				attrs[attrBandwidthID] = e.BandwidthID
			}
			// Only ever RECORDED when reported. An absent charge mode is
			// what keeps today's reservation billing in force (see above),
			// so writing a guessed default here would be the silent
			// under-billing this capture exists to prevent.
			if bw.ChargeMode != "" {
				attrs[attrChargeMode] = bw.ChargeMode
			}
			if st := shareTypeOf(e.BandwidthShareType, bw.ShareType); st != "" {
				attrs[attrShareType] = st
			}
			putTagAttrs(attrs, e.Tags, e.EnterpriseProjectID)
			out = append(out, Resource{
				ID: e.ID, Kind: KindEIP, Name: e.Address, Status: e.Status, Created: parseTime(e.CreateTime),
				Attrs: attrs,
			})
		}
		if len(resp.PublicIPs) < pageLimit {
			break
		}
		marker = resp.PublicIPs[len(resp.PublicIPs)-1].ID
	}

	// One resource per SHARED pipe. Its reservation is real whether or not
	// an address is attached right now, and it is billed here exactly once
	// — the addresses attached to it emit no reservation of their own.
	for _, b := range bandwidths {
		if !b.Shared() {
			continue
		}
		attrs := map[string]any{
			"bandwidth_mbps": b.Size, "bandwidth_name": b.Name, attrBandwidthID: b.ID,
			attrShareType: ShareTypeWhole, "status": b.Status,
			"public_ip_count": len(b.PublicIPs),
		}
		if b.ChargeMode != "" {
			attrs[attrChargeMode] = b.ChargeMode
		}
		if len(b.Addresses) > 0 {
			attrs["public_ip_addresses"] = b.Addresses
		}
		out = append(out, Resource{ID: b.ID, Kind: KindBandwidth, Name: b.Name, Status: b.Status, Attrs: attrs})
	}
	return out, nil
}

// shareTypeOf normalises the share type, preferring the address own report
// over the bandwidth one; empty when neither reported one.
func shareTypeOf(onAddress, onBandwidth string) string {
	for _, v := range []string{onAddress, onBandwidth} {
		if v = strings.ToUpper(strings.TrimSpace(v)); v != "" {
			return v
		}
	}
	return ""
}

// ListELB pages GET elb /v3/{pid}/elb/loadbalancers?limit=200&marker=<next>.
func (c *Client) ListELB(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var out []Resource
	marker := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
		if marker != "" {
			q.Set("marker", marker)
		}
		var resp struct {
			LoadBalancers []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
				Status    string `json:"provisioning_status"`
				// tags arrive as [{"key": ..., "value": ...}] on this API.
				Tags                json.RawMessage `json:"tags"`
				EnterpriseProjectID string          `json:"enterprise_project_id"`
			} `json:"loadbalancers"`
			PageInfo struct {
				NextMarker string `json:"next_marker"`
			} `json:"page_info"`
		}
		if err := c.Get(ctx, creds, "elb", region, "/v3/"+creds.ProjectID+"/elb/loadbalancers", q, &resp); err != nil {
			return nil, err
		}
		for _, lb := range resp.LoadBalancers {
			attrs := map[string]any{"status": lb.Status}
			putTagAttrs(attrs, lb.Tags, lb.EnterpriseProjectID)
			out = append(out, Resource{ID: lb.ID, Kind: KindELB, Name: lb.Name, Status: lb.Status, Created: parseTime(lb.CreatedAt), Attrs: attrs})
		}
		if resp.PageInfo.NextMarker == "" || len(resp.LoadBalancers) < pageLimit {
			break
		}
		marker = resp.PageInfo.NextMarker
	}
	return out, nil
}

// ListNAT pages GET nat /v2/{pid}/nat_gateways?limit=200&marker=<last id>.
func (c *Client) ListNAT(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var out []Resource
	marker := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
		if marker != "" {
			q.Set("marker", marker)
		}
		var resp struct {
			NATGateways []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Spec      string `json:"spec"`
				Status    string `json:"status"`
				CreatedAt string `json:"created_at"`
			} `json:"nat_gateways"`
		}
		if err := c.Get(ctx, creds, "nat", region, "/v2/"+creds.ProjectID+"/nat_gateways", q, &resp); err != nil {
			return nil, err
		}
		for _, n := range resp.NATGateways {
			out = append(out, Resource{ID: n.ID, Kind: KindNAT, Name: n.Name, Status: n.Status, Created: parseTime(n.CreatedAt), Attrs: map[string]any{"spec": n.Spec, "status": n.Status}})
		}
		if len(resp.NATGateways) < pageLimit {
			break
		}
		marker = resp.NATGateways[len(resp.NATGateways)-1].ID
	}
	return out, nil
}

// parseTime accepts the timestamp shapes the Huawei APIs emit.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// rawNumber turns a JSON number-or-string into an int64 (0 when unparseable):
// flavor.vcpus and flavor.ram arrive as strings on some builds, numbers on others.
func rawNumber(raw json.RawMessage) int64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

// String renders a resource for logs (no credentials are involved).
func (r Resource) String() string {
	return fmt.Sprintf("%s/%s(%s)", r.Kind, r.ID, r.Name)
}
