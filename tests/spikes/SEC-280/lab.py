#!/usr/bin/env python3
"""SEC-280 lab test: the OpenBao database engine beside CloudNativePG.

Runs only inside the GitHub Actions job of .github/workflows/spike-sec-280.yaml,
against a throwaway kind cluster. Two "regions" are two namespaces of that one
cluster: acme-a and acme-b, each with its own CloudNativePG Cluster (pg-a, pg-b,
a distributed-topology pair) and its own OpenBao (bao-a, bao-b).

Every measured value is printed as one line that starts with "RESULT".

    python3 lab.py steps1to4   # steps 1 to 4 of the lab test
    python3 lab.py step5       # step 5: OpenBao stopped for 30 minutes
    python3 lab.py diag        # diagnostics after a failure
"""
import datetime
import hashlib
import json
import os
import secrets
import subprocess
import re
import sys
import threading
import time
import traceback

PG_IMAGE = os.environ.get("PG_IMAGE", "ghcr.io/cloudnative-pg/postgresql:16.4")
OPENBAO_TAG = os.environ.get("OPENBAO_TAG", "2.7.2")
OPENBAO_CHART = os.environ.get("OPENBAO_CHART", "0.30.7")
CNPG_CHART = os.environ.get("CNPG_CHART", "0.28.0")
CSI_CHART = os.environ.get("CSI_CHART", "1.6.2")
OUTAGE_SECONDS = int(os.environ.get("OUTAGE_SECONDS", "1800"))
NORMAL_SECONDS = int(os.environ.get("NORMAL_SECONDS", "1200"))
OUT = os.environ.get("OUT_DIR", "out")
os.makedirs(OUT, exist_ok=True)

RW = {"a": "pg-a-rw.acme-a.svc", "b": "pg-b-rw.acme-b.svc"}
DBADMIN_PW = secrets.token_hex(16)
CTL_PW = secrets.token_hex(16)

# The statements of design section 4.10, verbatim except for the group name.
CREATE = ('CREATE ROLE "{{name}}" WITH LOGIN PASSWORD \'{{password}}\' '
          'VALID UNTIL \'{{expiration}}\' IN ROLE "GROUP";')
REVOKE = ('REASSIGN OWNED BY "{{name}}" TO "appdb_rw"; DROP OWNED BY "{{name}}"; '
          'DROP ROLE IF EXISTS "{{name}}";')
# Variant measured beside it: the dynamic user acts as the group role, so it never owns objects.
SET_ROLE = ' ALTER ROLE "{{name}}" SET role TO \'appdb_rw\';'
# Variant measured beside it: revocation also ends the user's open sessions.
TERMINATE = "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = '{{name}}'; "
REVOKE_NO_REASSIGN = 'DROP OWNED BY "{{name}}"; DROP ROLE IF EXISTS "{{name}}";'

RESULTS = []


# ----------------------------------------------------------------- helpers
def now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%H:%M:%SZ")


def log(msg):
    print(f"[{now()}] {msg}", flush=True)


def one(s, n=500):
    lines = [l.strip() for l in (s or "").strip().splitlines() if l.strip()]
    lines = [l for l in lines if not l.startswith("URL:") and l != "Errors:"]
    return " / ".join(lines)[:n]


def rec(step, key, value):
    if not isinstance(value, str):
        value = json.dumps(value, sort_keys=True)
    item = {"step": step, "key": key, "value": value, "t": now()}
    RESULTS.append(item)
    print(f"RESULT {step} | {key} | {value}", flush=True)
    with open(f"{OUT}/results.jsonl", "a") as f:
        f.write(json.dumps(item) + "\n")


def sh(cmd, input=None, timeout=300, check=False):
    try:
        p = subprocess.run(cmd, input=input, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        if check:
            raise RuntimeError(f"timeout after {timeout}s: {' '.join(cmd)[:200]}")
        return 124, "", f"timeout after {timeout}s"
    if check and p.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)[:200]} -> rc {p.returncode}: {one(p.stderr or p.stdout, 1500)}")
    return p.returncode, p.stdout, p.stderr


def kc(*args, input=None, timeout=300, check=False):
    return sh(["kubectl", *args], input=input, timeout=timeout, check=check)


def res(rc, out, err):
    return ("ok: " + one(out)) if rc == 0 else ("error: " + one(err or out))


def apply(obj, retries=12):
    last = ""
    for _ in range(retries):
        rc, out, err = kc("apply", "-f", "-", input=json.dumps(obj))
        if rc == 0:
            log(one(out))
            return
        last = one(err)
        log(f"apply retry: {last}")
        time.sleep(10)
    raise RuntimeError(f"apply failed: {last}")


def patch(kind, name, ns, body):
    return kc("patch", kind, name, "-n", ns, "--type", "merge", "-p", json.dumps(body))


def wait_for(desc, fn, timeout=300, interval=5):
    start = time.time()
    while time.time() - start < timeout:
        try:
            if fn():
                took = round(time.time() - start, 1)
                log(f"ready: {desc} after {took}s")
                return True, took
        except Exception as e:  # noqa: BLE001
            log(f"wait {desc}: {e}")
        time.sleep(interval)
    log(f"TIMEOUT: {desc} after {timeout}s")
    return False, timeout


def step(name):
    """Run a lab step; an exception is recorded, never fatal for the next step."""
    def deco(fn):
        def run(*a, **k):
            log(f"===== {name} =====")
            try:
                return fn(*a, **k)
            except Exception as e:  # noqa: BLE001
                rec(name, "exception", f"{type(e).__name__}: {e}")
                traceback.print_exc()
                return None
        return run
    return deco


def helm(*args, timeout=600):
    return sh(["helm", *args], timeout=timeout, check=True)


def sha(s):
    return hashlib.sha256(s.encode()).hexdigest()[:16]


# --------------------------------------------------------------- postgres
def su(r, sql, db="appdb", timeout=60):
    """Run SQL as the postgres superuser inside the instance pod of region r."""
    return kc("exec", "-n", f"acme-{r}", f"pg-{r}-1", "-c", "postgres", "--", "psql", "-d", db,
              "-v", "ON_ERROR_STOP=1", "-AtXq", "-F", "|", "-c", sql, timeout=timeout)


def su_ok(r, sql, db="appdb"):
    rc, out, err = su(r, sql, db)
    if rc != 0:
        raise RuntimeError(f"superuser SQL on {r}: {one(err)}")
    return out.strip()


def login(host, user, pw, sql="select current_user", db="appdb"):
    conn = f"host={host} port=5432 dbname={db} user={user} sslmode=require connect_timeout=5"
    rc, out, err = kc("exec", "-n", "client", "pgclient", "--", "env", f"PGPASSWORD={pw}", "psql", conn,
                      "-v", "ON_ERROR_STOP=1", "-AtXq", "-F", "|", "-c", sql, timeout=40)
    return res(rc, out, err)


ROLE_SQL = ("select r.rolname, coalesce(r.rolvaliduntil::text,'-'), md5(coalesce(a.rolpassword,'')), "
            "r.rolcanlogin, r.rolinherit, r.rolsuper, r.rolcreaterole "
            "from pg_roles r join pg_authid a on a.oid=r.oid where r.rolname !~ '^pg_' "
            "and r.rolname not in ('postgres','streaming_replica') order by 1")
MEM_SQL = ("select m.rolname, g.rolname, am.admin_option, am.inherit_option, am.set_option, gr.rolname "
           "from pg_auth_members am join pg_roles m on m.oid=am.member join pg_roles g on g.oid=am.roleid "
           "join pg_roles gr on gr.oid=am.grantor where m.rolname !~ '^pg_' order by 1,2,6")


def roles(r):
    out = su_ok(r, ROLE_SQL)
    d = {}
    for line in out.splitlines():
        f = line.split("|")
        d[f[0]] = {"validuntil": f[1], "pwhash": f[2][:12], "login": f[3], "inherit": f[4],
                   "super": f[5], "createrole": f[6]}
    return d


def members(r):
    out = su_ok(r, MEM_SQL)
    rows = []
    for line in out.splitlines():
        f = line.split("|")
        rows.append({"member": f[0], "role": f[1], "admin": f[2], "inherit": f[3], "set": f[4], "grantor": f[5]})
    return rows


def mem_of(rows, member=None, role=None):
    return [f"{x['member']}->{x['role']}(admin={x['admin']},inherit={x['inherit']},set={x['set']},grantor={x['grantor']})"
            for x in rows if (member is None or x["member"] == member) and (role is None or x["role"] == role)]


def in_recovery(r):
    rc, out, _ = su(r, "select pg_is_in_recovery()", db="postgres", timeout=20)
    return out.strip() if rc == 0 else "?"


# ---------------------------------------------------------------- openbao
BAO = {"a": {"ns": "bao-a"}, "b": {"ns": "bao-b"}}


