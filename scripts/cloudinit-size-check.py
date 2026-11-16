#!/usr/bin/env python3
"""cloudinit-size-check.py - the worst-case control-plane and worker user data of a provider module.

Called by scripts/cloudinit-size-check.sh (#7893). It renders the user data a
real installation sends to the cloud API, with the longest values a real
installation can carry, and prints one line per surface:

    <surface>\t<bytes>\t<limit>

How the render is made. The provider module's own expressions are used, not a
copy of them: this program reads infra/providers/<p>/main.tf, takes the
locals that build the user data (the module's user_data_worst_case.py names
them) together with every local they read, and writes them into a throwaway
OpenTofu configuration beside a verbatim copy of the module's variables.tf.
Resource attributes the locals read (an address, a random_password result, a
network name) become values of the local `res`, and a few locals derived from
resources become stand-ins. `tofu apply` of that configuration (no provider,
no network, nothing is created) evaluates the module's own text, so a change
to main.tf is measured as it is written, and a reference this program does
not know stops it with an error instead of measuring something else. The
variables pass through the module's own validation rules, so a value longer
than a rule allows stops the render too.

The worst-case values: the lengths every provider shares are below, the rest
and the limits are in infra/providers/<p>/user_data_worst_case.py; each says
what its length stands for and where that length comes from. Every token and
key is a deterministic filler of the right length and alphabet (filler()),
never a real secret.

Usage: cloudinit-size-check.py <provider> [--dump DIR] | --write-fixtures
Needs: tofu (TOFU_BIN), python3. No provider, no network, no cloud account.
"""
import base64
import hashlib
import importlib.util
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
TOFU = os.environ.get("TOFU_BIN", "tofu")
STRIP = re.compile(r"(?m)^[ ]*#( |$).*\n")  # the comment strip every module applies to user_data

ALNUM = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
UPPER_ALNUM = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
HEX = "0123456789abcdef"
LOWER = "abcdefghijklmnopqrstuvwxyz"
B64URL = ALNUM + "-_"


def filler(name, n, alphabet):
    """n characters of alphabet, deterministic per name, never a real secret.

    The characters are drawn from SHA-256 of the name and a counter, so the
    text is as hard to compress as a real random token: the packed (gz+b64)
    entries that carry it grow as they would with a real one.
    """
    out, i = [], 0
    while len(out) < n:
        for b in hashlib.sha256(f"{name}/{i}".encode()).digest():
            if b < 256 - 256 % len(alphabet):
                out.append(alphabet[b % len(alphabet)])
        i += 1
    return "".join(out[:n])


def label(name, n):
    """A DNS label of n characters: a letter, then letters and digits."""
    return filler(name, 1, LOWER) + filler(name + "+", n - 1, LOWER + "0123456789")


def ipv4(name):
    """An IPv4 address of the longest form, 15 characters (four 3-digit octets)."""
    h = hashlib.sha256(name.encode()).digest()
    return ".".join(str(100 + b % 100) for b in h[:4])


# ── Lengths shared by every provider ─────────────────────────────────────────
# FQDN. The object-storage bucket of a Sovereign is named
# `catalyst-<FQDN with dots as dashes>-<first 8 hex of the deployment id>`
# (products/catalyst/bootstrap/api/internal/providers/huawei/provider.go
# BucketNameForSovereign, .../hetzner/buckets.go), and both modules and the
# provisioner hold that name to ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$, at most 63
# characters (variables obs_bucket_name / object_storage_bucket_name,
# provisioner.go s3BucketNamePattern). 63 - 9 - 1 - 8 = 45. A provider module
# can have a tighter rule; its user_data_worst_case.py says which length it
# uses and why. The first label is as long as it can be under a pool domain of
# the canon.
def fqdn(length):
    return label("sovereign", length - len(".omani.works")) + ".omani.works"


