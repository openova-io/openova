package cloudprofile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

// docReader returns doc for every name.
func docReader(doc string) InstallationReader {
	return func(context.Context, string) ([]byte, error) { return []byte(doc), nil }
}

// TestForSovereignNeedsAKnownPack: an empty or unknown CLOUD_PROVIDER gives
// no profile and ErrUnknownPack.
func TestForSovereignNeedsAKnownPack(t *testing.T) {
	t.Setenv(InstallationEnv, "")
	for _, name := range []string{"", "  ", "nothere"} {
		t.Setenv(ProviderEnv, name)
		p, err := ForSovereign(context.Background(), providersDir(), nil)
		if p != nil {
			t.Errorf("%s=%q: got a profile", ProviderEnv, name)
		}
		wantField(t, err, ErrUnknownPack, ProviderEnv)
	}
}

// TestForSovereignPackAlone: with no installation, every pack of this
// repository gives its own pack profile, and no installation is read.
func TestForSovereignPackAlone(t *testing.T) {
	t.Setenv(InstallationEnv, "")
	profiles, err := Load(providersDir())
	if err != nil || len(profiles) == 0 {
		t.Fatalf("Load: %d packs, %v", len(profiles), err)
	}
	fail := func(context.Context, string) ([]byte, error) { return nil, errors.New("installation read") }
	for name, want := range profiles {
		t.Setenv(ProviderEnv, name)
		p, err := ForSovereign(context.Background(), providersDir(), fail)
		if err != nil || PackName(p) != name || p.Spec.Network.LoadBalancer.Mode != want.Spec.Network.LoadBalancer.Mode {
			t.Errorf("pack %s: got %+v, %v", name, p, err)
		}
	}
}

// TestForSovereignMergesInstallation reads the installation document from
// the ConfigMap that InstallationConfigMap prints, and merges it.
func TestForSovereignMergesInstallation(t *testing.T) {
	dir := fixtureDir(t, demoPackProfile)
	out, err := InstallationConfigMap(dir, []byte(installation))
	if err != nil {
		t.Fatalf("InstallationConfigMap: %v", err)
	}
	cm := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(out, &cm.Object); err != nil {
		t.Fatalf("decode ConfigMap: %v", err)
	}
	t.Setenv(ProviderEnv, "demo")
	t.Setenv(InstallationEnv, "site-a")
	read := InstallationFromCluster(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), cm))
	p, err := ForSovereign(context.Background(), dir, read)
	if err != nil || p.Metadata.Name != "site-a" || PackName(p) != "demo" || len(p.Spec.Regions) != 1 {
		t.Fatalf("ForSovereign: %+v, %v", p, err)
	}
}

// TestForSovereignRefusesBadInstallation: a document with another name, a
// missing reader or ConfigMap, and a non-installation field give no profile.
func TestForSovereignRefusesBadInstallation(t *testing.T) {
	dir := fixtureDir(t, demoPackProfile)
	ctx := context.Background()
	t.Setenv(ProviderEnv, "demo")
	t.Setenv(InstallationEnv, "site-b")
	_, err := ForSovereign(ctx, dir, docReader(installation))
	wantField(t, err, ErrBadValue, "metadata.name")
	t.Setenv(InstallationEnv, "site-a")
	for _, read := range []InstallationReader{nil, InstallationFromCluster(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))} {
		if p, err := ForSovereign(ctx, dir, read); err == nil || p != nil {
			t.Errorf("got %+v, %v; want an error", p, err)
		}
	}
	other := strings.Replace(installation, "  compute:\n", "  storage: {defaultClass: x, driver: x}\n  compute:\n", 1)
	_, err = ForSovereign(ctx, dir, docReader(other))
	wantField(t, err, ErrNotInstallationField, "spec.storage")
}