def bao(r, *args, data=None, timeout=90, token=None):
    b = BAO[r]
    cmd = ["exec"] + (["-i"] if data is not None else []) + [
        "-n", b["ns"], "openbao-0", "-c", "openbao", "--", "env", "BAO_ADDR=http://127.0.0.1:8200",
        f"BAO_TOKEN={token or b.get('token', '')}", "bao", *args]
    return kc(*cmd, input=data, timeout=timeout)


def baor(r, *args, data=None):
    rc, out, err = bao(r, *args, data=data)
    return res(rc, out, err)


def baoj(r, *args, data=None):
    rc, out, err = bao(r, *args, data=data)
    if rc != 0:
        return False, one(err or out)
    try:
        return True, json.loads(out)
    except ValueError:
        return True, one(out)


def bao_status(r):
    rc, out, _ = bao(r, "status", "-format=json", timeout=20)
    try:
        return json.loads(out)
    except ValueError:
        return {}


def install_openbao(r, csi):
    ns = BAO[r]["ns"]
    vals = {"injector": {"enabled": False},
            "server": {"image": {"tag": OPENBAO_TAG}, "standalone": {"enabled": True},
                       "dataStorage": {"enabled": True, "size": "1Gi"}},
            "csi": {"enabled": csi}}
    path = f"{OUT}/openbao-{r}-values.json"
    with open(path, "w") as f:
        json.dump(vals, f)
    helm("upgrade", "--install", "openbao", "openbao/openbao", "--version", OPENBAO_CHART, "-n", ns,
         "--create-namespace", "-f", path)


def bao_wait_started(r, timeout=300):
    ns = BAO[r]["ns"]
    return wait_for(f"openbao-0 started in {ns}", lambda: kc(
        "get", "pod", "openbao-0", "-n", ns, "-o",
        "jsonpath={.status.containerStatuses[?(@.name=='openbao')].started}")[1] == "true"
        and "initialized" in bao_status(r), timeout=timeout)


def bao_init(r):
    bao_wait_started(r)
    rc, out, err = bao(r, "operator", "init", "-key-shares=1", "-key-threshold=1", "-format=json")
    if rc != 0:
        raise RuntimeError(f"init {r}: {one(err)}")
    j = json.loads(out)
    BAO[r]["key"] = j["unseal_keys_b64"][0]
    BAO[r]["token"] = j["root_token"]
    bao_unseal(r)


def bao_unseal(r):
    rc, out, err = bao(r, "operator", "unseal", BAO[r]["key"])
    ok, took = wait_for(f"openbao {r} unsealed", lambda: bao_status(r).get("sealed") is False, timeout=120)
    if not ok:
        raise RuntimeError(f"unseal {r}: {one(err)}")
    return took


def bao_setup_engine(r, k8s_auth_ns=None):
    for args in (["secrets", "enable", "-path=db", "database"],):
        rc, out, err = bao(r, *args)
        log(res(rc, out, err))
    if k8s_auth_ns:
        log(baor(r, "auth", "enable", "kubernetes"))
        log(baor(r, "write", "auth/kubernetes/config", "kubernetes_host=https://kubernetes.default.svc:443"))
        pol = 'path "db/creds/*" { capabilities = ["read"] }\npath "db/static-creds/*" { capabilities = ["read"] }\n'
        log(baor(r, "policy", "write", "db-read", "-", data=pol))
        sa, ns = k8s_auth_ns
        log(baor(r, "write", "auth/kubernetes/role/workload", f"bound_service_account_names={sa}",
                 f"bound_service_account_namespaces={ns}", "audience=openbao", "token_policies=db-read",
                 "token_ttl=15m"))


def bao_config(r):
    cfg = {"plugin_name": "postgresql-database-plugin",
           "connection_url": "postgresql://{{username}}:{{password}}@" + RW[r] + ":5432/postgres?sslmode=require",
           "username": "openbao_dbadmin", "password": DBADMIN_PW,
           "allowed_roles": "acme.*", "password_authentication": "scram-sha-256"}
    return baor(r, "write", "db/config/acme.pg", "-", data=json.dumps(cfg))


def bao_dyn_role(r, name, group="appdb_rw", ttl="1h", max_ttl="24h", extra="", revoke=REVOKE):
    data = {"db_name": "acme.pg", "default_ttl": ttl, "max_ttl": max_ttl,
            "creation_statements": [CREATE.replace("GROUP", group) + extra],
            "revocation_statements": [revoke]}
    return baor(r, "write", f"db/roles/{name}", "-", data=json.dumps(data))


def issue(r, role):
    ok, j = baoj(r, "read", "-format=json", f"db/creds/{role}")
    if not ok:
        return None, j
    return {"user": j["data"]["username"], "pw": j["data"]["password"], "lease": j["lease_id"],
            "ttl": j["lease_duration"]}, "ok: " + j["data"]["username"]


def revoke(r, lease):
    return baor(r, "write", "sys/leases/revoke", f"lease_id={lease}", "sync=true")


def lease_state(r, lease):
    ok, j = baoj(r, "write", "-format=json", "sys/leases/lookup", f"lease_id={lease}")
    if not ok:
        return "absent: " + j
    d = j.get("data", {})
    return f"present: expire_time={d.get('expire_time')} ttl={d.get('ttl')}"


def static_creds(r, name="acme.app.appdb"):
    ok, j = baoj(r, "read", "-format=json", f"db/static-creds/{name}")
    if not ok:
        return None, None, j
    return j["data"]["username"], j["data"]["password"], "ok"


# -------------------------------------------------------------- cnpg model
STATE = {"admin_managed": True, "admin_inroles": ["appdb_rw", "appdb_ro"], "ro_comment": "start", "tag": "v1"}


def role_list():
    t = STATE["tag"]
    rl = [
        {"name": "appdb_rw", "ensure": "present", "login": False, "comment": f"group rw {t}"},
        {"name": "appdb_ro", "ensure": "present", "login": False, "comment": STATE["ro_comment"]},
        {"name": "app_static", "ensure": "present", "login": True, "inRoles": ["appdb_rw"],
         "comment": f"static role, no passwordSecret {t}"},
        {"name": "app_static_ctl", "ensure": "present", "login": True, "inRoles": ["appdb_rw"],
         "passwordSecret": {"name": "app-static-ctl"}, "comment": f"control role with passwordSecret {t}"},
    ]
    if STATE["admin_managed"]:
        rl.insert(2, {"name": "openbao_dbadmin", "ensure": "present", "login": True, "createrole": True,
                      "inRoles": list(STATE["admin_inroles"]), "passwordSecret": {"name": "openbao-dbadmin"},
                      "comment": f"management role {t}"})
    return rl


def ext(name, host, cert_secret, root_ca=None):
    e = {"name": name,
         "connectionParameters": {"host": host, "port": "5432", "user": "streaming_replica",
                                  "sslmode": "require", "dbname": "postgres"},
         "sslKey": {"name": cert_secret, "key": "tls.key"},
         "sslCert": {"name": cert_secret, "key": "tls.crt"}}
    if root_ca:
        e["sslRootCert"] = {"name": root_ca, "key": "ca.crt"}
    return e


def cluster_a(with_replica):
    spec = {"instances": 1, "imageName": PG_IMAGE, "storage": {"size": "1Gi"},
            "bootstrap": {"initdb": {"database": "appdb", "owner": "app"}},
            "enableSuperuserAccess": False,
            "managed": {"roles": role_list()}}
    if with_replica:
        spec["replica"] = {"self": "pg-a", "primary": "pg-a", "source": "pg-b"}
        spec["externalClusters"] = [ext("pg-a", RW["a"], "pg-a-replication"),
                                    ext("pg-b", RW["b"], "pg-a-replication")]
    return {"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
            "metadata": {"name": "pg-a", "namespace": "acme-a"}, "spec": spec}


def cluster_b():
    spec = {"instances": 1, "imageName": PG_IMAGE, "storage": {"size": "1Gi"},
            "bootstrap": {"pg_basebackup": {"source": "pg-a"}},
            "certificates": {"clientCASecret": "pg-a-ca"},
            "enableSuperuserAccess": False,
            "replica": {"self": "pg-b", "primary": "pg-a", "source": "pg-a"},
            "externalClusters": [ext("pg-a", RW["a"], "pg-a-replication", "pg-a-ca"),
                                 ext("pg-b", RW["b"], "pg-b-replication")],
            "managed": {"roles": role_list()}}
    return {"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
            "metadata": {"name": "pg-b", "namespace": "acme-b"}, "spec": spec}


def patch_roles(regions=("a", "b")):
    for r in regions:
        rc, out, err = patch("cluster.postgresql.cnpg.io", f"pg-{r}", f"acme-{r}",
                             {"spec": {"managed": {"roles": role_list()}}})
        log(f"patch roles pg-{r}: {res(rc, out, err)}")


