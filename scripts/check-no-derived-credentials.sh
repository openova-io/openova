#!/usr/bin/env bash
# check-no-derived-credentials.sh - secret guard G4 (SEC-090).
#
# No new credential derived with sha256sum in an own chart template: a line
# with sha256sum, not a checksum/ annotation, whose own line or one of the
# four lines above it holds secret, password, token, seed or salt (any
# case). A credential computed from public inputs can be computed by anyone.
#
# Allow-list: scripts/secret-guard-baselines/check-no-derived-credentials.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-derived-credentials.sh                 scan the tree
#   bash scripts/check-no-derived-credentials.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-derived-credentials.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-derived-credentials.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-derived-credentials "$@"
