#!/usr/bin/env python3
"""Scripted OpenAI-compatible backend for lab test AIP-030 (throwaway, spike branch only).

One process serves several Services (model-gateway, model-gateway-actorjwt,
forbidden). Every request is recorded; GET /log returns the record. The
scenario is chosen by the tag SPIKE-T<n> in the last user message:
  T5  first call: a tool call to the tool whose name contains "whoami";
      after the tool result: a final text that quotes the result.
  T6  first call: a tool call to "bash" with the egress probe command;
      after the tool result: a final text that quotes the result.
  T7  holds the response for up to 240 s and records when the caller goes away.
  T8  answers {"verdict":"yes"} (structured output test).
  any other: answers "pong-<tag>".
"""
import hashlib
import json
import os
import re
import select
import socket
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = []
LOCK = threading.Lock()
SEQ = [0]
PROBE = open(os.environ["PROBE_FILE"]).read() if os.environ.get("PROBE_FILE") else "echo no-probe"


def record(ev):
    with LOCK:
        SEQ[0] += 1
        ev["n"] = SEQ[0]
        ev["t"] = round(time.time(), 3)
        LOG.append(ev)
    shown = {k: v for k, v in ev.items() if k not in ("auth", "messages")}
    print(json.dumps(shown)[:3000], flush=True)


def text_of(content):
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(p.get("text", "") if isinstance(p, dict) else str(p) for p in content)
    return str(content)


def tool_names(req):
    out = []
    for t in req.get("tools") or []:
        fn = t.get("function") or {}
        out.append(fn.get("name") or t.get("name") or "?")
    return out


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, *a):
        pass

    def send_json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/healthz"):
            record({"ev": "healthz", "host": self.headers.get("Host", "")})
            return self.send_json(200, {"ok": True, "host": self.headers.get("Host", "")})
        if self.path.startswith("/log"):
            with LOCK:
                data = list(LOG)
            return self.send_json(200, data)
        if self.path.startswith("/v1/models"):
            return self.send_json(200, {"object": "list", "data": [{"id": "agent-default", "object": "model"}]})
        record({"ev": "get", "path": self.path, "host": self.headers.get("Host", "")})
        self.send_json(404, {"error": "not found"})

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        auth = self.headers.get("Authorization", "")
        base = {
            "ev": "post",
            "path": self.path,
            "host": self.headers.get("Host", ""),
            "auth": auth,
            "auth_sha256": hashlib.sha256(auth.encode()).hexdigest() if auth else "",
            "headers": {k: v for k, v in self.headers.items() if k.lower() not in ("authorization",)},
        }
        try:
            req = json.loads(raw or b"{}")
        except Exception:
            base["bad_json"] = raw[:500].decode(errors="replace")
            record(base)
            return self.send_json(400, {"error": "bad json"})
        if not self.path.endswith("/chat/completions"):
            base["body"] = json.dumps(req)[:2000]
            record(base)
            return self.send_json(400, {"error": {"message": "only chat completions are scripted", "type": "invalid_request_error"}})
        msgs = req.get("messages") or []
        last_user = max([i for i, m in enumerate(msgs) if m.get("role") == "user"] or [-1])
        user = text_of(msgs[last_user].get("content")) if last_user >= 0 else ""
        tool_results = [text_of(m.get("content")) for m in msgs[last_user + 1:] if m.get("role") == "tool"]
        tag = re.search(r"SPIKE-(T\d+)", user)
        scenario = tag.group(1) if tag else "none"
        names = tool_names(req)
        base.update({
            "scenario": scenario,
            "model": req.get("model"),
            "stream": bool(req.get("stream")),
            "stream_options": req.get("stream_options"),
            "tools": names,
            "response_format": req.get("response_format"),
            "user": user[:500],
            "tool_results": [t[:4000] for t in tool_results],
            "n_messages": len(msgs),
            "messages": [{"role": m.get("role"), "content": text_of(m.get("content"))[:600]} for m in msgs],
        })
        if scenario == "T7":
            return self.hold(req, base)
        if scenario in ("T5", "T6") and not tool_results:
            want = "whoami" if scenario == "T5" else "bash"
            pick = next((x for x in names if want in x), None)
            base["action"] = "tool_call:" + str(pick)
            record(base)
            if pick is None:
                return self.reply(req, content="no tool named %s offered; tools=%s" % (want, names))
            args = {} if scenario == "T5" else {"command": PROBE, "timeout_seconds": 120}
            return self.reply(req, tool=pick, args=args)
        if scenario in ("T5", "T6"):
            content = "%s-done: %s" % (scenario, " | ".join(tool_results)[:6000])
        elif scenario == "T8":
            content = json.dumps({"verdict": "yes"})
        else:
            content = "pong-" + scenario
        base["action"] = "text"
        record(base)
        return self.reply(req, content=content)

    def reply(self, req, content=None, tool=None, args=None):
        model = req.get("model") or "stub"
        cid = "chatcmpl-" + uuid.uuid4().hex[:12]
        usage = {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
        call = None
        if tool:
            call = {"id": "call_" + uuid.uuid4().hex[:10], "type": "function",
                    "function": {"name": tool, "arguments": json.dumps(args or {})}}
        if not req.get("stream"):
            msg = {"role": "assistant", "content": content}
            if call:
                msg = {"role": "assistant", "content": None, "tool_calls": [call]}
            return self.send_json(200, {
                "id": cid, "object": "chat.completion", "created": int(time.time()), "model": model,
                "choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls" if call else "stop"}],
                "usage": usage})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        def chunk(delta, finish=None, use=None):
            obj = {"id": cid, "object": "chat.completion.chunk", "created": int(time.time()), "model": model,
                   "choices": [] if use else [{"index": 0, "delta": delta, "finish_reason": finish}]}
            if use:
                obj["usage"] = use
            self.wfile.write(b"data: " + json.dumps(obj).encode() + b"\n\n")
            self.wfile.flush()

        if call:
            c = dict(call)
            c["index"] = 0
            chunk({"role": "assistant", "content": None, "tool_calls": [c]})
            chunk({}, "tool_calls")
        else:
            chunk({"role": "assistant", "content": content})
            chunk({}, "stop")
        if (req.get("stream_options") or {}).get("include_usage"):
            chunk(None, use=usage)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def hold(self, req, base, seconds=240):
        base["action"] = "hold"
        record(base)
        start = time.time()
        record({"ev": "t7_hold_start", "host": base["host"]})
        stream = bool(req.get("stream"))
        if stream:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
        gone = None
        while time.time() - start < seconds:
            r, _, _ = select.select([self.connection], [], [], 1.0)
            if r:
                try:
                    data = self.connection.recv(1, socket.MSG_PEEK)
                except Exception:
                    data = b""
                if not data:
                    gone = time.time()
                    break
            if stream:
                try:
                    self.wfile.write(b": keepalive\n\n")
                    self.wfile.flush()
                except Exception:
                    gone = time.time()
                    break
        record({"ev": "t7_hold_end", "client_gone": gone is not None,
                "held_s": round((gone or time.time()) - start, 1)})
        if gone is None:
            try:
                if stream:
                    self.wfile.write(b"data: [DONE]\n\n")
                else:
                    self.send_json(200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": "late"}, "finish_reason": "stop"}]})
            except Exception:
                pass


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
