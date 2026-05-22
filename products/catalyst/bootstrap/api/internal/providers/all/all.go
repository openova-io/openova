// Package all is the one-stop import that pulls every CloudProvider
// adapter into the binary so their init()s register against the
// providers.registry.
//
// Catalyst-api's main package imports this package once; that's
// enough to make providers.Get("hetzner") / providers.Get("huawei")
// return live values. Without this indirection every binary would
// have to import each adapter explicitly — which is fine, but the
// "all" package gives us a canonical single import that future
// providers (aws, gcp, azure) extend without touching main.
//
// Pattern: side-effect import. Each underscore import below pulls
// in the package solely for its init() — the local code does not
// reference any exported identifier.

package all

import (
	// Hetzner — production-ready adapter (Wave 2 forward).
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/hetzner"

	// Huawei — stub (Wave 2); concrete impl in Wave 3 per Issue #1841.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/huawei"
)
