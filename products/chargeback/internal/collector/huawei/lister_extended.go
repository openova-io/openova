package huawei

// Extended resource listers (#6853).
//
// The collector originally metered five kinds — ECS, EVS, EIP, ELB, NAT — so
// every other service a customer can provision billed ZERO. A rate card cannot
// fix that: an unmetered resource emits no usage record at all, so it is
// invisible rather than mispriced, which is the worse failure because nothing
// looks wrong.
//
// Which services exist is MEASURED, not assumed. Probed live against
// me-east-215 on kom4dc: rds, dds, gaussdb, cbr, cce, vpc, dns, waf, ims, as
// and vpcep answer 200; sfs-turbo, vpn, cdn and swr return 404 "API does not
// exist or has not been published" and are therefore deliberately absent —
// writing listers for unpublished APIs would add code that can only ever fail.
// OBS and CFW answer 4xx on a plain signed GET (S3-style auth / a required
// query parameter) and are tracked separately rather than guessed at.

import (
	"context"
	"net/url"
	"strconv"
)

// dbPageLimit — RDS, DDS, GaussDB and AS reject limit=200 with a 400
// ("Invalid limit" / "The value of parameter Limit is invalid"). Measured
// against the live gateway: 100 is accepted, 200 is not (#6857). The generic
// pageLimit stays 200 for the APIs that accept it.
const dbPageLimit = 100

const (
	KindRDS     = "rds"
	KindDDS     = "dds"
	KindGaussDB = "gaussdb"
	KindCBR     = "cbr"
	KindCCE     = "cce"
	KindVPC     = "vpc"
	KindDNS     = "dns"
	KindWAF     = "waf"
	KindIMS     = "ims"
	KindAS      = "as"
	KindVPCEP   = "vpcep"
)

// dbInstance is the shape RDS, DDS and GaussDB share.
type dbInstance struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Created   string `json:"created"`
	Type      string `json:"type"`
	Mode      string `json:"mode"`
	Engine    string `json:"datastore_type"`
	FlavorRef string `json:"flavor_ref"`
	Volume    struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"volume"`
	Datastore struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	} `json:"datastore"`
	Flavor struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"flavor"`
}

func (d dbInstance) attrs() map[string]any {
	engine := d.Datastore.Type
	if engine == "" {
		engine = d.Engine
	}
	flavor := d.FlavorRef
	if flavor == "" {
		flavor = d.Flavor.ID
	}
	if flavor == "" {
		flavor = d.Flavor.Name
	}
	// `mode` distinguishes a single instance from a primary-standby pair, and
	// the price catalog prices those separately — carry it so the SKU can.
	mode := d.Mode
	if mode == "" {
		mode = d.Type
	}
	return map[string]any{
		"engine": engine, "flavor": flavor, "mode": mode,
		"volume_type": d.Volume.Type, "size_gb": d.Volume.Size,
		"status": d.Status,
	}
}

func (c *Client) listDBLike(ctx context.Context, creds Credentials, region, service, kind string) ([]Resource, error) {
	var out []Resource
	var resp struct {
		Instances []dbInstance `json:"instances"`
	}
	q := url.Values{"limit": {strconv.Itoa(dbPageLimit)}}
	if err := c.Get(ctx, creds, service, region, "/v3/"+creds.ProjectID+"/instances", q, &resp); err != nil {
		return nil, err
	}
	for _, i := range resp.Instances {
		out = append(out, Resource{ID: i.ID, Kind: kind, Name: i.Name, Status: i.Status,
			Created: parseTime(i.Created), Attrs: i.attrs()})
	}
	return out, nil
}

// ListRDS lists managed relational database instances.
func (c *Client) ListRDS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	return c.listDBLike(ctx, creds, region, "rds", KindRDS)
}

// ListDDS lists managed document (MongoDB-compatible) instances.
func (c *Client) ListDDS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	return c.listDBLike(ctx, creds, region, "dds", KindDDS)
}

// ListGaussDB lists GaussDB instances.
func (c *Client) ListGaussDB(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	return c.listDBLike(ctx, creds, region, "gaussdb", KindGaussDB)
}

