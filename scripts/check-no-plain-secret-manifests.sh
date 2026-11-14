#!/usr/bin/env bash
# check-no-plain-secret-manifests.sh - secret guard G3 (SEC-090).
#
# No new Secret manifest in an own chart template: a line 'kind: Secret' in
# a file under <chart>/templates/ of a chart under platform/ or products/
# (vendored charts inside a charts/ folder are not counted).
#
# Allow-list: scripts/secret-guard-baselines/check-no-plain-secret-manifests.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-plain-secret-manifests.sh                 scan the tree
#   bash scripts/check-no-plain-secret-manifests.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-plain-secret-manifests.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-plain-secret-manifests.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-plain-secret-manifests "$@"
