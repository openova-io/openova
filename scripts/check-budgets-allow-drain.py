#!/usr/bin/env python3
"""check-budgets-allow-drain.py - a worker restart must always be able to finish.

WHY THIS EXISTS
---------------
kured restarts a worker after it drains it. A drain evicts every Pod of the node through
the eviction API, and the API refuses an eviction that would break a PodDisruptionBudget.
A budget that allows no disruption at the size its workload renders refuses that eviction
for ever: the drain never ends, kured keeps the node cordoned and the cluster's reboot
lock, and retries the eviction about five times a second (#7922). Measured on the test
Sovereign of 15 November 2026, both regions:
  - region a: CNPG Cluster guacamole/guacamole-pg, 1 instance. CNPG gave it the budget
    guacamole-pg-primary, minAvailable 1 on its only Pod: 4,510 refused evictions in each
    15-minute drain, the worker cordoned for 20 minutes of every 20;
  - region b: Deployment sigstore-system/sigstore-policy-controller-webhook, 1 replica,
    budget minAvailable 1: the same loop;
  - and, not yet chosen by kured: StatefulSet openbao/openbao (1 replica, budget
    maxUnavailable 0) in both regions, CNPG Cluster chargeback/chargeback-pg (1 instance).

WHAT IT CHECKS
--------------
Every chart of platform/*/chart, products/*/chart and products/*/*/chart (not a library
chart) is rendered with `helm template ... --api-versions postgresql.cnpg.io/v1`:
  - once with its own defaults, and
  - once for each HelmRelease of clusters/_template/bootstrap-kit that installs it, with
    that release's name, target namespace and values. The values are substituted the way
    Flux substitutes the kit: ${NAME}, ${NAME:-default}, ${NAME:=default} (value when set
    and not empty, else the default), ${NAME-default}, ${NAME=default} (value when set,
    else the default), with the values of tests/fixtures/variables/today.env, and "$$"
    gives "$". valuesFrom is not read: what it names lives in the cluster.
Each render is judged on its own:
  B1  A PodDisruptionBudget that selects the Pods of a Deployment or StatefulSet of the
      same render (same namespace, selector against the Pod template labels) and allows
      no disruption at their rendered replica count. The count is spec.replicas (absent
      means 1), or the minReplicas of a HorizontalPodAutoscaler of the render that scales
      the workload (absent means 1). Allowed disruptions follow the disruption controller:
      maxUnavailable scaled with rounding up; replicas - minAvailable, a percentage scaled
      with rounding up; neither field allows every Pod. So at one replica minAvailable 1,
      minAvailable 50%, maxUnavailable 0 and maxUnavailable 0% all fail.
  B2  A CNPG Cluster (postgresql.cnpg.io/v1) with one instance (spec.instances absent
      means 1) that does not set spec.enablePDB: false. CNPG gives every Cluster the
      budget <name>-primary, minAvailable 1 on its primary. With two or more instances
      CNPG switches the primary over to a replica before a drain evicts it (CNPG 1.29,
      "Kubernetes upgrade and maintenance"); with one instance no switchover is possible
      and no drain of its node can finish.
A render that fails judges nothing. When the chart's sources (its templates or its
dependency archives) name a PodDisruptionBudget or a CNPG Cluster, or its dependency
archives could not be fetched, that render cannot be judged and fails the guard, unless
UNRENDERABLE names it ("<chart folder> [<render>]") with its reason.

Usage (from the repository root):
  python3 scripts/check-budgets-allow-drain.py [--charts DIR ...]
  python3 scripts/check-budgets-allow-drain.py --self-test
--charts limits the sweep to those chart folders, for a quick local run; CI sweeps all.

Needs python3 with PyYAML and helm (3.16.2 or later; 3.16.0 cannot fetch a dependency on a
clean checkout). A chart whose dependency archives are missing gets them through
`helm dependency update`, three attempts.

Exit codes: 0 every render judged and none failed; 1 a budget blocks a drain, or a chart
that carries one cannot be judged; 2 nothing could be judged (no helm, no chart rendered,
or no budget and no CNPG Cluster seen), or the self-test failed.
"""
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

