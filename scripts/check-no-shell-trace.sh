#!/usr/bin/env bash
# check-no-shell-trace.sh - secret guard G12 (#7901).
#
# No new shell tracing in what runs on a cluster or on a node: the YAML,
# template and shell files under platform/, products/, clusters/, infra/ and
# core/, without their tests/ and testdata/ folders. A traced shell prints
# every command with its expanded arguments, so a script that holds a token,
# a seed or a key in a variable writes it to the pod log, which the log
# collector ships. The openbao-init Job did so with the ServiceAccount token,
# the recovery seed, the root token and the unseal key.
#
# Counted: 'set -x' and any 'set -<flags>' with an x, 'set -o xtrace',
# 'sh -x' and 'bash -x' (also with other flags), a container command
# ["sh", "-x..."], SHELLOPTS=...xtrace, and an interpreter line with -x.
# A comment line is not counted. The rule does not ask whether the file
# handles a secret: none of these files has a reason to trace.
#
# Allow-list: scripts/secret-guard-baselines/check-no-shell-trace.txt, one
# 'path:count' line per file (today one vendored upstream hook). A rise or a
# new path fails; a fall passes and prints the line to change or delete.
# Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-shell-trace.sh                 scan the tree
#   bash scripts/check-no-shell-trace.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-shell-trace.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-shell-trace.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-shell-trace "$@"
