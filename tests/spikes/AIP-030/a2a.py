#!/usr/bin/env python3
"""A2A 1.0 JSON-RPC caller for lab test AIP-030 (throwaway, spike branch only).

Modes: card (GET the agent card), send (SendMessage, then GetTask until a
terminal state), start (SendMessage with returnImmediately), get, cancel.
Prints one JSON object: http status, rpc error, task id, context id, state,
the texts and data parts found in the answer.
"""
import argparse
import json
import sys
import time
import urllib.error
import urllib.request
import uuid

p = argparse.ArgumentParser()
p.add_argument("url")
p.add_argument("--token", default="")
p.add_argument("--text", default="")
p.add_argument("--context", default="")
p.add_argument("--task", default="")
p.add_argument("--mode", choices=["card", "send", "start", "get", "cancel"], default="send")
p.add_argument("--timeout", type=float, default=600)
p.add_argument("--user", default="spike-caller")
a = p.parse_args()

TERMINAL = {"TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED",
            "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED"}


def http(method, url, body=None, timeout=60):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Accept", "application/json")
    req.add_header("A2A-Version", "1.0")
    req.add_header("X-User-Id", a.user)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    if a.token:
        req.add_header("Authorization", "Bearer " + a.token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read().decode(errors="replace")
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw[:1500]}
    except Exception as e:
        return 0, {"exception": repr(e)[:500]}


def rpc(method, params, timeout=60):
    return http("POST", a.url, {"jsonrpc": "2.0", "id": str(uuid.uuid4()), "method": method, "params": params}, timeout)


def walk(o, key, acc):
    if isinstance(o, dict):
        for k, v in o.items():
            if k == key:
                acc.append(v)
            walk(v, key, acc)
    elif isinstance(o, list):
        for v in o:
            walk(v, key, acc)
    return acc


def summary(status, body, extra=None):
    res = (body or {}).get("result") if isinstance(body, dict) else None
    task = None
    if isinstance(res, dict):
        task = res.get("task") or (res if "status" in res and "id" in res else None)
    states = [s for s in walk(body, "state", []) if isinstance(s, str) and s.startswith("TASK_STATE_")]
    out = {
        "http": status,
        "rpc_error": body.get("error") if isinstance(body, dict) else None,
        "task": (task or {}).get("id") or "",
        "contextId": (task or {}).get("contextId") or next(iter(walk(res, "contextId", [])), ""),
        "state": ((task or {}).get("status") or {}).get("state") or (states[-1] if states else ""),
        "texts": [t for t in walk(res, "text", []) if isinstance(t, str)][-6:],
        "data": walk(res, "data", [])[-3:],
        "raw": json.dumps(body)[:2500],
    }
    if extra:
        out.update(extra)
    return out


def message(text):
    m = {"messageId": str(uuid.uuid4()), "role": "ROLE_USER", "parts": [{"text": text}]}
    if a.context:
        m["contextId"] = a.context
    return m


t0 = time.time()
if a.mode == "card":
    url = a.url.rstrip("/") + "/.well-known/agent-card.json"
    st, body = http("GET", url, timeout=30)
    print(json.dumps({"http": st, "url": url, "card_url": (body or {}).get("url") if isinstance(body, dict) else None,
                      "interfaces": (body or {}).get("supportedInterfaces") if isinstance(body, dict) else None,
                      "raw": json.dumps(body)[:1500]}))
    sys.exit(0)
if a.mode in ("get", "cancel"):
    st, body = rpc("GetTask" if a.mode == "get" else "CancelTask", {"id": a.task}, timeout=60)
    print(json.dumps(summary(st, body, {"seconds": round(time.time() - t0, 1)})))
    sys.exit(0)
# Both modes return as soon as the task exists; "send" then polls GetTask, so a
# long first turn never depends on one HTTP connection staying open.
st, body = rpc("SendMessage", {"message": message(a.text), "configuration": {"returnImmediately": True}},
               timeout=min(a.timeout, 300))
s = summary(st, body)
s["sendHttp"] = st
if a.mode == "send" and s["task"] and s["state"] not in TERMINAL:
    while time.time() - t0 < a.timeout:
        time.sleep(3)
        st2, body2 = rpc("GetTask", {"id": s["task"]}, timeout=60)
        s2 = summary(st2, body2)
        if s2["state"]:
            s2["contextId"] = s2["contextId"] or s["contextId"]
            s = s2
        if s["state"] in TERMINAL:
            break
s["seconds"] = round(time.time() - t0, 1)
print(json.dumps(s))