ME = "check-budgets-allow-drain"
CHART_GLOBS = ("platform/*/chart", "products/*/chart", "products/*/*/chart")
KIT = "clusters/_template/bootstrap-kit"
FIXTURE = "tests/fixtures/variables/today.env"
CNPG_API = "postgresql.cnpg.io/v1"
HELM_TIMEOUT = int(os.environ.get("HELM_TIMEOUT", "60"))
DEP_TIMEOUT = int(os.environ.get("DEP_TIMEOUT", "180"))
SOURCE_MARKERS = (b"PodDisruptionBudget", b"postgresql.cnpg.io")

# Renders ("<chart folder> [<render>]") of charts that carry a budget or a CNPG Cluster and
# that fail for a reason outside the chart. Each entry gives the reason. The list may only
# shrink: an entry that renders is reported.
UNRENDERABLE = {
}


class CannotJudge(Exception):
    pass


def yaml_module():
    try:
        import yaml
    except ImportError:
        raise CannotJudge("PyYAML is not installed (pip install pyyaml)")
    return yaml


# ---- the rules

def scaled(value, total):
    """An int-or-percent of total, rounded up, as the disruption controller scales it."""
    if isinstance(value, bool):
        raise CannotJudge("a budget field is a boolean: %r" % value)
    if isinstance(value, int):
        return value
    if isinstance(value, str):
        m = re.fullmatch(r"\s*(\d+)\s*%\s*", value)
        if m:
            pct = int(m.group(1))
            return -(-pct * total // 100)
        if re.fullmatch(r"\s*\d+\s*", value):
            return int(value)
    raise CannotJudge("a budget field is neither a number nor a percentage: %r" % (value,))


def allowed_disruptions(spec, expected):
    """How many of expected Pods the budget lets the eviction API remove, all healthy."""
    if spec.get("maxUnavailable") is not None:
        return min(scaled(spec["maxUnavailable"], expected), expected)
    if spec.get("minAvailable") is not None:
        return max(expected - scaled(spec["minAvailable"], expected), 0)
    return expected


def selects(selector, labels):
    """True when a label selector matches the labels (an empty selector matches every Pod)."""
    selector = selector or {}
    for k, v in (selector.get("matchLabels") or {}).items():
        if labels.get(k) != str(v):
            return False
    for e in selector.get("matchExpressions") or []:
        key, op, values = e.get("key"), e.get("operator"), [str(v) for v in e.get("values") or []]
        if op == "In" and labels.get(key) not in values:
            return False
        if op == "NotIn" and key in labels and labels.get(key) in values:
            return False
        if op == "Exists" and key not in labels:
            return False
        if op == "DoesNotExist" and key in labels:
            return False
        if op not in ("In", "NotIn", "Exists", "DoesNotExist"):
            raise CannotJudge("a selector has the operator %r" % op)
    return True


def judge(docs, namespace):
    """The findings of one render, and what was judged: (findings, budgets, clusters)."""
    def ns(d):
        return (d.get("metadata") or {}).get("namespace") or namespace

    def name(d):
        return (d.get("metadata") or {}).get("name") or "?"

    workloads, hpas, pdbs, clusters = [], {}, [], []
    for d in docs:
        kind, api = d.get("kind"), str(d.get("apiVersion") or "")
        if kind in ("Deployment", "StatefulSet") and api == "apps/v1":
            workloads.append(d)
        elif kind == "HorizontalPodAutoscaler" and api.startswith("autoscaling/"):
            ref = (d.get("spec") or {}).get("scaleTargetRef") or {}
            hpas[(ns(d), ref.get("kind"), ref.get("name"))] = d
        elif kind == "PodDisruptionBudget" and api.startswith("policy/"):
            pdbs.append(d)
        elif kind == "Cluster" and api == CNPG_API:
            clusters.append(d)

    def replicas(w):
        hpa = hpas.get((ns(w), w.get("kind"), name(w)))
        if hpa is not None:
            n = (hpa.get("spec") or {}).get("minReplicas", 1)
        else:
            n = (w.get("spec") or {}).get("replicas", 1)
        if n is None:
            n = 1
        if isinstance(n, bool) or not isinstance(n, int):
            raise CannotJudge("%s/%s renders the replica count %r" % (w.get("kind"), name(w), n))
        return n

    findings, judged_budgets = [], 0
    for p in pdbs:
        spec = p.get("spec") or {}
        hit = [w for w in workloads if ns(w) == ns(p) and selects(
            spec.get("selector"), ((((w.get("spec") or {}).get("template") or {}).get("metadata") or {}).get("labels") or {}))]
        if not hit:
            continue
        judged_budgets += 1
        expected = sum(replicas(w) for w in hit)
        if expected < 1:
            continue
        allowed = allowed_disruptions(spec, expected)
        if allowed < 1:
            field = ("maxUnavailable=%s" % spec["maxUnavailable"]) if spec.get("maxUnavailable") is not None else (
                "minAvailable=%s" % spec.get("minAvailable"))
            what = ", ".join("%s/%s replicas=%d" % (w["kind"], name(w), replicas(w)) for w in hit)
            findings.append("B1 PodDisruptionBudget %s/%s (%s) allows 0 disruptions of %s: no drain of their node can finish"
                            % (ns(p), name(p), field, what))
    for c in clusters:
        spec = c.get("spec") or {}
        n = spec.get("instances", 1)
        if isinstance(n, bool) or not isinstance(n, int):
            raise CannotJudge("CNPG Cluster %s renders the instance count %r" % (name(c), n))
        if n == 1 and spec.get("enablePDB") is not False:
            findings.append("B2 CNPG Cluster %s/%s has 1 instance and no enablePDB: false: CNPG gives it the budget "
                            "%s-primary, minAvailable 1 on its only Pod, and no drain of its node can finish"
                            % (ns(c), name(c), name(c)))
    return findings, judged_budgets, len(clusters)


# ---- the kit

SUBST = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-=])([^${}]*))?\}")