TRIG = {"n": 0}


def trigger(primary, regions=("a", "b")):
    """Make the operator run one role reconciliation on the primary and wait until it has run."""
    TRIG["n"] += 1
    tag = f"trigger-{TRIG['n']}"
    STATE["ro_comment"] = tag
    patch_roles(regions)
    ok, took = wait_for(f"operator applied comment {tag} on {primary}", lambda: su_ok(
        primary, "select shobj_description(oid,'pg_authid') from pg_roles where rolname='appdb_ro'") == tag,
        timeout=240)
    time.sleep(20)
    return ok, took


def wait_cluster(ns, name, timeout=900):
    start = time.time()
    last = ""
    while time.time() - start < timeout:
        rc, out, _ = kc("get", "cluster.postgresql.cnpg.io", name, "-n", ns, "-o",
                        "jsonpath={.status.phase}|{.status.readyInstances}")
        if out != last:
            log(f"cluster {ns}/{name}: {out}")
            last = out
        if out.startswith("Cluster in healthy state|1"):
            return round(time.time() - start)
        time.sleep(5)
    raise RuntimeError(f"cluster {ns}/{name} not healthy after {timeout}s: {last}")


def secret(ns, name, data, typ="Opaque"):
    apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": ns},
           "type": typ, "stringData": data})


def copy_secret(src_ns, name, dst_ns):
    _, out, _ = kc("get", "secret", name, "-n", src_ns, "-o", "json", check=True)
    s = json.loads(out)
    apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": dst_ns},
           "type": s.get("type", "Opaque"), "data": s["data"]})


def ns(name):
    apply({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": name}})


def roles_status(r):
    _, out, _ = kc("get", "cluster.postgresql.cnpg.io", f"pg-{r}", "-n", f"acme-{r}", "-o",
                   "jsonpath={.status.managedRolesStatus}")
    try:
        j = json.loads(out)
        j.pop("passwordStatus", None)
        return j
    except ValueError:
        return out


def client_pod():
    ns("client")
    apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "pgclient", "namespace": "client"},
           "spec": {"containers": [{"name": "c", "image": PG_IMAGE, "command": ["sleep", "infinity"]}]}})
    kc("wait", "pod/pgclient", "-n", "client", "--for=condition=Ready", "--timeout=300s", check=True, timeout=320)


def install_common(csi):
    helm("repo", "add", "cnpg", "https://raw.githubusercontent.com/cloudnative-pg/charts/gh-pages")
    helm("repo", "add", "openbao", "https://openbao.github.io/openbao-helm")
    if csi:
        helm("repo", "add", "secrets-store-csi-driver",
             "https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts")
    helm("repo", "update")
    helm("upgrade", "--install", "cnpg", "cnpg/cloudnative-pg", "--version", CNPG_CHART, "-n", "cnpg-system",
         "--create-namespace", "--wait", "--timeout", "10m")
    if csi:
        helm("upgrade", "--install", "csi", "secrets-store-csi-driver/secrets-store-csi-driver", "--version",
             CSI_CHART, "-n", "kube-system", "--set", "syncSecret.enabled=false", "--set",
             "enableSecretRotation=true", "--set", "rotationPollInterval=30s", "--set",
             "tokenRequests[0].audience=openbao", "--wait", "--timeout", "10m")


def record_versions(step_name="S0"):
    _, out, _ = kc("get", "deploy", "-n", "cnpg-system", "-o", "jsonpath={..containers[*].image}")
    rec(step_name, "cnpg operator image", out.strip())
    rec(step_name, "postgres version", su_ok("a", "select version()", db="postgres")[:60])
    rc, out, err = bao("a", "version")
    rec(step_name, "openbao version", one(out or err))
    _, out, _ = kc("version", "-o", "json")
    try:
        rec(step_name, "kubernetes server", json.loads(out)["serverVersion"]["gitVersion"])
    except (ValueError, KeyError):
        pass
    rec(step_name, "charts", f"cloudnative-pg {CNPG_CHART}, openbao {OPENBAO_CHART}, "
                             f"secrets-store-csi-driver {CSI_CHART}, image {PG_IMAGE}")


def app_schema(r):
    names = [x["name"] for x in role_list()]
    q = "select count(*) from pg_roles where rolname in (" + ",".join(f"'{n}'" for n in names) + ")"
    ok, took = wait_for("operator created the managed roles", lambda: su_ok(r, q) == str(len(names)), timeout=300)
    rec("S0", "managed roles created by the operator after seconds", f"{ok} {took}")
    su_ok(r,"create table if not exists t(id serial primary key, note text, at timestamptz default now()); "
             "alter table t owner to appdb_rw; grant select on t to appdb_ro; "
             "grant usage, create on schema public to appdb_rw; grant usage on schema public to appdb_ro; "
             "insert into t(note) values ('seed');")


# =================================================================== JOB 1
def setup_pair():
    install_common(csi=True)
    for n in ("acme-a", "acme-b"):
        ns(n)
        secret(n, "openbao-dbadmin", {"username": "openbao_dbadmin", "password": DBADMIN_PW},
               "kubernetes.io/basic-auth")
        secret(n, "app-static-ctl", {"username": "app_static_ctl", "password": CTL_PW}, "kubernetes.io/basic-auth")
    install_openbao("a", csi=True)
    install_openbao("b", csi=False)
    client_pod()
    apply(cluster_a(with_replica=False))
    rec("S0", "region A cluster healthy after seconds", str(wait_cluster("acme-a", "pg-a")))
    app_schema("a")
    copy_secret("acme-a", "pg-a-ca", "acme-b")
    copy_secret("acme-a", "pg-a-replication", "acme-b")
    apply(cluster_b())
    rec("S0", "region B replica cluster healthy after seconds", str(wait_cluster("acme-b", "pg-b")))
    apply(cluster_a(with_replica=True))
    time.sleep(20)
    rec("S0", "pair: A in recovery / B in recovery", f"{in_recovery('a')} / {in_recovery('b')}")
    rec("S0", "pair: B replays A (rows of t on B)", su_ok("b", "select count(*) from t"))
    for r in ("a", "b"):
        bao_init(r)
    bao_setup_engine("a", k8s_auth_ns=("app", "acme-a"))
    bao_setup_engine("b")
    record_versions()


@step("S0")
def s0_facts(ctx):
    rl = roles("a")
    rec("S0", "openbao_dbadmin rolsuper / rolcreaterole", f"{rl['openbao_dbadmin']['super']} / "
                                                         f"{rl['openbao_dbadmin']['createrole']}")
    rec("S0", "memberships of openbao_dbadmin from managed.roles", mem_of(members("a"), member="openbao_dbadmin"))
    rec("S0", "openbao_dbadmin reads data in appdb (select count(*) from t)",
        login(RW["a"], "openbao_dbadmin", DBADMIN_PW, "select count(*) from t"))
    rec("S0", "db/config as in 4.10 on region A (verify_connection default)", bao_config("a"))
    for name, group, ttl, mx, extra, rv in (
            ("acme.app.appdb.rw", "appdb_rw", "1h", "24h", "", REVOKE),
            ("acme.app.appdb.ro", "appdb_ro", "1h", "24h", "", REVOKE),
            ("acme.app.appdb.short", "appdb_rw", "90s", "120s", "", REVOKE),
            ("acme.app.appdb.rwset", "appdb_rw", "90s", "120s", SET_ROLE, REVOKE),
            ("acme.app.appdb.shortterm", "appdb_rw", "90s", "120s", "", TERMINATE + REVOKE)):
        rec("S0", f"db/roles/{name}", bao_dyn_role("a", name, group, ttl, mx, extra, rv))
    _, msg = issue("a", "acme.app.appdb.rw")
    rec("S0", "issue rw user with managed.roles only (no admin option on appdb_rw)", msg)
    rec("S0", "out-of-band superuser grant: admin option on group roles", res(*su(
        "a", "grant appdb_rw, appdb_ro to openbao_dbadmin with admin option")))
    rec("S0", "memberships of openbao_dbadmin after grant", mem_of(members("a"), member="openbao_dbadmin"))
    u1, msg = issue("a", "acme.app.appdb.rw")
    rec("S0", "issue rw user after admin grant", msg)
    u2, msg = issue("a", "acme.app.appdb.ro")
    rec("S0", "issue ro user after admin grant", msg)
    ctx["u1"], ctx["u2"] = u1, u2
    if u1:
        rec("S0", "rw user inserts into t", login(RW["a"], u1["user"], u1["pw"],
                                                 "insert into t(note) values ('u1') returning id"))
        rec("S0", "creator membership rows of rw user", mem_of(members("a"), role=u1["user"]))
        rec("S0", "rw user memberships", mem_of(members("a"), member=u1["user"]))
    if u2:
        rec("S0", "ro user selects t", login(RW["a"], u2["user"], u2["pw"], "select count(*) from t"))
        rec("S0", "ro user inserts into t (expect denied)", login(RW["a"], u2["user"], u2["pw"],
                                                                  "insert into t(note) values ('u2')"))


