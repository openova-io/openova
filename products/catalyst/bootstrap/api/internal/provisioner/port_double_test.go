package provisioner

import (
	"context"
	"path/filepath"

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
	return providers.CheckCredentials(spec, profile)
}

func (d portDouble) TfVars(providers.ProvisionSpec, *cloudprofile.Profile) (map[string]any, error) {
	return map[string]any{}, nil
}

func (d portDouble) Wipe(context.Context, providers.WipeSpec, func(string)) (*providers.WipeResult, error) {
	return nil, providers.ErrNotSupported
}

func (d portDouble) ListServers(context.Context, string, string, providers.ProviderCreds) ([]providers.ServerInfo, error) {
	return nil, providers.ErrNotSupported
}

func (d portDouble) ValidateCreds(context.Context, providers.ProviderCreds) error { return nil }