def substitute(text, env):
    """Flux's substitution of the kit: innermost ${...} first, "$$" gives "$"."""
    guard = "\0DOLLAR\0"
    text = text.replace("$$", guard)

    def one(m):
        key, op, default = m.group(1), m.group(2), m.group(3) or ""
        if op is None:
            return env.get(key, "")
        if op.startswith(":"):
            return env[key] if env.get(key) else default
        return env[key] if key in env else default

    for _ in range(20):
        new = SUBST.sub(one, text)
        if new == text:
            break
        text = new
    if "${" in text:
        raise CannotJudge("a substitution of the kit has a form this guard cannot read: %s"
                          % text[text.index("${"):text.index("${") + 60])
    return text.replace(guard, "$")


def kit_loader():
    """A PyYAML loader with the YAML 1.2 core scalars Flux's own parse uses: "22:00" stays text
    (YAML 1.1 reads it as the number 1320), and only true and false are booleans."""
    yaml = yaml_module()

    class Loader(yaml.SafeLoader):
        pass

    dropped = ("tag:yaml.org,2002:bool", "tag:yaml.org,2002:int", "tag:yaml.org,2002:float")
    Loader.yaml_implicit_resolvers = {
        k: [(tag, rx) for tag, rx in v if tag not in dropped] for k, v in yaml.SafeLoader.yaml_implicit_resolvers.items()}
    Loader.add_implicit_resolver("tag:yaml.org,2002:bool", re.compile(r"^(?:true|True|TRUE|false|False|FALSE)$"),
                                 list("tTfF"))
    Loader.add_implicit_resolver("tag:yaml.org,2002:int", re.compile(r"^[-+]?[0-9]+$"), list("-+0123456789"))
    # YAML 1.2 core: "08" is the number 8 in base 10 (YAML 1.1 reads a leading 0 as octal).
    Loader.add_constructor("tag:yaml.org,2002:int", lambda loader, node: int(loader.construct_scalar(node), 10))
    Loader.add_implicit_resolver("tag:yaml.org,2002:float", re.compile(
        r"^(?:[-+]?(?:\.[0-9]+|[0-9]+\.[0-9]*)(?:[eE][-+]?[0-9]+)?|[-+]?[0-9]+[eE][-+]?[0-9]+|[-+]?\.(?:inf|Inf|INF)"
        r"|\.(?:nan|NaN|NAN))$"), list("-+0123456789."))
    return Loader


def read_env(path):
    env = {}
    if not os.path.isfile(path):
        return env
    for line in open(path):
        line = line.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        env[k.strip()] = v
    return env


