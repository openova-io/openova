#!/usr/bin/env python3
"""Test driver for lab test AIP-010 (spike branch only, never merged).

Runs on the CI runner, which is the single k3s node, and talks to the
gateway's ClusterIP Service directly (Cilium socket load balancing serves
ClusterIPs to host processes). It never uses a NodePort.

  probe.py keys <dir>      make two RSA keys; write <dir>/jwks.json (key A only)
  probe.py run  <out.json> run T2..T14 against the gateway and write results

Environment for `run`:
  GW_LLM     host:port of the gateway listener with token verification
  GW_VK      host:port of the gateway listener with virtual keys
  STUB       host:port of the scripted backend (for its request log)
  KEYS       directory written by `keys`
  ISSUER     issuer the gateway is configured with
  VK_KEY     the plaintext virtual key registered in the key ConfigMap
  MANIFESTS  directory holding t8-*.yaml
  NS         gateway namespace
"""
import base64
import hashlib
import http.client
import json
import os
import subprocess
import sys
import threading
import time
import traceback
import uuid

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa


def b64u(data):
    if isinstance(data, str):
        data = data.encode()
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def int_bytes(n):
    return n.to_bytes((n.bit_length() + 7) // 8, "big")


def jwk_of(pub):
    nums = pub.public_numbers()
    jwk = {"e": b64u(int_bytes(nums.e)), "kty": "RSA", "n": b64u(int_bytes(nums.n))}
    kid = b64u(hashlib.sha256(json.dumps(jwk, separators=(",", ":"), sort_keys=True).encode()).digest())
    return kid, {"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": jwk["n"], "e": jwk["e"]}


def cmd_keys(d):
    os.makedirs(d, exist_ok=True)
    keys = {}
    for name in ("a", "b"):
        k = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        with open(os.path.join(d, "key-%s.pem" % name), "wb") as f:
            f.write(k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                    serialization.NoEncryption()))
        keys[name] = jwk_of(k.public_key())
    with open(os.path.join(d, "jwks.json"), "w") as f:
        json.dump({"keys": [keys["a"][1]]}, f, separators=(",", ":"))
    with open(os.path.join(d, "kids.json"), "w") as f:
        json.dump({"a": keys["a"][0], "b": keys["b"][0]}, f)
    print("kid A", keys["a"][0])
    print("kid B (not published)", keys["b"][0])


# ---------------------------------------------------------------- tokens
def load_key(d, name):
    with open(os.path.join(d, "key-%s.pem" % name), "rb") as f:
        return serialization.load_pem_private_key(f.read(), password=None)


def sign(key, header, claims):
    h = b64u(json.dumps(header, separators=(",", ":")))
    c = b64u(json.dumps(claims, separators=(",", ":")))
    sig = key.sign(("%s.%s" % (h, c)).encode(), padding.PKCS1v15(), hashes.SHA256())
    return "%s.%s.%s" % (h, c, b64u(sig))


def agent_claims(issuer, **over):
    t = int(time.time())
    c = {
        "iss": issuer,
        "aud": ["model-gateway", "platform-tools", "catalyst-api"],
        "sub": "agent:acme/correlator",
        "typ": "agent",
        "org": "acme",
        "org_id": "acme",
        "agent": "correlator",
        "tier": "viewer",
        "realm_access": {"roles": ["catalyst-viewer"]},
        "role": "openova-user",
        "email": "correlator@acme.agents.invalid",
        "email_verified": False,
        "caller": "system:serviceaccount:spike:probe",
        "iat": t,
        "exp": t + 1800,
        "jti": str(uuid.uuid4()),
    }
    c.update(over)
    return c


# ---------------------------------------------------------------- http
def hostport(s):
    h, p = s.rsplit(":", 1)
    return h, int(p)


def request(target, method, path, body=None, headers=None, timeout=60):
    h, p = hostport(target)
    conn = http.client.HTTPConnection(h, p, timeout=timeout)
    t0 = time.time()
    try:
        if isinstance(body, (dict, list)):
            body = json.dumps(body, separators=(",", ":"), ensure_ascii=False).encode()
        elif isinstance(body, str):
            body = body.encode()
        hdrs = {"Content-Type": "application/json"}
        hdrs.update(headers or {})
        conn.request(method, path, body=body, headers=hdrs)
        resp = conn.getresponse()
        data = resp.read()
        return {"status": resp.status, "headers": [[k, v] for k, v in resp.getheaders()],
                "body": data, "elapsed": round(time.time() - t0, 3)}
    except Exception as e:  # noqa: BLE001 - every failure is evidence
        return {"status": None, "error": repr(e), "headers": [], "body": b"", "elapsed": round(time.time() - t0, 3)}
    finally:
        conn.close()


