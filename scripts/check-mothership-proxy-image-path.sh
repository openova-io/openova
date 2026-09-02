#!/usr/bin/env bash
# check-mothership-proxy-image-path.sh — prov pre-flight check 7 (#6778):
# every image a fresh Sovereign pulls through the mothership proxy must be
# FULLY resolvable — the way the kubelet resolves it — BEFORE a fire.
# Read-only: GETs only, never a registry or cluster write.
#
# ─── THE FAILURE CLASS THIS GUARDS AGAINST ────────────────────────────────
#
# 2026-09-01 the mothership registry blob store was wiped by hand (memory
# `reference_mothership_harbor_retention_filled_disk_never_wipe_registry`).
# Harbor's proxy-cache kept its MANIFEST rows, so every surface the pre-flight
# measured still answered 200:
#     harbor.openova.io/api/v2.0/health              -> 200 (components up)
#     http://212.72.24.20:5000/v2/ and :5002/v2/     -> 200 (registry ping)
#     GET /v2/proxy-ghcr/cloudnative-pg/postgresql/manifests/16 -> 200 (index)
# while the OCI index's linux/amd64 CHILD manifest, and the blobs behind it,
# were gone:
#     GET .../manifests/sha256:<amd64 child>            -> 404
# containerd fails that pull with `failed to copy: httpReadSeeker`, so on
# hw306 and hw307 every `shared-pg-*-initdb` Pod sat in ImagePullBackOff,
# Phase 1 timed out after 2 h 40 min, keycloak and every SSO-dependent chart
# never installed, and two fresh Sovereigns were burned on a fault the
# mothership already had. A health endpoint and a `/v2/` ping cannot fail on a
# damaged cache entry; neither can a manifest-by-tag GET. Only the full walk
# the kubelet performs — tag -> index -> platform child -> config blob — can.
#
# ─── WHY THE STAGE MATTERS (the contract's core distinction) ──────────────
#
# containerd does NOT treat every registry miss alike, so neither does this:
#
#   * INDEX 404 at the mirror  -> survivable. containerd falls back to the
#     upstream host (with the node's pull Secret). Slow and hostage to that
#     registry's uptime, but it converges. Reported WARN, and the fallback is
#     PROBED: if upstream cannot serve it either, the pin is genuinely
#     unpullable and that IS a FAIL.
#   * CHILD or CONFIG missing behind a 200 index -> fatal. containerd has
#     already accepted this mirror's index; there is no fallback from mid-walk.
#     The pull dies. This is the hw306/hw307 shape and it hard-FAILS the gate.
#
# Collapsing those two into one verdict would either fire into a wedge or
# refuse to fire over a slow path. Both were observed live on 2026-09-02.
#
# ─── CONTRACT ─────────────────────────────────────────────────────────────
#
#   1. The image list is BUILT FROM THE REPO, never a hand list:
#        - `scripts/check-freshprov-image-registry.sh --list-images` renders
#          every bootstrap-kit chart (the #5281 renderer; there is no second
#          one) and emits every install `image:` / `imageName:` ref;
#        - the CNPG images the shared-pg / per-app postgres Clusters pull:
#          platform/postgres (imageName + pgVersion), platform/newapi
#          (cnpg.cluster.imageName), platform/cnpg-pair (repository:tag when
#          pinned), platform/cnpg (the cloudnative-pg controller image) —
#          those charts render empty at default values, so the pins are read
#          from their values.yaml;
#        - the Phase-0 images cloud-init pulls: the flux `crictl pull` warm-up
#          list and the cilium `helm upgrade --install --version` pair, read
#          from infra/providers/_shared/cloudinit-control-plane.tftpl.
#   2. Each ref is resolved THE WAY THE NODE RESOLVES IT, from the Terraform
#      that writes /etc/rancher/k3s/registries.yaml
#      (infra/providers/huawei/main.tf `registry_mirror_yaml_huawei`). That
#      heredoc is YAML and is parsed as YAML — host -> endpoint + rewrite
#      rules — so a mirror edit changes what this probe walks, with nothing to
#      keep in sync. On kom4dc that endpoint is the bastion EIP
#      (http://212.72.24.20:5000 / :5002), the mothership Harbor's NAT-bypass
#      face (#3913) — NOT https://harbor.openova.io, which a node never uses
#      and which answers differently (measured 2026-09-02: a child manifest
#      404 on the external face was 200 on the face the node pulls through).
#      A host with no mirror entry (xpkg.upbound.io, mirror.gcr.io, …) is
#      SKIPPED and named — that class belongs to
#      scripts/check-freshprov-image-registry.sh (#5281), not here.
#   3. Per image: GET the manifest for the tag (OCI index, docker list and
#      single manifests all accepted); if it is an index, GET the linux/amd64
#      child by digest; then GET the config blob with `Range: bytes=0-255`
#      (200/206). 401 challenges are answered by minting a token from the
#      WWW-Authenticate realm — one code path for Harbor, ghcr, Docker Hub,
#      quay and gcr — with the robot pull credential for the mirror host and
#      the ghcr pull credential for upstream ghcr. Tokens are cached per realm
#      and never printed. Redirects are followed (Harbor 301s a Docker Hub
#      path onto library/, and blob GETs may redirect to an object store);
#      a token is never handed to a host it was not minted for.
#   4. Summary line (paste it into the train manifest):
#          IMAGE-PATH PASS <n> images resolved through proxy-* (index+child+config)
#          IMAGE-PATH FAIL <k>/<n>: <first 5 failures>
#      An EMPTY image list is a FAIL ("no images extracted") — a probe that
#      passes because it probed nothing is worse than no probe.
#
# ─── USAGE ────────────────────────────────────────────────────────────────
#   HW_TFVARS=<tofu.auto.tfvars.json> scripts/check-mothership-proxy-image-path.sh
#       credentials = .harbor_robot_token / .ghcr_pull_token of that file
#       (or HARBOR_ROBOT_TOKEN / GHCR_PULL_TOKEN in the environment)
#   scripts/check-mothership-proxy-image-path.sh --images <file>   # targeted list
#   scripts/check-mothership-proxy-image-path.sh --list-only       # print the built list, no network
#   scripts/check-mothership-proxy-image-path.sh --resolve <ref>   # show the node's resolution of one ref
#   scripts/check-mothership-proxy-image-path.sh --self-test       # local fake-mirror fixture
# Env: PROBE_PARALLEL (default 4), PROBE_TIMEOUT (seconds, default 25),
#      MIRROR_TF (the Terraform carrying registries.yaml),
#      HARBOR_ROBOT_USER (default robot$catalyst), GHCR_PULL_USER.
# Exit: 0 = every image resolved (WARN mirror-misses allowed); 1 = at least
#       one image failed; 2 = usage / vacuity (no images) / setup error.
#
# Credentials are NEVER printed. Fix for a FAIL at stage=child or stage=config:
# purge the damaged proxy-cache entry as Harbor ADMIN (the robot gets 401 on
# delete) so the next pull re-fetches from upstream, then re-run.
# Refs #6778 #6795.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
HARBOR_ROBOT_USER="${HARBOR_ROBOT_USER:-robot\$catalyst}"
GHCR_PULL_USER="${GHCR_PULL_USER:-openova-bot}"
PROBE_PARALLEL="${PROBE_PARALLEL:-4}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-25}"
MIRROR_TF="${MIRROR_TF:-${REPO_ROOT}/infra/providers/huawei/main.tf}"
CLOUDINIT="${CLOUDINIT:-${REPO_ROOT}/infra/providers/_shared/cloudinit-control-plane.tftpl}"
HARBOR_HOST="${HARBOR_HOST:-harbor.openova.io}"

