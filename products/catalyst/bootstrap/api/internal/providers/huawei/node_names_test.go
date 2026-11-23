package huawei

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// A Sovereign FQDN of 45 characters, the longest the bucket rule allows
// (63 - len("catalyst-") - 1 - 8), and the node names the module gives it.
const (
	longFQDN     = "a-sovereign-of-a-long-name-012345.omani.works"
	longNodeName = "catalyst-a-sovereign-of-a-lon-0123abcd-me-east-215-a-cp1-abcdef"
)

// The module cuts the stem of a node name where the sweeps cut it.
func TestNodeNameStemLenIsTheModuleCut(t *testing.T) {
	b, err := os.ReadFile("../../../../../../../infra/providers/huawei/main.tf")
	if err != nil {
		t.Fatalf("read the module: %v", err)
	}
	m := regexp.MustCompile(`(?m)^  node_name_prefix = "catalyst-\$\{substr\(local\.fqdn_slug, 0, (\d+)\)\}-\$\{substr\(var\.deployment_id, 0, 8\)\}"$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("infra/providers/huawei/main.tf has no local node_name_prefix of the form this package matches")
	}
	if n, _ := strconv.Atoi(string(m[1])); n != nodeNameStemLen {
		t.Fatalf("the module cuts the stem of a node name at %d characters, the sweeps at %d", n, nodeNameStemLen)
	}
	if len(longNodeName) != 63 {
		t.Fatalf("the fixture node name has %d characters, want 63", len(longNodeName))
	}
}

// A live deployment of a long FQDN is alive by its node names: the sweeper
// must never take it for gone.
func TestLongFQDNNodesKeepTheDeploymentAlive(t *testing.T) {
	a := cleanAnswers()
	a["/cloudservers/detail"] = `{"servers":[{"id":"1","name":"` + longNodeName + `","status":"ACTIVE"}]}`
	fakeCloud(t, a)
	alive, err := New().HasServersByNamePrefix(context.Background(), "ak", "sk", "proj", "me-east-215", longFQDN, "0123abcd99")
	if err != nil {
		t.Fatalf("HasServersByNamePrefix: %v", err)
	}
	if !alive {
		t.Fatalf("a deployment whose control plane %s is listed was reported gone", longNodeName)
	}
}

// The wipe's scope owns the nodes of a long FQDN (the ECS sweep and the
// VPC delete's refusal ask ownsServer), still by the deployment id, and the
// cut name selects no other kind of resource.
func TestDeploymentScopeOwnsTheNodesOfALongFQDN(t *testing.T) {
	s, err := newDeploymentScope(longFQDN, "0123abcd99")
	if err != nil {
		t.Fatalf("newDeploymentScope: %v", err)
	}
	for name, want := range map[string]bool{
		longNodeName: true,
		"catalyst-a-sovereign-of-a-long-name-012345-omani-works-0123abcd-me-east-215-a-cp1-abcdef": true,  // named before the cut
		"catalyst-a-sovereign-of-a-lon-0123abce-me-east-215-a-cp1-abcdef":                          false, // another deployment
		"catalyst-a-sovereign-of-a-lon-0123abcd-bastion":                                           false,
	} {
		if got := s.ownsServer(name); got != want {
			t.Errorf("ownsServer(%s) = %v, want %v", name, got, want)
		}
	}
	if s.owns(longNodeName) {
		t.Errorf("owns(%s) = true: the cut name must select servers only", longNodeName)
	}
}

// The leftover report of a long FQDN names its nodes, by the cut name and by
// the full name of a server created before the cut, and no other
// deployment's node.
func TestListLeftoversNamesLongFQDNNodes(t *testing.T) {
	const preCut = "catalyst-a-sovereign-of-a-long-name-012345-omani-works-0123abcd-me-east-215-a-cp1-abcdef"
	a := cleanAnswers()
	a["/cloudservers/detail"] = `{"servers":[{"name":"` + longNodeName + `"},{"name":"` + preCut + `"},{"name":"catalyst-a-sovereign-of-a-lon-0123abce-me-east-215-a-cp1-abcdef"}]}`
	fakeCloud(t, a)
	spec := providers.LeftoverSpec{DeploymentID: "0123abcd99", SovereignFQDN: longFQDN, Creds: leftoverSpec.Creds}
	r, err := New().ListLeftovers(context.Background(), spec)
	if err != nil {
		t.Fatalf("ListLeftovers: %v", err)
	}
	seen := false
	for _, k := range r.Kinds {
		if k.Kind != "servers" {
			continue
		}
		seen = true
		if want := []string{longNodeName, preCut}; !reflect.DeepEqual(k.Leftovers, want) {
			t.Fatalf("servers left %v, want %v", k.Leftovers, want)
		}
	}
	if !seen {
		t.Fatalf("the report has no servers kind: %+v", r.Kinds)
	}
}
