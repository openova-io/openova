// leftovers.go — the read-only leftover lister (providers.LeftoverLister).
// It lists every kind the module infra/providers/huawei creates, in the whole
// project, and names what the wiped deployment left. It never deletes.
//
// A leftover is a resource named with the name prefix of main.tf
// (catalyst-<fqdn, dots as dashes>-<first 8 characters of the deployment id>,
// alone or followed by a dash), an address that is DOWN with no port and not
// the bastion's, or a detached CSI volume (pvc-*, available, no attachment).
// Fail closed: a transport error, a status other than 200, an answer without
// its list or that does not decode, or a "count" above the length of the list
// ends the call with an error and no report.
// Credentials: huawei_access_key, huawei_secret_key, huawei_project_id and
// huawei_region (the variables document), or access_key, secret_key,
// project_id and region. The region defaults to defaultRegion.

package huawei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

var _ providers.LeftoverLister = (*Provider)(nil)

// obsEndpointFor is the object-storage host of a region. A package variable
// only so tests can point it at a test server.
var obsEndpointFor = func(region string) string {
	return fmt.Sprintf("obs.%s.kom4dc.nationalcloud.om", region)
}

// leftoverListing is one listing call: the report kind, the service, the
// path ({p} is the project id), the key of the list in the answer, and the
// rule that returns an item's label and whether it is a leftover.
type leftoverListing struct {
	kind, service, path, key string
	pick                     func(item map[string]any, own func(string) bool) (string, bool)
}

func itemString(item map[string]any, key string) string {
	s, _ := item[key].(string)
	return s
}

func leftoverByName(item map[string]any, own func(string) bool) (string, bool) {
	return itemString(item, "name"), own(itemString(item, "name"))
}

var leftoverListings = []leftoverListing{
	{"servers", "ecs", "/v1/{p}/cloudservers/detail", "servers", leftoverByName},
	{"volumes", "evs", "/v2/{p}/cloudvolumes/detail", "volumes", func(it map[string]any, own func(string) bool) (string, bool) {
		name := itemString(it, "name")
		att, _ := it["attachments"].([]any)
		detached := strings.HasPrefix(name, "pvc-") && itemString(it, "status") == "available" && len(att) == 0
		return name, own(name) || detached
	}},
	{"addresses", "vpc", "/v1/{p}/publicips", "publicips", func(it map[string]any, own func(string) bool) (string, bool) {
		e := rawPublicIP{Address: itemString(it, "public_ip_address"), BandwidthName: itemString(it, "bandwidth_name")}
		unbound := itemString(it, "status") == "DOWN" && itemString(it, "port_id") == "" && !isProtectedBastionEIP(e)
		return e.Address, own(e.BandwidthName) || unbound
	}},
	{"load_balancers", "elb", "/v3/{p}/elb/loadbalancers", "loadbalancers", leftoverByName},
	{"nat_gateways", "nat", "/v2/{p}/nat_gateways", "nat_gateways", leftoverByName},
	{"networks", "vpc", "/v1/{p}/vpcs", "vpcs", leftoverByName},
	{"security_groups", "vpc", "/v1/{p}/security-groups", "security_groups", leftoverByName},
	{"keypairs", "ecs", "/v2/{p}/os-keypairs", "keypairs", func(it map[string]any, own func(string) bool) (string, bool) {
		kp, _ := it["keypair"].(map[string]any)
		return itemString(kp, "name"), own(itemString(kp, "name"))
	}},
	{"peerings", "vpc", "/v2.0/vpc/peerings", "peerings", leftoverByName},
}

// listItems runs one listing call and returns its items, failing closed.
func listItems(client *http.Client, hw hwCreds, url, key string) ([]map[string]any, error) {
	body, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", status, snippet(body, 240))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("decode the answer: %w", err)
	}
	list, ok := top[key]
	if !ok {
		return nil, fmt.Errorf("the answer has no %q list", key)
	}
	var items []map[string]any
	if err := json.Unmarshal(list, &items); err != nil {
		return nil, fmt.Errorf("decode %q: %w", key, err)
	}
	var count int
	if c, ok := top["count"]; ok && json.Unmarshal(c, &count) == nil && count > len(items) {
		return nil, fmt.Errorf("the answer counts %d but lists %d", count, len(items))
	}
	return items, nil
}

// listBuckets lists every bucket of the account as items with a name.
func listBuckets(ctx context.Context, transport http.RoundTripper, hw hwCreds, region string) ([]map[string]any, error) {
	client, err := minio.New(obsEndpointFor(region), &minio.Options{Secure: true, Region: region, Transport: transport,
		Creds: credentials.NewStaticV4(hw.AccessKey, hw.SecretKey, "")})
	if err != nil {
		return nil, err
	}
	buckets, err := client.ListBuckets(ctx)
	items := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		items = append(items, map[string]any{"name": b.Name})
	}
	return items, err
}

// ListLeftovers implements providers.LeftoverLister.
func (p *Provider) ListLeftovers(ctx context.Context, spec providers.LeftoverSpec) (*providers.LeftoverReport, error) {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(spec.Creds.Get(k)); v != "" {
				return v
			}
		}
		return ""
	}
	hw := hwCreds{AccessKey: first("huawei_access_key", "access_key"), SecretKey: first("huawei_secret_key", "secret_key"),
		ProjectID: first("huawei_project_id", "project_id")}
	if hw.AccessKey == "" || hw.SecretKey == "" || hw.ProjectID == "" {
		return nil, errors.New("huawei leftovers: the access key, the secret key and the project id are required")
	}
	region := first("huawei_region", "region")
	if region == "" {
		region = defaultRegion
	}
	fqdn, dep := strings.TrimSpace(spec.SovereignFQDN), strings.TrimSpace(spec.DeploymentID)
	if fqdn == "" || len(dep) < 8 {
		return nil, errors.New("huawei leftovers: the Sovereign FQDN and a deployment id of at least 8 characters are required")
	}
	prefix := "catalyst-" + strings.ReplaceAll(fqdn, ".", "-") + "-" + dep[:8]
	own := func(name string) bool { return name == prefix || strings.HasPrefix(name, prefix+"-") }
	report := &providers.LeftoverReport{NamePrefix: prefix}
	collect := func(l leftoverListing, items []map[string]any) {
		labels := []string{}
		for _, it := range items {
			if label, left := l.pick(it, own); left {
				labels = append(labels, label)
			}
		}
		sort.Strings(labels)
		report.Kinds = append(report.Kinds, providers.LeftoverKind{Kind: l.kind, Used: len(items), Leftovers: labels})
	}
	client := httpClientFor(spec.Creds)
	for _, l := range leftoverListings {
		items, err := listItems(client, hw, endpointFor(l.service, region)+strings.ReplaceAll(l.path, "{p}", hw.ProjectID), l.key)
		if err != nil {
			return nil, fmt.Errorf("huawei leftovers: list %s: %w", l.kind, err)
		}
		collect(l, items)
	}
	items, err := listBuckets(ctx, client.Transport, hw, region)
	if err != nil {
		return nil, fmt.Errorf("huawei leftovers: list buckets: %w", err)
	}
	collect(leftoverListing{kind: "buckets", pick: leftoverByName}, items)
	return report, nil
}