# The parent Sovereign the callback address names: a parent on any cloud, so 45.
PARENT_FQDN = label("parent", 45 - len(".omantel.biz")) + ".omantel.biz"
# deployment_id: 16 hex characters (handler/deployments.go newID: 8 random bytes).
DEPLOYMENT_ID = filler("deployment-id", 16, HEX)
# org_name: no rule bounds it (neither variables.tf nor provisioner.go); 100
# characters stand for a long legal name.
ORG_NAME = ("Omani National Sovereign Cloud Services & Data Platforms Company "
            "(Closed Joint Stock) & Partners LLC")[:100].ljust(100, "x")
# org_email: 254 characters, the longest address RFC 5321 allows (local part
# 64 characters); variables.tf only asks for one `@` and a dot.
ORG_EMAIL = (filler("email-local", 64, LOWER) + "@" + label("email-a", 63) + "." + label("email-b", 63) + "."
             + label("email-c", 254 - 64 - 1 - 63 - 1 - 63 - 1 - 3) + ".om")
# catalyst_api_url: the callback address of the parent, https://console.<parent FQDN>/sovereign.
CATALYST_API_URL = f"https://console.{PARENT_FQDN}/sovereign"
TOKENS = {
    # k3s server and agent tokens: random_password length 64, special = false.
    "k3s": 64,
    # kubeconfig post-back bearer: 32 random bytes, hex (handler/deployments.go newBearerToken).
    "bearer": 64,
    # ghcr pull token: a GitHub fine-grained token is 93 characters (github_pat_ + 82); the
    # filler has that length but not that prefix, so no secret scanner mistakes it for one.
    "ghcr": 93,
    # Harbor robot secret: 32 characters.
    "harbor": 32,
    # PowerDNS API key: randAlphaNum 32 (platform/powerdns/chart api-credentials-secret.yaml).
    "powerdns": 32,
    # pool-domain-manager basic auth: no format in the code; 32 and 64 characters.
    "pdm_user": 32,
    "pdm_pass": 64,
}
# handover_jwt_public_key: the RSA-2048 public JWK that handoverjwt.PublicJWK
# marshals (kty, use, alg, n, e; n is 256 bytes, 342 base64url characters): 399 characters.
HANDOVER_JWK = json.dumps({"alg": "RS256", "e": "AQAB", "kty": "RSA", "n": filler("jwk-n", 342, B64URL), "use": "sig"},
                          separators=(",", ":"), sort_keys=True)
assert len(HANDOVER_JWK) == 399, len(HANDOVER_JWK)
GHCR_TOKEN = "ghcr_filler_" + filler("ghcr", 81, ALNUM)
assert len(GHCR_TOKEN) == TOKENS["ghcr"]


