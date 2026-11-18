package provisioner

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// The tests of this package cannot import the adapters, because the adapters
// import this package. They run against portDouble, registered under the
// names of the two cloud packs (once, as the CloudProvider of the adoption
// stand-ins of adoption_kinds_test.go) so that Validate reads the real cloud
// profiles of the repository. Its Normalize applies only the rule every
// adapter shares, the required credentials; each adapter's own rules are
// tested in its package.
func init() {
	ProvidersDir = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
	providers.RegisterProvider("aliasing", aliasDouble{portDouble{name: "aliasing"}})
}

// aliasDouble names two legacy JSON fields (providers.LegacyAliaser); it has
// no cloud profile and serves the decoding tests only.
type aliasDouble struct{ portDouble }

func (aliasDouble) LegacyCredentialAliases() map[string]string {
	return map[string]string{"legacyToken": "token", "legacyProject": "project"}
}

type portDouble struct{ name string }

func (d portDouble) Name() string { return d.name }

func (d portDouble) Normalize(spec *providers.ProvisionSpec, profile *cloudprofile.Profile) error {
	if doubleNormalize != nil {
		doubleNormalize(spec)
	}
	return providers.CheckCredentials(spec, profile)
}

func (d portDouble) TfVars(spec providers.ProvisionSpec, _ *cloudprofile.Profile) (map[string]any, error) {
	if doubleTfVars != nil {
		return doubleTfVars(spec)
	}
	return map[string]any{}, nil
}

// doubleNormalize and doubleTfVars give the double, for one test, what an
// adapter does to the spec before its checks (the place of each region) and
// the variables it returns (CLD-104). withDouble sets them and resets them when
// the test ends; a test that uses it does not run in parallel.
var (
	doubleNormalize func(*providers.ProvisionSpec)
	doubleTfVars    func(providers.ProvisionSpec) (map[string]any, error)
)

func withDouble(t *testing.T, normalize func(*providers.ProvisionSpec), tfVars func(providers.ProvisionSpec) (map[string]any, error)) {
	t.Helper()
	doubleNormalize, doubleTfVars = normalize, tfVars
	t.Cleanup(func() { doubleNormalize, doubleTfVars = nil, nil })
}

// oneCloudRegion places every region of spec the way an adapter of a cloud
// with one cloud region per Sovereign does: the entry's cloudRegion becomes
// its code, the credential "region" its cloud region, and the zone is empty.
func oneCloudRegion(spec *providers.ProvisionSpec) {
	for i := range spec.Regions {
		if spec.Regions[i].Code == "" {
			spec.Regions[i].Code = spec.Regions[i].CloudRegion
		}
		spec.Regions[i].CloudRegion = spec.Creds.Get("region")
		spec.Regions[i].Zone = ""
	}
}

func (d portDouble) Wipe(context.Context, providers.WipeSpec, func(string)) (*providers.WipeResult, error) {
	return nil, providers.ErrNotSupported
}

func (d portDouble) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, providers.ErrNotSupported
}

func (d portDouble) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }
