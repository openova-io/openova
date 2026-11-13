#!/usr/bin/env bash
# Lab test ACC-020, part 2: the scripted sessions (items a, b, c, d, e, h, i,
# j, k-warm). Each run's result is one JSON line in evidence/drive-<id>.json.
# shellcheck source=tests/spikes/ACC-020/lib.sh
source "$(dirname "$0")/lib.sh"

drive() { # id, then driver flags
  local id=$1; shift
  timeout 240 kubectl -n guacamole exec access-spike -c agent -- /agent drive "$@" > "$EV/drive-$id.out" 2> "$EV/drive-$id.err"
  grep '^RESULT ' "$EV/drive-$id.out" | sed 's/^RESULT //' > "$EV/drive-$id.json"
  echo "=== drive $id ($*)"
  head -c 2500 "$EV/drive-$id.json"; echo
}

proxy_log() { kubectl -n catalyst-system logs deploy/k8s-ws-proxy --since="${1:-40s}" 2>&1 | tail -8 > "$EV/proxy-$2.log"; cat "$EV/proxy-$2.log"; }

note "item b: SSH with certificates from the OpenBao SSH mount ($(( $(date -u +%s) - $(cat "$EV/certs-issued-at.txt") ))s after issue, ttl 300s)"
drive b-ed25519 -scenario ssh-cert-ed25519 -tag CERT
drive b-ed25519-b64 -scenario ssh-cert-ed25519-b64 -tag CERTB
drive b-keyonly -scenario ssh-key-only-ed25519 -tag KEYONLY
drive b-ecdsa -scenario ssh-cert-ecdsa -tag CERTEC
drive b-rsa -scenario ssh-cert-rsa -tag CERTRSA
kubectl -n spike logs deploy/sshd 2>&1 | grep -E 'Accepted|Failed|cert|CERT|invalid|refused|error' | tail -30 | tee "$EV/sshd-auth.log"

note "item a and e: SSH with a password; recording files growing during the session"
drive a-password -scenario ssh-password -script growth -tag SSHPW -extra "stty size; hostname; id"
note "item e: delay from keystroke to bytes on disk (key events recorded)"
drive e-keys -scenario ssh-password -run ssh-keys -script keys -set recording-include-keys:true -tag KEYS

note "item h: clipboard controls on SSH (control run, then both disabled)"
drive h-ctl -scenario ssh-password -run clip-ctl -script clip -set disable-copy:false -set disable-paste:false -tag CLIPC
drive h-off -scenario ssh-password -run clip-off -script clip -tag CLIPO

note "item c: protocol kubernetes through the exec proxy built from main"
drive c-cert -scenario k8s-cert -tag K8S -extra "stty size; hostname; id"; proxy_log 40s c-cert
drive c-nocert -scenario k8s-nocert -tag K8SNC; proxy_log 40s c-nocert
drive c-sessioncert -scenario k8s-sessioncert -tag K8SSC; proxy_log 40s c-sessioncert
drive c-multiword -scenario k8s-multiword -script observe -hold 6; proxy_log 30s c-multiword

note "item d: VNC (with clipboard checks against the X selection of the target)"
vnc_run() { # id, driver flags
  local id=$1; shift
  drive "$id" -scenario vnc -run "$id" -script vnc -hold 22 "$@" &
  local pid=$!
  sleep 9
  kubectl -n spike exec deploy/vnc -- sh -c 'echo "clipboard=[$(DISPLAY=:0 timeout 3 xclip -o -selection clipboard 2>&1)] primary=[$(DISPLAY=:0 timeout 3 xclip -o -selection primary 2>&1)]"' \
    > "$EV/vnc-$id-server-clipboard.txt" 2>&1
  echo "server clipboard after paste ($id): $(cat "$EV/vnc-$id-server-clipboard.txt")"
  kubectl -n spike exec deploy/vnc -- sh -c "printf VNCCOPY-$id > /tmp/c.txt; (DISPLAY=:0 timeout 20 xclip -selection clipboard -quiet -i /tmp/c.txt >/dev/null 2>&1 &); echo set" >/dev/null 2>&1
  wait "$pid"
}
vnc_run d-vnc-ctl -set disable-copy:false -set disable-paste:false
vnc_run d-vnc-off
kubectl -n spike logs deploy/vnc --tail=40 > "$EV/vnc-server.log" 2>&1

note "item i: recording-write-existing into a FIFO"
kubectl -n guacamole exec access-spike -c guacd -- mkfifo /rec/screen-fifo
kubectl -n guacamole exec access-spike -c guacd -- ls -l /rec/screen-fifo | tee "$EV/fifo-before.txt"
drive i-fifo -scenario ssh-password -run fifo -set recording-write-existing:true -fifo /rec/screen-fifo -tag FIFO
kubectl -n guacamole exec access-spike -c guacd -- ls -l /rec | grep -E 'fifo' | tee "$EV/fifo-after.txt"

note "item j: memory and CPU of the guacd container (cgroup v2 files of the container)"
CID=$(kubectl -n guacamole get pod access-spike -o jsonpath='{.status.containerStatuses[?(@.name=="guacd")].containerID}' | sed 's#.*://##')
PID=$(sudo k3s crictl inspect "$CID" | jq -r '.info.pid')
CG=/sys/fs/cgroup$(sudo awk -F: '$1=="0"{print $3}' "/proc/$PID/cgroup")
echo "guacd container $CID pid $PID cgroup $CG" | tee "$EV/j-samples.txt"
sample() { # label
  local mem cpu anon file
  mem=$(sudo cat "$CG/memory.current"); cpu=$(sudo awk '$1=="usage_usec"{print $2}' "$CG/cpu.stat")
  anon=$(sudo awk '$1=="anon"{print $2}' "$CG/memory.stat"); file=$(sudo awk '$1=="file"{print $2}' "$CG/memory.stat")
  echo "$1 t=$(date +%s.%N) memory_current=$mem anon=$anon file=$file cpu_usage_usec=$cpu procs=$(sudo wc -l < "$CG/cgroup.procs")" | tee -a "$EV/j-samples.txt"
}
sample no-session-0; sleep 10; sample no-session-10
drive j-idle -scenario ssh-password -run res-idle -script idle -hold 45 -tag IDLE &
JP=$!
sleep 18; sample idle-session-18; sleep 20; sample idle-session-38
wait "$JP"
sleep 3; sample after-idle
drive j-active -scenario ssh-password -run res-active -script active -active 30 -tag ACT &
JP=$!
sleep 12; sample active-session-12; sleep 15; sample active-session-27
kubectl -n guacamole exec access-spike -c guacd -- ps -o pid,rss,args 2>/dev/null | tee "$EV/j-ps.txt" \
  || kubectl -n guacamole exec access-spike -c guacd -- ps | tee "$EV/j-ps.txt"
wait "$JP"
sudo cat "$CG/memory.peak" 2>/dev/null | sed 's/^/memory_peak_lifetime=/' | tee -a "$EV/j-samples.txt"
kubectl -n guacamole top pod access-spike --containers 2>&1 | tee -a "$EV/j-samples.txt"

note "recording volume after the scripted sessions"
kubectl -n guacamole exec access-spike -c guacd -- ls -l /rec | tee "$EV/rec-listing.txt"

note "item k: three more session pods with warm images"
for n in 1 2 3; do
  time_to_ready "access-spike-k$n" "$([ "${SESSION_POD_SHAPE:-strict}" = relaxed ] && echo 1 || echo 0)"
  kubectl -n guacamole delete pod "access-spike-k$n" --wait=false >/dev/null
done