@step("S2-V1")
def s2_v1(ctx):
    u0, msg = issue("a", "acme.app.appdb.rw")
    rec("S2-V1", "issue u0", msg)
    if u0:
        rec("S2-V1", "revoke u0 at once, statements of 4.10 (sync)", revoke("a", u0["lease"]))
        rec("S2-V1", "u0 still in pg_roles after revoke", str(u0["user"] in roles("a")))
        rec("S2-V1", "u0 lease after revoke", lease_state("a", u0["lease"]))
        ctx["u0"] = u0
    u1, u2 = ctx.get("u1"), ctx.get("u2")
    before_r, before_m = roles("a"), members("a")
    rec("S2-V1", "openbao_dbadmin memberships before reconcile", mem_of(before_m, member="openbao_dbadmin"))
    ok, took = trigger("a")
    rec("S2-V1", "operator reconcile ran (comment applied) seconds", f"{ok} {took}")
    after_r, after_m = roles("a"), members("a")
    rec("S2-V1", "openbao_dbadmin memberships after reconcile", mem_of(after_m, member="openbao_dbadmin"))
    lost = sorted(set(mem_of(before_m)) - set(mem_of(after_m)))
    gained = sorted(set(mem_of(after_m)) - set(mem_of(before_m)))
    rec("S2-V1", "membership rows removed by the reconcile", lost)
    rec("S2-V1", "membership rows added by the reconcile", gained)
    for u in (u1, u2):
        if not u:
            continue
        rec("S2-V1", f"{u['user']} attributes unchanged (exists, valid until, password hash)",
            str(before_r.get(u["user"]) == after_r.get(u["user"])))
        rec("S2-V1", f"{u['user']} login after reconcile", login(RW["a"], u["user"], u["pw"]))
    rec("S2-V1", "cnpg managedRolesStatus", roles_status("a"))
    if u1:
        rec("S2-V1", "revoke rw user after reconcile (sync)", revoke("a", u1["lease"]))
        rec("S2-V1", "rw user still in pg_roles after revoke", str(u1["user"] in roles("a")))
    _, out, _ = kc("logs", "-n", "acme-a", "pg-a-1", "-c", "postgres", "--since=10m")
    lines = [l for l in out.splitlines() if "roles_reconciler" in l and ("REVOKE" in l or "revok" in l.lower()
                                                                          or "membership" in l.lower())]
    rec("S2-V1", "instance manager log lines on memberships", [one(l, 300) for l in lines[-5:]])


