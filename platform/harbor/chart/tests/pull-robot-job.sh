#!/usr/bin/env bash
# Render and run test of templates/pull-robot-job.yaml (SOV-161). CI runs every
# chart/tests/*.sh as `bash <script> <chart dir>`. Each case is named where it runs.
# Cases 1 to 6 render the chart and need bash, helm and Python with PyYAML only.
# Case 7 runs the Job's script against stand-ins for curl (Harbor) and kubectl
# (the pull Secret), so it needs jq and base64 too.
# `bash <script> <chart dir> render` runs cases 1 to 6 only, for a place without
# jq. Without that word every case runs, and a missing jq fails the test: case 7
# is never left out silently.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }

MODE="${2:-all}"
case "$MODE" in
  all)
    command -v jq >/dev/null 2>&1 || fail "jq is needed: the Job's script parses JSON with it"
    command -v base64 >/dev/null 2>&1 || fail "base64 is needed: the Job's script uses it"
    ;;
  render) ;;
  *) fail "unknown mode '$MODE': give no second argument, or the word render" ;;
esac

cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build >/dev/null
fi

render() {
  helm template harbor . --namespace harbor --show-only templates/pull-robot-job.yaml "$@"
}

# nothing <file> <values...>: passes only when the whole chart renders and no
# object comes from the template. A render that fails, for example on a
# template error, does not count as "renders nothing".
MARK='# Source: bp-harbor/templates/pull-robot-job.yaml'
nothing() {
  local out="$1"; shift
  helm template harbor . --namespace harbor "$@" > "$out" 2>&1 || return 1
  ! grep -q -x -F "$MARK" "$out"
}

# Checks the four objects of one render, writes the Job's script to $2 and prints
# "<job name> | <robot> | <host> | <secret namespace>/<secret name>".
inspect() {
  python3 - "$1" "$2" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
kinds = [d["kind"] for d in docs]
assert kinds == ["ServiceAccount", "Role", "RoleBinding", "Job"], kinds
sa, role, binding, job = docs
assert sa["metadata"] == {"name": "harbor-pull-robot", "namespace": "harbor", "labels": sa["metadata"]["labels"]}, sa["metadata"]
pod = job["spec"]["template"]["spec"]
(c,) = pod["containers"]
env = {e["name"]: e.get("value") for e in c["env"]}
ns, name = env["PULL_CONFIG_NAMESPACE"], env["PULL_CONFIG_NAME"]
assert role["metadata"]["namespace"] == ns and binding["metadata"]["namespace"] == ns, "Role and RoleBinding must sit beside the pull Secret"
assert role["rules"] == [{"apiGroups": [""], "resources": ["secrets"], "resourceNames": [name], "verbs": ["get", "patch"]}], role["rules"]
assert binding["roleRef"] == {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]}
assert binding["subjects"] == [{"kind": "ServiceAccount", "name": "harbor-pull-robot", "namespace": "harbor"}], binding["subjects"]
assert job["metadata"]["name"].startswith("harbor-pull-robot-"), job["metadata"]["name"]
assert job["metadata"]["namespace"] == "harbor"
assert "helm.sh/hook" not in (job["metadata"].get("annotations") or {}), "the Job must not be a Helm hook"
assert pod["restartPolicy"] == "Never"
assert pod["serviceAccountName"] == "harbor-pull-robot"
assert pod["automountServiceAccountToken"] is True
assert pod["securityContext"]["runAsNonRoot"] is True
assert "/proxy-" in c["image"], "the image must come through a proxy project: " + c["image"]
req = c["resources"]["requests"]
assert req.get("cpu") and req.get("memory"), "cpu and memory requests are required"
assert c["securityContext"]["readOnlyRootFilesystem"] is True
for e in c["env"]:
    for word in ("PASSWORD", "TOKEN", "KEY", "SECRET"):
        assert word not in e["name"].upper(), "env name %s is refused by the policy secret-not-in-env" % e["name"]
assert env["HARBOR_CORE_URL"] == "http://harbor-core.harbor.svc.cluster.local", env["HARBOR_CORE_URL"]
vols = {v["name"]: v for v in pod["volumes"]}
assert vols["harbor-admin"]["secret"]["secretName"] == "harbor-admin"
open(sys.argv[2], "w").write(c["args"][0])
print("%s | %s | %s | %s/%s" % (job["metadata"]["name"], env["ROBOT_NAME"], env["LOCAL_REGISTRY_HOST"], ns, name))
PY
}

