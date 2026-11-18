package providers

import (
	"errors"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

func testProfile(multiRegion, multiCloudRegion bool) *cloudprofile.Profile {
	p := &cloudprofile.Profile{}
	p.Metadata.Name = "test"
	p.Spec.Credentials.Fields = []cloudprofile.CredentialField{
		{Key: "token", Label: "Token", Required: true},
		{Key: "project", Label: "Project"},
	}
	p.Spec.Capabilities = cloudprofile.Capabilities{MultiRegion: multiRegion, MultiCloudRegion: multiCloudRegion}
	return p
}

func fieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestCheckCredentials(t *testing.T) {
	p := testProfile(true, true)
	for name, tc := range map[string]struct {
		creds map[string]string
		want  string
	}{
		"present":      {map[string]string{"token": "x"}, ""},
		"missing":      {map[string]string{"project": "x"}, "credentials.token"},
		"blank":        {map[string]string{"token": "  "}, "credentials.token"},
		"nil map":      {nil, "credentials.token"},
		"optional out": {map[string]string{"token": "x", "other": "y"}, ""},
	} {
		spec := ProvisionSpec{Creds: ProviderCreds{Raw: tc.creds}}
		err := CheckCredentials(&spec, p)
		if got := fieldOf(err); got != tc.want || (tc.want == "" && err != nil) {
			t.Errorf("%s: got %v, want field %q", name, err, tc.want)
		}
	}
	if fieldOf(CheckCredentials(&ProvisionSpec{}, nil)) != "provider" {
		t.Error("no profile: want a field error on provider")
	}
}

func TestCheckRegions(t *testing.T) {
	one := []RegionSpec{{CloudRegion: "a"}}
	two := []RegionSpec{{CloudRegion: "a"}, {CloudRegion: "a"}}
	split := []RegionSpec{{CloudRegion: "a"}, {CloudRegion: "b"}}
	cases := []struct {
		name                    string
		multiRegion, multiCloud bool
		regions                 []RegionSpec
		want                    string
	}{
		{"one region, no capability", false, false, one, ""},
		{"two regions, multiRegion false", false, true, two, "regions"},
		{"two regions in one cloud region", true, false, two, ""},
		{"two cloud regions, multiCloudRegion false", true, false, split, "regions[1].cloudRegion"},
		{"two cloud regions, both capabilities", true, true, split, ""},
	}
	for _, tc := range cases {
		spec := ProvisionSpec{Regions: tc.regions}
		err := CheckRegions(&spec, testProfile(tc.multiRegion, tc.multiCloud))
		if got := fieldOf(err); got != tc.want || (tc.want == "" && err != nil) {
			t.Errorf("%s: got %v, want field %q", tc.name, err, tc.want)
		}
	}
}