def variables_doc(fqdn, parent_fqdn, api_url):
    """The variables document (contract C5) a child gets: provisioner.go childVariables,
    upstream.Served. Every key the code can fill for a child holds its longest form;
    ORIGIN_REGISTRY_CA_B64, ORIGIN_PROXY_ENDPOINTS_JSON and the MAIL_* values stay
    empty because upstream.Served clears them for every child."""
    parent_registry = "registry." + parent_fqdn
    return {
        "SOVEREIGN_MODE": "sovereign",
        "SOVEREIGN_CONSUME": "origin",
        "HANDOVER_AUTO": "true",
        "HANDOVER_HOLD_SECONDS": "600",
        "REGISTRY_HOST": parent_registry,
        "REGISTRY_SCHEME": "https",
        "LOCAL_REGISTRY_HOST": "registry." + fqdn,
        "ORIGIN_REGISTRY_HOST": parent_registry,
        "ORIGIN_REGISTRY_SCHEME": "https",
        "ORIGIN_REGISTRY_CA_B64": "",
        "ORIGIN_PROXY_ENDPOINTS_JSON": "{}",
        "REGISTRY_PULL_SECRET": "ghcr-pull",
        "REGISTRY_PATH_CHARTS": "openova-io",
        "REGISTRY_PATH_RELEASE": "openova-io/release",
        "REGISTRY_PATH_PROVIDER_PACKS": "openova-io/provider-packs",
        "REGISTRY_PATH_XPKG": "proxy-xpkg",
        "REGISTRY_PATH_TOFU_PROVIDERS": "tofu-providers",
        "REGISTRY_PATH_NODE_ARTIFACTS": "node-artifacts",
        "REGISTRY_PATH_VULN_DB": "proxy-vulndb/proxy-vulndb/aquasecurity/trivy-db",
        "REGISTRY_PROXY_PROJECTS_JSON": json.dumps({
            "docker.io": "proxy-dockerhub", "gcr.io": "proxy-gcr", "ghcr.io": "proxy-ghcr",
            "public.ecr.aws": "proxy-ecr", "quay.io": "proxy-quay", "registry-1.docker.io": "proxy-dockerhub",
            "registry.k8s.io": "proxy-k8s", "xpkg.upbound.io": "proxy-xpkg"}, separators=(",", ":"), sort_keys=True),
        # The parent's release key: a cosign ECDSA P-256 public key PEM (178 bytes), base64 (240).
        "RELEASE_PUBLIC_KEY_B64": base64.b64encode(
            ("-----BEGIN PUBLIC KEY-----\n" + filler("cosign", 64, ALNUM) + "\n" + filler("cosign2", 60, ALNUM)
             + "==\n-----END PUBLIC KEY-----\n").encode()).decode(),
        "RELEASE_CHANNEL": "candidate",
        "RELEASE_VERSION": "",
        "RELEASE_AUTO": "false",
        "RELEASE_SYNC_SCHEDULE": "0 3 * * *",
        "ORIGIN_PARENT_FQDN": parent_fqdn,
        "ORIGIN_PARENT_API_URL": api_url,
        "DNS_MODE": "upstream-managed",
        "DNS_UPSTREAM_API_URL": "",
        "MAIL_MODE": "local",
        "MAIL_RELAY_HOST": "",
        "MAIL_RELAY_PORT": "587",
        "MAIL_FROM": "",
        "MAIL_RELAY_SECRET": "",
        "K3S_VERSION": "v1.31.8+k3s1",
        "K3S_UPGRADE_CONCURRENCY": "1",
        "CLOUD_CREDENTIAL_MODE": "supplied",
        "CLOUD_CREDENTIAL_ROTATION_SCHEDULE": "0 2 1 * *",
        "CLOUD_CREDENTIAL_ROTATION_NONCE": "",
        "GITOPS_REPO_URL": "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack",
    }


# Parent domains: the Sovereign's own FQDN as primary plus the three Organization
# pool domains of the canon, so PARENT_DOMAINS_FILTER and TENANT_PARENT_DOMAIN render too.
ORG_POOLS = ["omani.homes", "omani.rest", "omani.trade"]
# Provider API CIDRs of the step-08 deny-egress allow-list: four /24s of the longest form.
PROVIDER_API_CIDRS = [f"{ipv4('cidr' + str(i)).rsplit('.', 1)[0]}.0/24" for i in range(4)]
SSH_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI" + filler("ssh", 43, ALNUM) + " sovereign-admin"


# ── The provider modules ─────────────────────────────────────────────────────

def providers():
    """Every provider module folder that declares a worst case (user_data_worst_case.py)."""
    base = os.path.join(REPO, "infra", "providers")
    return sorted(d for d in os.listdir(base) if os.path.isfile(os.path.join(base, d, "user_data_worst_case.py")))