IMAGES_FILE=""; MODE="probe"; RESOLVE_REF=""
while [ $# -gt 0 ]; do
  case "$1" in
    --images) IMAGES_FILE="${2:?--images needs a file}"; shift 2 ;;
    --parallel) PROBE_PARALLEL="${2:?--parallel needs N}"; shift 2 ;;
    --list-only) MODE="list"; shift ;;
    --resolve) MODE="resolve"; RESOLVE_REF="${2:?--resolve needs an image ref}"; shift 2 ;;
    --self-test) MODE="self-test"; shift ;;
    -h|--help) sed -n '2,120p' "$0"; exit 0 ;;
    *) echo "usage error: unknown argument '$1' (see --help)" >&2; exit 2 ;;
  esac
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ─── The worker: node-path resolution + the 3-stage walk + the fixture ─────
WORKER="${WORK}/probe.py"
cat > "$WORKER" <<'PY'
import base64, http.client, json, os, re, ssl, sys, time, urllib.parse, hashlib

ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])
INDEX_TYPES = {"application/vnd.oci.image.index.v1+json",
               "application/vnd.docker.distribution.manifest.list.v2+json"}

def mirror_table(tf_path):
    """The node's own resolution table, parsed from the Terraform that WRITES
    /etc/rancher/k3s/registries.yaml. The heredoc body is YAML, so it is read
    as YAML rather than pattern-matched: host -> {endpoint: [...],
    rewrite: {regex: replacement}}."""
    src = open(tf_path).read()
    m = re.search(r'registry_mirror_yaml_huawei\s*=\s*<<-?EOT\n(.*?)\n\s*EOT', src, re.S)
    if not m:
        raise SystemExit("FATAL: registry_mirror_yaml_huawei heredoc not found in %s" % tf_path)
    body = re.sub(r'\$\{[^}]*\}', 'REDACTED', m.group(1))
    import yaml
    return (yaml.safe_load(body) or {}).get("mirrors", {}) or {}

def apply_rewrite(rules, path):
    for pattern, repl in (rules or {}).items():
        try:
            rx = re.compile(pattern)
        except re.error:
            continue
        mm = rx.match(path)
        if mm:
            out = repl.replace("$0", mm.group(0))
            for i in range(1, (mm.re.groups or 0) + 1):
                out = out.replace("$%d" % i, mm.group(i) or "")
            return out
    return None

