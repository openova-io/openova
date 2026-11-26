#!/bin/sh
# One evidence run of SEC-335: renders, then every start case. No cluster; KUBECONFIG is /dev/null throughout.
export KUBECONFIG=/dev/null
T=/home/openova/.cache/ns-scratch/SEC-335-tools
cd "$T" || exit 1
date -u +%FT%TZ > "$T/run-all.started"
sh "$T/render-base.sh" > "$T/render-base.out" 2>&1
sh "$T/route/render-route.sh" > "$T/route/render-route.out" 2>&1
: > "$T/cases/results.txt"
for c in gitea harbor grafana pda keycloak; do
  sh "$T/cases/c-$c.sh" > "$T/cases/c-$c.out" 2>&1
done
date -u +%FT%TZ > "$T/run-all.finished"