HOST=registry.alpha.example.org

echo "[pull-robot] Case 1: default render (no host) renders nothing"
nothing "$TMP/1.yaml" || fail "a Job rendered without pullRobot.host, or the chart did not render: $(tail -5 "$TMP/1.yaml")"

echo "[pull-robot] Case 2: a host"
render --set pullRobot.host=$HOST > "$TMP/2.yaml"
r2="$(inspect "$TMP/2.yaml" "$TMP/script.sh")"
[ "${r2#* | }" = "local-pull | $HOST | flux-system/ghcr-pull" ] || fail "with a host: $r2"
render --set pullRobot.host=$HOST > "$TMP/2b.yaml"
[ "$(inspect "$TMP/2b.yaml" "$TMP/2b.sh")" = "$r2" ] || fail "two renders of the same values differ"
if nothing "$TMP/2c.yaml" --set pullRobot.host=$HOST; then
  fail "the check for 'renders nothing' passes although the Job renders"
fi

echo "[pull-robot] Case 3: the host equals the origin host"
nothing "$TMP/3.yaml" --set pullRobot.host=$HOST --set pullRobot.originHost=$HOST \
  || fail "a Job rendered that would replace the origin login, or the chart did not render: $(tail -5 "$TMP/3.yaml")"

echo "[pull-robot] Case 4: a region key and another Secret name"
render --set pullRobot.host=$HOST --set pullRobot.originHost=ghcr.io --set pullRobot.regionKey=Hw-ME_east--1- --set pullRobot.secretName=kit-pull > "$TMP/4.yaml"
r4="$(inspect "$TMP/4.yaml" "$TMP/4.sh")"
[ "${r4#* | }" = "local-pull-hw-me-east-1 | $HOST | flux-system/kit-pull" ] || fail "with a region key: $r4"
[ "${r4%% | *}" != "${r2%% | *}" ] || fail "the Job name did not change with its inputs"

echo "[pull-robot] Case 5: enabled=false"
nothing "$TMP/5.yaml" --set pullRobot.host=$HOST --set pullRobot.enabled=false \
  || fail "a Job rendered with enabled=false, or the chart did not render: $(tail -5 "$TMP/5.yaml")"

echo "[pull-robot] Case 6: the values of bootstrap-kit slot 19"
# The sets: full = every key of the fixture; kit = the kit keys only (no
# LOCAL_REGISTRY_HOST); host = LOCAL_REGISTRY_HOST only. Flux substitutes into
# the text and parses it again, so a value that becomes empty may reach the
# chart as the empty string or as null: "-null" writes null for it.
for set in full kit kit-null host-null; do
  python3 - "$REPO" "$set" > "$TMP/6-$set-values.yaml" <<'PY'
import re, sys, yaml
repo, which = sys.argv[1:]
which, _, empty = which.partition("-")
values = {}
for line in open(repo + "/tests/fixtures/variables/today.env"):
    line = line.rstrip("\n")
    if line and not line.startswith("#"):
        k, _, v = line.partition("=")
        values[k] = v
if which == "kit":
    keep = {l.strip() for l in open(repo + "/docs/contracts/c5-kit-keys.txt") if l.strip()}
    values = {k: v for k, v in values.items() if k in keep}
if which == "host":
    values = {k: v for k, v in values.items() if k == "LOCAL_REGISTRY_HOST"}
form = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?::=([^}]*))?\}")
def sub(x):
    if isinstance(x, dict):
        return {k: sub(v) for k, v in x.items()}
    if isinstance(x, str):
        y = form.sub(lambda m: values.get(m.group(1)) or (m.group(2) or ""), x)
        return None if (y == "" and empty == "null") else y
    return x
docs = yaml.safe_load_all(open(repo + "/clusters/_template/bootstrap-kit/19-harbor.yaml"))
(hr,) = [d for d in docs if d and d.get("kind") == "HelmRelease" and d["metadata"]["name"] == "bp-harbor"]
print(yaml.safe_dump({"pullRobot": sub(hr["spec"]["values"]["pullRobot"])}))
PY
done
render -f "$TMP/6-full-values.yaml" > "$TMP/6-full.yaml"
r6="$(inspect "$TMP/6-full.yaml" "$TMP/6.sh")"
[ "${r6#* | }" = "local-pull-region-a | $HOST | flux-system/ghcr-pull" ] || fail "slot 19 with the full values: $r6"
for set in kit kit-null; do
  nothing "$TMP/6-$set.yaml" -f "$TMP/6-$set-values.yaml" \
    || fail "slot 19 with the kit keys only (no LOCAL_REGISTRY_HOST), set $set, rendered a Job, or the chart did not render: $(tail -5 "$TMP/6-$set.yaml")"