def split_ref(ref):
    """image ref -> (host, path, reference). A bare name resolves to docker.io."""
    name, digest = ref.split("@", 1) if "@" in ref else (ref, None)
    last = name.rsplit("/", 1)[-1]
    if ":" in last:
        name, tag = name.rsplit(":", 1)
    else:
        tag = "latest"
    first = name.split("/", 1)[0]
    if "/" in name and ("." in first or ":" in first or first == "localhost"):
        host, path = first, name.split("/", 1)[1]
    else:
        host, path = "docker.io", name
    # Docker Hub's implicit namespace: `busybox` and `docker.io/busybox` are
    # both `library/busybox` on the wire. The mirror serves the wire path.
    if host in ("docker.io", "registry-1.docker.io", "index.docker.io") and "/" not in path:
        path = "library/" + path
    return host, path, (digest or tag)

def resolve(ref, mirrors):
    """-> ("ok", endpoint, repo, reference, upstream_host, upstream_path)
       |  ("skip", reason, None, None, None, None)"""
    if "{{" in ref or "${" in ref:
        return ("skip", "helm/envsubst placeholder", None, None, None, None)
    if "*" in ref or "?" in ref:
        # A Kyverno allowed-image GLOB (`*/proxy-*/*`) renders into an `image:`
        # field but is a policy pattern, not something a kubelet pulls.
        return ("skip", "image GLOB pattern (Kyverno policy match), not a pull", None, None, None, None)
    if "/" not in ref and ":" not in ref and "@" not in ref:
        # A registry-less, tag-less bare word is a cloud SERVER-image slug
        # (`ubuntu-24.04`, the cluster-autoscaler node config), not a container
        # image — the same case scripts/check-freshprov-image-registry.sh names.
        return ("skip", "registry-less untagged slug '%s' — a server image, not a container image" % ref, None, None, None, None)
    host, path, reference = split_ref(ref)
    entry = mirrors.get(host)
    if not entry:
        return ("skip", "host %s is not in the node registry mirror (#5281 class)" % host, None, None, None, None)
    endpoints = entry.get("endpoint") or []
    if not endpoints:
        return ("skip", "host %s has a mirror entry with no endpoint" % host, None, None, None, None)
    repo = apply_rewrite(entry.get("rewrite"), path) or path
    return ("ok", endpoints[0].rstrip("/"), repo, reference, host, path)

class Client:
    """GET with WWW-Authenticate Bearer handling — one code path for Harbor,
    ghcr, Docker Hub, quay and gcr. Tokens cached per realm, never printed."""
    def __init__(self, timeout, creds, cache_dir):
        self.timeout = timeout; self.creds = creds; self.cache = cache_dir; self.conns = {}
    def _conn(self, scheme, host, port):
        key = (scheme, host, port)
        if key not in self.conns:
            if scheme == "https":
                ctx = ssl.create_default_context()
                if os.environ.get("PROBE_INSECURE") == "1":
                    ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
                self.conns[key] = http.client.HTTPSConnection(host, port or 443, timeout=self.timeout, context=ctx)
            else:
                self.conns[key] = http.client.HTTPConnection(host, port or 80, timeout=self.timeout)
        return self.conns[key]
    def _raw(self, url, headers):
        u = urllib.parse.urlsplit(url)
        key = (u.scheme, u.hostname, u.port)
        for attempt in (1, 2):
            try:
                c = self._conn(*key)
                c.request("GET", u.path + (("?" + u.query) if u.query else ""), headers=headers)
                r = c.getresponse(); body = r.read()
                return r.status, {k.lower(): v for k, v in r.getheaders()}, body
            except Exception as e:
                self.conns.pop(key, None)
                if attempt == 2:
                    return 0, {}, str(e).encode()
    def _token(self, challenge, host):
        parts = dict(re.findall(r'(\w+)="([^"]*)"', challenge))
        realm = parts.get("realm")
        if not realm:
            return ""
        q = {k: parts[k] for k in ("service", "scope") if parts.get(k)}
        url = realm + ("?" + urllib.parse.urlencode(q) if q else "")
        ck = None
        if self.cache:
            ck = os.path.join(self.cache, hashlib.sha1(url.encode()).hexdigest() + ".tok")
            if os.path.exists(ck) and time.time() - os.path.getmtime(ck) < 900:
                with open(ck) as f:
                    return f.read().strip()
        hdr = {"Accept": "application/json"}
        cred = self.creds.get(host)
        if cred:
            hdr["Authorization"] = "Basic " + base64.b64encode(("%s:%s" % cred).encode()).decode()
        st, _, body = self._raw(url, hdr)
        if st != 200:
            return ""
        try:
            d = json.loads(body); tok = d.get("token") or d.get("access_token") or ""
        except Exception:
            return ""
        if ck and tok:
            tmp = ck + ".%d" % os.getpid()
            with open(tmp, "w") as f:
                f.write(tok)
            os.replace(tmp, ck)
        return tok
    def get(self, url, extra=None, redirects=3):
        headers = dict(extra or {})
        host = urllib.parse.urlsplit(url).hostname
        st, hd, body = self._raw(url, headers)
        for _ in range(redirects + 1):
            if st == 401 and hd.get("www-authenticate", "").lower().startswith("bearer"):
                tok = self._token(hd["www-authenticate"], host)
                if not tok:
                    break
                headers["Authorization"] = "Bearer " + tok
                st, hd, body = self._raw(url, headers)
            if st in (301, 302, 303, 307, 308) and hd.get("location"):
                nxt = urllib.parse.urljoin(url, hd["location"])
                if urllib.parse.urlsplit(nxt).hostname != host:
                    headers.pop("Authorization", None)   # never hand a token to a foreign blob store
                url = nxt; host = urllib.parse.urlsplit(url).hostname
                st, hd, body = self._raw(url, headers)
                continue
            break
        return st, hd, body

