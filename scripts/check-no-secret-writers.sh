#!/usr/bin/env bash
# check-no-secret-writers.sh - secret guard G5 (SEC-090).
#
# No new Secret writer in Go code (files ending .go, not _test.go): a call
# Secrets(...).Create, .Update, .Patch or .Apply, and a line 'kind: Secret'
# inside Go source.
#
# Allow-list: scripts/secret-guard-baselines/check-no-secret-writers.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-secret-writers.sh                 scan the tree
#   bash scripts/check-no-secret-writers.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-secret-writers.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-secret-writers.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-secret-writers "$@"
