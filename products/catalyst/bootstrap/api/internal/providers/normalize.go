// normalize.go — rules of Normalize that every adapter applies the same way.
//
// An adapter's Normalize calls these with the effective cloud profile of the
// request. They read the profile only; they never name a cloud.

package providers

import (
	"fmt"
	"regexp"
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

// CheckCredentialValues checks a credentials map against the credential
// fields of the profile before the cloud is called: a non-blank value must
// match the field's pattern, and when required is true every field that the
// profile marks required must be non-blank. The *FieldError names the
// credential key. A pattern that does not compile is a plain error.
func CheckCredentialValues(creds ProviderCreds, profile *cloudprofile.Profile, required bool) error {
	if profile == nil {
		return &FieldError{Field: "provider", Message: "no cloud profile"}
	}
	for _, f := range profile.Spec.Credentials.Fields {
		v := strings.TrimSpace(creds.Get(f.Key))
		if v == "" {
			if required && f.Required {
				return &FieldError{Field: f.Key, Message: "required (" + f.Label + ")"}
			}
			continue
		}
		if f.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(f.Pattern)
		if err != nil {
			return fmt.Errorf("cloud profile %q: credential field %s: pattern: %w", profile.Metadata.Name, f.Key, err)
		}
		if !re.MatchString(v) {
			return &FieldError{Field: f.Key, Message: fmt.Sprintf("%s does not match the pattern %s of the cloud profile %q", f.Label, f.Pattern, profile.Metadata.Name)}
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