def kit_releases(kit_dir, env):
    """Every HelmRelease of the kit: (file, chart name, release name, namespace, values)."""
    yaml = yaml_module()
    loader = kit_loader()
    out = []
    for path in sorted(glob.glob(os.path.join(kit_dir, "*.yaml"))):
        try:
            docs = [d for d in yaml.load_all(open(path), Loader=loader) if isinstance(d, dict)]
        except yaml.YAMLError as e:
            raise CannotJudge("%s does not parse: %s" % (path, e))
        for d in docs:
            if d.get("kind") != "HelmRelease":
                continue
            d = yaml.load(substitute(yaml.safe_dump(d, sort_keys=True), env), Loader=loader)
            spec = d.get("spec") or {}
            chart = ((spec.get("chart") or {}).get("spec") or {}).get("chart")
            if not chart:
                continue
            meta = d.get("metadata") or {}
            target = spec.get("targetNamespace") or meta.get("namespace") or "default"
            release = spec.get("releaseName") or (
                "%s-%s" % (spec["targetNamespace"], meta.get("name")) if spec.get("targetNamespace") else meta.get("name"))
            out.append((os.path.basename(path), chart, release, target, spec.get("values") or {}))
    return out


# ---- rendering

def run(cmd, timeout, env=None):
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=env)
        return p.returncode, p.stdout, p.stderr
    except subprocess.TimeoutExpired:
        return 124, "", "timed out after %ss" % timeout


def chart_name(chart):
    yaml = yaml_module()
    meta = yaml.safe_load(open(os.path.join(chart, "Chart.yaml"))) or {}
    return meta.get("name"), meta.get("type"), meta.get("dependencies") or []


def ensure_dependencies(chart, deps, repo_config):
    """Fetch the dependency archives a clean checkout lacks. Returns the last error, or None.
    helm resolves each repository from Chart.yaml; repo_config is an empty repository list, so
    the update refreshes no repository a host happens to have registered, and helm's and
    docker's login files next to it are empty, so an OCI pull is anonymous: a CI runner has
    neither. (A stale login of a host for ghcr.io turns its public charts into 403.)"""
    if not deps:
        return None
    have = set(os.listdir(os.path.join(chart, "charts"))) if os.path.isdir(os.path.join(chart, "charts")) else set()
    if all(any(f.startswith("%s-" % dep.get("name")) for f in have) or
           os.path.isdir(os.path.join(chart, "charts", str(dep.get("name")))) for dep in deps):
        return None
    # helm 3.16.0 misreads a missing charts/ folder as a repository named "charts".
    os.makedirs(os.path.join(chart, "charts"), exist_ok=True)
    here = os.path.dirname(repo_config)
    env = dict(os.environ, HELM_REPOSITORY_CONFIG=repo_config,
               HELM_REGISTRY_CONFIG=os.path.join(here, "registry.json"), DOCKER_CONFIG=os.path.join(here, "docker"))
    err = None
    for attempt in range(3):
        rc, _, e = run(["helm", "dependency", "update", chart], DEP_TIMEOUT, env)
        if rc == 0:
            return None
        err = (e.strip().splitlines() or ["rc=%d" % rc])[-1]
        if attempt < 2:
            time.sleep(5)
    return err


def render(chart, release, namespace, values, tmp):
    """The documents of one render, or the reason it failed."""
    yaml = yaml_module()
    cmd = ["helm", "template", release, chart, "--namespace", namespace, "--api-versions", CNPG_API]
    if values:
        vf = os.path.join(tmp, "values-%d.json" % len(os.listdir(tmp)))
        with open(vf, "w") as f:
            json.dump(values, f)
        cmd += ["-f", vf]
    rc, out, err = run(cmd, HELM_TIMEOUT)
    if rc != 0:
        return None, (err.strip().splitlines() or ["rc=%d" % rc])[0][:300]
    # Trailing blanks are no YAML token to the API server's parser, but PyYAML refuses a tab
    # there (the upstream harbor chart renders "- apiVersion: v1<TAB>").
    out = re.sub(r"[ \t]+$", "", out, flags=re.M)
    try:
        return [d for d in yaml.safe_load_all(out) if isinstance(d, dict)], None
    except yaml.YAMLError as e:
        return None, "the render does not parse: %s" % str(e).splitlines()[0]


