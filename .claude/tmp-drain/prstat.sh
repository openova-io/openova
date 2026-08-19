#!/usr/bin/env bash
# prstat.sh <pr>  -> prints mergeable/state, pending count, and REAL failures
n=$1
gh pr view "$n" --json mergeable,mergeStateStatus,statusCheckRollup --jq '
  "#'"$n"' \(.mergeable)/\(.mergeStateStatus)",
  "  pending: \([.statusCheckRollup[]? | select((.status // "COMPLETED") != "COMPLETED")] | length)",
  "  failures: \([.statusCheckRollup[]? | select((.status // "COMPLETED")=="COMPLETED") | select((.conclusion // .state) as $c | $c=="FAILURE" or $c=="TIMED_OUT" or $c=="CANCELLED" or $c=="ERROR" or $c=="ACTION_REQUIRED") | (.name // .context)] | join(", "))"
'
