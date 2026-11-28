#!/usr/bin/env python3
"""check-gate-parent-access.py - only the gate workflows use the gate's access to the parent (FND-078).

The access of FND-079 is the secret ACCEPTANCE_PARENT_KUBECONFIG of the GitHub environment gate,
which only the default branch may use. With it a job creates Jobs on the parent cluster
(scripts/gate-parent-job.sh). This guard reads .github/workflows/*.y*ml and the manifest ACCESS, and
runs only `gate-parent-job.sh policy`, which prints the NetworkPolicy of a run without a cluster.

  python3 scripts/check-gate-parent-access.py [--root DIR]
  python3 scripts/check-gate-parent-access.py --self-test

Rules (LIST: the workflows that may use the access):
  P0  every workflow of LIST exists and can be read.
  P1  a workflow not on LIST names the secret ACCEPTANCE_PARENT_KUBECONFIG, runs
      scripts/gate-parent-job.sh, has a job with environment gate (or an environment given by an
      expression), or calls a workflow of LIST.
  P2  a workflow of LIST has a trigger other than workflow_dispatch, workflow_call, pull_request,
      merge_group and schedule (pull_request_target and workflow_run would run the file of main
      with its secrets for a change of another ref).
  P3  in a workflow of LIST, a job that names the secret or runs the Job script (any command of it
      but --self-test, execution and recipe, which never reach the parent) has no environment
      gate, has a permission other than contents read and packages write (its own, or else the
      workflow's; none stated at all fails), or has an if that is not false on pull_request and on
      merge_group, read as rule N2 of scripts/check-pr-jobs.py reads an if (github.event_name is
      known, every other value is unknown, and an unknown if is not false).
  P4  the job outside of foundation-gate.yaml has an environment or names a secret (secrets: of a
      call, or an expression that reads the context secrets, toJSON(secrets) included): its dial
      of NET-G02 must come from a hosted machine that holds nothing of the gate.
  P5  ACCESS holds anything but gate-egress and the objects of ROLES with exactly their rights (a RoleBinding: subjects and Role).
  P6  a NetworkPolicy differs from POLICIES: gate-egress is default deny with DNS only; the one the Job script
      creates for a run allows TCP 6443 and 443, UDP and TCP 53 to each address as /32, for the Pods of the run's Job only
      (another range, port, peer or selector fails).

GitHub reads the names of secrets and environments in any letter case, so the guard does too. A
job is read as YAML parses it: each key and each string on its own (a quoted path is the path), and
a mention of the Job script counts as using the access unless a harmless command follows it.

Exit codes: 0 pass, 1 a rule failed, 2 a file cannot be read.
"""
import glob
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile

import yaml

LIST = (".github/workflows/foundation-gate.yaml", ".github/workflows/foundation-gate-standing.yaml", ".github/workflows/release-train.yaml")
SECRET = "ACCEPTANCE_PARENT_KUBECONFIG"
SCRIPT = "gate-parent-job.sh"
ENVIRONMENT = "gate"
TRIGGERS = {"workflow_dispatch", "workflow_call", "pull_request", "merge_group", "schedule"}
ALLOWED = {("contents", "read"), ("packages", "write")}
OUTSIDE = (".github/workflows/foundation-gate.yaml", "outside")
HERE = os.path.dirname(os.path.abspath(__file__))
HARMLESS = ("--self-test", "execution", "recipe", "preflight", "targets", "policy")
ACCESS = "scripts/gate-parent-access.yaml"
ROLES = {k: {(g, r, n, v) for g, r, n, vs in rights for v in vs.split()} for k, rights in (("Role acceptance-gate/gate-jobs", (("batch", "jobs", "*", "create get list watch delete"), ("", "pods", "*", "create get list watch delete"),
    ("", "pods/log", "*", "get"), ("", "secrets", "*", "create delete"), ("networking.k8s.io", "networkpolicies", "*", "create get delete"))), ("Role catalyst/acceptance-gate-cld-g12", (("apps", "deployments", "catalyst-api", "get"),
    ("", "pods", "*", "get list"), ("", "pods/exec", "*", "create"))), ("RoleBinding catalyst/acceptance-gate-cld-g12", (("acceptance-gate", "ServiceAccount", "gate", "Role:acceptance-gate-cld-g12"),)))}