def walk(cli, base, repo, reference):
    """The kubelet's own walk: tag -> index -> linux/amd64 child -> config blob.
    -> (True, note) | (False, stage, http, detail)"""
    st, hd, body = cli.get("%s/v2/%s/manifests/%s" % (base, repo, reference), {"Accept": ACCEPT})
    if st != 200:
        return (False, "index", st, "%s:%s" % (repo, reference))
    try:
        doc = json.loads(body)
    except Exception:
        return (False, "index", st, "%s:%s (manifest is not JSON)" % (repo, reference))
    mt = (hd.get("content-type") or "").split(";")[0].strip() or doc.get("mediaType", "")
    child = None
    if mt in INDEX_TYPES or "manifests" in doc:
        cands = [m for m in doc.get("manifests", [])
                 if (m.get("platform") or {}).get("os") == "linux"
                 and (m.get("platform") or {}).get("architecture") == "amd64"]
        if not cands:
            return (False, "child", 0, "%s:%s (index carries no linux/amd64 entry)" % (repo, reference))
        child = cands[0]["digest"]
        st, hd, body = cli.get("%s/v2/%s/manifests/%s" % (base, repo, child), {"Accept": ACCEPT})
        if st != 200:
            return (False, "child", st, "%s@%s" % (repo, child))
        try:
            doc = json.loads(body)
        except Exception:
            return (False, "child", st, "%s@%s (child manifest is not JSON)" % (repo, child))
    cfg = (doc.get("config") or {}).get("digest")
    if not cfg:
        if doc.get("schemaVersion") == 1:
            return (True, "child=%s config=n/a(schema1)" % (child or "-")[:19])
        return (False, "config", 0, "%s (manifest carries no config digest)" % repo)
    st, hd, body = cli.get("%s/v2/%s/blobs/%s" % (base, repo, cfg), {"Range": "bytes=0-255"})
    if st not in (200, 206):
        return (False, "config", st, "%s blob %s" % (repo, cfg[:19]))
    return (True, "child=%s config=%s" % ((child or "-")[:19], st))

def probe(ref, mirrors, creds, cache):
    kind, base, repo, reference, up_host, up_path = resolve(ref, mirrors)
    if kind == "skip":
        return "SKIP %s %s" % (ref, base)
    cli = Client(float(os.environ.get("PROBE_TIMEOUT", "25")), creds, cache)
    res = walk(cli, base, repo, reference)
    if res[0]:
        return "PASS %s -> %s/%s:%s %s" % (ref, base, repo, reference, res[1])
    stage, code, detail = res[1], res[2], res[3]
    if stage != "index":
        # THE hw306/hw307 SHAPE. containerd has already accepted this mirror's
        # index; a missing child or config aborts the pull outright (`failed to
        # copy: httpReadSeeker`). There is no upstream fallback from mid-walk,
        # so there is no second opinion to ask for.
        return "FAIL %s stage=%s http=%s %s [the mirror served an index whose %s is gone — the pull dies here, no fallback]" % (
            ref, stage, code, detail, stage)
    # A clean miss at the INDEX is survivable: containerd falls back to the
    # upstream host with the node's pull Secret. Probe that fallback rather than
    # calling a slow path a wedge — and FAIL when upstream cannot serve it
    # either, which is a genuinely unpullable pin.
    up_base = "https://%s" % ("registry-1.docker.io" if up_host in ("docker.io", "index.docker.io") else up_host)
    ures = walk(cli, up_base, up_path, reference)
    if ures[0]:
        return "WARN %s mirror-miss stage=index http=%s (%s) — upstream %s/%s serves it, so the node falls back to a DIRECT pull (slow, and hostage to that registry)" % (
            ref, code, detail, up_host, up_path)
    return "FAIL %s stage=index http=%s %s [upstream %s/%s also fails: stage=%s http=%s]" % (
        ref, code, detail, up_host, up_path, ures[1], ures[2])

