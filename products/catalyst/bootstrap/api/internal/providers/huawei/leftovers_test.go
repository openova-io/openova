package huawei

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

const lp = "catalyst-t1-omani-works-0123abcd" // the name prefix of leftoverSpec

var leftoverSpec = providers.LeftoverSpec{
	DeploymentID: "0123abcd99", SovereignFQDN: "t1.omani.works",
	Creds: providers.ProviderCreds{Raw: map[string]string{
		"huawei_access_key": "ak", "huawei_secret_key": "sk", "huawei_project_id": "proj"}},
}

// cleanAnswers: only the bastion and another deployment (prefix ...-0123abce).
func cleanAnswers() map[string]string {
	return map[string]string{
		"/cloudservers/detail": `{"count":2,"servers":[{"name":"bastion-openova"},{"name":"catalyst-t1-omani-works-0123abce-a-cp1"}]}`,
		"/cloudvolumes/detail": `{"volumes":[{"name":"pvc-in-use","status":"in-use","attachments":[{"server_id":"s"}]}]}`,
		"/publicips":           `{"publicips":[{"public_ip_address":"212.72.24.20","status":"DOWN","port_id":""},{"public_ip_address":"10.0.0.9","status":"ELB","port_id":""}]}`,
		"/elb/loadbalancers":   `{"loadbalancers":[]}`,
		"/nat_gateways":        `{"nat_gateways":[]}`,
		"/vpcs":                `{"vpcs":[{"name":"bastion-vpc"}]}`,
		"/security-groups":     `{"security_groups":[{"name":"default"}]}`,
		"/os-keypairs":         `{"keypairs":[{"keypair":{"name":"bastion-openova-kp"}}]}`,
		"/vpc/peerings":        `{"peerings":[]}`,
		"/":                    buckets("catalyst-t1-omani-works-0123abce"),
	}
}

func buckets(names ...string) string {
	b := `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>o</ID></Owner><Buckets>`
	for _, n := range names {
		b += "<Bucket><Name>" + n + "</Name><CreationDate>2026-01-01T00:00:00.000Z</CreationDate></Bucket>"
	}
	return b + "</Buckets></ListAllMyBucketsResult>"
}