def carries_budget_source(chart):
    """True when the chart's templates or dependency archives name a budget or a CNPG Cluster."""
    for path in glob.glob(os.path.join(chart, "templates", "**", "*"), recursive=True):
        if os.path.isfile(path):
            data = open(path, "rb").read()
            if any(m in data for m in SOURCE_MARKERS):
                return True
    for path in glob.glob(os.path.join(chart, "charts", "*.tgz")):
        try:
            with tarfile.open(path) as t:
                for member in t.getmembers():
                    if member.isfile() and "/templates/" in member.name:
                        data = t.extractfile(member).read()
                        if any(m in data for m in SOURCE_MARKERS):
                            return True
        except (tarfile.TarError, OSError):
            return True
    for path in glob.glob(os.path.join(chart, "charts", "*", "templates", "**", "*"), recursive=True):
        if os.path.isfile(path) and any(m in open(path, "rb").read() for m in SOURCE_MARKERS):
            return True
    return False


def sweep(root, chart_dirs, kit_dir, env, unrenderable, log=print):
    """Render and judge every chart. Returns (findings, cannot, stats)."""
    releases = kit_releases(kit_dir, env)
    by_chart = {}
    for r in releases:
        by_chart.setdefault(r[1], []).append(r)
    findings, cannot, stale = [], [], []
    stats = {"charts": 0, "rendered_charts": 0, "renders": 0, "failed_renders": 0, "budgets": 0, "clusters": 0,
             "library": 0}
    tmp = tempfile.mkdtemp(prefix=ME + "-")
    repo_config = os.path.join(tmp, "repositories.yaml")
    with open(repo_config, "w") as f:
        f.write("apiVersion: \"\"\ngenerated: \"0001-01-01T00:00:00Z\"\nrepositories: []\n")
    try:
        for chart in chart_dirs:
            rel = os.path.relpath(chart, root)
            if not os.path.isfile(os.path.join(chart, "Chart.yaml")):
                continue
            name, ctype, deps = chart_name(chart)
            if ctype == "library":
                stats["library"] += 1
                continue
            stats["charts"] += 1
            dep_err = ensure_dependencies(chart, deps, repo_config)
            plans = [("defaults", name or os.path.basename(chart), "default", {})]
            plans += [("kit %s" % f, release, ns, values) for f, _, release, ns, values in by_chart.get(name, [])]
            failed = []
            for label, release, ns, values in plans:
                key = "%s [%s]" % (rel, label)
                docs, why = render(chart, release, ns, values, tmp)
                if docs is None:
                    stats["failed_renders"] += 1
                    failed.append((key, why))
                    continue
                stats["renders"] += 1
                if key in unrenderable:
                    stale.append(key)
                found, nb, nc = judge(docs, ns)
                stats["budgets"] += nb
                stats["clusters"] += nc
                for f in found:
                    findings.append("%s %s" % (key, f))
            if len(failed) < len(plans):
                stats["rendered_charts"] += 1
            if not failed:
                log("OK      %s: %d of %d renders judged" % (rel, len(plans), len(plans)))
                continue
            # A render that failed judged nothing. That is no verdict only when the chart carries no
            # budget and no CNPG Cluster at all; with missing dependency archives nothing says so.
            carries = bool(dep_err) or carries_budget_source(chart)
            for key, why in failed:
                if dep_err:
                    why = "%s; dependencies: %s" % (why, dep_err)
                if not carries:
                    log("SKIP    %s: does not render (%s), and the chart carries no budget and no CNPG Cluster"
                        % (key, why))
                elif key in unrenderable:
                    log("SKIP    %s: does not render, known: %s" % (key, unrenderable[key]))
                else:
                    cannot.append("%s does not render, and the chart carries a PodDisruptionBudget or a CNPG "
                                  "Cluster (or its dependencies are missing): %s" % (key, why))
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    for key in stale:
        log("NOTE    %s is in UNRENDERABLE but rendered: remove it from the list" % key)
    return findings, cannot, stats


# ---- self-test