# ─── self-test fixture: a fake mirror with healthy and damaged entries ─────
def serve(port_file):
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
    OK_CHILD = "sha256:" + "a" * 64; BROKEN_CHILD = "sha256:" + "b" * 64; CFG = "sha256:" + "c" * 64
    def index(child):
        return json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
                           "manifests": [{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": child, "size": 1,
                                          "platform": {"os": "linux", "architecture": "amd64"}},
                                         {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + "d" * 64, "size": 1,
                                          "platform": {"os": "linux", "architecture": "arm64"}}]}).encode()
    def manifest():
        return json.dumps({"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
                           "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": CFG, "size": 256},
                           "layers": []}).encode()
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a): pass
        def _send(self, code, body=b"", ctype="application/json", extra=None):
            self.send_response(code); self.send_header("Content-Type", ctype)
            for k, v in (extra or {}).items():
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
        def do_GET(self):
            p = urllib.parse.urlsplit(self.path).path
            if p == "/token":
                return self._send(200, json.dumps({"token": "fixture-token"}).encode())
            if p == "/v2/":
                # the ping the OLD pre-flight trusted: 200 even for the damaged entry
                return self._send(200, b"{}")
            parts = p.split("/")
            if len(parts) >= 5 and parts[1] == "v2" and parts[-2] == "manifests":
                repo, ref = "/".join(parts[2:-2]), parts[-1]
                if not self.headers.get("Authorization"):
                    return self._send(401, b"{}", extra={"WWW-Authenticate": 'Bearer realm="http://%s/token",service="fixture"' % self.headers.get("Host")})
                if repo.endswith("redirected/app"):
                    # Harbor redirects EVERY path under a repo it relocates (a
                    # Docker Hub short path onto library/), tag and digest alike.
                    return self._send(301, b"", extra={"Location": "/v2/%s/manifests/%s" % (repo.replace("redirected/app", "healthy/app"), ref)})
                healthy = repo.endswith("healthy/app"); damaged = repo.endswith("damaged/app"); single = repo.endswith("single/app")
                if not (healthy or damaged or single):
                    return self._send(404, b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}')
                if not ref.startswith("sha256:"):
                    if single:
                        return self._send(200, manifest(), "application/vnd.oci.image.manifest.v1+json")
                    return self._send(200, index(OK_CHILD if healthy else BROKEN_CHILD), "application/vnd.oci.image.index.v1+json")
                if ref == OK_CHILD:
                    return self._send(200, manifest(), "application/vnd.oci.image.manifest.v1+json")
                return self._send(404, b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}')   # the hw306/hw307 shape
            if len(parts) >= 5 and parts[1] == "v2" and parts[-2] == "blobs":
                repo = "/".join(parts[2:-2])
                if repo.endswith("redirected/app"):
                    return self._send(301, b"", extra={"Location": "/v2/%s/blobs/%s" % (repo.replace("redirected/app", "healthy/app"), parts[-1])})
                if parts[-1] == CFG:
                    self.send_response(206); self.send_header("Content-Type", "application/octet-stream")
                    self.send_header("Content-Range", "bytes 0-255/256"); self.send_header("Content-Length", "256")
                    self.end_headers(); self.wfile.write(b"{" + b" " * 255); return
                return self._send(404, b'{"errors":[{"code":"BLOB_UNKNOWN"}]}')
            return self._send(404, b"{}")
    srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
    with open(port_file + ".tmp", "w") as f:
        f.write(str(srv.server_address[1]))
    os.replace(port_file + ".tmp", port_file)
    srv.serve_forever()

def load_ctx():
    mirrors = json.loads(os.environ["PROBE_MIRRORS"])
    creds = {}
    if os.environ.get("PROBE_HARBOR_HOST") and os.environ.get("PROBE_ROBOT_TOKEN"):
        creds[os.environ["PROBE_HARBOR_HOST"]] = (os.environ.get("PROBE_ROBOT_USER", "robot$catalyst"), os.environ["PROBE_ROBOT_TOKEN"])
    if os.environ.get("PROBE_GHCR_TOKEN"):
        creds["ghcr.io"] = (os.environ.get("PROBE_GHCR_USER", "openova-bot"), os.environ["PROBE_GHCR_TOKEN"])
    return mirrors, creds, os.environ.get("PROBE_TOKEN_CACHE", "")

if __name__ == "__main__":
    op = sys.argv[1]
    if op == "mirrors":
        print(json.dumps(mirror_table(sys.argv[2])))
    elif op == "probe":
        mirrors, creds, cache = load_ctx()
        print(probe(sys.argv[2], mirrors, creds, cache), flush=True)
    elif op == "resolve":
        mirrors, _, _ = load_ctx()
        kind, base, repo, reference, _, _ = resolve(sys.argv[2], mirrors)
        print("%s %s %s" % (kind.upper(), base if kind == "skip" else "%s/%s" % (base, repo), reference or ""))
    elif op == "serve":
        serve(sys.argv[2])
PY