done
grep -q -x -F '  host: null' "$TMP/6-kit-null-values.yaml" || fail "the set kit-null does not hold a null host"
render -f "$TMP/6-host-null-values.yaml" > "$TMP/6-host-null.yaml"
r6n="$(inspect "$TMP/6-host-null.yaml" "$TMP/6n.sh")"
[ "${r6n#* | }" = "local-pull | $HOST | flux-system/ghcr-pull" ] || fail "slot 19 with a host and a null origin host and region key: $r6n"

if [ "$MODE" = render ]; then
  echo "pull-robot-job: the render cases passed (mode render: case 7 did not run)"
  exit 0
fi

echo "[pull-robot] Case 7: the Job's script against stand-ins for Harbor and the pull Secret"
BIN="$TMP/bin"; ST="$TMP/state"
mkdir -p "$BIN" "$ST" "$TMP/admin" "$TMP/run"
printf 'adminpw' > "$TMP/admin/HARBOR_ADMIN_PASSWORD"
sed -e "s#/tmp/#$TMP/run/#g" -e "s#/etc/harbor-admin/#$TMP/admin/#g" "$TMP/script.sh" > "$TMP/run.sh"
grep -q "$TMP/admin/HARBOR_ADMIN_PASSWORD" "$TMP/run.sh" || fail "the script no longer reads the admin password file"

cat > "$BIN/curl" <<'PY'
#!/usr/bin/env python3
# Stand-in for curl: a Harbor with robots, held in $ST/harbor.json.
import base64, json, os, sys
st = os.environ["ST"]
h = json.load(open(st + "/harbor.json"))
args = sys.argv[1:]
out, fmt, auth, method, data, fail_flag, url = None, "", None, "GET", None, False, None
i = 0
while i < len(args):
    a = args[i]
    if a in ("-o", "-w", "-u", "-X", "--data", "-H", "--max-time"):
        v = args[i + 1]; i += 2
        if a == "-o": out = v
        elif a == "-w": fmt = v
        elif a == "-u": auth = v
        elif a == "-X": method = v
        elif a == "--data": data = v
        continue
    if a.startswith("-"):
        fail_flag = fail_flag or "f" in a.lstrip("-")
        i += 1
        continue
    url = a; i += 1
path = url.split("://", 1)[1].split("/", 1)[1]
base = "/" + path.split("?", 1)[0]
open(st + "/calls.log", "a").write("%s %s\n" % (method, base))
admin = auth == "admin:" + open(os.environ["ADMIN_FILE"]).read()
code, body = 404, {}
if base == "/api/v2.0/ping":
    code, body = 200, "Pong"
elif base == "/service/token":
    user, _, pw = (auth or "").partition(":")
    ok = any(r["name"] == user and r["secret"] == pw and not r["disable"] for r in h["robots"])
    code, body = (200, {"token": "t"}) if ok else (401, {"errors": [{"code": "UNAUTHORIZED"}]})
elif not admin:
    code, body = 401, {"errors": [{"code": "UNAUTHORIZED"}]}
elif base == "/api/v2.0/robots" and method == "GET":
    code, body = 200, [{k: v for k, v in r.items() if k != "secret"} for r in h["robots"]]
elif base == "/api/v2.0/robots" and method == "POST":
    req = json.loads(data)
    open(st + "/post.json", "w").write(data)
    if h.get("post_code", 201) != 201:
        code, body = h["post_code"], {"errors": [{"code": "CONFLICT"}]}
    else:
        h["next_id"] += 1
        r = {"id": h["next_id"], "name": "robot$" + req["name"], "level": req["level"], "disable": req["disable"],
             "secret": "Sec%dret" % h["next_id"], "duration": req["duration"], "permissions": req["permissions"]}
        h["robots"].append(r)
        code, body = 201, {"id": r["id"], "name": r["name"], "secret": r["secret"], "expires_at": -1}
elif base.startswith("/api/v2.0/robots/") and method == "DELETE":
    rid = int(base.rsplit("/", 1)[1])
    before = len(h["robots"])
    h["robots"] = [r for r in h["robots"] if r["id"] != rid]
    code = 200 if len(h["robots"]) < before else 404