@step("S3-V1")
def s3_v1(ctx):
    rec("S3-V1", "app_static password hash before OpenBao (md5 prefix of rolpassword, empty = none)",
        roles("a")["app_static"]["pwhash"])
    data = json.dumps({"db_name": "acme.pg", "username": "app_static", "rotation_period": "720h"})
    rec("S3-V1", "create static role, no admin option on app_static",
        baor("a", "write", "db/static-roles/acme.app.appdb", "-", data=data))
    rec("S3-V1", "out-of-band grant: admin option on app_static (inherit false, set false)", res(*su(
        "a", "grant app_static to openbao_dbadmin with admin option, inherit false, set false")))
    rec("S3-V1", "create static role after grant",
        baor("a", "write", "db/static-roles/acme.app.appdb", "-", data=data))
    rec("S3-V1", "rotate after grant", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    u, p, msg = static_creds("a")
    rec("S3-V1", "read db/static-creds", msg)
    if p:
        rec("S3-V1", "login with static password", login(RW["a"], u, p))
    h1 = roles("a")["app_static"]["pwhash"]
    ok, took = trigger("a")
    rec("S3-V1", "operator reconcile ran", f"{ok} {took}")
    rec("S3-V1", "openbao_dbadmin -> app_static after reconcile", mem_of(members("a"), member="openbao_dbadmin",
                                                                       role="app_static"))
    rec("S3-V1", "app_static password hash unchanged by reconcile", str(roles("a")["app_static"]["pwhash"] == h1))
    if p:
        rec("S3-V1", "login with static password after reconcile", login(RW["a"], u, p))
    rec("S3-V1", "rotate after reconcile", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))

    # V1b: the management role stays in managed.roles and declares the static role in inRoles.
    STATE["admin_inroles"] = ["appdb_rw", "appdb_ro", "app_static"]
    ok, took = trigger("a")
    rec("S3-V1b", "inRoles of openbao_dbadmin += app_static, reconcile ran", f"{ok} {took}")
    rec("S3-V1b", "openbao_dbadmin -> app_static as granted by the operator",
        mem_of(members("a"), member="openbao_dbadmin", role="app_static"))
    rec("S3-V1b", "out-of-band grant on top: admin option, inherit false, set false", res(*su(
        "a", "grant app_static to openbao_dbadmin with admin option, inherit false, set false")))
    rec("S3-V1b", "openbao_dbadmin -> app_static after grant",
        mem_of(members("a"), member="openbao_dbadmin", role="app_static"))
    rec("S3-V1b", "rotate", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    u, p, msg = static_creds("a")
    if p:
        rec("S3-V1b", "login with rotated static password", login(RW["a"], u, p))
    h1 = roles("a")["app_static"]["pwhash"]
    ok, took = trigger("a")
    rec("S3-V1b", "operator reconcile ran", f"{ok} {took}")
    rec("S3-V1b", "openbao_dbadmin -> app_static after reconcile",
        mem_of(members("a"), member="openbao_dbadmin", role="app_static"))
    rec("S3-V1b", "app_static password hash unchanged by reconcile", str(roles("a")["app_static"]["pwhash"] == h1))
    if p:
        rec("S3-V1b", "login with static password after reconcile", login(RW["a"], u, p))
    rec("S3-V1b", "rotate after reconcile", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    u, p, msg = static_creds("a")
    if p:
        rec("S3-V1b", "login after second rotation", login(RW["a"], u, p))
    rec("S3-V1b", "openbao_dbadmin reads data through app_static/appdb_rw",
        login(RW["a"], "openbao_dbadmin", DBADMIN_PW, "select count(*) from t"))


@step("V2-setup")
def v2_setup(ctx):
    """V2: the management role is created and granted outside managed.roles."""
    STATE["admin_managed"] = False
    STATE["tag"] = "v2"
    ok, took = trigger("a")
    rec("V2-setup", "openbao_dbadmin removed from managed.roles, reconcile ran", f"{ok} {took}")
    rec("V2-setup", "openbao_dbadmin still exists, rolsuper", roles("a").get("openbao_dbadmin", {}).get("super"))
    rec("V2-setup", "superuser SQL: createrole_self_grant + admin grants", res(*su(
        "a", "alter role openbao_dbadmin set createrole_self_grant = 'set, inherit'; "
             "grant appdb_rw, appdb_ro to openbao_dbadmin with admin option; "
             "grant app_static, app_static_ctl to openbao_dbadmin with admin option, inherit false, set false")))
    rec("V2-setup", "db/reset so the setting reaches the OpenBao connection",
        baor("a", "write", "-f", "db/reset/acme.pg"))
    ok, took = trigger("a")
    rec("V2-setup", "reconcile ran", f"{ok} {took}")
    rec("V2-setup", "openbao_dbadmin memberships after reconcile", mem_of(members("a"), member="openbao_dbadmin"))
    rec("V2-setup", "cnpg managedRolesStatus", roles_status("a"))


@step("S2-V2")
def s2_v2(ctx):
    u3, msg = issue("a", "acme.app.appdb.rw")
    rec("S2-V2", "issue rw user u3", msg)
    u4, msg = issue("a", "acme.app.appdb.ro")
    rec("S2-V2", "issue ro user u4", msg)
    if not (u3 and u4):
        return
    before_r, before_m = roles("a"), members("a")
    rec("S2-V2", "creator membership row of u3", mem_of(before_m, role=u3["user"]))
    ok, took = trigger("a")
    rec("S2-V2", "operator reconcile ran", f"{ok} {took}")
    time.sleep(30)
    after_r, after_m = roles("a"), members("a")
    lost = sorted(set(mem_of(before_m)) - set(mem_of(after_m)))
    rec("S2-V2", "membership rows removed by the reconcile", lost)
    for u in (u3, u4):
        rec("S2-V2", f"{u['user']} attributes unchanged", str(before_r.get(u["user"]) == after_r.get(u["user"])))
        rec("S2-V2", f"{u['user']} login after reconcile", login(RW["a"], u["user"], u["pw"]))
    rec("S2-V2", "cnpg managedRolesStatus", roles_status("a"))
    rec("S2-V2", "revoke u4 (sync)", revoke("a", u4["lease"]))
    rec("S2-V2", "u4 still in pg_roles after revoke", str(u4["user"] in roles("a")))
    ctx["u3"] = u3


@step("S3-V2")
def s3_v2(ctx):
    data = json.dumps({"db_name": "acme.pg", "username": "app_static", "rotation_period": "720h"})
    rec("S3-V2", "static role write (idempotent)", baor("a", "write", "db/static-roles/acme.app.appdb", "-",
                                                        data=data))
    rec("S3-V2", "rotate", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    u, p, msg = static_creds("a")
    rec("S3-V2", "read static creds", msg)
    if not p:
        return
    rec("S3-V2", "login with static password", login(RW["a"], u, p))
    h1 = roles("a")["app_static"]["pwhash"]
    ok, took = trigger("a")
    rec("S3-V2", "operator reconcile ran", f"{ok} {took}")
    rec("S3-V2", "app_static hash unchanged by reconcile", str(roles("a")["app_static"]["pwhash"] == h1))
    rec("S3-V2", "login after reconcile", login(RW["a"], u, p))
    # instance restart: the role synchroniser runs again at start-up
    uid0 = kc("get", "pod", "pg-a-1", "-n", "acme-a", "-o", "jsonpath={.metadata.uid}")[1].strip()
    kc("delete", "pod", "pg-a-1", "-n", "acme-a", "--wait=false")
    ok, took = wait_for("pg-a-1 recreated and primary again", lambda: kc(
        "get", "pod", "pg-a-1", "-n", "acme-a", "-o", "jsonpath={.metadata.uid}")[1].strip() not in ("", uid0)
        and in_recovery("a") == "f", timeout=600)
    rec("S3-V2", "instance pod restarted, back as primary after seconds", f"{ok} {took}")
    time.sleep(30)
    rec("S3-V2", "app_static hash unchanged by restart", str(roles("a")["app_static"]["pwhash"] == h1))
    rec("S3-V2", "login after restart", login(RW["a"], u, p))
    rec("S3-V2", "openbao_dbadmin memberships after restart", mem_of(members("a"), member="openbao_dbadmin"))

    # Control: a role WITH passwordSecret, rotated by OpenBao, then the Secret changes.
    cdata = json.dumps({"db_name": "acme.pg", "username": "app_static_ctl", "rotation_period": "720h"})
    rec("S3-ctl", "static role on app_static_ctl (has passwordSecret)",
        baor("a", "write", "db/static-roles/acme.app.ctl", "-", data=cdata))
    cu, cp, msg = static_creds("a", "acme.app.ctl")
    if cp:
        rec("S3-ctl", "login with OpenBao password", login(RW["a"], cu, cp))
        rc, out, err = kc("annotate", "secret", "app-static-ctl", "-n", "acme-a", f"lab/touch={int(time.time())}",
                          "--overwrite")
        rec("S3-ctl", "touch the Secret (metadata only, same password)", res(rc, out, err))
        ok, took = trigger("a")
        rec("S3-ctl", "reconcile ran", f"{ok} {took}")
        rec("S3-ctl", "login with OpenBao password after Secret touch", login(RW["a"], cu, cp))
        rec("S3-ctl", "login with Secret password after Secret touch", login(RW["a"], cu, CTL_PW))

    # CSI: a pod reads db/static-creds through the Secrets Store CSI driver.
    apply({"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": "app", "namespace": "acme-a"}})
    objects = ("- objectName: username\n  secretPath: db/static-creds/acme.app.appdb\n  secretKey: username\n"
               "- objectName: password\n  secretPath: db/static-creds/acme.app.appdb\n  secretKey: password\n")
    apply({"apiVersion": "secrets-store.csi.x-k8s.io/v1", "kind": "SecretProviderClass",
           "metadata": {"name": "app-static", "namespace": "acme-a"},
           "spec": {"provider": "openbao", "parameters": {
               "vaultAddress": "http://openbao.bao-a.svc.cluster.local:8200", "roleName": "workload",
               "audience": "openbao", "objects": objects}}})
    apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "static-reader", "namespace": "acme-a"},
           "spec": {"serviceAccountName": "app",
                    "containers": [{"name": "c", "image": PG_IMAGE, "command": ["sleep", "infinity"],
                                    "volumeMounts": [{"name": "db", "mountPath": "/var/run/secrets/openova",
                                                      "readOnly": True}]}],
                    "volumes": [{"name": "db", "csi": {"driver": "secrets-store.csi.k8s.io", "readOnly": True,
                                                        "volumeAttributes": {"secretProviderClass": "app-static"}}}]}})
    rc, out, err = kc("wait", "pod/static-reader", "-n", "acme-a", "--for=condition=Ready", "--timeout=240s",
                      timeout=260)
    rec("S3-CSI", "pod with CSI mount Ready", res(rc, out, err))
    if rc != 0:
        _, ev, _ = kc("get", "events", "-n", "acme-a", "--field-selector", "involvedObject.name=static-reader")
        rec("S3-CSI", "pod events", one(ev, 1500))
        return

    def file_sha():
        rc, out, _ = kc("exec", "-n", "acme-a", "static-reader", "--", "sh", "-c",
                        "tr -d '\\n' < /var/run/secrets/openova/password | sha256sum | cut -c1-16")
        return out.strip() if rc == 0 else "?"

    def file_login():
        rc, out, err = kc("exec", "-n", "acme-a", "static-reader", "--", "sh", "-c",
                          'PGPASSWORD="$(cat /var/run/secrets/openova/password)" psql "host=' + RW["a"] +
                          ' dbname=appdb sslmode=require connect_timeout=5 user=$(cat /var/run/secrets/openova/'
                          'username)" -AtXc "select current_user"')
        return res(rc, out, err)

    u, p, _ = static_creds("a")
    rec("S3-CSI", "mounted file equals OpenBao static password", str(file_sha() == sha(p)))
    rec("S3-CSI", "login with mounted files", file_login())
    rec("S3-CSI", "rotate", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    t_rot = time.time()
    u, p2, _ = static_creds("a")
    rec("S3-CSI", "OpenBao password changed by rotation", str(p2 != p))
    ok, took = wait_for("mounted file shows the rotated password", lambda: file_sha() == sha(p2), timeout=300,
                        interval=3)
    rec("S3-CSI", "file shows rotated password (rotationPollInterval=30s), seconds", f"{ok} {round(time.time()-t_rot)}")
    rec("S3-CSI", "login with mounted files after rotation", file_login())


@step("S4")
def s4(ctx):
    uo, msg = issue("a", "acme.app.appdb.short")
    rec("S4", "issue short user with open session (ttl 90s)", msg)
    uw, msg = issue("a", "acme.app.appdb.short")
    rec("S4", "issue short user that will own a table", msg)
    us, msg = issue("a", "acme.app.appdb.rwset")
    rec("S4", "issue short user with SET role variant", msg)
    ut, msg = issue("a", "acme.app.appdb.shortterm")
    rec("S4", "issue short user whose revocation terminates sessions", msg)
    if not uo:
        return
    t_issue = time.time()
    script = ("i=0; : > /tmp/loop.sql; while [ $i -lt 44 ]; do "
              "echo \"select 'tick $i', now()::time(0), current_user, session_user, (select count(*) from t);\" >> /tmp/loop.sql; "
              "echo \"insert into t(note) values ('s4 $i');\" >> /tmp/loop.sql; "
              "echo \"select pg_sleep(5);\" >> /tmp/loop.sql; i=$((i+1)); done; "
              f"psql \"host={RW['a']} port=5432 dbname=appdb user={uo['user']} sslmode=require connect_timeout=5\" "
              "-AtX -f /tmp/loop.sql 2>&1; echo \"psql exit $?\"")
    apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "session-open", "namespace": "client"},
           "spec": {"restartPolicy": "Never", "containers": [{"name": "c", "image": PG_IMAGE, "env": [
               {"name": "PGPASSWORD", "value": uo["pw"]}], "command": ["sh", "-c", script]}]}})
    if ut:
        apply({"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "session-term", "namespace": "client"},
               "spec": {"restartPolicy": "Never", "containers": [{"name": "c", "image": PG_IMAGE, "env": [
                   {"name": "PGPASSWORD", "value": ut["pw"]}], "command": ["sh", "-c",
                                                                         script.replace(uo["user"], ut["user"])]}]}})
    if uw:
        rec("S4", "short user creates a table in appdb", login(RW["a"], uw["user"], uw["pw"],
            "create table owned_by_dyn(id int); select tableowner from pg_tables where tablename='owned_by_dyn'"))
    if us:
        rec("S4", "SET-role user creates a table in appdb", login(RW["a"], us["user"], us["pw"],
            "create table owned_by_set(id int); select tableowner from pg_tables where tablename='owned_by_set'"))
    gone = {}
    deadline = t_issue + 240
    while time.time() < deadline:
        rl = roles("a")
        for name, u in (("open", uo), ("owner", uw), ("setrole", us), ("terminate", ut)):
            if u and u["user"] not in rl and name not in gone:
                gone[name] = round(time.time() - t_issue)
        time.sleep(5)
    for name, u in (("open", uo), ("owner", uw), ("setrole", us), ("terminate", ut)):
        if not u:
            continue
        rec("S4", f"{name} user: dropped from pg_roles, seconds after issue", str(gone.get(name, "not dropped")))
        rec("S4", f"{name} user: lease 240s after issue", lease_state("a", u["lease"]))
    rec("S4", "new login with the expired open-session user", login(RW["a"], uo["user"], uo["pw"]))
    _, out, _ = kc("logs", "-n", "client", "session-open", timeout=60)
    with open(f"{OUT}/s4-session.log", "w") as f:
        f.write(out)
    print(out, flush=True)
    ticks = [l for l in out.splitlines() if l.startswith("tick ")]
    errs = [l for l in out.splitlines() if "ERROR" in l or "FATAL" in l or "fatal" in l]
    rec("S4", "open session: ticks answered / error lines", f"{len(ticks)} / {len(errs)}")
    rec("S4", "open session: first error line", errs[0] if errs else "none")
    rec("S4", "open session: last tick line", ticks[-1] if ticks else "none")
    if ut:
        _, out, _ = kc("logs", "-n", "client", "session-term", timeout=60)
        with open(f"{OUT}/s4-session-term.log", "w") as f:
            f.write(out)
        print(out, flush=True)
        ticks = [l for l in out.splitlines() if l.startswith("tick ")]
        tail = [l for l in out.splitlines() if l.strip()][-4:]
        rec("S4", "terminate variant session: ticks answered", str(len(ticks)))
        rec("S4", "terminate variant session: last tick line", ticks[-1] if ticks else "none")
        rec("S4", "terminate variant session: last lines", tail)
    _, out, _ = kc("logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--since=6m")
    lines = [one(l, 400) for l in out.splitlines() if "revok" in l.lower()]
    rec("S4", "OpenBao log lines about revocation", lines[-6:])
    if us:
        rec("S4", "owner of owned_by_set after revocation", su_ok("a", "select tableowner from pg_tables where "
                                                                      "tablename='owned_by_set'"))