# ─── Build the image list from the repo ───────────────────────────────────
build_image_list() {
  local out="$1"
  : > "$out"
  # (a) every rendered bootstrap-kit install image — the #5281 renderer.
  ROOT="$REPO_ROOT" bash "${SCRIPT_DIR}/check-freshprov-image-registry.sh" --list-images >> "$out" \
    || echo "WARN: check-freshprov-image-registry.sh --list-images exited non-zero (the rendered set may be partial)" >&2
  # (b) CNPG postgres + cloudnative-pg controller pins. platform/postgres renders
  #     no `image:` (the CNPG Cluster CR carries imageName) and cnpg-pair/newapi
  #     are conditional at default values, so the pins are read from the values.
  python3 - "$REPO_ROOT" <<'PY' >> "$out"
import sys, os, yaml
root = sys.argv[1]
def load(p):
    try:
        with open(os.path.join(root, p)) as f:
            return yaml.safe_load(f) or {}
    except Exception:
        return {}
out = set()
def walk(d):
    if isinstance(d, dict):
        if isinstance(d.get("imageName"), str) and "/" in d["imageName"]:
            img = d["imageName"]; pg = str(d.get("pgVersion") or "")
            out.add(img if ":" in img.rsplit("/", 1)[-1] else (img + ":" + pg if pg else img))
        if isinstance(d.get("repository"), str) and "cloudnative-pg" in d["repository"] and d.get("tag"):
            out.add("%s:%s" % (d["repository"], d["tag"]))
        for x in d.values():
            walk(x)
    elif isinstance(d, list):
        for x in d:
            walk(x)
for p in ("platform/postgres/chart/values.yaml", "platform/newapi/chart/values.yaml",
          "platform/cnpg-pair/chart/values.yaml", "platform/cnpg/chart/values.yaml"):
    walk(load(p))
for x in sorted(out):
    print(x)
PY
  # (c) Phase-0 cloud-init pulls: the flux `crictl pull` warm-up list + the
  #     cilium chart version the node helm-installs.
  if [ -s "$CLOUDINIT" ]; then
    awk '/for img in \\$/{f=1; next} f && /; do/{f=0} f{gsub(/[[:space:]\\]/,""); if ($0!="") print}' "$CLOUDINIT" >> "$out"
    cv="$(grep -oE 'helm upgrade --install cilium cilium/cilium --version [0-9][0-9.]*' "$CLOUDINIT" | head -1 | awk '{print $NF}')"
    if [ -n "$cv" ]; then printf 'quay.io/cilium/cilium:v%s\nquay.io/cilium/operator-generic:v%s\n' "$cv" "$cv" >> "$out"; fi
  else
    echo "WARN: cloud-init source not found at $CLOUDINIT — Phase-0 flux/cilium images not added" >&2
  fi
  sed -i '/^$/d' "$out"
  sort -u -o "$out" "$out"
}

# ─── Probe runner ─────────────────────────────────────────────────────────
# run_probe <images-file> — per-image lines + the IMAGE-PATH summary.
# 0 = pass (WARN mirror-misses allowed), 1 = fail, 2 = vacuity.
run_probe() {
  local list="$1" results="${WORK}/results.txt" n start elapsed pass fail warn skip probed
  n="$(sed '/^$/d' "$list" | wc -l)"
  if [ "$n" -eq 0 ]; then
    echo "IMAGE-PATH FAIL 0/0: no images extracted"
    return 2
  fi
  mkdir -p "${WORK}/tokens"
  start="$(date +%s)"
  sed '/^$/d' "$list" | PROBE_TOKEN_CACHE="${WORK}/tokens" PROBE_TIMEOUT="$PROBE_TIMEOUT" \
    xargs -P "$PROBE_PARALLEL" -n 1 -d '\n' python3 "$WORKER" probe > "$results" 2>>"${WORK}/probe.err"
  elapsed=$(( $(date +%s) - start ))
  pass="$(grep -c '^PASS ' "$results" || true)"
  fail="$(grep -c '^FAIL ' "$results" || true)"
  warn="$(grep -c '^WARN ' "$results" || true)"
  skip="$(grep -c '^SKIP ' "$results" || true)"
  sort "$results"
  [ -s "${WORK}/probe.err" ] && { echo "  worker stderr:"; head -5 "${WORK}/probe.err"; }
  [ "$skip" -gt 0 ] && echo "  ⚠ ${skip} ref(s) skipped (no node-mirror entry — the #5281 guard owns that class): $(grep '^SKIP ' "$results" | awk '{print $2}' | head -5 | tr '\n' ' ')"
  [ "$warn" -gt 0 ] && echo "  ⚠ ${warn} ref(s) miss the mirror at the index and fall back to a DIRECT upstream pull (slow, not a wedge): $(grep '^WARN ' "$results" | awk '{print $2}' | head -5 | tr '\n' ' ')"
  probed=$((pass + warn + fail))
  echo "  ℹ probed ${probed} image(s) in ${elapsed}s (parallel=${PROBE_PARALLEL}, node-mirror endpoints from $(basename "$MIRROR_TF"))"
  if [ "$probed" -eq 0 ]; then
    echo "IMAGE-PATH FAIL 0/0: no images extracted (every ref was skipped or the worker produced no result)"
    return 2
  fi
  if [ "$fail" -eq 0 ]; then
    echo "IMAGE-PATH PASS $((pass + warn)) images resolved through proxy-* (index+child+config)"
    return 0
  fi
  local first5
  first5="$(grep '^FAIL ' "$results" | head -5 | awk '{printf "%s%s (%s %s)", (NR>1?"; ":""), $2, $3, $4}')"
  echo "IMAGE-PATH FAIL ${fail}/${probed}: ${first5}"
  return 1
}

