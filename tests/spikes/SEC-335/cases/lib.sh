#!/bin/sh
# Shared settings of the SEC-335 start tests. Sourced by each case script.
# No cluster is used: every container runs under rootless podman with no network
# (a pod with --network none shares only its loopback between its containers).
export KUBECONFIG=/dev/null
T=/home/openova/.cache/ns-scratch/SEC-335-tools
R=$T/render
C=$T/cases
IMG_K8S=docker.io/alpine/k8s:1.31.4
IMG_CURL=docker.io/curlimages/curl:8.10.1
IMG_GRAFANA=docker.io/grafana/grafana:12.3.1
IMG_PDA=docker.io/powerdnsadmin/pda-legacy:0.4.2
IMG_KC=docker.io/bitnamilegacy/keycloak:26.3.3-debian-12-r0
IMG_KCCLI=docker.io/bitnamilegacy/keycloak-config-cli:6.4.0-debian-12-r11
IMG_PG=docker.io/library/postgres:16-alpine

# newvalue prints a fresh random test value (32 letters and digits).
newvalue() { tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32; }

# putfile DIR NAME VALUE writes VALUE with no trailing newline, mode 0644,
# the shape SEC-140 measured for a CSI-mounted object.
putfile() { mkdir -p "$1"; printf '%s' "$3" > "$1/$2"; chmod 0644 "$1/$2"; }

# capture_start POD DIR starts a listener on 127.0.0.1:8099 inside POD that
# reads each whole request (headers, then Content-Length bytes of body), writes it
# to DIR/req.<n>, and only then answers 200 with {}.
capture_start() {
  mkdir -p "$2"; chmod 0777 "$2"
  cat > "$2/../respond.sh" <<'EOF'
#!/bin/sh
f=/cap/req.$(date +%s)-$$
len=0
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$f"
  l=$(printf '%s' "$line" | tr -d '\r')
  case "$l" in
    [Cc]ontent-[Ll]ength:*) len=$(printf '%s' "$l" | sed 's/^[^:]*: *//') ;;
    "") break ;;
  esac
done
if [ "$len" -gt 0 ] 2>/dev/null; then head -c "$len" >> "$f"; fi
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}'
EOF
  chmod 0755 "$2/../respond.sh"
  podman run -d --name "$1-capture" --pod "$1" -v "$2/../respond.sh:/respond.sh:ro" -v "$2:/cap" "$IMG_K8S" nc -lk -p 8099 -e /respond.sh > /dev/null
}

# result CASE VERDICT TEXT appends one result line.
result() { printf '%s %s %s\n' "$1" "$2" "$3" | tee -a "$C/results.txt"; }