def stream_request(target, path, body, headers, timeout=120):
    """POST and read a server-sent-event stream line by line with arrival times."""
    h, p = hostport(target)
    conn = http.client.HTTPConnection(h, p, timeout=timeout)
    t0 = time.time()
    lines = []
    try:
        hdrs = {"Content-Type": "application/json", "Accept": "text/event-stream"}
        hdrs.update(headers or {})
        conn.request("POST", path, body=json.dumps(body, separators=(",", ":")).encode(), headers=hdrs)
        resp = conn.getresponse()
        status = resp.status
        rh = [[k, v] for k, v in resp.getheaders()]
        while True:
            line = resp.readline()
            if not line:
                break
            lines.append((time.time(), line.decode(errors="replace").rstrip("\r\n")))
        return {"status": status, "headers": rh, "lines": lines, "t0": t0, "elapsed": round(time.time() - t0, 3)}
    except Exception as e:  # noqa: BLE001
        return {"status": None, "error": repr(e), "lines": lines, "t0": t0, "elapsed": round(time.time() - t0, 3)}
    finally:
        conn.close()


def jbody(r):
    try:
        return json.loads(r["body"] or b"null")
    except ValueError:
        return None


def short(b, n=300):
    if isinstance(b, bytes):
        b = b.decode(errors="replace")
    return b if len(b) <= n else b[:n] + "...(%d bytes)" % len(b)


# ---------------------------------------------------------------- stub log
class Stub:
    def __init__(self, target):
        self.target = target

    def last(self):
        r = request(self.target, "GET", "/_spike/log?after=999999999")
        return (jbody(r) or {}).get("last", 0)

    def find(self, req_id, after=0, wait=5.0):
        deadline = time.time() + wait
        while True:
            r = request(self.target, "GET", "/_spike/log?after=%d" % after)
            recs = (jbody(r) or {}).get("records", [])
            hit = [x for x in recs if any(k.lower() == "x-spike-req" and v == req_id for k, v in x["headers"])]
            if hit or time.time() > deadline:
                return hit[-1] if hit else None
            time.sleep(0.3)

    @staticmethod
    def header(rec, name):
        return [v for k, v in (rec or {}).get("headers", []) if k.lower() == name.lower()]

    @staticmethod
    def body(rec):
        return base64.b64decode((rec or {}).get("body_b64", "") or b"")