def transitions(samples, r):
    out, last = [], None
    for t, rr, ok, msg in samples:
        if rr != r:
            continue
        if ok != last:
            out.append(f"{'ok' if ok else 'fail'} from {t}s" + ("" if ok else f" ({msg})"))
            last = ok
    return out


def instance_log(r, since="6m"):
    _, out, _ = kc("logs", "-n", f"acme-{r}", f"pg-{r}-1", "-c", "postgres", f"--since={since}", timeout=60)
    keep = []
    for line in out.splitlines():
        try:
            j = json.loads(line)
        except ValueError:
            continue
        msg = j.get("msg", "")
        rec_ = j.get("record") or {}
        if isinstance(rec_, dict) and rec_.get("message"):
            msg = f"{msg}: {rec_.get('message')}"
        if re.search(r"shut ?down|promot|token|smart|fast|restart|waiting|demot|timeout", msg, re.I):
            keep.append(f"{str(j.get('ts', ''))[11:19]} {msg[:160]}")
    return keep


@step("S6")
def s6(ctx):
    """A dynamic user read with a short-lived login token: does the user outlive the token?"""
    rec("S6", "auth role workload-2m (token_ttl 2m, as workload but shorter)", baor(
        "a", "write", "auth/kubernetes/role/workload-2m", "bound_service_account_names=app",
        "bound_service_account_namespaces=acme-a", "audience=openbao", "token_policies=db-read",
        "token_ttl=2m", "token_max_ttl=2m"))
    rc, jwt, err = kc("create", "token", "app", "-n", "acme-a", "--audience", "openbao", "--duration", "10m")
    ok, j = baoj("a", "write", "-format=json", "auth/kubernetes/login", "role=workload-2m", f"jwt={jwt.strip()}")
    if not ok:
        rec("S6", "login", j)
        return
    tok = j["auth"]["client_token"]
    rec("S6", "login token lease seconds", str(j["auth"]["lease_duration"]))
    rc, out, err = bao("a", "read", "-format=json", "db/creds/acme.app.appdb.rw", token=tok)
    if rc != 0:
        rec("S6", "read db/creds with that token", one(err))
        return
    c = json.loads(out)
    user, pw, lease = c["data"]["username"], c["data"]["password"], c["lease_id"]
    rec("S6", "db/creds lease seconds (role default_ttl 1h)", str(c["lease_duration"]))
    rec("S6", "login with the user", login(RW["a"], user, pw))
    t0 = time.time()
    ok, took = wait_for("dynamic user dropped", lambda: user not in roles("a"), timeout=240, interval=5)
    rec("S6", "user dropped while its own lease still runs, seconds after read", f"{ok} {round(time.time() - t0)}")
    rec("S6", "db/creds lease after that", lease_state("a", lease))
    rec("S6", "login with the user after that", login(RW["a"], user, pw))
    _, out, _ = kc("logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--since=5m")
    rec("S6", "OpenBao log lines for that lease", [one(l, 300) for l in out.splitlines()
                                                    if lease.split("/")[-1] in l][-4:])


def switch(frm, to):
    sessions = su(to, "select usename, count(*) from pg_stat_activity where backend_type='client backend' "
                      "and pid <> pg_backend_pid() group by 1 order by 1", db="postgres")[1].strip()
    rec("S1", f"client sessions on pg-{to} (standby) before the switch", sessions.replace("\n", "; ") or "none")
    t0 = time.time()
    stop, samples = threading.Event(), []

    def probe():
        while not stop.is_set():
            for r in ("a", "b"):
                u, msg = issue(r, "acme.app.appdb.rw")
                samples.append((round(time.time() - t0), r, bool(u), "" if u else one(msg, 400)[-90:]))
            stop.wait(8)
    th = threading.Thread(target=probe, daemon=True)
    th.start()
    rc, out, err = patch("cluster.postgresql.cnpg.io", f"pg-{frm}", f"acme-{frm}",
                         {"spec": {"replica": {"primary": f"pg-{to}", "promotionToken": None}}})
    log(f"demote pg-{frm}: {res(rc, out, err)}")
    tok = {"v": ""}

    def got_token():
        _, o, _ = kc("get", "cluster.postgresql.cnpg.io", f"pg-{frm}", "-n", f"acme-{frm}", "-o",
                     "jsonpath={.status.demotionToken}")
        tok["v"] = o.strip()
        return bool(tok["v"])
    ok1, _ = wait_for(f"demotion token of pg-{frm}", got_token, timeout=420)
    t_dem = round(time.time() - t0)
    rc, out, err = patch("cluster.postgresql.cnpg.io", f"pg-{to}", f"acme-{to}",
                         {"spec": {"replica": {"primary": f"pg-{to}", "promotionToken": tok["v"]}}})
    log(f"promote pg-{to}: {res(rc, out, err)}")
    ok2, _ = wait_for(f"pg-{to} writable", lambda: in_recovery(to) == "f", timeout=420)
    t_prom = round(time.time() - t0)
    ok3, _ = wait_for(f"pg-{frm} streams from pg-{to}", lambda: su_ok(
        frm, "select coalesce((select status from pg_stat_wal_receiver),'none')", db="postgres") == "streaming",
        timeout=420)
    t_stream = round(time.time() - t0)
    time.sleep(16)
    stop.set()
    th.join(timeout=120)
    rec("S1", f"issuance probe during {frm}->{to}, region A", transitions(samples, "a"))
    rec("S1", f"issuance probe during {frm}->{to}, region B", transitions(samples, "b"))
    rec("S1", f"pg-{to} instance log during promotion", instance_log(to)[-14:])
    rec("S1", f"pg-{frm} instance log during demotion", instance_log(frm)[-10:])
    return f"demotion token {ok1} at {t_dem}s, new primary writable {ok2} at {t_prom}s, old primary streaming {ok3} at {t_stream}s"