DNS = {"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}}, "podSelector": {"matchLabels": {"k8s-app": "kube-dns"}}}],
       "ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}
RUN, GATE_EGRESS = ("gate-1-1-live-abcdef", "198.51.100.9", "203.0.113.7"), "NetworkPolicy acceptance-gate/gate-egress"
POLICIES = {  # each NetworkPolicy exactly: any other range, port, peer or selector is a finding
    GATE_EGRESS: ({"podSelector": {}, "policyTypes": ["Ingress", "Egress"], "ingress": [], "egress": [DNS]}, "default deny with DNS only"),
    SCRIPT + " policy": ({"podSelector": {"matchLabels": {"batch.kubernetes.io/job-name": RUN[0]}}, "policyTypes": ["Egress"],
                          "egress": [{"to": [{"ipBlock": {"cidr": a + "/32"}} for a in RUN[1:]], "ports": [{"protocol": t, "port": p} for t, p in (("TCP", 6443), ("TCP", 443), ("UDP", 53), ("TCP", 53))]}]},
                         "TCP 6443 and 443, UDP and TCP 53 to each address of the install as /32, for the Pods of the run's Job only")}


def access(root):
    """P5 and P6: the objects of ACCESS, and the NetworkPolicy that the Job script prints for the run RUN."""
    try:
        docs = [d for d in yaml.safe_load_all(open(os.path.join(root, ACCESS), encoding="utf-8")) if isinstance(d, dict)]
    except (OSError, yaml.YAMLError) as e:
        raise Unreadable("%s: %s" % (ACCESS, e))
    got = {"%s %s/%s" % (d.get("kind"), (d.get("metadata") or {}).get("namespace"), (d.get("metadata") or {}).get("name")): d for d in docs}
    grants = {k: {(g, r, n, v) for x in o.get("rules") or [] for g in x.get("apiGroups") or ["?"] for r in x.get("resources") or ["?"] for n in x.get("resourceNames") or ["*"] for v in x.get("verbs") or []}
              | {(s.get("namespace"), s.get("kind"), s.get("name"), "%s:%s" % ((o.get("roleRef") or {}).get("kind"), (o.get("roleRef") or {}).get("name"))) for s in o.get("subjects") or []} for k, o in [(k, got.pop(k, {})) for k in ROLES]}
    found = ["P5 %s: the %s grants %s and lacks %s" % (ACCESS, k, sorted(grants[k] - v, key=str), sorted(v - grants[k], key=str)) for k, v in ROLES.items() if grants[k] != v]
    found += ["P5 %s: holds %s, which the access does not need" % (ACCESS, k) for k in sorted(set(got) - {GATE_EGRESS})]
    out = subprocess.run(["bash", os.path.join(root, "scripts", SCRIPT), "policy", RUN[0], ",".join(RUN[1:])], capture_output=True, text=True).stdout
    got[SCRIPT + " policy"] = json.loads(out) if out.strip() else {"spec": "nothing printed"}
    for k, (spec, text) in sorted(POLICIES.items()):
        if (got.get(k) or {}).get("spec") != spec:
            found.append("P6 %s: %s is not exactly %s: %s" % (ACCESS if k == GATE_EGRESS else SCRIPT, k, text, json.dumps((got.get(k) or {}).get("spec"))))
    return found


class Unreadable(Exception):
    pass


def pr_jobs():
    spec = importlib.util.spec_from_file_location("check_pr_jobs", os.path.join(HERE, "check-pr-jobs.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def load(root, rel):
    try:
        text = open(os.path.join(root, rel), encoding="utf-8").read()
        d = yaml.safe_load(text)
    except (OSError, yaml.YAMLError) as e:
        raise Unreadable("%s: %s" % (rel, e))
    if not isinstance(d, dict):
        raise Unreadable("%s: not a workflow" % rel)
    return text, d


def triggers(d):
    on = d.get(True, d.get("on"))
    return [on] if isinstance(on, str) else list(on or [])  # a list as it is, a mapping by its keys


def jobs(d):
    j = d.get("jobs") or {}
    return j.items() if isinstance(j, dict) else []


def environment_of(job):
    e = job.get("environment")
    return e.get("name") if isinstance(e, dict) else e


def is_gate(e):
    """True when an environment name is gate in any letter case (GitHub does not tell them apart)."""
    return isinstance(e, str) and e.strip().lower() == ENVIRONMENT


def strings_of(x):
    """Every key and every scalar of a parsed job, each as one string, as YAML parsed it."""
    if isinstance(x, dict):
        for k, v in x.items():
            yield str(k)
            yield from strings_of(v)
    elif isinstance(x, list):
        for v in x:
            yield from strings_of(v)
    elif x is not None:
        yield str(x)


# The Job script, an optional closing quote, then the command word when blanks follow; else no command is seen.
CALL = re.compile(re.escape(SCRIPT) + r"[\"']?(?:\s+([^\s\"'\\;&|()]*))?")


def uses_access(job):
    """True when a string of the job names the secret (any letter case), or names the Job script without a harmless
    command right after it: a path in a variable, a quoted path or a command built at run time all count."""
    for s in strings_of(job):
        if SECRET.lower() in s.lower() or any(m.group(1) not in HARMLESS for m in CALL.finditer(s)):
            return True
    return False


def names_secrets(job):
    """True when a job holds secrets (the key of a call) or an expression that reads the context secrets."""
    if "secrets" in job:
        return True
    exprs = [e for s in strings_of(job) for e in re.findall(r"\$\{\{(.*?)\}\}", s, re.S)] + [str(job.get("if", ""))]
    return any(re.search(r"\bsecrets\b", e, re.I) for e in exprs)


def calls_list(job):
    uses = str(job.get("uses", ""))
    return any(re.search(r"(^|/)" + re.escape(rel) + r"(@|$)", uses) for rel in LIST)


def check(root, evaluate):
    """Returns the findings, one line each."""
    found = []
    files = sorted(os.path.relpath(p, root) for p in glob.glob(os.path.join(root, ".github/workflows/*.y*ml")))
    for rel in LIST:
        if rel not in files:
            found.append("P0 %s: the workflow of the list is missing" % rel)
    for rel in files:
        text, d = load(root, rel)
        if rel not in LIST:
            if SECRET.lower() in text.lower():
                found.append("P1 %s: names the secret %s" % (rel, SECRET))
            if SCRIPT in text:
                found.append("P1 %s: runs scripts/%s" % (rel, SCRIPT))
            for name, job in jobs(d):
                if not isinstance(job, dict):
                    continue
                e = environment_of(job)
                if is_gate(e):
                    found.append("P1 %s: job %s has environment %s" % (rel, name, ENVIRONMENT))
                elif isinstance(e, str) and "${{" in e:
                    found.append("P1 %s: job %s names its environment by an expression" % (rel, name))
                if calls_list(job):
                    found.append("P1 %s: job %s calls a workflow of the list" % (rel, name))
            continue
        for t in triggers(d):
            if t not in TRIGGERS:
                found.append("P2 %s: trigger %s" % (rel, t))
        for name, job in jobs(d):
            if not isinstance(job, dict):
                continue
            if not uses_access(job):
                continue
            if not is_gate(environment_of(job)):
                found.append("P3 %s: job %s uses the access without environment %s" % (rel, name, ENVIRONMENT))
            perms = job.get("permissions", d.get("permissions"))
            if perms is None:
                found.append("P3 %s: job %s states no permissions, so the default token's apply" % (rel, name))
            elif not isinstance(perms, dict):
                found.append("P3 %s: job %s has the permissions %s" % (rel, name, perms))
            else:
                for k, v in sorted(perms.items()):
                    if (k, v) not in ALLOWED:
                        found.append("P3 %s: job %s has the permission %s %s" % (rel, name, k, v))
            for event in ("pull_request", "merge_group"):
                if evaluate(job.get("if"), event) is not False:
                    found.append("P3 %s: job %s may run on %s (its if is not false there)" % (rel, name, event))
        if rel == OUTSIDE[0]:
            job = dict(jobs(d)).get(OUTSIDE[1])
            if not isinstance(job, dict):
                found.append("P4 %s: there is no job %s" % (rel, OUTSIDE[1]))
            else:
                if "environment" in job:
                    found.append("P4 %s: job %s has an environment" % (rel, OUTSIDE[1]))
                if names_secrets(job):
                    found.append("P4 %s: job %s names a secret" % (rel, OUTSIDE[1]))
    return found + access(root), len(files)


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    root = os.getcwd()
    if argv[:1] == ["--root"] and len(argv) == 2:
        root = argv[1]
    elif argv:
        print(__doc__)
        return 2
    try:
        found, n = check(root, pr_jobs().evaluate)
    except Unreadable as e:
        print("gate-parent-access: cannot read %s" % e)
        return 2
    for f in found:
        print("gate-parent-access: FAIL " + f)
    print("gate-parent-access: %s; %d workflows read, %d on the list"
          % ("%d finding(s)" % len(found) if found else "ok", n, len(LIST)))
    return 1 if found else 0


# ---- self-test
GATE, STANDING, OTHER = re.split(r"(?m)^--- # fixture \w+\n", open(os.path.join(HERE, "check-gate-parent-access.fixtures.yaml"), encoding="utf-8").read())[1:]
REGIONS_STEPS = "    steps: [{run: bash scripts/gate-parent-job.sh run --mode regions}]\n"


def regions(run, env=None):
    """STANDING whose job regions has no environment and one step: the lines of run as a block scalar, and env."""
    if REGIONS_STEPS not in STANDING or "    environment: gate\n" not in STANDING:
        raise AssertionError("the fixture STANDING changed: the helper regions cannot rewrite it")
    step = "    steps:\n      - run: |\n" + "".join("          %s\n" % line for line in run)
    if env:
        step += "        env:\n" + "".join("          %s: %s\n" % (k, json.dumps(v)) for k, v in env.items())
    return STANDING.replace(REGIONS_STEPS, step).replace("    environment: gate\n", "")


def self_test():
    evaluate = pr_jobs().evaluate
    fails, count = [0], [0]
    MANIFEST, JOB = open(os.path.join(HERE, "..", ACCESS)).read(), open(os.path.join(HERE, SCRIPT)).read()

    def run(files):
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, ".github/workflows"))
            for rel, text in files.items():
                os.makedirs(os.path.dirname(os.path.join(root, rel)), exist_ok=True)
                if text is not None:
                    open(os.path.join(root, rel), "w").write(text)
            return check(root, evaluate)[0]

    def case(name, files, want):
        count[0] += 1
        base = {LIST[0]: GATE, LIST[1]: STANDING, LIST[2]: STANDING, ".github/workflows/other.yaml": OTHER, ACCESS: MANIFEST, "scripts/" + SCRIPT: JOB}
        base.update(files)
        got = run(base)
        ok = (not got) if want is None else bool(got) and all(f.startswith(want) for f in got)
        print("%s %s%s" % ("ok  " if ok else "FAIL", name, "" if ok else " -> %s" % got))
        fails[0] += 0 if ok else 1

    case("clean: the two gate workflows use the access as allowed", {}, None)
    case("P0 a workflow of the list is missing", {LIST[2]: None}, "P0 .github/workflows/release-train.yaml: the workflow of the list is missing")
    case("P1 another workflow names the secret", {".github/workflows/other.yaml": OTHER + "# ACCEPTANCE_PARENT_KUBECONFIG\n"}, "P1 .github/workflows/other.yaml: names the secret ACCEPTANCE_PARENT_KUBECONFIG")
    case("P1 another workflow runs the Job script",
         {".github/workflows/other.yaml": OTHER.replace("echo hi", "bash scripts/gate-parent-job.sh run")}, "P1 .github/workflows/other.yaml: runs scripts/gate-parent-job.sh")
    case("P1 another workflow has a job with environment gate",
         {".github/workflows/other.yaml": OTHER.replace("runs-on: ubuntu-latest", "runs-on: ubuntu-latest\n    environment: {name: gate}")}, "P1 .github/workflows/other.yaml: job build has environment gate")
    case("P1 another workflow names its environment by an expression",
         {".github/workflows/other.yaml": OTHER.replace("runs-on: ubuntu-latest", "runs-on: ubuntu-latest\n    environment: ${{ inputs.e }}")}, "P1 .github/workflows/other.yaml: job build names its environment by an expression")
    case("P1 another workflow calls a gate workflow",
         {".github/workflows/other.yaml": OTHER + "  call:\n    uses: openova-io/openova/.github/workflows/foundation-gate-standing.yaml@main\n"}, "P1 .github/workflows/other.yaml: job call calls a workflow of the list")
    case("P2 a gate workflow runs on pull_request_target",
         {LIST[1]: STANDING.replace("  pull_request: {}", "  pull_request_target: {}")}, "P2 .github/workflows/foundation-gate-standing.yaml: trigger pull_request_target")
    case("P2 a gate workflow runs on workflow_run",
         {LIST[0]: GATE.replace("  merge_group:", "  merge_group:\n  workflow_run: {workflows: [x]}")}, "P2 .github/workflows/foundation-gate.yaml: trigger workflow_run")
    case("P3 a job that uses the access has no environment gate",
         {LIST[0]: GATE.replace("    environment: gate\n", "")}, "P3 .github/workflows/foundation-gate.yaml: job live uses the access without environment gate")
    case("P3 a job that uses the access may write contents",
         {LIST[0]: GATE.replace("{contents: read, packages: write}", "{contents: write, packages: write}")}, "P3 .github/workflows/foundation-gate.yaml: job live has the permission contents write")
    case("P3 a job that uses the access may read actions",
         {LIST[1]: STANDING.replace("{contents: read}", "{contents: read, actions: read}")}, "P3 .github/workflows/foundation-gate-standing.yaml: job regions has the permission actions read")
    case("P3 a job that uses the access states no permissions anywhere",
         {LIST[1]: STANDING.replace("    permissions: {contents: read}\n", "")}, "P3 .github/workflows/foundation-gate-standing.yaml: job regions states no permissions, so the default token's apply")
    case("P3 a job that uses the access runs on pull_request (no if)",
         {LIST[1]: STANDING.replace("    if: github.event_name == 'workflow_dispatch'\n", "")}, "P3 .github/workflows/foundation-gate-standing.yaml: job regions may run on ")
    case("P3 a job that uses the access runs on merge_group",
         {LIST[0]: GATE.replace(" && github.event_name != 'merge_group'", "")}, "P3 .github/workflows/foundation-gate.yaml: job live may run on merge_group (its if is not false there)")
    case("P3 an if that cannot be read is not false",
         {LIST[1]: STANDING.replace("if: github.event_name == 'workflow_dispatch'", "if: inputs.go == 'yes'")}, "P3 .github/workflows/foundation-gate-standing.yaml: job regions may run on ")
    no_gate = "P3 %s: job regions uses the access without environment gate" % LIST[1]
    case("P3 the Job script by a double-quoted path, without environment gate",
         {LIST[1]: regions(['bash "$GITHUB_WORKSPACE/scripts/gate-parent-job.sh" run --mode regions'])}, no_gate)
    case("P3 the Job script by a single-quoted path, without environment gate",
         {LIST[1]: regions(["bash '/home/runner/work/openova/openova/scripts/gate-parent-job.sh' run --mode regions"])}, no_gate)
    case("P3 the Job script by an unquoted path, without environment gate",
         {LIST[1]: regions(["bash scripts/gate-parent-job.sh run --mode regions"])}, no_gate)
    case("P3 the Job script by a path built from a variable, without environment gate",
         {LIST[1]: regions(['d="$GITHUB_WORKSPACE/scripts"', 'bash "${d}/gate-parent-job.sh" run --mode regions'])}, no_gate)
    case("P3 the Job script named in a variable of the step, without environment gate",
         {LIST[1]: regions(['bash "$JOB_SCRIPT" run --mode regions'], {"JOB_SCRIPT": "scripts/gate-parent-job.sh"})}, no_gate)
    case("P3 a harmless call first, then the run, without environment gate",
         {LIST[1]: regions(['e="$(bash scripts/gate-parent-job.sh execution "$E")"', 'bash "scripts/gate-parent-job.sh" run --mode regions'])}, no_gate)
    case("P3 a job that only names the secret, without environment gate",
         {LIST[1]: regions(["echo hi"], {"K": "${{ secrets.ACCEPTANCE_PARENT_KUBECONFIG }}"})}, no_gate)
    case("P3 a job that names the secret in lower case, without environment gate",
         {LIST[1]: regions(["echo hi"], {"K": "${{ secrets.acceptance_parent_kubeconfig }}"})}, no_gate)
    case("clean: a harmless command by a quoted path is not a use of the access",
         {LIST[1]: regions(['e="$(bash "$GITHUB_WORKSPACE/scripts/gate-parent-job.sh" execution "$E")"', 'echo "$e"'])}, None)
    case("clean: environment Gate in another letter case is the environment gate",
         {LIST[1]: STANDING.replace("    environment: gate\n", "    environment: {name: Gate}\n")}, None)
    case("P1 another workflow names the secret in another letter case",
         {".github/workflows/other.yaml": OTHER.replace("echo hi", "echo hi, env: {K: '${{ secrets.Acceptance_Parent_Kubeconfig }}'}")},
         "P1 .github/workflows/other.yaml: names the secret ACCEPTANCE_PARENT_KUBECONFIG")
    case("P1 another workflow has a job with environment GATE (letter case)",
         {".github/workflows/other.yaml": OTHER.replace("runs-on: ubuntu-latest", "runs-on: ubuntu-latest\n    environment: GATE")},
         "P1 .github/workflows/other.yaml: job build has environment gate")
    case("P4 the job outside has an environment",
         {LIST[0]: GATE.replace("  outside:\n    runs-on: ubuntu-latest", "  outside:\n    runs-on: ubuntu-latest\n    environment: dial")}, "P4 .github/workflows/foundation-gate.yaml: job outside has an environment")
    case("P4 the job outside names a secret",
         {LIST[0]: GATE.replace("dial x}", "dial x, env: {T: '${{ secrets.ANY }}'}}")}, "P4 .github/workflows/foundation-gate.yaml: job outside names a secret")
    case("P4 the job outside reads every secret with toJSON(secrets)",
         {LIST[0]: GATE.replace("dial x}", "dial x, env: {ALL: '${{ toJSON(secrets) }}'}}")}, "P4 .github/workflows/foundation-gate.yaml: job outside names a secret")
    case("clean: the job outside may read a variable (the resources of the Job), which is no secret",
         {LIST[0]: GATE.replace("dial x}", "dial x, env: {R: '${{ vars.GATE_PARENT_RESOURCES }}'}}")}, None)
    case("P4 the job outside is gone", {LIST[0]: GATE.replace("  outside:", "  outside2:")}, "P4 .github/workflows/foundation-gate.yaml: there is no job outside")
    sec, np, dns = "[secrets], verbs: [create, delete]}", "[networkpolicies], verbs: [create, get, delete]}", "egress:\n  - to: [{namespaceSelector"
    std_p6, run_p6 = "P6 %s: %s is not exactly default deny" % (ACCESS, GATE_EGRESS), "P6 %s: %s policy is not exactly TCP 6443 and 443, UDP and TCP 53" % (SCRIPT, SCRIPT)
    case("P5 the Role of the gate may also read Secrets", {ACCESS: MANIFEST.replace(sec, "[secrets], verbs: [create, delete, get, list]}")}, "P5 %s: the Role" % ACCESS)
    case("P5 the Role of the gate cannot create the run's NetworkPolicy", {ACCESS: MANIFEST.replace(np, "[networkpolicies], verbs: [get, delete]}")}, "P5 %s: the Role" % ACCESS)
    case("P5 the Role of the gate may update NetworkPolicies", {ACCESS: MANIFEST.replace(np, np[:-2] + ", update]}")}, "P5 %s: the Role" % ACCESS)
    case("P5 the manifest holds another object", {ACCESS: MANIFEST + "---\nkind: NetworkPolicy\nmetadata: {name: x, namespace: acceptance-gate}\n"}, "P5 %s: holds NetworkPolicy acceptance-gate/x" % ACCESS)
    case("P6 the standing policy allows a range wider than /32", {ACCESS: MANIFEST.replace(dns, "egress:\n  - to: [{ipBlock: {cidr: 5.37.79.0/24}}]\n  - to: [{namespaceSelector")}, std_p6)
    case("P6 the standing policy allows a port other than DNS", {ACCESS: MANIFEST.replace("{protocol: TCP, port: 53}", "{protocol: TCP, port: 22}")}, std_p6)
    case("P6 the standing policy selects some Pods only", {ACCESS: MANIFEST.replace("podSelector: {}", "podSelector: {matchLabels: {a: b}}")}, std_p6)
    case("P6 the run's policy allows a range wider than /32", {"scripts/" + SCRIPT: JOB.replace("$a/32", "$a/24")}, run_p6)
    case("P6 the run's policy allows a port other than 443 and 6443", {"scripts/" + SCRIPT: JOB.replace('"port": 443}', '"port": 22}')}, run_p6)
    case("P6 the run's policy selects every Pod of the namespace", {"scripts/" + SCRIPT: JOB.replace('{"matchLabels": {"batch.kubernetes.io/job-name": "%s"}}', "{}")}, run_p6)
    case("P6 the Job script prints no policy", {"scripts/" + SCRIPT: JOB.replace("cmd_policy() {", "cmd_policy() { exit 0;")}, run_p6)
    print("gate-parent-access self-test: %d of %d cases passed" % (count[0] - fails[0], count[0]))
    return 1 if fails[0] else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