json.dump(h, open(st + "/harbor.json", "w"))
if out and out != "/dev/null":
    open(out, "w").write(body if isinstance(body, str) else json.dumps(body))
sys.stdout.write(fmt.replace("%{http_code}", str(code)))
sys.exit(22 if fail_flag and code >= 400 else 0)
PY

cat > "$BIN/kubectl" <<'PY'
#!/usr/bin/env python3
# Stand-in for kubectl: one Secret, held in $ST/secret.json.
import json, os, sys
st = os.environ["ST"]
a = sys.argv[1:]
open(st + "/calls.log", "a").write("kubectl %s\n" % " ".join(x for x in a if not x.startswith("{")))
ns = a[a.index("-n") + 1]
verb, kind, name = [x for x in a if x not in ("-n", ns)][:3]
f = st + "/secret.json"
if not os.path.exists(f) or kind != "secret":
    sys.stderr.write('Error from server (NotFound): secrets "%s" not found\n' % name); sys.exit(1)
s = json.load(open(f))
if s["metadata"] != {"name": name, "namespace": ns}:
    sys.stderr.write('Error from server (NotFound): secrets "%s" not found\n' % name); sys.exit(1)
if verb == "get":
    assert a[a.index("-o") + 1] == "json"
    print(json.dumps(s))
elif verb == "patch":
    assert "--type=merge" in a
    p = json.loads(a[a.index("-p") + 1])
    assert set(p) == {"data"}, p
    s["data"].update(p["data"])
    json.dump(s, open(f, "w"))
else:
    sys.exit(2)
PY
chmod +x "$BIN/curl" "$BIN/kubectl"

export ST ADMIN_FILE="$TMP/admin/HARBOR_ADMIN_PASSWORD"
GHCR='{"username":"bot","password":"ghcr-pw","auth":"Ym90OmdoY3ItcHc="}'
# put_secret <auths object> writes the pull Secret; harbor <robots> writes Harbor's state.
put_secret() {
  jq -n -c --argjson a "$1" --arg c "$(jq -n -c --argjson a "$1" '{auths: $a}' | base64 -w0)" \
    '{metadata: {name: "ghcr-pull", namespace: "flux-system"}, data: {".dockerconfigjson": $c}}' > "$ST/secret.json"
}
harbor() { jq -n -c --argjson r "$1" '{robots: $r, next_id: 10}' > "$ST/harbor.json"; }
auths() { jq -r '.data[".dockerconfigjson"]' "$ST/secret.json" | base64 -d; }
run() {
  : > "$ST/calls.log"
  env -i PATH="$BIN:$PATH" ST="$ST" ADMIN_FILE="$ADMIN_FILE" \
    HARBOR_CORE_URL=http://harbor-core.harbor.svc.cluster.local LOCAL_REGISTRY_HOST=$HOST \
    ROBOT_NAME=local-pull PULL_CONFIG_NAME=ghcr-pull PULL_CONFIG_NAMESPACE=flux-system \
    sh "$TMP/run.sh" > "$ST/out.log" 2>&1
}
count() { grep -c -x -F "$1" "$ST/calls.log" || true; }
check_entry() {
  python3 - "$ST" "$HOST" "$GHCR" <<'PY'
import base64, json, sys
st, host, ghcr = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
s = json.load(open(st + "/secret.json"))
auths = json.loads(base64.b64decode(s["data"][".dockerconfigjson"]))["auths"]
(robot,) = [r for r in json.load(open(st + "/harbor.json"))["robots"] if r["name"] == "robot$local-pull"]
want = {"username": robot["name"], "password": robot["secret"],
        "auth": base64.b64encode((robot["name"] + ":" + robot["secret"]).encode()).decode()}
assert auths[host] == want, (auths[host], want)
assert auths["ghcr.io"] == ghcr, "the ghcr.io entry changed"
assert sorted(auths) == sorted(["ghcr.io", host]), sorted(auths)
PY
}