def self_test():
    failures = []

    def expect(label, cond):
        if not cond:
            failures.append(label)

    def dep(name, replicas, labels, kind="Deployment", namespace=None):
        d = {"apiVersion": "apps/v1", "kind": kind, "metadata": {"name": name},
             "spec": {"template": {"metadata": {"labels": labels}}}}
        if replicas is not None:
            d["spec"]["replicas"] = replicas
        if namespace:
            d["metadata"]["namespace"] = namespace
        return d

    def pdb(name, selector, **fields):
        return {"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": {"name": name},
                "spec": dict(selector={"matchLabels": selector}, **fields)}

    def cnpg(name, instances=None, enable=None):
        d = {"apiVersion": CNPG_API, "kind": "Cluster", "metadata": {"name": name}, "spec": {}}
        if instances is not None:
            d["spec"]["instances"] = instances
        if enable is not None:
            d["spec"]["enablePDB"] = enable
        return d

    L = {"app": "x"}
    cases = [
        ("1 replica, minAvailable 1", [dep("w", 1, L), pdb("p", L, minAvailable=1)], 1),
        ("replicas absent (1), minAvailable 1", [dep("w", None, L), pdb("p", L, minAvailable=1)], 1),
        ("1 replica, maxUnavailable 0", [dep("w", 1, L), pdb("p", L, maxUnavailable=0)], 1),
        ("1 replica, maxUnavailable 0%", [dep("w", 1, L), pdb("p", L, maxUnavailable="0%")], 1),
        ("1 replica, minAvailable 50% (rounds up to 1)", [dep("w", 1, L), pdb("p", L, minAvailable="50%")], 1),
        ("1 replica, minAvailable 100%", [dep("w", 1, L), pdb("p", L, minAvailable="100%")], 1),
        ("StatefulSet 1 replica, maxUnavailable 0", [dep("s", 1, L, "StatefulSet"), pdb("p", L, maxUnavailable=0)], 1),
        ("3 replicas, minAvailable 3", [dep("w", 3, L), pdb("p", L, minAvailable=3)], 1),
        ("HPA minReplicas 1 over replicas 3, minAvailable 1",
         [dep("w", 3, L), pdb("p", L, minAvailable=1),
          {"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": {"name": "h"},
           "spec": {"scaleTargetRef": {"kind": "Deployment", "name": "w"}, "minReplicas": 1}}], 1),
        ("CNPG 1 instance, no enablePDB", [cnpg("db", 1)], 1),
        ("CNPG instances absent (1)", [cnpg("db")], 1),
        ("CNPG 1 instance, enablePDB true", [cnpg("db", 1, True)], 1),
        ("matchExpressions In selects the Pod",
         [dep("w", 1, L), {"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": {"name": "p"},
                           "spec": {"minAvailable": 1, "selector": {"matchExpressions": [
                               {"key": "app", "operator": "In", "values": ["x"]}]}}}], 1),
        # controls
        ("2 replicas, minAvailable 1", [dep("w", 2, L), pdb("p", L, minAvailable=1)], 0),
        ("1 replica, maxUnavailable 1", [dep("w", 1, L), pdb("p", L, maxUnavailable=1)], 0),
        ("1 replica, maxUnavailable 50% (rounds up to 1)", [dep("w", 1, L), pdb("p", L, maxUnavailable="50%")], 0),
        ("1 replica, no budget", [dep("w", 1, L)], 0),
        ("1 replica, budget selects other Pods", [dep("w", 1, L), pdb("p", {"app": "y"}, minAvailable=1)], 0),
        ("1 replica, budget in another namespace",
         [dep("w", 1, L, namespace="a"), dict(pdb("p", L, minAvailable=1), metadata={"name": "p", "namespace": "b"})], 0),
        ("HPA minReplicas 2 over replicas 1, minAvailable 1",
         [dep("w", 1, L), pdb("p", L, minAvailable=1),
          {"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "metadata": {"name": "h"},
           "spec": {"scaleTargetRef": {"kind": "Deployment", "name": "w"}, "minReplicas": 2}}], 0),
        ("CNPG 1 instance, enablePDB false", [cnpg("db", 1, False)], 0),
        ("CNPG 2 instances", [cnpg("db", 2)], 0),
        ("matchExpressions NotIn leaves the Pod out",
         [dep("w", 1, L), {"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": {"name": "p"},
                           "spec": {"minAvailable": 1, "selector": {"matchExpressions": [
                               {"key": "app", "operator": "NotIn", "values": ["x"]}]}}}], 0),
    ]
    for label, docs, want in cases:
        try:
            got = len(judge(docs, "default")[0])
        except CannotJudge as e:
            got = "cannot judge: %s" % e
        expect("rule: %s: %r findings, want %d" % (label, got, want), got == want)
    try:
        judge([dep("w", "{{ .Values.n }}", L), pdb("p", L, minAvailable=1)], "default")
        expect("rule: a templated replica count was judged", False)
    except CannotJudge:
        pass
    except Exception as e:  # noqa: BLE001
        expect("rule: a templated replica count raised %r" % e, False)
    try:
        judge([dep("w", 1, L), pdb("p", L, minAvailable="one")], "default")
        expect("rule: a budget field that is no number was judged", False)
    except CannotJudge:
        pass

    env = {"SET": "v", "EMPTY": ""}
    for text, want in [("${SET}", "v"), ("${UNSET}", ""), ("${SET:-d}", "v"), ("${EMPTY:-d}", "d"),
                       ("${UNSET:=d}", "d"), ("${EMPTY-d}", ""), ("${UNSET-d}", "d"), ("${UNSET=d}", "d"),
                       ("$${SET}", "${SET}"), ("${UNSET:-${SET:-x}}", "v"), ("a${SET}b", "avb")]:
        got = substitute(text, env)
        expect("substitution of %r: %r, want %r" % (text, got, want), got == want)
    kit_tmp = tempfile.mkdtemp(prefix=ME + "-kit-")
    try:
        open(os.path.join(kit_tmp, "01-x.yaml"), "w").write(
            "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-x\n  namespace: flux-system\n"
            "spec:\n  targetNamespace: x\n  chart:\n    spec:\n      chart: bp-x\n  values:\n"
            "    end: \"${END:=22:00}\"\n    on: ${ON:=true}\n    n: ${N:-2}\n    word: \"off\"\n"
            "    day: ${DAY:=08}\n")
        [(_, chart, release, ns, values)] = kit_releases(kit_tmp, {"N": "3"})
        want = {"end": "22:00", "on": True, "n": 3, "word": "off", "day": 8}
        expect("kit: %r %r %r %r, want bp-x x-bp-x x %r" % (chart, release, ns, values, want),
               (chart, release, ns, values) == ("bp-x", "x-bp-x", "x", want))
    finally:
        shutil.rmtree(kit_tmp, ignore_errors=True)

    # The render path, end to end: a chart whose default render is clean, whose CNPG Cluster
    # renders only with the CNPG API version, and whose release in a kit sets one replica.
    if shutil.which("helm") is None:
        failures.append("helm is not on PATH: the render path cannot be tested")
    else:
        tmp = tempfile.mkdtemp(prefix=ME + "-self-")
        try:
            chart = os.path.join(tmp, "repo", "platform", "fixture", "chart")
            os.makedirs(os.path.join(chart, "templates"))
            kit = os.path.join(tmp, "repo", "kit")
            os.makedirs(kit)
            open(os.path.join(chart, "Chart.yaml"), "w").write("apiVersion: v2\nname: bp-fixture\nversion: 0.1.0\n")
            open(os.path.join(chart, "values.yaml"), "w").write("replicas: 2\ninstances: 2\n")
            open(os.path.join(chart, "templates", "w.yaml"), "w").write(
                "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: w\nspec:\n  replicas: {{ .Values.replicas }}\n"
                "  selector:\n    matchLabels: {app: w}\n  template:\n    metadata:\n      labels: {app: w}\n"
                "    spec:\n      containers: [{name: c, image: x}]\n---\n"
                "apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata:\n  name: w\nspec:\n  minAvailable: 1\n"
                "  selector:\n    matchLabels: {app: w}\n"
                "{{- if .Capabilities.APIVersions.Has \"postgresql.cnpg.io/v1\" }}\n---\n"
                "apiVersion: postgresql.cnpg.io/v1\nkind: Cluster\nmetadata:\n  name: db\nspec:\n"
                "  instances: {{ .Values.instances }}\n{{- end }}\n")
            open(os.path.join(kit, "01-fixture.yaml"), "w").write(
                "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-fixture\n"
                "  namespace: flux-system\nspec:\n  releaseName: fixture\n  targetNamespace: fixture\n"
                "  chart:\n    spec:\n      chart: bp-fixture\n      version: 0.1.0\n"
                "  values:\n    replicas: ${FIXTURE_REPLICAS:=1}\n    instances: ${FIXTURE_INSTANCES:-1}\n")
            findings, cannot, stats = sweep(os.path.join(tmp, "repo"), [chart], kit, {}, {}, log=lambda *_: None)
            want_b1 = [f for f in findings if "[kit 01-fixture.yaml] B1 PodDisruptionBudget fixture/w" in f]
            want_b2 = [f for f in findings if "[kit 01-fixture.yaml] B2 CNPG Cluster fixture/db" in f]
            expect("render: the kit release (1 replica) is caught: %r" % findings, len(want_b1) == 1)
            expect("render: the CNPG Cluster renders with its API version and is caught: %r" % findings,
                   len(want_b2) == 1)
            expect("render: the defaults (2 replicas, 2 instances) are clean: %r" % findings,
                   not [f for f in findings if "[defaults]" in f])
            expect("render: 2 renders judged, 2 budgets, 2 clusters: %r" % stats,
                   stats["renders"] == 2 and stats["budgets"] == 2 and stats["clusters"] == 2)
            # A chart that carries a budget and renders nothing cannot be judged, unless it is listed.
            open(os.path.join(chart, "templates", "fail.yaml"), "w").write('{{ fail "values required" }}\n')
            findings, cannot, stats = sweep(os.path.join(tmp, "repo"), [chart], kit, {}, {}, log=lambda *_: None)
            expect("render: each failed render of a chart with a budget is reported: %r" % cannot, len(cannot) == 2)
            rel = os.path.relpath(chart, os.path.join(tmp, "repo"))
            listed = {"%s [defaults]" % rel: "fixture", "%s [kit 01-fixture.yaml]" % rel: "fixture"}
            findings, cannot, stats = sweep(os.path.join(tmp, "repo"), [chart], kit, {}, listed, log=lambda *_: None)
            expect("render: a listed render that fails is skipped: %r" % cannot, not cannot)
            # Without the budget in its sources, a render that fails is no verdict either way.
            os.remove(os.path.join(chart, "templates", "w.yaml"))
            findings, cannot, stats = sweep(os.path.join(tmp, "repo"), [chart], kit, {}, {}, log=lambda *_: None)
            expect("render: a failed render of a chart with no budget is skipped: %r" % cannot, not cannot)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    if failures:
        for f in failures:
            print("SELF-TEST FAIL: %s" % f, file=sys.stderr)
        return 2
    print("%s self-test: PASS (%d rule cases, 11 substitutions, the kit parse, the render path)" % (ME, len(cases)))
    return 0


