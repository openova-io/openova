package handler

import (
	"path/filepath"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// Validate reads the cloud profile of a request's provider (CLD-103); the
// tests read the profiles of this repository.
func init() {
	provisioner.ProvidersDir = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
}