# ---------------------------------------------------------------- tests
class Run:
    def __init__(self):
        self.gw = os.environ["GW_LLM"]
        self.vk = os.environ["GW_VK"]
        self.stub = Stub(os.environ["STUB"])
        self.keys = os.environ["KEYS"]
        self.issuer = os.environ["ISSUER"]
        self.vk_key = os.environ["VK_KEY"]
        self.manifests = os.environ["MANIFESTS"]
        self.ns = os.environ.get("NS", "model-gateway")
        self.ka = load_key(self.keys, "a")
        self.kb = load_key(self.keys, "b")
        with open(os.path.join(self.keys, "kids.json")) as f:
            self.kids = json.load(f)
        self.results = []
        self.values = {}
        self.obs = {}

    def tok(self, key=None, header=None, **claims):
        key = key or self.ka
        hdr = {"alg": "RS256", "typ": "JWT", "kid": self.kids["a"]}
        if header is not None:
            hdr = header
        return sign(key, hdr, agent_claims(self.issuer, **claims))

    def auth(self, token=None):
        return {"Authorization": "Bearer " + (token or self.tok())}

    def add(self, tid, result, evidence, detail=None, informative=False):
        print("[%s] %s - %s" % (tid, result, evidence), flush=True)
        self.results.append({"id": tid, "result": result, "evidence": evidence, "informative": informative,
                             "detail": detail or {}})

    def chat(self, model="conformance-echo", target=None, mode=None, extra=None, headers=None, token=None,
             body=None, timeout=60, path="/v1/chat/completions"):
        rid = str(uuid.uuid4())
        h = {"x-spike-req": rid}
        if token is not False:
            h.update(self.auth(token))
        if mode:
            h["x-spike-mode"] = mode
        h.update(headers or {})
        if body is None:
            body = {"model": model, "messages": [{"role": "user", "content": "hello spike"}]}
            body.update(extra or {})
        before = self.stub.last()
        r = request(target or self.gw, "POST", path, body=body, headers=h, timeout=timeout)
        r["rid"] = rid
        r["stub_before"] = before
        return r

    def rec_for(self, r, wait=5.0):
        return self.stub.find(r["rid"], after=r["stub_before"], wait=wait)

    # ---------- readiness
    def wait_ready(self, limit=300):
        t0 = time.time()
        seen = []
        while time.time() - t0 < limit:
            r = self.chat()
            seen.append(r["status"])
            if r["status"] == 200:
                self.obs["readyAfterSeconds"] = round(time.time() - t0, 1)
                self.obs["readyStatusesSeen"] = sorted(set(str(s) for s in seen))
                print("gateway answers 200 after %.1fs" % (time.time() - t0), flush=True)
                return True
            time.sleep(3)
        self.obs["readyStatusesSeen"] = sorted(set(str(s) for s in seen))
        self.obs["lastNotReadyResponse"] = short(r.get("body") or r.get("error", ""), 600)
        print("gateway never answered 200 in %ds; statuses %s; last %s" % (
            limit, self.obs["readyStatusesSeen"], self.obs["lastNotReadyResponse"]), flush=True)
        return False

    # ---------- T2
    def t2(self):
        tool_a = ('{"type":"function","function":{"name":"get_weather","description":"Weather for a city '
                  '— «quotes» & <tags>","parameters":{"type":"object","properties":{"city":'
                  '{"type":"string","description":"City name, e.g. Muscat"},"unit":{"type":"string","enum":["c","f"]},'
                  '"days":{"type":"integer","minimum":1,"maximum":14}},"required":["city"],'
                  '"additionalProperties":false},"strict":true}}')
        tool_b = ('{"type":"function","function":{"name":"lookup_ticket","description":"Look up a ticket",'
                  '"parameters":{"type":"object","properties":{"id":{"type":"string","pattern":"^[A-Z]{2}-[0-9]+$"},'
                  '"fields":{"type":"array","items":{"type":"string"}},"filter":{"type":"object","properties":'
                  '{"status":{"type":"string"}}}},"required":["id"]}}}')
        body = ('{"model":"conformance-echo","messages":[{"role":"user","content":"call both tools"}],'
                '"tools":[' + tool_a + ',' + tool_b + '],"tool_choice":"auto"}')
        r = self.chat(mode="tools2", body=body.encode())
        rec = self.rec_for(r)
        got = self.stub.body(rec)
        a_raw = tool_a.encode() in got
        b_raw = tool_b.encode() in got
        sem = {}
        try:
            recv = json.loads(got, object_pairs_hook=lambda kv: kv)
            sent = json.loads(body, object_pairs_hook=lambda kv: kv)
            rtools = dict(recv).get("tools")
            stools = dict(sent).get("tools")
            sem["ordered_equal"] = rtools == stools
            sem["unordered_equal"] = json.loads(json.dumps(dict(json.loads(got)).get("tools"))) == json.loads(
                json.dumps(dict(json.loads(body)).get("tools")))
            if not (a_raw and b_raw):
                sem["received_tools_compact"] = short(json.dumps(json.loads(got).get("tools"), separators=(",", ":"),
                                                                 ensure_ascii=False), 1500)
        except Exception as e:  # noqa: BLE001
            sem["error"] = repr(e)
        expected = [
            {"id": "call_spike_A1", "type": "function", "function": {"name": "get_weather",
                                                                     "arguments": "{\"city\":\"Muscat\",\"unit\":\"c\",\"days\":3}"}},
            {"id": "call_spike_B2", "type": "function", "function": {"name": "lookup_ticket",
                                                                     "arguments": "{\"id\":\"OM-42\",\"fields\":[\"title\",\"état\"],\"filter\":{\"status\":\"open \\\"q\\\"\"}}"}},
        ]
        jb = jbody(r) or {}
        tc = None
        try:
            tc = jb["choices"][0]["message"]["tool_calls"]
            tc_cmp = [{"id": c["id"], "type": c.get("type"), "function": {"name": c["function"]["name"],
                                                                         "arguments": c["function"]["arguments"]}} for c in tc]
        except Exception:  # noqa: BLE001
            tc_cmp = None
        calls_equal = tc_cmp == expected
        ok = r["status"] == 200 and rec is not None and a_raw and b_raw and calls_equal
        self.add("T2", "pass" if ok else "fail",
                 "POST /v1/chat/completions with two tools (compact JSON): status %s; tool A bytes found in backend body: %s; "
                 "tool B: %s; tool_calls returned to caller equal to backend's (ids, names, argument strings): %s"
                 % (r["status"], a_raw, b_raw, calls_equal),
                 {"semantic": sem, "caller_tool_calls": tc, "backend_path": (rec or {}).get("path"),
                  "response": short(r["body"], 1500) if not ok else None, "finish_reason":
                      (jb.get("choices") or [{}])[0].get("finish_reason") if isinstance(jb, dict) else None})

    # ---------- T3
    def t3(self):
        rid = str(uuid.uuid4())
        before = self.stub.last()
        body = {"model": "conformance-echo", "stream": True,
                "messages": [{"role": "user", "content": "slow tool call"}],
                "tools": [{"type": "function", "function": {"name": "lookup", "parameters": {
                    "type": "object", "properties": {"q": {"type": "string"}}}}}]}
        h = {"x-spike-req": rid, "x-spike-mode": "slow-args"}
        h.update(self.auth())
        s = stream_request(self.gw, "/v1/chat/completions", body, h, timeout=90)
        rec = self.stub.find(rid, after=before, wait=5)
        sent = (rec or {}).get("sent", {})
        acc = ""
        seen = {}
        for t, line in s.get("lines", []):
            if not line.startswith("data:"):
                continue
            data = line[5:].strip()
            if data == "[DONE]":
                seen.setdefault("DONE", t)
                continue
            try:
                ev = json.loads(data)
            except ValueError:
                continue
            for ch in ev.get("choices") or []:
                for tcall in (ch.get("delta") or {}).get("tool_calls") or []:
                    acc += ((tcall.get("function") or {}).get("arguments") or "")
            for mark in ("P1", "P2", "P3"):
                if mark in acc and mark not in seen:
                    seen[mark] = t
        delays = {m: round(seen[m] - sent[m], 3) for m in ("P1", "P2", "P3") if m in seen and m in sent}
        ok = s.get("status") == 200 and "P1" in delays and delays["P1"] < 1.0
        self.add("T3", "pass" if ok else "fail",
                 "streamed tool call, argument sent in 3 chunks 5 s apart: status %s; caller receive delay after backend "
                 "send, seconds: %s; arguments assembled by caller: %s" % (s.get("status"), delays, json.dumps(acc)),
                 {"lines": len(s.get("lines", [])), "error": s.get("error"),
                  "first_lines": [l for _, l in s.get("lines", [])[:3]]})

    # ---------- T4 and T12
    def t4(self):
        now = int(time.time())
        cases = [
            ("valid", self.tok(), 200),
            ("no token", False, 401),
            ("bad signature (signed by an unpublished key, kid of the published key)",
             self.tok(key=self.kb), 401),
            ("unknown kid (signed by the published key)",
             self.tok(header={"alg": "RS256", "typ": "JWT", "kid": "no-such-kid-" + uuid.uuid4().hex[:8]}), 401),
            ("unknown kid and unpublished key",
             self.tok(key=self.kb, header={"alg": "RS256", "typ": "JWT", "kid": self.kids["b"]}), 401),
            ("wrong issuer", self.tok(iss="https://console.other.invalid"), 401),
            ("expired", self.tok(iat=now - 3600, exp=now - 600), 401),
            ("aud without model-gateway", self.tok(aud=["platform-tools", "catalyst-api"]), 401),
            ("aud as a single string model-gateway", self.tok(aud="model-gateway"), 200),
        ]
        rows = []
        ok = True
        for name, token, want in cases:
            r = self.chat(token=token)
            rows.append({"case": name, "want": want, "got": r["status"], "body": short(r["body"], 160)})
            if name.startswith("aud as a single"):
                continue  # informative
            ok = ok and r["status"] == want
        self.add("T4", "pass" if ok else "fail",
                 "; ".join("%s -> %s" % (x["case"], x["got"]) for x in rows), {"cases": rows})
        r = self.chat(token=self.tok(header={"alg": "RS256", "typ": "JWT"}))
        self.values["kidlessAccepted"] = r["status"] == 200
        self.add("T12", "info", "token signed by the published key with no kid header -> %s (%s)" % (
            r["status"], "accepted" if r["status"] == 200 else "refused"), {"body": short(r["body"], 200)},
                 informative=True)

    # ---------- T5
    def t5(self):
        forged = {"x-openova-organization": "forged-org", "X-OpenOva-Principal": "forged-principal",
                  "x-openova-principal-kind": "forged-kind", "x-openova-alias": "forged-alias",
                  "x-request-id": "spike-req-0123456789"}
        r = self.chat(headers=forged)
        rec = self.rec_for(r)
        org = Stub.header(rec, "x-openova-organization")
        pr = Stub.header(rec, "x-openova-principal")
        all_vals = [v for _, v in (rec or {}).get("headers", [])]
        leaked = [v for v in all_vals if "forged" in v]
        ok = r["status"] == 200 and org == ["acme"] and pr == ["correlator"] and not leaked
        detail = {
            "x-openova-organization": org, "x-openova-principal": pr,
            "x-openova-principal-kind": Stub.header(rec, "x-openova-principal-kind"),
            "x-openova-alias": Stub.header(rec, "x-openova-alias"),
            "x-request-id at backend (caller sent spike-req-0123456789)": Stub.header(rec, "x-request-id"),
            "authorization reaches backend": bool(Stub.header(rec, "authorization")),
            "host at backend": Stub.header(rec, "host"),
            "response x-request-id": [v for k, v in r["headers"] if k.lower() == "x-request-id"],
            "all backend header names": sorted(set(k.lower() for k, _ in (rec or {}).get("headers", []))),
        }
        r2 = self.chat()
        rec2 = self.rec_for(r2)
        detail["x-request-id at backend when caller sent none"] = Stub.header(rec2, "x-request-id")
        detail["response x-request-id when caller sent none"] = [v for k, v in r2["headers"] if k.lower() == "x-request-id"]
        self.obs["backendHost"] = Stub.header(rec, "host")
        self.obs["tokenForwardedToBackend"] = detail["authorization reaches backend"]
        self.add("T5", "pass" if ok else "fail",
                 "valid token + caller-forged x-openova-* headers: status %s; backend saw organization %s, principal %s; "
                 "forged values reaching backend: %s" % (r["status"], org, pr, leaked), detail)

    # ---------- T6
    def t6(self):
        r = self.chat(model="agent-default")
        rec = self.rec_for(r)
        got_model = None
        try:
            got_model = json.loads(self.stub.body(rec)).get("model")
        except Exception:  # noqa: BLE001
            pass
        r2 = self.chat(model="agent-fast")
        rec2 = self.rec_for(r2)
        got2 = None
        try:
            got2 = json.loads(self.stub.body(rec2)).get("model")
        except Exception:  # noqa: BLE001
            pass
        r3 = self.chat(model="no-such-alias")
        jb3 = jbody(r3)
        resp_model = (jbody(r) or {}).get("model") if isinstance(jbody(r), dict) else None
        ok = r["status"] == 200 and got_model == "sim-default" and r3["status"] == 404
        self.add("T6", "pass" if ok else "fail",
                 "model agent-default -> status %s, backend received model %r; agent-fast -> backend received %r; "
                 "unknown model -> status %s body %s" % (r["status"], got_model, got2, r3["status"], short(r3["body"], 200)),
                 {"model in response to caller": resp_model, "unknown_body": jb3,
                  "backend path": (rec or {}).get("path")})

    # ---------- T7 (background)
    def t7_start(self):
        self.t7 = {}

        def main():
            r = self.chat(model="agent-default", mode="silent-600", timeout=1900)
            rec = self.rec_for(r, wait=10)
            self.t7["main"] = {"status": r["status"], "elapsed": r["elapsed"], "error": r.get("error"),
                               "body": short(r["body"], 300),
                               "backend_silent_seconds": round((rec or {}).get("silent_end", 0) - (rec or {}).get("silent_start", 0), 1)}

        def streamed():
            rid = str(uuid.uuid4())
            h = {"x-spike-req": rid, "x-spike-mode": "stream-idle-600"}
            h.update(self.auth(self.tok(exp=int(time.time()) + 3600)))
            s = stream_request(self.gw, "/v1/chat/completions",
                               {"model": "agent-default", "stream": True, "messages": [{"role": "user", "content": "idle"}]},
                               h, timeout=1900)
            text = "\n".join(l for _, l in s.get("lines", []))
            self.t7["stream"] = {"status": s.get("status"), "elapsed": s.get("elapsed"), "error": s.get("error"),
                                 "got_after_idle": "after-idle" in text, "got_done": "[DONE]" in text}

        def control():
            r = self.chat(model="timeout-probe", mode="silent-20", timeout=120)
            self.t7["control"] = {"status": r["status"], "elapsed": r["elapsed"], "body": short(r["body"], 200),
                                  "error": r.get("error")}

        self.t7_threads = [threading.Thread(target=f, daemon=True) for f in (main, streamed, control)]
        for t in self.t7_threads:
            t.start()
        self.t7_started = time.time()

    def t7_finish(self):
        for t in self.t7_threads:
            t.join(timeout=max(1, 1500 - (time.time() - self.t7_started)))
        m = self.t7.get("main", {})
        ok = m.get("status") == 200 and (m.get("elapsed") or 0) >= 595
        self.add("T7", "pass" if ok else "fail",
                 "backend silent 600 s with request timeouts 1800 s: status %s after %ss (backend slept %ss)"
                 % (m.get("status"), m.get("elapsed"), m.get("backend_silent_seconds")),
                 {"main": m, "streamed_idle_600_informative": self.t7.get("stream"),
                  "control_5s_backend_timeout_informative": self.t7.get("control")})

    # ---------- T8
    def proxy_pods(self):
        out = subprocess.run(["kubectl", "-n", self.ns, "get", "pods", "-l",
                              "gateway.networking.k8s.io/gateway-name=model-gateway", "-o", "json"],
                             capture_output=True, text=True)
        try:
            items = json.loads(out.stdout)["items"]
        except Exception:  # noqa: BLE001
            return []
        return sorted((p["metadata"]["name"], p["metadata"]["uid"],
                       sum(c.get("restartCount", 0) for c in p.get("status", {}).get("containerStatuses", [])))
                      for p in items)

    def whoami(self, model):
        r = self.chat(model=model, mode="whoami")
        try:
            content = json.loads(jbody(r)["choices"][0]["message"]["content"])
            return r["status"], content.get("instance"), content.get("host")
        except Exception:  # noqa: BLE001
            return r["status"], None, None

    def t8(self):
        # meter-simulation selects the backend pods of instance model-stub, meter-selfhosted those of
        # model-stub-b; the backend names its instance in every answer.
        before_pods = self.proxy_pods()
        st, inst0, host0 = self.whoami("agent-fast")
        path = os.path.join(self.manifests, "t8-agent-fast-selfhosted.yaml")
        ap = subprocess.run(["kubectl", "apply", "-f", path], capture_output=True, text=True)
        t_apply = time.time()
        switched = None
        polls = []
        while time.time() - t_apply < 120:
            s, inst, _ = self.whoami("agent-fast")
            polls.append([round(time.time() - t_apply, 1), s, inst])
            if inst == "model-stub-b":
                switched = round(time.time() - t_apply, 1)
                break
            time.sleep(1)
        after_pods = self.proxy_pods()
        same = before_pods == after_pods and bool(before_pods)
        ok = inst0 == "model-stub" and switched is not None and switched <= 60 and same
        self.add("T8", "pass" if ok else "fail",
                 "kubectl apply moved alias agent-fast from Service meter-simulation (instance %s) to meter-selfhosted: "
                 "answers came from instance model-stub-b %ss after the apply returned; proxy pods unchanged "
                 "(name, uid, restarts): %s" % (inst0, switched, same),
                 {"apply": (ap.stdout + ap.stderr).strip(), "polls_head": polls[:3], "polls_tail": polls[-3:],
                  "pods_before": before_pods, "pods_after": after_pods, "host_seen_by_backend": host0})

    # ---------- T9
    def t9(self):
        rid = str(uuid.uuid4())
        before = self.stub.last()
        body = {"model": "conformance-echo", "max_tokens": 256,
                "messages": [{"role": "user", "content": "use the tool"}],
                "tools": [{"name": "get_weather", "description": "Weather for a city",
                           "input_schema": {"type": "object", "properties": {"city": {"type": "string"}},
                                            "required": ["city"]}}]}
        h = {"x-spike-req": rid, "x-spike-mode": "tools1", "anthropic-version": "2023-06-01"}
        h.update(self.auth())
        r = request(self.gw, "POST", "/v1/messages", body=body, headers=h)
        rec = self.stub.find(rid, after=before, wait=5)
        jb = jbody(r) or {}
        blocks = jb.get("content") if isinstance(jb, dict) else None
        tu = [b for b in (blocks or []) if isinstance(b, dict) and b.get("type") == "tool_use"]
        ok = r["status"] == 200 and len(tu) == 1 and tu[0].get("name") == "get_weather" and tu[0].get("input") == {"city": "Muscat"}
        self.values["messagesRoute"] = "pass" if ok else "fail"
        backend_tools = None
        try:
            backend_tools = json.loads(self.stub.body(rec)).get("tools")
        except Exception:  # noqa: BLE001
            pass
        self.add("T9", "pass" if ok else "fail",
                 "POST /v1/messages with one tool to a Chat Completions backend: status %s; tool_use blocks %s; "
                 "backend path %s" % (r["status"], json.dumps(tu), (rec or {}).get("path")),
                 {"response": short(r["body"], 800), "backend_tools": backend_tools,
                  "stop_reason": jb.get("stop_reason") if isinstance(jb, dict) else None}, informative=True)

    # ---------- T10
    def t10(self):
        r = self.chat(target=self.vk, token=self.vk_key)
        rec = self.rec_for(r)
        r_bad = self.chat(target=self.vk, token="ovk_" + "A" * 40)
        r_jwt = self.chat(target=self.vk)
        org = Stub.header(rec, "x-openova-organization")
        pr = Stub.header(rec, "x-openova-principal")
        kind = Stub.header(rec, "x-openova-principal-kind")
        ok = r["status"] == 200 and org == ["acme"] and pr == ["billing-app"] and r_bad["status"] == 401
        self.add("T10", "pass" if ok else "fail",
                 "virtual key from a labelled ConfigMap: status %s, backend saw organization %s principal %s kind %s; "
                 "unknown key -> %s; agent token on the key listener -> %s"
                 % (r["status"], org, pr, kind, r_bad["status"], r_jwt["status"]),
                 {"body": short(r["body"], 300), "bad_body": short(r_bad["body"], 200)}, informative=True)

    # ---------- T11
    def t11(self):
        big = "a" * (8 * 1024 * 1024)
        body = json.dumps({"model": "conformance-echo", "messages": [{"role": "user", "content": big}]},
                          separators=(",", ":")).encode()
        r = self.chat(mode="size", body=body, timeout=120)
        rec = self.rec_for(r, wait=10)
        ok = r["status"] == 200 and rec is not None and rec.get("content_len") == len(big)
        self.add("T11", "pass" if ok else "fail",
                 "request body of %d bytes (user content %d bytes): status %s; backend received a body of %s bytes "
                 "whose user content is %s bytes (the body is re-encoded with the alias renamed to %r)"
                 % (len(body), len(big), r["status"], (rec or {}).get("body_len"), (rec or {}).get("content_len"),
                    (rec or {}).get("model_received")),
                 {"response": short(r["body"], 300), "error": r.get("error")}, informative=True)

    # ---------- T13 task routes
    def t13(self):
        out = {}
        forged = {"x-openova-organization": "forged-org", "x-openova-principal": "forged-principal"}

        def check(name, path, body, ctype="application/json", raw=None):
            rid = str(uuid.uuid4())
            before = self.stub.last()
            h = {"x-spike-req": rid, "Content-Type": ctype}
            h.update(forged)
            noauth = request(self.gw, "POST", path, body=raw if raw is not None else body, headers=dict(h))
            h.update(self.auth())
            r = request(self.gw, "POST", path, body=raw if raw is not None else body, headers=h)
            rec = self.stub.find(rid, after=before, wait=4)
            recs_all = [rec] if rec else []
            leaked = [v for x in recs_all for _, v in x.get("headers", []) if "forged" in v]
            routed = r["status"] == 200 and rec is not None
            ok = routed and noauth["status"] == 401 and Stub.header(rec, "x-openova-organization") == ["acme"] and not leaked
            out[name] = {"path": path, "no_token": noauth["status"], "with_token": r["status"],
                         "backend_path": (rec or {}).get("path"),
                         "backend_organization": Stub.header(rec, "x-openova-organization"),
                         "forged_reached_backend": bool(leaked), "result": "pass" if ok else "fail",
                         "body": short(r["body"], 200)}
            return ok

        e = check("embeddings", "/v1/embeddings", {"model": "task-embedding", "input": "hello"})
        rr = check("rerank", "/v1/rerank", {"model": "task-rerank", "query": "q", "documents": ["a", "b"]})
        if not rr:
            out["rerank_v1"] = out.pop("rerank")
            rr = check("rerank", "/v2/rerank", {"model": "task-rerank", "query": "q", "documents": ["a", "b"]})
        boundary = "spikeboundary%s" % uuid.uuid4().hex[:8]
        wav = (b"RIFF$\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80>\x00\x00\x00}\x00\x00\x02\x00\x10\x00"
               b"data\x00\x00\x00\x00")
        mp = (("--%s\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ntask-transcription\r\n" % boundary).encode()
              + ("--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\n"
                 % boundary).encode() + wav + ("\r\n--%s--\r\n" % boundary).encode())
        t = check("transcription", "/v1/audio/transcriptions", None, ctype="multipart/form-data; boundary=" + boundary, raw=mp)
        self.values["taskRoutes"] = {k: v["result"] for k, v in out.items() if k in ("embeddings", "rerank", "transcription")}
        self.add("T13", "pass" if (e and rr and t) else "fail",
                 "task paths through the gateway with credential check and header removal: %s"
                 % ", ".join("%s %s (no token %s, token %s, backend %s)" % (k, v["result"], v["no_token"], v["with_token"],
                                                                         v["backend_path"]) for k, v in out.items()),
                 out, informative=True)

    # ---------- T14 owned_by
    def t14(self):
        r = request(self.gw, "GET", "/v1/models", headers=self.auth())
        jb = jbody(r) or {}
        data = jb.get("data") if isinstance(jb, dict) else None
        owned = {m.get("id"): m.get("owned_by") for m in (data or []) if isinstance(m, dict)}
        noauth = request(self.gw, "GET", "/v1/models")
        # owned-final: Custom provider, providerOverride "simulation", alias renamed by finalTransformations.
        # owned-plain: Custom provider, providerOverride "stub", no rename.
        rf = self.chat(model="owned-final", mode="size")
        recf = self.rec_for(rf)
        rp = self.chat(model="owned-plain", mode="size")
        recp = self.rec_for(rp)
        final_model = (recf or {}).get("model_received")
        ok = owned.get("owned-final") == "simulation" and final_model == "sim-default" and rf["status"] == 200
        self.values["ownedBy"] = {
            "settable": ok,
            "how": "custom.providerOverride on a Custom-provider AgentgatewayModel; the alias rename must then be "
                   "a policies.finalTransformations entry (a policies.transformations rename of the model field "
                   "drops the provider from discovery and owned_by falls back to openai)",
            "observed": owned}
        self.add("T14", "pass" if ok else "fail",
                 "GET /v1/models: status %s; owned_by per id %s; without token %s. owned-final (providerOverride "
                 "simulation, rename in finalTransformations): status %s, backend received model %r; owned-plain "
                 "(providerOverride stub, no rename): status %s, backend received model %r"
                 % (r["status"], json.dumps(owned, sort_keys=True), noauth["status"], rf["status"], final_model,
                    rp["status"], (recp or {}).get("model_received")),
                 {"response": short(r["body"], 1500)}, informative=True)

    # ---------- informative: how the meter can tell the backend; request identifiers
    def meter_routing(self):
        forged = {"x-openova-backend": "forged-backend"}
        r = self.chat(model="meter-routing-probe", headers=forged)
        rec = self.rec_for(r)
        rv = self.chat(target=self.vk, token=self.vk_key, headers={"x-request-id": "spike-vk-0123456789"})
        recv = self.rec_for(rv)
        rv2 = self.chat(target=self.vk, token=self.vk_key)
        recv2 = self.rec_for(rv2)
        out = {
            "per_model_header": {"status": r["status"], "x-openova-backend at backend": Stub.header(rec, "x-openova-backend"),
                                 "host at backend": Stub.header(rec, "host")},
            "host_rewrite_auto_on_vk_listener": {"status": rv["status"], "host at backend": Stub.header(recv, "host")},
            "x_request_id_on_vk_listener": {
                "caller sent valid id -> backend": Stub.header(recv, "x-request-id"),
                "caller sent valid id -> response": [v for k, v in rv["headers"] if k.lower() == "x-request-id"],
                "caller sent none -> backend": Stub.header(recv2, "x-request-id"),
                "caller sent none -> response": [v for k, v in rv2["headers"] if k.lower() == "x-request-id"]},
        }
        self.values["meterRouting"] = out
        self.obs["meterRouting"] = out
        ok = r["status"] == 200 and Stub.header(rec, "x-openova-backend") == ["simulation"]
        self.add("T15", "pass" if ok else "fail",
                 "informative, for the meter: a per-model request header set on the AgentgatewayModel reaches the "
                 "backend as %s (caller forged it: replaced=%s); Host at the backend by default %s, with "
                 "hostRewrite Auto %s; x-request-id generated when absent: backend %s, response %s"
                 % (Stub.header(rec, "x-openova-backend"), "forged-backend" not in str(Stub.header(rec, "x-openova-backend")),
                    Stub.header(rec, "host"), Stub.header(recv, "host"), Stub.header(recv2, "x-request-id"),
                    out["x_request_id_on_vk_listener"]["caller sent none -> response"]),
                 out, informative=True)


