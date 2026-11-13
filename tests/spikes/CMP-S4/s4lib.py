"""Spike CMP-S4 helpers: a minimal Kubernetes API client (direct TLS with the
k3s admin kubeconfig, no third-party package) and discovery of the report
kinds that the scanner and the policy engine write.

Lab test only. Never merged.
"""
import base64
import http.client
import json
import os
import ssl
import tempfile
import urllib.parse

import yaml

# API groups whose objects count as "report objects" for this lab test.
REPORT_GROUPS = (
    "aquasecurity.github.io",  # scanner (trivy-operator)
    "wgpolicyk8s.io",          # policy engine (kyverno) policy reports
    "reports.kyverno.io",      # policy engine ephemeral reports
    "openreports.io",          # policy engine, when OpenReports is switched on
)


class Api:
    def __init__(self, kubeconfig):
        kc = yaml.safe_load(open(kubeconfig))
        cl = kc["clusters"][0]["cluster"]
        us = kc["users"][0]["user"]
        u = urllib.parse.urlparse(cl["server"])
        self.host, self.port = u.hostname, u.port or 443
        d = tempfile.mkdtemp(prefix="cmps4-")

        def w(name, b64):
            p = os.path.join(d, name)
            with open(p, "wb") as f:
                f.write(base64.b64decode(b64))
            os.chmod(p, 0o600)
            return p

        ca = w("ca.crt", cl["certificate-authority-data"])
        crt = w("client.crt", us["client-certificate-data"])
        key = w("client.key", us["client-key-data"])
        ctx = ssl.create_default_context(cafile=ca)
        ctx.load_cert_chain(crt, key)
        ctx.check_hostname = False  # 127.0.0.1; the chain is still verified
        self.ctx = ctx

    def conn(self, timeout=120):
        return http.client.HTTPSConnection(self.host, self.port, context=self.ctx, timeout=timeout)

    def req(self, method, path, body=None, ctype="application/json", conn=None):
        """Returns (status, raw bytes). Reuses `conn` when given (keep-alive)."""
        data = body
        if body is not None and not isinstance(body, (bytes, bytearray)):
            data = json.dumps(body, separators=(",", ":")).encode()
        headers = {"Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = ctype
        own = conn is None
        c = conn or self.conn()
        try:
            c.request(method, path, body=data, headers=headers)
            r = c.getresponse()
            raw = r.read()
            return r.status, raw
        finally:
            if own:
                c.close()

    def get_json(self, path):
        st, raw = self.req("GET", path)
        if st != 200:
            raise RuntimeError(f"GET {path}: HTTP {st}: {raw[:300]!r}")
        return json.loads(raw)

    def ensure_namespace(self, name, labels=None):
        body = {"apiVersion": "v1", "kind": "Namespace",
                "metadata": {"name": name, "labels": labels or {}}}
        st, raw = self.req("POST", "/api/v1/namespaces", body)
        if st not in (201, 409):
            raise RuntimeError(f"create namespace {name}: HTTP {st}: {raw[:300]!r}")


def report_resources(api):
    """[(group, version, plural, namespaced, kind)] for every report kind served."""
    out = []
    groups = api.get_json("/apis").get("groups", [])
    for g in groups:
        if g["name"] not in REPORT_GROUPS:
            continue
        gv = g["preferredVersion"]["groupVersion"]
        for r in api.get_json(f"/apis/{gv}").get("resources", []):
            if "/" in r["name"]:
                continue
            out.append((g["name"], gv.split("/", 1)[1], r["name"], r["namespaced"], r["kind"]))
    return sorted(out)


def list_path(group, version, plural):
    return f"/apis/{group}/{version}/{plural}"


def item_path(group, version, plural, namespaced, ns, name=""):
    base = f"/apis/{group}/{version}"
    p = f"{base}/namespaces/{ns}/{plural}" if namespaced else f"{base}/{plural}"
    return f"{p}/{name}" if name else p


def list_all(api, path, label_selector=None, limit=500):
    """Paginated list; returns items."""
    items, cont = [], None
    while True:
        q = {"limit": str(limit)}
        if cont:
            q["continue"] = cont
        if label_selector:
            q["labelSelector"] = label_selector
        doc = api.get_json(path + "?" + urllib.parse.urlencode(q))
        items.extend(doc.get("items", []))
        cont = (doc.get("metadata") or {}).get("continue")
        if not cont:
            return items