# ─── Self-test: the probe logic against a local fake mirror ───────────────
if [ "$MODE" = "self-test" ]; then
  st_fail=0
  expect() { # expect <label> <want-rc> <got-rc> <must-contain> <output>
    if [ "$3" = "$2" ] && printf '%s' "$5" | grep -q -- "$4"; then echo "  ok   $1"
    else echo "  FAIL $1 (rc=$3 want $2; output must contain '$4')"; printf '%s\n' "$5" | sed 's/^/       | /'; st_fail=1; fi
  }
  echo "== self-test: the node's resolution table comes from the Terraform =="
  real_mirrors="$(python3 "$WORKER" mirrors "$MIRROR_TF" 2>&1)"; mrc=$?
  expect "registries.yaml heredoc parses out of $(basename "$MIRROR_TF")" 0 "$mrc" '"ghcr.io"' "$real_mirrors"
  export PROBE_MIRRORS="$real_mirrors"
  r() { python3 "$WORKER" resolve "$1"; }
  expect "ghcr bare -> bastion proxy-ghcr"        0 0 "^OK http://212.72.24.20:5000/proxy-ghcr/cloudnative-pg/postgresql 16$" "$(r ghcr.io/cloudnative-pg/postgresql:16)"
  expect "private catalyst -> hosted project"     0 0 "^OK http://212.72.24.20:5000/openova-io/openova/catalyst-api 1e45db8$" "$(r ghcr.io/openova-io/openova/catalyst-api:1e45db8)"
  expect "harbor host -> the same bastion face"   0 0 "^OK http://212.72.24.20:5000/proxy-ghcr/cloudnative-pg/postgresql 16$" "$(r harbor.openova.io/proxy-ghcr/cloudnative-pg/postgresql:16)"
  expect "quay -> proxy-quay"                     0 0 "^OK http://212.72.24.20:5000/proxy-quay/cilium/cilium v1.19.3$" "$(r quay.io/cilium/cilium:v1.19.3)"
  expect "docker.io -> the :5002 face, path kept" 0 0 "^OK http://212.72.24.20:5002/velero/velero v1.18.0$" "$(r docker.io/velero/velero:v1.18.0)"
  expect "short name -> :5002 library/"           0 0 "^OK http://212.72.24.20:5002/library/busybox 1.36$" "$(r busybox:1.36)"
  expect "digest ref probes BY DIGEST"            0 0 "^OK http://212.72.24.20:5000/proxy-k8s/pause sha256:0123$" "$(r registry.k8s.io/pause:3.9@sha256:0123)"
  expect "docker.io official image gets library/" 0 0 "^OK http://212.72.24.20:5002/library/busybox latest$" "$(r docker.io/busybox:latest)"
  expect "host with no mirror entry is SKIP"      0 0 "^SKIP host xpkg.upbound.io is not in the node registry mirror" "$(r xpkg.upbound.io/crossplane/crossplane:v1.18.0)"
  expect "placeholder is SKIP"                    0 0 "^SKIP helm/envsubst placeholder" "$(r '{{ .Values.image }}:latest')"
  expect "Kyverno allowed-image GLOB is SKIP"     0 0 "^SKIP image GLOB pattern" "$(r '*/proxy-*/*')"
  expect "server-image slug is SKIP"              0 0 "^SKIP registry-less untagged slug" "$(r ubuntu-24.04)"

  port_file="${WORK}/port"
  python3 "$WORKER" serve "$port_file" & srv_pid=$!
  for _ in $(seq 1 50); do [ -s "$port_file" ] && break; sleep 0.1; done
  [ -s "$port_file" ] || { echo "SELF-TEST FAILED — the fixture mirror did not start" >&2; kill "$srv_pid" 2>/dev/null; exit 1; }
  fport="$(cat "$port_file")"
  # A fixture mirror table of the same SHAPE as the real one: every upstream
  # host is mirrored onto the fixture, so the probe walks it end to end.
  fixture_mirrors="$(python3 -c '
