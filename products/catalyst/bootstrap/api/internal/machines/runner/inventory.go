package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// trivyEnv keeps Trivy from reaching any server: no telemetry, no version check.
var trivyEnv = []string{"TRIVY_DISABLE_TELEMETRY=true", "TRIVY_SKIP_VERSION_CHECK=true", "TRIVY_NO_PROGRESS=true"}

// inventory writes the software inventory of the node's file system as CycloneDX. Trivy
// runs offline: it updates no database and fetches nothing.
func inventory(ctx context.Context, _ Config, env Env) taskResult {
	out := filepath.Join(env.WorkDir, FileSBOM)
	if r := collect(ctx, trivyEnv, []int{0}, env.Trivy,
		"rootfs", "--format", "cyclonedx", "--skip-db-update", "--offline-scan",
		"--cache-dir", filepath.Join(env.WorkDir, "trivy-cache"),
		"--output", out, env.HostRoot); r != nil {
		return *r
	}
	data, err := os.ReadFile(out) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return failed("trivy wrote no %s", FileSBOM)
	}
	var bom struct {
		BOMFormat string `json:"bomFormat"`
	}
	if json.Unmarshal(data, &bom) != nil || bom.BOMFormat != "CycloneDX" {
		return failed("%s is not a CycloneDX document", FileSBOM)
	}
	if err := writeMarker(env, TaskInventory); err != nil {
		return failed("the completion marker was not written: %v", err)
	}
	return taskResult{outcome: runresult.Succeeded, files: []file{{KindSBOM, FileSBOM}}}
}
