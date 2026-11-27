#!/bin/sh
# write.sh - writes products/catalyst/bootstrap/api/internal/catalog/capabilities.json, the capability index
# the install API resolves against: every contract file platform/_capabilities/<name>.yaml (checked against
# platform/_schemas/capability.schema.json) and, per capability, the Blueprints whose blueprint.yaml provides
# it. With --check it writes nothing and exits 1 when the committed index differs; it exits 2 when an input is
# wrong. Run it after a change to a contract file, the capability schema or the provides/requires of a
# blueprint.yaml, and commit the result. The job "regenerate and prove no diff" of
# .github/workflows/check-capability-index.yaml runs it with --check.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../.."
exec go run ./internal/capability/cmd/capability-index -root "$here/../../../../../.." "$@"
