// normalize.go — rules of Normalize that every adapter applies the same way.
//
// An adapter's Normalize calls these with the effective cloud profile of the
// request. They read the profile only; they never name a cloud.

package providers

import (
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

// CheckCredentials returns a *FieldError for the first credential field that
// the profile marks required (spec.credentials.fields[].required) and that
// spec.Creds lacks or holds only blanks. The field is "credentials.<key>".
func CheckCredentials(spec *ProvisionSpec, profile *cloudprofile.Profile) error {
	if profile == nil {
		return &FieldError{Field: "provider", Message: "no cloud profile"}
	}
	for _, f := range profile.Spec.Credentials.Fields {
		if f.Required && strings.TrimSpace(spec.Creds.Get(f.Key)) == "" {
			return &FieldError{Field: "credentials." + f.Key, Message: "required (" + f.Label + ")"}
		}
	}
	return nil
}

// CheckRegions applies the region capabilities of the profile: more than one
// region needs spec.capabilities.multiRegion, and regions in more than one
// cloud region need spec.capabilities.multiCloudRegion. It returns a
// *FieldError on "regions".
func CheckRegions(spec *ProvisionSpec, profile *cloudprofile.Profile) error {
	if profile == nil {
		return &FieldError{Field: "provider", Message: "no cloud profile"}
	}
	caps := profile.Spec.Capabilities
	if len(spec.Regions) > 1 && !caps.MultiRegion {
		return &FieldError{Field: "regions", Message: fmt.Sprintf("%d regions requested; the cloud profile %q allows one", len(spec.Regions), profile.Metadata.Name)}
	}
	if caps.MultiCloudRegion {
		return nil
	}
	for i, r := range spec.Regions {
		if r.CloudRegion != spec.Regions[0].CloudRegion {
			return &FieldError{Field: fmt.Sprintf("regions[%d].cloudRegion", i), Message: fmt.Sprintf("%q differs from %q; the cloud profile %q allows one cloud region", r.CloudRegion, spec.Regions[0].CloudRegion, profile.Metadata.Name)}
		}
	}
	return nil
}