@step("S1")
def s1(ctx):
    u5, msg = issue("a", "acme.app.appdb.rw")
    rec("S1", "issue u5 from region A (A primary)", msg)
    if u5:
        rec("S1", "u5 writes on A", login(RW["a"], u5["user"], u5["pw"], "insert into t(note) values ('u5') returning id"))
        time.sleep(3)
        rec("S1", "u5 reads on B (standby)", login(RW["b"], u5["user"], u5["pw"], "select count(*) from t"))
    rec("S1", "region B db/config while B is standby (verify_connection default)", bao_config("b"))
    rec("S1", "region B db/roles", bao_dyn_role("b", "acme.app.appdb.rw"))
    _, msg = issue("b", "acme.app.appdb.rw")
    rec("S1", "issue from region B while B is standby", msg)
    data = json.dumps({"db_name": "acme.pg", "username": "app_static", "rotation_period": "720h"})
    rec("S1", "region B static role while B is standby",
        baor("b", "write", "db/static-roles/acme.app.appdb", "-", data=data))

    ush, msg = issue("a", "acme.app.appdb.short")
    rec("S1", "short user (ttl 90s) issued by A just before the switch", msg)
    rec("S1", "switchover A -> B", switch("a", "b"))
    rec("S1", "after switch: A in recovery / B in recovery", f"{in_recovery('a')} / {in_recovery('b')}")
    if u5:
        rec("S1", "u5 (issued by A) writes on B", login(RW["b"], u5["user"], u5["pw"],
                                                       "insert into t(note) values ('u5 on B') returning id"))
        rec("S1", "u5 reads on A (now standby)", login(RW["a"], u5["user"], u5["pw"], "select count(*) from t"))
    u6, msg = issue("b", "acme.app.appdb.rw")
    rec("S1", "issue u6 from region B after switch", msg)
    if u6:
        rec("S1", "u6 writes on B", login(RW["b"], u6["user"], u6["pw"], "insert into t(note) values ('u6') returning id"))
    _, msg = issue("a", "acme.app.appdb.rw")
    rec("S1", "issue from region A after switch", msg)
    if u5:
        rec("S1", "renew u5 lease via A after switch", baor("a", "write", "sys/leases/renew",
                                                          f"lease_id={u5['lease']}", "increment=3600"))
        rec("S1", "revoke u5 via A after switch (sync)", revoke("a", u5["lease"]))
        rec("S1", "u5 still in pg_roles on B", str(u5["user"] in roles("b")))
    su_, sp, msg = static_creds("a")
    if sp:
        rec("S1", "A's static password logs in on B", login(RW["b"], su_, sp))
    rec("S1", "rotate static role via A after switch", baor("a", "write", "-f", "db/rotate-role/acme.app.appdb"))
    rec("S1", "region B creates static role after switch",
        baor("b", "write", "db/static-roles/acme.app.appdb", "-", data=data))
    bu, bp, msg = static_creds("b")
    if bp:
        rec("S1", "B's static password logs in on B", login(RW["b"], bu, bp))
    if sp:
        rec("S1", "A's static password logs in on B after B set it", login(RW["b"], su_, sp))
    rec("S1", "cnpg managedRolesStatus on B (now primary)", roles_status("b"))
    rec("S1", "app_static_ctl after B became primary: Secret password", login(RW["b"], "app_static_ctl", CTL_PW))
    if ctx.get("u3"):
        u3 = ctx["u3"]
        rec("S1", "u3 (A, before switch) login on B", login(RW["b"], u3["user"], u3["pw"]))

    rec("S1", "switchback B -> A", switch("b", "a"))
    rec("S1", "after switchback: A in recovery / B in recovery", f"{in_recovery('a')} / {in_recovery('b')}")
    if u5:
        rec("S1", "revoke u5 via A after switchback (sync)", revoke("a", u5["lease"]))
        rec("S1", "u5 still in pg_roles after switchback revoke", str(u5["user"] in roles("a")))
    if u6:
        rec("S1", "revoke u6 via B after switchback (sync)", revoke("b", u6["lease"]))
        rec("S1", "u6 still in pg_roles", str(u6["user"] in roles("a")))
        rec("S1", "u6 lease on B", lease_state("b", u6["lease"]))
        rec("S1", "u6 login on A (new primary)", login(RW["a"], u6["user"], u6["pw"]))
    if ush:
        rec("S1", "short user of A (expired during the switch): lease now", lease_state("a", ush["lease"]))
        rec("S1", "short user of A (expired during the switch): still in pg_roles", str(ush["user"] in roles("a")))
        _, out, _ = kc("logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--since=12m")
        rec("S1", "OpenBao A log lines for that lease", [one(l, 300) for l in out.splitlines()
                                                         if ush["lease"].split("/")[-1] in l][-6:])


@step("S2-V3")
def s2_v3(ctx):
    """V3: management role with no read on data. Admin option only, no inherit, dynamic users own nothing,
    revocation is DROP ROLE only."""
    rec("S2-V3", "superuser cleanup of every earlier dynamic user", res(*su("a", (
        "DO $$ DECLARE r text; BEGIN FOR r IN SELECT rolname FROM pg_roles WHERE rolname LIKE 'v-%' LOOP "
        "BEGIN EXECUTE format('REASSIGN OWNED BY %I TO appdb_rw', r); EXECUTE format('DROP OWNED BY %I', r); "
        "EXECUTE format('DROP ROLE %I', r); EXCEPTION WHEN others THEN RAISE NOTICE '%: %', r, SQLERRM; END; "
        "END LOOP; END $$;"))))
    rec("S2-V3", "dynamic users left after cleanup", str(sum(1 for u in roles("a") if u.startswith("v-"))))
    rec("S2-V3", "reset createrole_self_grant; group roles: admin true, inherit false, set false", res(*su(
        "a", "alter role openbao_dbadmin reset createrole_self_grant; "
             "grant appdb_rw, appdb_ro to openbao_dbadmin with admin option, inherit false, set false")))
    rec("S2-V3", "openbao_dbadmin memberships", mem_of(members("a"), member="openbao_dbadmin"))
    rec("S2-V3", "openbao_dbadmin reads data (expect denied)",
        login(RW["a"], "openbao_dbadmin", DBADMIN_PW, "select count(*) from t"))
    rec("S2-V3", "role rw3: SET role at login, revocation DROP ROLE only", bao_dyn_role(
        "a", "acme.app.appdb.rw3", "appdb_rw", "1h", "24h", SET_ROLE, 'DROP ROLE IF EXISTS "{{name}}";'))
    rec("S2-V3", "db/reset", baor("a", "write", "-f", "db/reset/acme.pg"))
    u7, msg = issue("a", "acme.app.appdb.rw3")
    rec("S2-V3", "issue u7", msg)
    if u7:
        rec("S2-V3", "creator membership row of u7", mem_of(members("a"), role=u7["user"]))
        rec("S2-V3", "u7 inserts and creates a table; table owner", login(RW["a"], u7["user"], u7["pw"],
            "insert into t(note) values ('u7'); create table owned_by_u7(id int); "
            "select tableowner from pg_tables where tablename='owned_by_u7'"))
        rec("S2-V3", "openbao_dbadmin reads data while u7 lives (expect denied)",
            login(RW["a"], "openbao_dbadmin", DBADMIN_PW, "select count(*) from t"))
        ok, took = trigger("a")
        rec("S2-V3", "reconcile ran", f"{ok} {took}")
        rec("S2-V3", "u7 login after reconcile", login(RW["a"], u7["user"], u7["pw"]))
        rec("S2-V3", "revoke u7 (sync)", revoke("a", u7["lease"]))
        rec("S2-V3", "u7 still in pg_roles", str(u7["user"] in roles("a")))
    u9, msg = issue("a", "acme.app.appdb.rw3")
    rec("S2-V3", "issue u9 (will own a table on purpose: SET ROLE NONE)", msg)
    if u9:
        rec("S2-V3", "u9 creates a table as itself", login(RW["a"], u9["user"], u9["pw"],
            "set role none; create table owned_by_u9(id int); select tableowner from pg_tables "
            "where tablename='owned_by_u9'"))
        rec("S2-V3", "revoke u9 (sync)", revoke("a", u9["lease"]))
        rec("S2-V3", "u9 still in pg_roles", str(u9["user"] in roles("a")))
    u8, msg = issue("a", "acme.app.appdb.rw")
    rec("S2-V3", "issue u8 with the 4.10 statements", msg)
    if u8:
        rec("S2-V3", "revoke u8 with the 4.10 statements (REASSIGN TO appdb_rw)", revoke("a", u8["lease"]))
        rec("S2-V3", "u8 still in pg_roles", str(u8["user"] in roles("a")))


def steps1to4():
    setup_pair()
    ctx = {}
    s0_facts(ctx)
    s2_v1(ctx)
    s3_v1(ctx)
    v2_setup(ctx)
    s2_v2(ctx)
    s3_v2(ctx)
    s4(ctx)
    s6(ctx)
    s1(ctx)
    s2_v3(ctx)
    rec("END", "final roles on A", roles("a"))
    rec("END", "final memberships of openbao_dbadmin on A", mem_of(members("a"), member="openbao_dbadmin"))


