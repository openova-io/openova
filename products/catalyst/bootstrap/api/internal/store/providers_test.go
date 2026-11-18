package store

import (
	"path/filepath"

	// Validate resolves the adapter of a request's provider and reads its
	// cloud profile (CLD-103); the tests that run it need both.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func init() {
	provisioner.ProvidersDir = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
}