def main(argv):
    if "--self-test" in argv:
        try:
            return self_test()
        except CannotJudge as e:
            print("%s: SELF-TEST CANNOT JUDGE: %s" % (ME, e), file=sys.stderr)
            return 2
    root = os.getcwd()
    if "--charts" in argv:
        chart_dirs = [os.path.join(root, c.rstrip("/")) for c in argv[argv.index("--charts") + 1:]]
    else:
        chart_dirs = sorted(d for g in CHART_GLOBS for d in glob.glob(os.path.join(root, g)))
    if shutil.which("helm") is None:
        print("%s: CANNOT JUDGE: helm is not on PATH" % ME, file=sys.stderr)
        return 2
    try:
        findings, cannot, stats = sweep(root, chart_dirs, os.path.join(root, KIT), read_env(os.path.join(root, FIXTURE)),
                                        UNRENDERABLE)
    except CannotJudge as e:
        print("%s: CANNOT JUDGE: %s" % (ME, e), file=sys.stderr)
        return 2
    except Exception as e:  # noqa: BLE001 - a crash judges nothing; it must not read as a finding (exit 1)
        print("%s: CANNOT JUDGE: %s: %s" % (ME, type(e).__name__, e), file=sys.stderr)
        return 2
    print("%s: %d charts (%d rendered, %d library charts left to their consumers), %d renders judged "
          "(%d failed), %d budgets and %d CNPG Clusters judged"
          % (ME, stats["charts"], stats["rendered_charts"], stats["library"], stats["renders"],
             stats["failed_renders"], stats["budgets"], stats["clusters"]))
    for f in findings:
        print("FAIL: %s" % f, file=sys.stderr)
    for c in cannot:
        print("FAIL: cannot judge: %s" % c, file=sys.stderr)
    if stats["renders"] == 0 or stats["budgets"] + stats["clusters"] == 0:
        print("%s: CANNOT JUDGE: no render, or no budget and no CNPG Cluster in any render" % ME, file=sys.stderr)
        return 2
    if findings or cannot:
        print("%s: %d budgets block a drain, %d charts cannot be judged (#7922)" % (ME, len(findings), len(cannot)),
              file=sys.stderr)
        return 1
    print("%s: PASS - every budget lets a drain finish" % ME)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