def provider_module(provider):
    path = os.path.join(REPO, "infra", "providers", provider, "user_data_worst_case.py")
    spec = importlib.util.spec_from_file_location("user_data_worst_case_" + provider.replace("-", "_"), path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# ── A small HCL scanner: where an expression ends, and which parts of it are code ──

HEREDOC = re.compile(r"<<(-?)([A-Za-z_][A-Za-z0-9_]*)\n")

def scan_expr(src, i):
    """From the start of an attribute's expression at src[i], return (end, chunks):
    end is the index of the newline that ends the expression, chunks a list of
    (start, end, is_code) covering src[i:end]. Strings, heredocs and comments
    are followed, so a bracket or a newline inside them does not count."""
    chunks = []
    stack = [["code", 0]]  # code: [kind, bracket depth]; interp: same; str; heredoc: [kind, marker, line_start]
    start, is_code = i, True
    n = len(src)

    def switch(pos, code):
        nonlocal start, is_code
        if pos > start:
            chunks.append((start, pos, is_code))
        start, is_code = pos, code

    while i < n:
        top = stack[-1]
        c = src[i]
        if top[0] in ("code", "interp"):
            if c == "#" or src.startswith("//", i):
                j = src.find("\n", i)
                i = n if j < 0 else j
                continue
            if src.startswith("/*", i):
                i = src.index("*/", i) + 2
                continue
            if c == '"':
                switch(i + 1, False)
                stack.append(["str"])
                i += 1
                continue
            m = HEREDOC.match(src, i)
            if m:
                i = m.end()
                switch(i, False)
                stack.append(["heredoc", m.group(2)])
                continue
            if c in "([{":
                top[1] += 1
            elif c in ")]}":
                if top[0] == "interp" and top[1] == 0 and c == "}":
                    stack.pop()
                    switch(i, False)
                    i += 1
                    continue
                top[1] -= 1
            elif c == "\n" and len(stack) == 1 and top[1] == 0:
                switch(i, True)
                return i, chunks
            i += 1
            continue
        if top[0] == "str":
            if c == "\\":
                i += 2
                continue
            if src.startswith("$${", i) or src.startswith("%%{", i):
                i += 3
                continue
            if src.startswith("${", i) or src.startswith("%{", i):
                i += 2
                switch(i, True)
                stack.append(["interp", 0])
                continue
            if c == '"':
                switch(i, True)
                stack.pop()
                i += 1
                continue
            i += 1
            continue
        if top[0] == "heredoc":
            line_start = src.rfind("\n", 0, i) + 1
            if i == line_start:
                j = src.find("\n", i)
                line = src[i:n if j < 0 else j]
                if line.strip() == top[1]:
                    stack.pop()
                    switch(i, True)
                    i = i + len(line)
                    continue
            if src.startswith("$${", i) or src.startswith("%%{", i):
                i += 3
                continue
            if src.startswith("${", i) or src.startswith("%{", i):
                i += 2
                switch(i, True)
                stack.append(["interp", 0])
                continue
            i += 1
            continue
    raise SystemExit("cloudinit-size-check: an expression of main.tf does not end")


def module_locals(src):
    """Every local of the module: name -> (expression text, list of code chunks)."""
    out = {}
    for m in re.finditer(r"(?m)^locals \{\n", src):
        i = m.end()
        while True:
            lm = re.compile(r"[ \t]*(?:#[^\n]*)?\n|[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*|\}\n?").match(src, i)
            if lm is None:
                raise SystemExit(f"cloudinit-size-check: cannot read the locals block at offset {i}")
            if lm.group(0).startswith("}"):
                break
            if lm.group(1) is None:
                i = lm.end()
                continue
            name = lm.group(1)
            end, chunks = scan_expr(src, lm.end())
            if name in out:
                raise SystemExit(f"cloudinit-size-check: local {name} is defined twice")
            out[name] = (src[lm.end():end], [(a - lm.end(), b - lm.end(), code) for a, b, code in chunks])
            i = end + 1
    return out




def code_of(expr, chunks):
    return " ".join(expr[a:b] for a, b, code in chunks if code)


def rewrite(expr, chunks, resource_types):
    """Point resource references at local.res and path.module at the module folder, in code only."""
    types = "(?:" + "|".join(re.escape(t) for t in sorted(resource_types)) + ")"
    out = []
    for a, b, code in chunks:
        part = expr[a:b]
        if code:
            part = re.sub(r"(?<![\w.])(" + types + r")\.", r"local.res.\1.", part)
            part = re.sub(r"(?<![\w.])path\.module\b", "local.module_dir", part)
        out.append(part)
    return "".join(out)


def render(provider, dump=None):
    pdir = os.path.join(REPO, "infra", "providers", provider)
    src = open(os.path.join(pdir, "main.tf")).read()
    variables, res, standins, roots, surfaces = provider_module(provider).worst_case(sys.modules[__name__])
    locals_ = module_locals(src)
    needed, todo = [], list(roots)
    while todo:
        name = todo.pop()
        if name in needed or name in standins:
            continue
        if name not in locals_:
            raise SystemExit(f"cloudinit-size-check: main.tf of {provider} has no local {name}")
        needed.append(name)
        expr, chunks = locals_[name]
        code = code_of(expr, chunks)
        if re.search(r"(?<![\w.])data\.", code):
            raise SystemExit(f"cloudinit-size-check: local {name} reads a data source; give it a stand-in")
        todo.extend(re.findall(r"(?<![\w.])local\.([A-Za-z_][A-Za-z0-9_]*)", code))
    work = tempfile.mkdtemp(prefix=f"cloudinit-size-{provider}.")
    try:
        shutil.copy(os.path.join(pdir, "variables.tf"), os.path.join(work, "variables.tf"))
        with open(os.path.join(work, "terraform.tfvars.json"), "w") as f:
            json.dump(variables, f)
        with open(os.path.join(work, "res.json"), "w") as f:
            json.dump({"res": res, "standins": standins}, f)
        with open(os.path.join(work, "main.tf"), "w") as f:
            f.write("# Generated by scripts/cloudinit-size-check.py from infra/providers/%s/main.tf.\n" % provider)
            f.write("locals {\n")
            f.write('  res        = jsondecode(file("${path.module}/res.json")).res\n')
            f.write("  module_dir = %s\n" % json.dumps(pdir))
            for name in sorted(standins):
                f.write('  %s = jsondecode(file("${path.module}/res.json")).standins.%s\n' % (name, name))
            for name in sorted(needed):
                expr, chunks = locals_[name]
                f.write("  %s = %s\n" % (name, rewrite(expr, chunks, res)))
            f.write("}\n")
            for name in roots:
                f.write('output "%s" {\n  value     = local.%s\n  sensitive = true\n}\n' % (name, name))
        env = dict(os.environ, TF_IN_AUTOMATION="1", TF_DATA_DIR=os.path.join(work, ".data"))
        for args in (["init", "-input=false", "-no-color"], ["apply", "-auto-approve", "-input=false", "-no-color"]):
            p = subprocess.run([TOFU, "-chdir=" + work] + args, env=env, capture_output=True, text=True)
            if p.returncode != 0:
                sys.stderr.write(p.stdout + p.stderr)
                raise SystemExit(f"cloudinit-size-check: tofu {args[0]} of the {provider} worst-case render failed")
        p = subprocess.run([TOFU, "-chdir=" + work, "output", "-json"], env=env, capture_output=True, text=True)
        if p.returncode != 0:
            sys.stderr.write(p.stderr)
            raise SystemExit("cloudinit-size-check: tofu output failed")
        out = {k: v["value"] for k, v in json.loads(p.stdout).items()}
        if dump:
            os.makedirs(dump, exist_ok=True)
            shutil.copy(os.path.join(work, "main.tf"), os.path.join(dump, f"{provider}-worst-case.tf"))
    finally:
        shutil.rmtree(work, ignore_errors=True)
    rows = []
    for label_, text, limit in surfaces(out):
        # The modules strip comment lines before user_data; measure what is sent.
        text = STRIP.sub("", text)
        if dump:
            with open(os.path.join(dump, f"{provider} {label_}.yaml".replace(" ", "_").replace(",", "").replace("(", "").replace(")", "")), "w") as f:
                f.write(text)
        rows.append((label_, len(text.encode()), limit))
    return rows


# ── The same worst case in the module tests ──────────────────────────────────
# infra/providers/<p>/tests/multi_region.tftest.hcl renders the real module
# (mocked providers) with these variables, between the two marker lines below.
# The pinned OpenTofu (1.8.5) takes no function call in a test's variables, so
# the values are written there as literals by --write-fixtures, and every run
# of this program checks that the test file holds exactly what it would write.
TEST_FILE = "multi_region.tftest.hcl"
BEGIN = "  # BEGIN worst-case variables, written by scripts/cloudinit-size-check.py --write-fixtures\n"
END = "  # END worst-case variables\n"


def hcl(v, indent):
    """An HCL literal for a JSON value."""
    pad = "  " * indent
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (int, float)):
        return json.dumps(v)
    if isinstance(v, str):
        return json.dumps(v).replace("${", "$${").replace("%{", "%%{")
    if isinstance(v, list):
        if not v:
            return "[]"
        return "[\n" + "".join(f"{pad}  {hcl(x, indent + 1)},\n" for x in v) + pad + "]"
    if isinstance(v, dict):
        if not v:
            return "{}"
        return "{\n" + "".join(f"{pad}  {json.dumps(k)} = {hcl(x, indent + 1)}\n" for k, x in sorted(v.items())) + pad + "}"
    raise TypeError(type(v))