// fakeCloud answers by path suffix ("!<code> <body>" answers that status) and points both seams at it.
func fakeCloud(t *testing.T, answers map[string]string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("the lister sent %s %s; it may only read", r.Method, r.URL.Path)
		}
		for suffix, body := range answers {
			if (suffix == "/" && r.URL.Path == "/") || (suffix != "/" && strings.HasSuffix(r.URL.Path, suffix)) {
				if code, rest, ok := strings.Cut(body, " "); ok && strings.HasPrefix(code, "!") {
					c, _ := strconv.Atoi(code[1:])
					w.WriteHeader(c)
					body = rest
				}
				_, _ = w.Write([]byte(body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	origAPI, origOBS := endpointFor, obsEndpointFor
	endpointFor = func(string, string) string { return srv.URL }
	obsEndpointFor = func(string) string { return strings.TrimPrefix(srv.URL, "https://") }
	t.Cleanup(func() { endpointFor, obsEndpointFor = origAPI, origOBS; srv.Close() })
}

func TestListLeftoversCleanAccount(t *testing.T) {
	fakeCloud(t, cleanAnswers())
	r, err := New().ListLeftovers(context.Background(), leftoverSpec)
	if err != nil || r.NamePrefix != lp || r.LeftoverCount() != 0 {
		t.Fatalf("report %+v, error %v; want the prefix %q and no leftover", r, err, lp)
	}
	var kinds []string
	used := map[string]int{}
	for _, k := range r.Kinds {
		kinds, used[k.Kind] = append(kinds, k.Kind), k.Used
		if k.Leftovers == nil {
			t.Errorf("kind %s: Leftovers is nil, want an empty list", k.Kind)
		}
	}
	want := []string{"servers", "volumes", "addresses", "load_balancers", "nat_gateways", "networks",
		"security_groups", "keypairs", "peerings", "buckets"}
	if !reflect.DeepEqual(kinds, want) || used["servers"] != 2 || used["addresses"] != 2 || used["buckets"] != 1 {
		t.Fatalf("kinds %v, used %v; want %v, servers 2, addresses 2, buckets 1", kinds, used, want)
	}
}

func TestListLeftoversNamesEveryKind(t *testing.T) {
	a := cleanAnswers()
	a["/cloudservers/detail"] = `{"servers":[{"name":"` + lp + `-a-cp1-abc"},{"name":"catalyst-t1-omani-works-0123abcdx"}]}`
	a["/cloudvolumes/detail"] = `{"volumes":[{"name":"pvc-left","status":"available","attachments":[]},{"name":"` + lp + `-disk","status":"in-use","attachments":[{}]}]}`
	a["/publicips"] = `{"publicips":[{"public_ip_address":"10.0.0.1","bandwidth_name":"` + lp + `-a-nat-bw","status":"ACTIVE","port_id":"p"},{"public_ip_address":"10.0.0.2","status":"DOWN","port_id":""}]}`
	a["/elb/loadbalancers"] = `{"loadbalancers":[{"name":"` + lp + `-elb-primary"}]}`
	a["/nat_gateways"] = `{"nat_gateways":[{"name":"` + lp + `-a-nat"}]}`
	a["/vpcs"] = `{"vpcs":[{"name":"` + lp + `-a-vpc"}]}`
	a["/security-groups"] = `{"security_groups":[{"name":"` + lp + `-a-sg"}]}`
	a["/os-keypairs"] = `{"keypairs":[{"keypair":{"name":"` + lp + `-key"}}]}`
	a["/vpc/peerings"] = `{"peerings":[{"name":"` + lp + `-peer-1"}]}`
	a["/"] = buckets(lp, "catalyst-t1-omani-works-0123abce")
	fakeCloud(t, a)
	r, err := New().ListLeftovers(context.Background(), leftoverSpec)
	if err != nil {
		t.Fatalf("ListLeftovers: %v", err)
	}
	got := map[string][]string{}
	for _, k := range r.Kinds {
		got[k.Kind] = k.Leftovers
	}
	want := map[string][]string{
		"servers": {lp + "-a-cp1-abc"}, "volumes": {lp + "-disk", "pvc-left"}, "addresses": {"10.0.0.1", "10.0.0.2"},
		"load_balancers": {lp + "-elb-primary"}, "nat_gateways": {lp + "-a-nat"}, "networks": {lp + "-a-vpc"},
		"security_groups": {lp + "-a-sg"}, "keypairs": {lp + "-key"}, "peerings": {lp + "-peer-1"}, "buckets": {lp},
	}
	if !reflect.DeepEqual(got, want) || r.LeftoverCount() != 12 {
		t.Fatalf("%d leftovers\n got %v\nwant %v (12)", r.LeftoverCount(), got, want)
	}
}

// Each listing that cannot be read completely gives an error and no report.
func TestListLeftoversFailsClosed(t *testing.T) {
	cases := map[string][2]string{
		"a status other than 200":    {"/vpcs", `!500 {"vpcs":[]}`},
		"a 404 that carries a list":  {"/elb/loadbalancers", `!404 {"loadbalancers":[]}`},
		"no answer for a listing":    {"/os-keypairs", ""},
		"an answer without its list": {"/nat_gateways", `{"other":[]}`},
		"an answer that is not JSON": {"/security-groups", `<html>`},
		"a list cut short":           {"/cloudvolumes/detail", `{"count":3,"volumes":[{"name":"a"}]}`},
		"the bucket listing fails":   {"/", "!500 "},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := cleanAnswers()
			if c[1] == "" {
				delete(a, c[0])
			} else {
				a[c[0]] = c[1]
			}
			fakeCloud(t, a)
			if r, err := New().ListLeftovers(context.Background(), leftoverSpec); err == nil || r != nil {
				t.Fatalf("report %v, error %v; want no report and an error", r, err)
			}
		})
	}
	noKeys, shortID := leftoverSpec, leftoverSpec
	noKeys.Creds = providers.ProviderCreds{Raw: map[string]string{"huawei_access_key": "ak"}}
	shortID.DeploymentID = "0123"
	for _, s := range []providers.LeftoverSpec{noKeys, shortID} {
		if _, err := New().ListLeftovers(context.Background(), s); err == nil {
			t.Errorf("no error without the keys or with a short deployment id: %+v", s)
		}
	}
}