def cmd_run(out_path):
    run = Run()
    ready = run.wait_ready()
    steps = []
    if ready:
        run.t7_start()
        steps = [run.t2, run.t3, run.t4, run.t5, run.t6, run.t9, run.t10, run.t11, run.t13, run.t14,
                 run.meter_routing, run.t8]
    for step in steps:
        try:
            step()
        except Exception:  # noqa: BLE001
            run.add(step.__name__.upper(), "fail", "driver error: " + traceback.format_exc()[-600:])
        with open(out_path, "w") as f:
            json.dump({"tests": run.results, "values": run.values, "observations": run.obs}, f, indent=1, default=str)
    if ready:
        print("waiting for T7 (600 s silent backend) ...", flush=True)
        run.t7_finish()
    else:
        for tid in ("T2", "T3", "T4", "T5", "T6", "T7", "T8"):
            run.add(tid, "fail", "not run: the gateway never answered 200 for a valid request")
    with open(out_path, "w") as f:
        json.dump({"tests": run.results, "values": run.values, "observations": run.obs}, f, indent=1, default=str)


if __name__ == "__main__":
    if sys.argv[1] == "keys":
        cmd_keys(sys.argv[2])
    elif sys.argv[1] == "run":
        cmd_run(sys.argv[2])
    else:
        sys.exit("usage: probe.py keys <dir> | run <out.json>")