def test_block(provider):
    variables = provider_module(provider).worst_case(sys.modules[__name__])[0]
    body = "".join(f"    {k} = {hcl(variables[k], 2)}\n" for k in sorted(variables))
    return BEGIN + "  variables {\n" + body + "  }\n" + END


def test_file_with_block(provider):
    """The provider's test file with the worst-case block written in and tofu fmt applied."""
    path = os.path.join(REPO, "infra", "providers", provider, "tests", TEST_FILE)
    text = open(path).read()
    if text.count(BEGIN) != 1 or text.count(END) != 1:
        raise SystemExit(f"cloudinit-size-check: {os.path.relpath(path, REPO)} must hold the two worst-case marker lines once each")
    a, b = text.index(BEGIN), text.index(END) + len(END)
    work = tempfile.mkdtemp(prefix="cloudinit-size-fmt.")
    try:
        tmp = os.path.join(work, TEST_FILE)
        with open(tmp, "w") as f:
            f.write(text[:a] + test_block(provider) + text[b:])
        p = subprocess.run([TOFU, "fmt", "-no-color", tmp], capture_output=True, text=True)
        if p.returncode != 0:
            raise SystemExit("cloudinit-size-check: tofu fmt of the worst-case test block failed: " + p.stderr)
        return path, text, open(tmp).read()
    finally:
        shutil.rmtree(work, ignore_errors=True)


def main():
    args = sys.argv[1:]
    if args == ["--write-fixtures"]:
        for provider in providers():
            path, _, want = test_file_with_block(provider)
            with open(path, "w") as f:
                f.write(want)
            print("wrote the worst-case variables into " + os.path.relpath(path, REPO))
        return 0
    dump = None
    if "--dump" in args:
        k = args.index("--dump")
        dump = args[k + 1]
        del args[k:k + 2]
    if len(args) != 1 or args[0] not in providers():
        sys.stderr.write(f"usage: cloudinit-size-check.py <{'|'.join(providers())}> [--dump DIR] | --write-fixtures\n")
        return 2
    path, have, want = test_file_with_block(args[0])
    if have != want:
        sys.stderr.write(f"cloudinit-size-check: the worst-case variables in {os.path.relpath(path, REPO)} are not the "
                         "ones this program measures; run scripts/cloudinit-size-check.py --write-fixtures and commit the file\n")
        return 1
    for label_, size, limit in render(args[0], dump):
        print(f"{label_}\t{size}\t{limit}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
