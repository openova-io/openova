package cloudprofile

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// installationFor is the installation document of this package's tests
// (const installation), extending the pack named pack instead of demo.
func installationFor(pack string) string {
	return strings.Replace(installation, "  extends: demo\n", "  extends: "+pack+"\n", 1)
}

// TestCheckInstallationAgainstRepositoryPacks merges a complete installation
// document over every pack of this repository that requires one. It fails when
// no pack requires an installation, or when a document that sets every
// installation field is refused.
func TestCheckInstallationAgainstRepositoryPacks(t *testing.T) {
	dir := providersDir()
	profiles, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var names []string
	for name, p := range profiles {
		if p.Spec.RequiresInstallation {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatalf("no pack below %s requires an installation", dir)
	}
	for _, name := range names {
		eff, err := CheckInstallation(dir, []byte(installationFor(name)))
		if err != nil {
			t.Errorf("pack %s: %v", name, err)
			continue
		}
		s := eff.Spec
		if eff.Metadata.Name != "site-a" || s.Extends != name || s.Lifecycle != "proven" || len(s.Regions) != 1 ||
			s.Compute.Image == "" || len(s.Compute.Flavors) != 1 || s.Compute.Defaults == nil ||
			s.Registry == nil || s.Registry.MirrorEndpoint == "" || s.API == nil || s.API.Domain == "" {
			t.Errorf("pack %s: effective profile %+v", name, eff)
		}
	}
}

func TestInstallationConfigMap(t *testing.T) {
	dir := fixtureDir(t, demoPackProfile)
	doc := installation
	out, err := InstallationConfigMap(dir, []byte(doc))
	if err != nil {
		t.Fatalf("InstallationConfigMap: %v", err)
	}
	var cm struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if err := yaml.UnmarshalStrict(out, &cm); err != nil {
		t.Fatalf("the ConfigMap is not YAML of the expected form: %v\n%s", err, out)
	}
	if cm.APIVersion != "v1" || cm.Kind != "ConfigMap" || cm.Metadata.Name != "cloud-installation-site-a" ||
		cm.Metadata.Namespace != "catalyst-system" || len(cm.Metadata.Labels) != 1 ||
		cm.Metadata.Labels["catalyst.openova.io/cloud-installation"] != "true" {
		t.Errorf("ConfigMap header: %+v", cm)
	}
	if len(cm.Data) != 1 || cm.Data["profile.yaml"] != doc {
		t.Errorf("ConfigMap data: %q, want the document unchanged under the key profile.yaml", cm.Data)
	}
	if _, err := CheckInstallation(dir, []byte(cm.Data["profile.yaml"])); err != nil {
		t.Errorf("the document read back from the ConfigMap: %v", err)
	}
}

func TestInstallationConfigMapRefusesBadDocument(t *testing.T) {
	dir := fixtureDir(t, demoPackProfile)
	cases := []struct {
		name string
		doc  string
		err  error
		path string
	}{
		{"no spec.extends", strings.Replace(installation, "  extends: demo\n", "", 1), ErrMissingField, "spec.extends"},
		{"unknown pack", installationFor("nothere"), ErrUnknownPack, "spec.extends"},
		{"field that is not an installation field", installation + "  storage: {defaultClass: other, driver: other}\n", ErrNotInstallationField, "spec.storage"},
		{"no image", strings.Replace(installation, "    image: 00000000-0000-0000-0000-000000000000\n", "", 1), ErrIncomplete, "spec.compute.image"},
		{"bad lifecycle", strings.Replace(installation, "lifecycle: proven", "lifecycle: beta", 1), ErrBadEnum, "spec.lifecycle"},
		{"name too long for a ConfigMap", strings.Replace(installation, "metadata: {name: site-a}", "metadata: {name: "+strings.Repeat("a", 240)+"}", 1), ErrBadValue, "metadata.name"},
	}
	for _, c := range cases {
		out, err := InstallationConfigMap(dir, []byte(c.doc))
		var fe *FieldError
		if !errors.Is(err, c.err) || !errors.As(err, &fe) || fe.Path != c.path {
			t.Errorf("%s: error %v, want %v at %s", c.name, err, c.err, c.path)
		}
		if out != nil {
			t.Errorf("%s: a ConfigMap was returned for a refused document", c.name)
		}
	}
}
