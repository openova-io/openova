package orgdns

import (
	"slices"
	"testing"
)

func TestRecordSetNames(t *testing.T) {
	cases := []struct {
		name string
		org  Org
		want []string
	}{
		{
			name: "pool parent, subdomain set",
			org:  Org{Slug: "acme", ParentDomain: "omani.homes", Subdomain: "shop"},
			want: []string{"console.shop.omani.homes.", "*.shop.omani.homes."},
		},
		{
			name: "pool parent, subdomain defaults to slug",
			org:  Org{Slug: "acme", ParentDomain: "omani.homes."},
			want: []string{"console.acme.omani.homes.", "*.acme.omani.homes."},
		},
		{
			name: "internal Organization under the Sovereign FQDN",
			org:  Org{Slug: "ops", Kind: "Internal", SovereignRef: "HW1.omani.works"},
			want: []string{"console.ops.hw1.omani.works.", "*.ops.hw1.omani.works."},
		},
		{
			name: "customer Organization without a parent has none",
			org:  Org{Slug: "acme", Kind: "customer", SovereignRef: "hw1.omani.works"},
		},
		{
			name: "internal Organization of a two-label Sovereign has none",
			org:  Org{Slug: "ops", Kind: "internal", SovereignRef: "omani.works"},
		},
		{
			name: "no label has none",
			org:  Org{ParentDomain: "omani.homes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecordSetNames(tc.org); !slices.Equal(got, tc.want) {
				t.Fatalf("RecordSetNames(%+v) = %q, want %q", tc.org, got, tc.want)
			}
		})
	}
}

func TestParentZones(t *testing.T) {
	parent, zones := Parent(Org{Kind: "internal", SovereignRef: "hw1.omani.works"})
	if parent != "hw1.omani.works" || !slices.Equal(zones, []string{"hw1.omani.works", "omani.works"}) {
		t.Fatalf("Parent = %q %q", parent, zones)
	}
	parent, zones = Parent(Org{ParentDomain: "omani.homes", Kind: "internal", SovereignRef: "hw1.omani.works"})
	if parent != "omani.homes" || !slices.Equal(zones, []string{"omani.homes"}) {
		t.Fatalf("Parent = %q %q", parent, zones)
	}
}