import json,sys
p=sys.argv[1]
print(json.dumps({"ghcr.io": {"endpoint": ["http://127.0.0.1:%s" % p], "rewrite": {"(.*)": "proxy-ghcr/$1"}},
                  "harbor.openova.io": {"endpoint": ["http://127.0.0.1:%s" % p]}}))' "$fport")"
  export PROBE_MIRRORS="$fixture_mirrors"
  echo "== self-test: all-200 fixture must PASS =="
  printf 'ghcr.io/healthy/app:16\nharbor.openova.io/single/app:1.0\nharbor.openova.io/redirected/app:1.0\n' > "${WORK}/ok.txt"
  out="$(run_probe "${WORK}/ok.txt")"; rc=$?
  expect "healthy index+child+config (and a 301) -> PASS 3" 0 "$rc" "^IMAGE-PATH PASS 3 images resolved through proxy-\* (index+child+config)$" "$out"
  expect "the 401 challenge was answered (no auth failures)" 0 0 "^PASS harbor.openova.io/single/app:1.0" "$out"
  echo "== self-test: index 200 + amd64 child 404 must FAIL (the hw306/hw307 shape) =="
  printf 'ghcr.io/healthy/app:16\nghcr.io/damaged/app:16\n' > "${WORK}/bad.txt"
  out="$(run_probe "${WORK}/bad.txt")"; rc=$?
  expect "damaged child -> FAIL 1/2 naming stage=child http=404" 1 "$rc" "^IMAGE-PATH FAIL 1/2: ghcr.io/damaged/app:16 (stage=child http=404)$" "$out"
  expect "and it says the pull dies with no fallback" 0 0 "the pull dies here, no fallback" "$out"
  expect "the /v2/ ping the old check trusted answers 200 for that same damaged entry" 0 0 "^200$" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${fport}/v2/")"
  echo "== self-test: a mirror index miss whose upstream is ALSO gone must FAIL =="
  printf 'ghcr.io/missing/app:1\n' > "${WORK}/missing.txt"
  out="$(run_probe "${WORK}/missing.txt")"; rc=$?
  expect "missing everywhere -> FAIL stage=index http=404 + upstream named" 1 "$rc" "stage=index http=404" "$out"
  expect "  ^ and the upstream verdict is reported too" 0 0 "upstream ghcr.io/missing/app also fails" "$out"
  echo "== self-test: vacuity — an empty list must FAIL =="
  : > "${WORK}/empty.txt"
  out="$(run_probe "${WORK}/empty.txt")"; rc=$?
  expect "empty list -> FAIL 'no images extracted'" 2 "$rc" "^IMAGE-PATH FAIL 0/0: no images extracted" "$out"
  printf 'xpkg.upbound.io/crossplane/crossplane:v1.18.0\n' > "${WORK}/allskip.txt"
  out="$(run_probe "${WORK}/allskip.txt")"; rc=$?
  expect "all-skipped list -> FAIL 'no images extracted'" 2 "$rc" "^IMAGE-PATH FAIL 0/0: no images extracted" "$out"
  kill "$srv_pid" 2>/dev/null; wait "$srv_pid" 2>/dev/null
  echo ""
  if [ "$st_fail" -ne 0 ]; then echo "SELF-TEST FAILED — the image-path probe is broken." >&2; exit 1; fi
  echo "SELF-TEST PASSED — parses the node mirror table, fails a damaged child, passes a healthy entry, refuses an empty list."
  exit 0
fi

# ─── The node's resolution table ──────────────────────────────────────────
[ -s "$MIRROR_TF" ] || { echo "IMAGE-PATH FAIL 0/0: mirror source $MIRROR_TF not found (needs the registries.yaml heredoc)"; exit 2; }
MIRRORS="$(python3 "$WORKER" mirrors "$MIRROR_TF")" || { echo "IMAGE-PATH FAIL 0/0: could not parse the node registry mirror from $MIRROR_TF"; exit 2; }
export PROBE_MIRRORS="$MIRRORS"

if [ "$MODE" = "resolve" ]; then python3 "$WORKER" resolve "$RESOLVE_REF"; exit 0; fi

# ─── Image list ───────────────────────────────────────────────────────────
LIST="${WORK}/images.txt"
if [ -n "$IMAGES_FILE" ]; then
  [ -s "$IMAGES_FILE" ] || { echo "IMAGE-PATH FAIL 0/0: no images extracted (--images file '$IMAGES_FILE' is empty or missing)"; exit 2; }
  sed '/^$/d' "$IMAGES_FILE" | sort -u > "$LIST"
else
  build_image_list "$LIST"
fi
if [ "$MODE" = "list" ]; then cat "$LIST"; exit 0; fi

# ─── Credentials (never printed) ──────────────────────────────────────────
if [ -n "${HW_TFVARS:-}" ] && [ -s "${HW_TFVARS}" ]; then
  [ -z "${HARBOR_ROBOT_TOKEN:-}" ] && HARBOR_ROBOT_TOKEN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("harbor_robot_token",""))' "$HW_TFVARS" 2>/dev/null)"
  [ -z "${GHCR_PULL_TOKEN:-}" ] && GHCR_PULL_TOKEN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("ghcr_pull_token",""))' "$HW_TFVARS" 2>/dev/null)"
fi
export PROBE_HARBOR_HOST="$HARBOR_HOST"
export PROBE_ROBOT_USER="$HARBOR_ROBOT_USER"
export PROBE_ROBOT_TOKEN="${HARBOR_ROBOT_TOKEN:-}"
export PROBE_GHCR_USER="$GHCR_PULL_USER"
export PROBE_GHCR_TOKEN="${GHCR_PULL_TOKEN:-}"

echo "Probing $(wc -l < "$LIST") image ref(s) along the node's own mirror path (index -> linux/amd64 child -> config blob) …"
run_probe "$LIST"
exit $?