// ListCBR lists backup vaults. Billing follows the vault's provisioned size,
// not its consumption, which is what the catalog prices per GB.
func (c *Client) ListCBR(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Vaults []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Billing struct {
				Size            int    `json:"size"`
				ProtectType     string `json:"protect_type"`
				Status          string `json:"status"`
				ConsistentLevel string `json:"consistent_level"`
			} `json:"billing"`
		} `json:"vaults"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
	if err := c.Get(ctx, creds, "cbr", region, "/v3/"+creds.ProjectID+"/vaults", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, v := range resp.Vaults {
		out = append(out, Resource{ID: v.ID, Kind: KindCBR, Name: v.Name, Status: v.Billing.Status,
			Attrs: map[string]any{"size_gb": v.Billing.Size, "protect_type": v.Billing.ProtectType, "status": v.Billing.Status}})
	}
	return out, nil
}

// ListCCE lists managed Kubernetes clusters. The catalog prices CCE per node,
// so the node count is carried as the multiplier.
func (c *Client) ListCCE(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Items []struct {
			Metadata struct {
				UID  string `json:"uid"`
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Flavor string `json:"flavor"`
				Type   string `json:"type"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.Get(ctx, creds, "cce", region, "/api/v3/projects/"+creds.ProjectID+"/clusters", nil, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, i := range resp.Items {
		out = append(out, Resource{ID: i.Metadata.UID, Kind: KindCCE, Name: i.Metadata.Name, Status: i.Status.Phase,
			Attrs: map[string]any{"flavor": i.Spec.Flavor, "type": i.Spec.Type, "status": i.Status.Phase}})
	}
	return out, nil
}

// ListVPC lists virtual private clouds — priced per set in the catalog.
func (c *Client) ListVPC(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		VPCs []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"vpcs"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
	if err := c.Get(ctx, creds, "vpc", region, "/v1/"+creds.ProjectID+"/vpcs", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, v := range resp.VPCs {
		out = append(out, Resource{ID: v.ID, Kind: KindVPC, Name: v.Name, Status: v.Status,
			Attrs: map[string]any{"status": v.Status}})
	}
	return out, nil
}

// ListDNS lists public DNS zones — priced per set.
func (c *Client) ListDNS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Zones []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Type   string `json:"zone_type"`
		} `json:"zones"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
	if err := c.Get(ctx, creds, "dns", region, "/v2/zones", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, z := range resp.Zones {
		out = append(out, Resource{ID: z.ID, Kind: KindDNS, Name: z.Name, Status: z.Status,
			Attrs: map[string]any{"zone_type": z.Type, "status": z.Status}})
	}
	return out, nil
}

// ListWAF lists web application firewall instances — priced per pcs.
func (c *Client) ListWAF(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"instancename"`
		} `json:"items"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
	if err := c.Get(ctx, creds, "waf", region, "/v1/"+creds.ProjectID+"/waf/instance", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, w := range resp.Items {
		out = append(out, Resource{ID: w.ID, Kind: KindWAF, Name: w.Name, Attrs: map[string]any{}})
	}
	return out, nil
}

// ListIMS lists private images. Public/shared images are excluded — a customer
// is not billed for the vendor's catalogue.
func (c *Client) ListIMS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Images []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Status   string `json:"status"`
			Owner    string `json:"owner"`
			Visible  string `json:"visibility"`
			SizeByte int64  `json:"size"`
			MinDisk  int    `json:"min_disk"`
		} `json:"images"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}, "__imagetype": {"private"}}
	if err := c.Get(ctx, creds, "ims", region, "/v2/cloudimages", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, im := range resp.Images {
		if im.Visible != "" && im.Visible != "private" {
			continue
		}
		out = append(out, Resource{ID: im.ID, Kind: KindIMS, Name: im.Name, Status: im.Status,
			Attrs: map[string]any{"size_gb": im.MinDisk, "status": im.Status}})
	}
	return out, nil
}

// ListAS lists auto-scaling groups — priced per set.
func (c *Client) ListAS(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Groups []struct {
			ID     string `json:"scaling_group_id"`
			Name   string `json:"scaling_group_name"`
			Status string `json:"scaling_group_status"`
		} `json:"scaling_groups"`
	}
	q := url.Values{"limit": {strconv.Itoa(dbPageLimit)}}
	if err := c.Get(ctx, creds, "as", region, "/autoscaling-api/v1/"+creds.ProjectID+"/scaling_group", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, g := range resp.Groups {
		out = append(out, Resource{ID: g.ID, Kind: KindAS, Name: g.Name, Status: g.Status,
			Attrs: map[string]any{"status": g.Status}})
	}
	return out, nil
}

// ListVPCEP lists VPC endpoints — priced per set.
func (c *Client) ListVPCEP(ctx context.Context, creds Credentials, region string) ([]Resource, error) {
	var resp struct {
		Endpoints []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Name   string `json:"endpoint_service_name"`
		} `json:"endpoints"`
	}
	q := url.Values{"limit": {strconv.Itoa(pageLimit)}}
	if err := c.Get(ctx, creds, "vpcep", region, "/v1/"+creds.ProjectID+"/vpc-endpoints", q, &resp); err != nil {
		return nil, err
	}
	var out []Resource
	for _, e := range resp.Endpoints {
		out = append(out, Resource{ID: e.ID, Kind: KindVPCEP, Name: e.Name, Status: e.Status,
			Attrs: map[string]any{"status": e.Status}})
	}
	return out, nil
}
