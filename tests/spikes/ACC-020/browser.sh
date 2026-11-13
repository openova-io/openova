#!/usr/bin/env bash
# Lab test ACC-020, part 3: the browser library against the relay (items a
# and g) and replay of a recording served in pieces (item f). The runner
# reaches the agent on the pod IP; there is no Service and no node port.
# shellcheck source=tests/spikes/ACC-020/lib.sh
source "$(dirname "$0")/lib.sh"

PW_VERSION=1.49.1
mkdir -p /tmp/pw "$EV/browser"
(
  cd /tmp/pw || exit 1
  npm init -y >/dev/null 2>&1
  npm i --silent "playwright@$PW_VERSION" >/dev/null 2>&1
  npx playwright install --with-deps chromium > "$EV/playwright-install.log" 2>&1
) || note "playwright install reported an error"
cp "$SPIKE_DIR/browser.mjs" /tmp/pw/browser.mjs

S='scenario=ssh-password'
cat > /tmp/pw/runs.json <<EOF
[
 {"id": "g1-default",         "path": "?$S&run=br-g1&uuidframe=1&echoping=1&framing=aligned&observe=20000&type=1", "timeout": 45000},
 {"id": "g2-no-uuid-frame",   "path": "?$S&run=br-g2&uuidframe=0&echoping=1&framing=aligned&observe=12000&type=1", "timeout": 35000},
 {"id": "g3-no-ping-echo",    "path": "?$S&run=br-g3&uuidframe=1&echoping=0&framing=aligned&observe=22000&type=0", "timeout": 45000},
 {"id": "g4-split-frames",    "path": "?$S&run=br-g4&uuidframe=1&echoping=1&framing=split&observe=12000&type=1", "timeout": 35000},
 {"id": "g5-internal-to-guacd", "path": "?$S&run=br-g5&uuidframe=1&echoping=1&fwdinternal=1&observe=12000&type=1", "timeout": 35000},
 {"id": "g6-no-subprotocol",  "path": "?$S&run=br-g6&subproto=0&observe=6000&type=0", "timeout": 25000},
 {"id": "f-replay-browser-session", "path": "replay.html?file=screen-br-g1&piece=4093&delay=5&part=16384", "timeout": 200000},
 {"id": "f-replay-driver-session",  "path": "replay.html?file=screen-ssh-password&piece=4093&delay=5&part=16384", "timeout": 200000}
]
EOF
note "browser runs against http://$POD_IP:8443"
(cd /tmp/pw && node browser.mjs "http://$POD_IP:8443" "$EV/browser" /tmp/pw/runs.json) 2>&1 | tee "$EV/browser.log"

note "typescripts of the browser sessions: does the command typed in the page run on the target?"
for r in br-g1 br-g2 br-g4 br-g5; do
  echo "$r: BROWSER-81 lines in typescript-$r = $(guacd_cat "typescript-$r" | grep -a -c 'BROWSER-81')" | tee -a "$EV/browser-typescripts.txt"
done
kubectl -n guacamole exec access-spike -c guacd -- ls -l /rec | tee "$EV/rec-listing-after-browser.txt"
