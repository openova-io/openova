// registry.go — provider registry.
//
// Resolves a deployment's `Provider` field (the string enum on
// core/controllers/environment/api/v1/types.go and on every
// deployment record) to a concrete CloudProvider implementation.
//
// Use Get(name) at every catalyst-api handler that previously
// imported internal/hetzner directly. The handler stays cloud-
// agnostic; the registry returns the per-cloud adapter; the
// adapter delegates to its internal/<cloud>/ package.

package providers

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// registry holds the active CloudProvider implementations.
// Populated lazily by RegisterProvider to keep import cycles
// impossible — each adapter package (providers/hetzner,
// providers/huawei) calls RegisterProvider() from its own
// init() so wiring this package up does not pull every adapter
// in transitively at compile time.
var (
	registryMu sync.RWMutex
	registry   = map[string]CloudProvider{}
)

// RegisterProvider records an adapter implementation under the
// canonical provider name. Called from each adapter package's
// init(). MUST NOT be called from non-init contexts.
//
// Calling RegisterProvider twice for the same name panics —
// that signals a duplicate adapter registration (e.g. two
// packages both claiming "hetzner"), which is always a bug.
func RegisterProvider(name string, p CloudProvider) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		panic("providers.RegisterProvider: empty name")
	}
	if p == nil {
		panic("providers.RegisterProvider: nil provider for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("providers.RegisterProvider: duplicate registration for " + name)
	}
	registry[name] = p
}

// Get returns the registered CloudProvider for `name`. Returns
// an error with a human-readable message when the name is
// unknown — the handler typically surfaces this as a 400 on
// the wizard's POST /deployments.
//
// `name` matching is case-insensitive + whitespace-trimmed so
// the deployment record can store either "hetzner" or "Hetzner"
// without confusing callers. The canonical form is lower-case.
func Get(name string) (CloudProvider, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, fmt.Errorf("providers.Get: empty provider name")
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[key]
	if !ok {
		return nil, fmt.Errorf("providers.Get: unknown provider %q (known: %s)", name, knownNamesLocked())
	}
	return p, nil
}

// Names returns the sorted slice of registered provider names.
// Used by /api/v1/providers (planned) so the UI can render the
// "Provider" dropdown without a hard-coded list.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// knownNamesLocked — comma-joined list of registered names.
// Caller must hold registryMu (read or write).
func knownNamesLocked() string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// reset is exposed to tests only (the file lives in the
// non-_test.go set so test files in OTHER packages can call
// it). Production code MUST NOT call reset; double-registration
// is a bug per RegisterProvider's contract.
func reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]CloudProvider{}
}
