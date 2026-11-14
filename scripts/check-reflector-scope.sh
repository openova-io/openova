#!/usr/bin/env bash
# check-reflector-scope.sh - secret guard G6 (SEC-090).
#
# No new wide reflector scope and no new use of the legacy cluster-wide
# store: (a) a reflection-auto-namespaces value that is empty or holds .*
# or [a-z], outside comments, under infra/, clusters/, own chart templates
# and Go code; (b) in an own chart template that declares an ExternalSecret
# or a PushSecret, a 'name:' line naming the store vault-region1.
#
# Allow-list: scripts/secret-guard-baselines/check-reflector-scope.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-reflector-scope.sh                 scan the tree
#   bash scripts/check-reflector-scope.sh --self-test     prove that each rule can fail
#   bash scripts/check-reflector-scope.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-reflector-scope.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" reflector-scope "$@"