# =================================================================== JOB 2
def analyse(path, t0, t_stop, t_up):
    rows = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line.startswith("{"):
                try:
                    rows.append(json.loads(line))
                except ValueError:
                    pass

    def rel(t):
        return round((t - t0) / 60, 1)

    def window(a, b):
        w = [x for x in rows if a <= x["t"] < b]
        return {"ok": sum(x["ok"] for x in w), "err": sum(x["err"] for x in w),
                "pinned_ok": sum(x["pinned_ok"] for x in w), "pinned_err": sum(x["pinned_err"] for x in w)}
    bursts, cur = [], None
    for x in rows:
        if x["err"] + x["pinned_err"] > 0:
            if cur is None:
                cur = {"from_min": rel(x["t"]), "query_err": 0, "pinned_err": 0,
                       "first_error": (x["last_err"] or x["pinned_last_err"])[:120]}
            cur["to_min"] = rel(x["t"])
            cur["query_err"] += x["err"]
            cur["pinned_err"] += x["pinned_err"]
        elif cur:
            bursts.append(cur)
            cur = None
    if cur:
        bursts.append(cur)
    users = []
    for x in rows:
        if x.get("user") and (not users or users[-1][1] != x["user"][-24:]):
            users.append((rel(x["t"]), x["user"][-24:], x.get("expires")))
    end = rows[-1]["t"] + 1 if rows else t_up
    return {
        "queries (normal, outage, after)": [window(0, t_stop), window(t_stop, t_up), window(t_up, end)],
        "error bursts (minutes after start)": bursts,
        "credential changes (minute, user, expires)": users,
        "fetch attempts ok / failed": f"{rows[-1]['fetch_ok']} / {rows[-1]['fetch_err']}" if rows else None,
        "connections closed at max lifetime": rows[-1]["lifetime_closed"] if rows else None,
    }


POOLS = (("pool-1h", "acme.app.appdb.rw1h"), ("pool-15m", "acme.app.appdb.rw15m"),
         ("pool-1h-term", "acme.app.appdb.rw1hterm"))


def pool_tail():
    for name, _ in POOLS:
        log(f"{name}: {kc('logs', '-n', 'acme-a', f'deploy/{name}', '--tail=1')[1].strip()}")


def step5():
    install_common(csi=False)
    BAO["a"] = {"ns": "bao-a"}
    ns("acme-a")
    secret("acme-a", "app-static-ctl", {"username": "app_static_ctl", "password": CTL_PW}, "kubernetes.io/basic-auth")
    STATE["admin_managed"] = False
    STATE["tag"] = "v2"
    install_openbao("a", csi=False)
    client_pod()
    apply(cluster_a(with_replica=False))
    rec("S5", "cluster healthy after seconds", str(wait_cluster("acme-a", "pg-a")))
    app_schema("a")
    su_ok("a", f"create role openbao_dbadmin login createrole password '{DBADMIN_PW}'; "
               "alter role openbao_dbadmin set createrole_self_grant = 'set, inherit'; "
               "grant appdb_rw, appdb_ro to openbao_dbadmin with admin option")
    bao_init("a")
    bao_setup_engine("a", k8s_auth_ns=("consumer", "acme-a"))
    record_versions("S5")
    rec("S5", "db/config", bao_config("a"))
    rec("S5", "role rw1h", bao_dyn_role("a", "acme.app.appdb.rw1h", ttl="1h", max_ttl="24h"))
    rec("S5", "role rw15m", bao_dyn_role("a", "acme.app.appdb.rw15m", ttl="15m", max_ttl="24h"))
    rec("S5", "role rw1hterm (revocation ends sessions)", bao_dyn_role("a", "acme.app.appdb.rw1hterm", ttl="1h",
                                                                       max_ttl="24h", revoke=TERMINATE + REVOKE))
    apply({"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": "consumer", "namespace": "acme-a"}})
    t0 = time.time()
    for name, role in POOLS:
        apply({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name, "namespace": "acme-a"},
               "spec": {"replicas": 1, "selector": {"matchLabels": {"app": name}},
                        "template": {"metadata": {"labels": {"app": name}}, "spec": {
                            "serviceAccountName": "consumer", "automountServiceAccountToken": False,
                            "containers": [{"name": "c", "image": "sec280-consumer:ci", "imagePullPolicy": "Never",
                                            "env": [{"name": "OPENBAO_ADDR",
                                                     "value": "http://openbao.bao-a.svc.cluster.local:8200"},
                                                    {"name": "DB_ROLE", "value": role},
                                                    {"name": "PG_BASE", "value": f"host={RW['a']} port=5432 dbname=appdb "
                                                                                 "sslmode=require connect_timeout=5"}],
                                            "volumeMounts": [{"name": "tok", "mountPath": "/var/run/secrets/openova-token",
                                                              "readOnly": True}]}],
                            "volumes": [{"name": "tok", "projected": {"sources": [{"serviceAccountToken": {
                                "audience": "openbao", "expirationSeconds": 3600, "path": "token"}}]}}]}}}})
    for name, _ in POOLS:
        ok, took = wait_for(f"{name} answers queries", lambda n=name: '"ok":' in kc(
            "logs", "-n", "acme-a", f"deploy/{n}", "--tail=1")[1] and '"ok":0,' not in kc(
            "logs", "-n", "acme-a", f"deploy/{n}", "--tail=1")[1], timeout=300)
        rec("S5", f"{name} serving before the outage", f"{ok} {took}")
    marks = {}
    while time.time() < t0 + NORMAL_SECONDS:
        pool_tail()
        m = round((time.time() - t0) / 60)
        if m in (5, 10, 14, 16, 18) and m not in marks:
            marks[m] = sorted(u[-24:] for u in roles("a") if u.startswith("v-"))
            rec("S5", f"dynamic users at minute {m} (normal operation)", marks[m])
        time.sleep(30)
    rec("S5", "dynamic users before outage", sorted(u[-24:] for u in roles("a") if u.startswith("v-")))
    _, out, _ = kc("logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--since=25m")
    rec("S5", "OpenBao revocations during normal operation", [one(l, 200) for l in out.splitlines()
                                                              if "revoked lease" in l or "failed to revoke" in l][-8:])
    t_stop = time.time()
    kc("scale", "statefulset", "openbao", "-n", "bao-a", "--replicas=0", check=True)
    ok, took = wait_for("openbao pod gone", lambda: kc("get", "pod", "openbao-0", "-n", "bao-a")[0] != 0,
                        timeout=180)
    rec("S5", "OpenBao stopped (pod gone) seconds", f"{ok} {took}")
    while time.time() < t_stop + OUTAGE_SECONDS:
        pool_tail()
        time.sleep(60)
    kc("scale", "statefulset", "openbao", "-n", "bao-a", "--replicas=1", check=True)
    bao_wait_started("a")
    bao_unseal("a")
    t_up = time.time()
    rec("S5", "outage length, minutes (stop to unsealed)", str(round((t_up - t_stop) / 60, 1)))
    rec("S5", "timeline marks (minutes after start): stop, unsealed",
        f"{round((t_stop - t0) / 60, 1)}, {round((t_up - t0) / 60, 1)}")
    time.sleep(20)
    rec("S5", "dynamic users 20s after OpenBao is back", sorted(u[-24:] for u in roles("a") if u.startswith("v-")))
    t_end = time.time() + 480
    while time.time() < t_end:
        pool_tail()
        time.sleep(60)
    rec("S5", "dynamic users at the end", sorted(u[-24:] for u in roles("a") if u.startswith("v-")))
    for name, _ in POOLS:
        _, out, _ = kc("logs", "-n", "acme-a", f"deploy/{name}", timeout=120)
        path = f"{OUT}/{name}.log"
        with open(path, "w") as f:
            f.write(out)
        a = analyse(path, t0, t_stop, t_up)
        for k, v in a.items():
            rec("S5", f"{name}: {k}", v)
    _, out, _ = kc("logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--since=10m")
    rec("S5", "OpenBao log lines about revocation after restart",
        [one(l, 300) for l in out.splitlines() if "revok" in l.lower()][-6:])


def diag():
    for args in (["get", "pods", "-A", "-o", "wide"], ["get", "cluster.postgresql.cnpg.io", "-A", "-o", "wide"],
                 ["get", "events", "-A", "--sort-by=.lastTimestamp"],
                 ["logs", "-n", "cnpg-system", "deploy/cnpg-cloudnative-pg", "--tail=80"],
                 ["logs", "-n", "bao-a", "openbao-0", "-c", "openbao", "--tail=80"],
                 ["logs", "-n", "bao-b", "openbao-0", "-c", "openbao", "--tail=40"]):
        rc, out, err = kc(*args, timeout=60)
        print(f"----- kubectl {' '.join(args)}\n{out[-8000:]}{err[-2000:]}", flush=True)


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    if mode == "steps1to4":
        steps1to4()
    elif mode == "step5":
        step5()
    elif mode == "diag":
        diag()
    else:
        sys.exit("usage: lab.py steps1to4|step5|diag")
    print("\n===== SUMMARY =====", flush=True)
    for r in RESULTS:
        print(f"{r['step']:9} {r['t']} {r['key']}: {r['value']}", flush=True)
