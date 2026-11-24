#!/bin/sh
# write.sh - writes products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.generated.ts,
# the kit rings the installation wizard lists and the vendored component logos (SVC-230), from the
# catalogue and the charts. With --check it writes nothing and exits 1 when the file differs.
# The job "regenerate and prove no diff" of .github/workflows/check-catalog-generated-drift.yaml runs it.
set -eu
cd "$(dirname "$0")/../.."
exec go run ./cmd/kit-rings "$@"