echo "[pull-robot] Case 7a: a first run creates the robot and writes its login"
harbor '[]'; put_secret "{\"ghcr.io\":$GHCR}"
run || fail "first run: $(cat "$ST/out.log")"
[ "$(count 'POST /api/v2.0/robots')" = 1 ] || fail "first run: the robot was not created once: $(cat "$ST/calls.log")"
[ "$(count 'kubectl -n flux-system patch secret ghcr-pull --type=merge -p')" = 1 ] || fail "first run: the Secret was not patched once"
check_entry || fail "first run: the pull Secret is wrong: $(auths)"
python3 -c 'import json,sys; r=json.load(open(sys.argv[1])); assert r["name"]=="local-pull" and r["level"]=="system" and r["duration"]==-1 and r["disable"] is False and r["permissions"]==[{"kind":"project","namespace":"*","access":[{"resource":"repository","action":"pull"}]}], r' "$ST/post.json" \
  || fail "the robot is not a system robot with pull access to every project"

echo "[pull-robot] Case 7b: a second run changes nothing"
cp "$ST/secret.json" "$ST/secret-before.json"; cp "$ST/harbor.json" "$ST/harbor-before.json"
run || fail "second run: $(cat "$ST/out.log")"
grep -q 'nothing to do' "$ST/out.log" || fail "second run: $(cat "$ST/out.log")"
[ "$(count 'POST /api/v2.0/robots')" = 0 ] || fail "second run created a robot"
cmp -s "$ST/secret.json" "$ST/secret-before.json" || fail "second run changed the Secret"
cmp -s "$ST/harbor.json" "$ST/harbor-before.json" || fail "second run changed Harbor"

echo "[pull-robot] Case 7c: a login that no longer works is replaced"
jq -c '.robots[0].secret = "Changed1x"' "$ST/harbor.json" > "$ST/h.json" && mv "$ST/h.json" "$ST/harbor.json"
run || fail "lost secret: $(cat "$ST/out.log")"
[ "$(count 'DELETE /api/v2.0/robots/11')" = 1 ] && [ "$(count 'POST /api/v2.0/robots')" = 1 ] || fail "lost secret: $(cat "$ST/calls.log")"
check_entry || fail "lost secret: the pull Secret is wrong: $(auths)"

echo "[pull-robot] Case 7d: an admin login under the host is replaced by the robot's"
harbor '[{"id":3,"name":"robot$local-pull","level":"system","disable":false,"secret":"Old1secret"},{"id":4,"name":"robot$local-pull-region-b","level":"system","disable":false,"secret":"B1secret"}]'
put_secret "{\"ghcr.io\":$GHCR,\"$HOST\":{\"username\":\"admin\",\"auth\":\"YWRtaW46YWRtaW5wdw==\"}}"
run || fail "admin login: $(cat "$ST/out.log")"
[ "$(count 'DELETE /api/v2.0/robots/3')" = 1 ] && [ "$(count 'DELETE /api/v2.0/robots/4')" = 0 ] || fail "admin login: $(cat "$ST/calls.log")"
check_entry || fail "admin login: the pull Secret is wrong: $(auths)"
jq -e '[.robots[] | select(.name == "robot$local-pull-region-b" and .secret == "B1secret")] | length == 1' "$ST/harbor.json" >/dev/null \
  || fail "the robot of another region was touched"

echo "[pull-robot] Case 7e: a disabled robot is created again"
jq -c '(.robots[] | select(.name == "robot$local-pull")).disable = true' "$ST/harbor.json" > "$ST/h.json" && mv "$ST/h.json" "$ST/harbor.json"
run || fail "disabled robot: $(cat "$ST/out.log")"
[ "$(count 'POST /api/v2.0/robots')" = 1 ] || fail "disabled robot: $(cat "$ST/calls.log")"
check_entry || fail "disabled robot: the pull Secret is wrong: $(auths)"

echo "[pull-robot] Case 7f: no pull Secret fails before Harbor is changed"
harbor '[]'; rm -f "$ST/secret.json"
if run; then fail "a run without the pull Secret passed"; fi
[ "$(count 'POST /api/v2.0/robots')" = 0 ] || fail "a robot was created without a pull Secret"

echo "[pull-robot] Case 7g: Harbor refuses the robot"
harbor '[]'; put_secret "{\"ghcr.io\":$GHCR}"
jq -c '.post_code = 409' "$ST/harbor.json" > "$ST/h.json" && mv "$ST/h.json" "$ST/harbor.json"
cp "$ST/secret.json" "$ST/secret-before.json"
if run; then fail "a run passed although Harbor refused the robot"; fi
grep -q 'answered 409' "$ST/out.log" || fail "refused robot: $(cat "$ST/out.log")"
cmp -s "$ST/secret.json" "$ST/secret-before.json" || fail "the Secret changed although Harbor refused the robot"

echo "pull-robot-job: all cases passed"
