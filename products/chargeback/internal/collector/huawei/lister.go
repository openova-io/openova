package huawei

import (
	"context"
	"encoding/json"
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
)

var pageLimit = 200

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
	kinds := []struct {
		kind string
		fn   func(context.Context, Credentials, string) ([]Resource, error)
	}{
		{KindECS, c.ListECS},
		{KindEVS, c.ListEVS},
		{KindEIP, c.ListEIP},
		{KindELB, c.ListELB},
		{KindNAT, c.ListNAT},
	}
	for _, k := range kinds {
		rs, err := k.fn(ctx, creds, region)
		if err != nil {
			failed[k.kind] = err
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
			out = append(out, Resource{
				ID: s.ID, Kind: KindECS, Name: s.Name, Status: s.Status, Created: parseTime(s.Created),
				Attrs: map[string]any{
					"flavor": flavor,
					"vcpus":  rawNumber(s.Flavor.VCPUs),
					"ram_mb": rawNumber(s.Flavor.RAM),
					"status": s.Status,
				},
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
			out = append(out, Resource{ID: v.ID, Kind: KindEVS, Name: v.Name, Status: v.Status, Created: parseTime(v.CreatedAt), Attrs: attrs})
		}
		if len(resp.Volumes) < pageLimit {
			break
		}
	}
	return out, nil
}

// ListEIP pages GET vpc /v1/{pid}/publicips?limit=200&marker=<last id>.
func (c *Client) ListEIP(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
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
				BandwidthSize int    `json:"bandwidth_size"`
				Status        string `json:"status"`
				CreateTime    string `json:"create_time"`
				Type          string `json:"type"`
			} `json:"publicips"`
		}
		if err := c.Get(ctx, creds, "vpc", region, "/v1/"+creds.ProjectID+"/publicips", q, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.PublicIPs {
			out = append(out, Resource{
				ID: e.ID, Kind: KindEIP, Name: e.Address, Status: e.Status, Created: parseTime(e.CreateTime),
				Attrs: map[string]any{"public_ip_address": e.Address, "bandwidth_mbps": e.BandwidthSize, "status": e.Status, "type": e.Type},
			})
		}
		if len(resp.PublicIPs) < pageLimit {
			break
		}
		marker = resp.PublicIPs[len(resp.PublicIPs)-1].ID
	}
	return out, nil
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
			} `json:"loadbalancers"`
			PageInfo struct {
				NextMarker string `json:"next_marker"`
			} `json:"page_info"`
		}
		if err := c.Get(ctx, creds, "elb", region, "/v3/"+creds.ProjectID+"/elb/loadbalancers", q, &resp); err != nil {
			return nil, err
		}
		for _, lb := range resp.LoadBalancers {
			out = append(out, Resource{ID: lb.ID, Kind: KindELB, Name: lb.Name, Status: lb.Status, Created: parseTime(lb.CreatedAt), Attrs: map[string]any{"status": lb.Status}})
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
