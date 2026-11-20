#!/bin/sh
# write-list.sh - writes clusters/_template/infrastructure/providers/base/opentofu-providers.yaml,
# the ConfigMap crossplane-system/opentofu-providers, from the tofu-providers.txt of every
# provider pack (SOV-342). With -check it writes nothing and exits 1 when the file differs.
# The job "regenerate and prove no diff" of .github/workflows/mirror-tofu-providers.yaml runs it.
set -eu
cd "$(dirname "$0")/../.."
exec go run ./cmd/tofu-providers list "$@"
