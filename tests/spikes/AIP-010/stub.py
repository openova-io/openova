#!/usr/bin/env python3
"""Throwaway backend for lab test AIP-010 (spike branch only, never merged).

Two roles, chosen by the first argument:

  stub    the scripted model backend behind the gateway (port 8081). It
          answers the OpenAI routes the gateway forwards, records every request
          it receives (method, path, headers, body) and serves that log on
          GET /_spike/log so the test driver can see exactly what reached the
          backend.
  keyset  a stand-in for the platform's key-set route (port 8080):
          GET /api/v1/auth/signers.jwks returns the JSON in $JWKS_FILE.

Behaviour of the stub follows the scripted backend of the model contract:
with tools and no tool message it returns one tool call to the first tool
with arguments {} and identifier call_stub_1; when the last message has role
tool it returns "echo:" + that content; otherwise "echo:" + the last user
text. Usage is deterministic (bytes / 4, rounded up). The request header
x-spike-mode selects a scripted behaviour for one test:

  tools2          two tool calls, one per tool, with fixed identifiers and
                  argument strings (T2)
  tools1          one tool call to the first tool with {"city":"Muscat"} (T9)
  slow-args       streamed; the tool-call argument is sent in three chunks
                  five seconds apart; send times are recorded (T3)
  silent-<n>      send nothing for n seconds, then answer (T7)
  stream-idle-<n> send headers and the role chunk, stay silent n seconds,
                  then finish the stream (T7, informative)
  size            answer with the number of body bytes received (T11)
  whoami          answer with the Host header and the model received (T8)

Standard library only.
"""
import base64
import hashlib
import json
import math
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

ROLE = sys.argv[1] if len(sys.argv) > 1 else "stub"
INSTANCE = os.environ.get("STUB_INSTANCE", "stub")
LOG = []
LOG_LOCK = threading.Lock()
SEQ = [0]
MAX_LOG = 2000
BODY_KEEP = 256 * 1024

T2_CALLS = [
    {"id": "call_spike_A1", "name_from": 0,
     "arguments": "{\"city\":\"Muscat\",\"unit\":\"c\",\"days\":3}"},
    {"id": "call_spike_B2", "name_from": 1,
     "arguments": "{\"id\":\"OM-42\",\"fields\":[\"title\",\"état\"],\"filter\":{\"status\":\"open \\\"q\\\"\"}}"},
]


def now():
    return time.time()


def tokens(n_bytes):
    return max(1, int(math.ceil(n_bytes / 4.0)))


def record(handler, body, extra=None):
    with LOG_LOCK:
        SEQ[0] += 1
        entry = {
            "seq": SEQ[0],
            "instance": INSTANCE,
            "t_recv": handler.t_start,
            "method": handler.command,
            "path": handler.path,
            "headers": [[k, v] for k, v in handler.headers.items()],
            "body_len": len(body),
            "body_sha256": hashlib.sha256(body).hexdigest(),
            "body_b64": base64.b64encode(body[:BODY_KEEP]).decode(),
            "body_truncated": len(body) > BODY_KEEP,
        }
        if extra:
            entry.update(extra)
        LOG.append(entry)
        if len(LOG) > MAX_LOG:
            del LOG[: len(LOG) - MAX_LOG]
        return entry


def update_record(entry, **kw):
    with LOG_LOCK:
        entry.update(kw)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "spike-stub/1"

    def log_message(self, fmt, *args):  # quiet access log, one line per request
        sys.stderr.write("%s %s\n" % (self.command, self.path))

    # ---------- body helpers
    def read_body(self):
        te = (self.headers.get("Transfer-Encoding") or "").lower()
        if "chunked" in te:
            chunks = []
            while True:
                line = self.rfile.readline()
                if not line:
                    break
                size = int(line.split(b";")[0].strip() or b"0", 16)
                if size == 0:
                    while True:
                        trailer = self.rfile.readline()
                        if trailer in (b"\r\n", b"\n", b""):
                            break
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.readline()
            return b"".join(chunks)
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def send_json(self, code, obj, extra_headers=None):
        data = json.dumps(obj, separators=(",", ":"), ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("x-spike-instance", INSTANCE)
        for k, v in (extra_headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(data)
        self.wfile.flush()

    def start_stream(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Transfer-Encoding", "chunked")
        self.send_header("x-spike-instance", INSTANCE)
        self.end_headers()
        self.wfile.flush()

    def send_chunk(self, payload):
        if isinstance(payload, (dict, list)):
            payload = "data: " + json.dumps(payload, separators=(",", ":"), ensure_ascii=False) + "\n\n"
        data = payload.encode() if isinstance(payload, str) else payload
        self.wfile.write(("%x\r\n" % len(data)).encode() + data + b"\r\n")
        self.wfile.flush()
        return now()

    def end_stream(self):
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    # ---------- routing
    def do_GET(self):
        self.t_start = now()
        u = urlparse(self.path)
        if u.path in ("/healthz", "/readyz"):
            return self.send_json(200, {"ok": True})
        if ROLE == "keyset":
            body = b""
            record(self, body)
            if u.path == "/api/v1/auth/signers.jwks":
                with open(os.environ["JWKS_FILE"], "rb") as f:
                    data = f.read()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Cache-Control", "max-age=300")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
                return
            return self.send_json(404, {"error": "not-found"})
        if u.path == "/_spike/log":
            after = int((parse_qs(u.query).get("after") or ["0"])[0])
            with LOG_LOCK:
                recs = [r for r in LOG if r["seq"] > after]
                last = SEQ[0]
            return self.send_json(200, {"last": last, "records": recs})
        body = b""
        record(self, body)
        if u.path.endswith("/models"):
            return self.send_json(200, {"object": "list", "data": [
                {"id": "echo", "object": "model", "created": 0, "owned_by": "stub-upstream"}]})
        return self.send_json(404, {"error": {"message": "no route", "type": "invalid_request", "code": "not_found"}})

    def do_POST(self):
        self.t_start = now()
        body = self.read_body()
        u = urlparse(self.path)
        entry = record(self, body)
        if ROLE == "keyset":
            return self.send_json(404, {"error": "not-found"})
        p = u.path
        if p.endswith("/chat/completions"):
            return self.chat(body, entry)
        if p.endswith("/embeddings"):
            return self.embeddings(body)
        if p.endswith("/rerank"):
            return self.rerank(body)
        if p.endswith("/audio/transcriptions"):
            return self.send_json(200, {"text": "spike transcript", "duration": 1.0,
                                        "usage": {"type": "duration", "seconds": 1}})
        if p.endswith("/responses") or p.endswith("/messages"):
            return self.send_json(404, {"error": {"message": "route not served by this backend: " + p,
                                                  "type": "invalid_request", "code": "not_found"}})
        return self.send_json(404, {"error": {"message": "no route " + p, "type": "invalid_request", "code": "not_found"}})

    # ---------- task routes
    def embeddings(self, body):
        try:
            req = json.loads(body or b"{}")
        except ValueError:
            req = {}
        inp = req.get("input", "")
        items = inp if isinstance(inp, list) else [inp]
        data = [{"object": "embedding", "index": i, "embedding": [0.1, 0.2, 0.3]} for i in range(len(items))]
        n = tokens(len(body))
        return self.send_json(200, {"object": "list", "data": data, "model": req.get("model", ""),
                                    "usage": {"prompt_tokens": n, "total_tokens": n}})

    def rerank(self, body):
        try:
            req = json.loads(body or b"{}")
        except ValueError:
            req = {}
        docs = req.get("documents") or []
        results = [{"index": i, "relevance_score": round(1.0 / (i + 1), 3)} for i in range(len(docs))]
        return self.send_json(200, {"id": "rerank-spike", "model": req.get("model", ""), "results": results,
                                    "meta": {"billed_units": {"search_units": 1}},
                                    "usage": {"total_tokens": tokens(len(body))}})

    # ---------- chat completions
    def chat(self, body, entry):
        try:
            req = json.loads(body or b"{}")
        except ValueError:
            return self.send_json(400, {"error": {"message": "invalid json", "type": "invalid_request", "code": "invalid_request"}})
        mode = (self.headers.get("x-spike-mode") or "").strip()
        model = req.get("model", "")
        stream = bool(req.get("stream"))
        include_usage = bool((req.get("stream_options") or {}).get("include_usage"))
        tools = req.get("tools") or []
        msgs = req.get("messages") or []
        created = int(now())
        cid = "chatcmpl-spike-%d" % entry["seq"]

        if mode.startswith("silent-"):
            secs = float(mode.split("-", 1)[1])
            update_record(entry, silent_start=now())
            time.sleep(secs)
            update_record(entry, silent_end=now())

        tool_calls = None
        text = None
        if mode == "tools2" and len(tools) >= 2:
            tool_calls = []
            for c in T2_CALLS:
                tool_calls.append({"id": c["id"], "type": "function",
                                   "function": {"name": tools[c["name_from"]]["function"]["name"],
                                                "arguments": c["arguments"]}})
        elif mode == "tools1" and tools:
            tool_calls = [{"id": "call_spike_T9", "type": "function",
                           "function": {"name": tools[0]["function"]["name"],
                                        "arguments": "{\"city\":\"Muscat\"}"}}]
        elif mode == "size":
            users = [m for m in msgs if m.get("role") == "user"]
            c = users[-1].get("content", "") if users else ""
            clen = len(c.encode()) if isinstance(c, str) else -1
            update_record(entry, content_len=clen, model_received=model)
            text = "received %d bytes, last user content %d bytes" % (len(body), clen)
        elif mode == "whoami":
            text = json.dumps({"host": self.headers.get("Host"), "model": model, "instance": INSTANCE},
                              separators=(",", ":"))
        elif tools and not any(m.get("role") == "tool" for m in msgs):
            tool_calls = [{"id": "call_stub_1", "type": "function",
                           "function": {"name": tools[0]["function"]["name"], "arguments": "{}"}}]
        else:
            last = msgs[-1] if msgs else {}
            content = last.get("content", "")
            if isinstance(content, list):
                content = "".join(p.get("text", "") for p in content if isinstance(p, dict))
            if last.get("role") != "tool":
                users = [m for m in msgs if m.get("role") == "user"]
                c2 = users[-1].get("content", "") if users else ""
                if isinstance(c2, list):
                    c2 = "".join(p.get("text", "") for p in c2 if isinstance(p, dict))
                content = c2
            text = "echo:" + (content or "")

        out_bytes = len(text.encode()) if text is not None else sum(
            len(c["function"]["arguments"].encode()) for c in tool_calls)
        usage = {"prompt_tokens": tokens(len(body)), "completion_tokens": tokens(out_bytes),
                 "prompt_tokens_details": {"cached_tokens": 0}}
        usage["total_tokens"] = usage["prompt_tokens"] + usage["completion_tokens"]
        finish = "tool_calls" if tool_calls else "stop"

        if mode == "slow-args":
            return self.slow_args(entry, cid, created, model, tools, usage, include_usage)
        if mode.startswith("stream-idle-"):
            return self.stream_idle(entry, cid, created, model, float(mode.rsplit("-", 1)[1]), usage, include_usage)

        if not stream:
            msg = {"role": "assistant", "content": text}
            if tool_calls:
                msg["content"] = None
                msg["tool_calls"] = tool_calls
            return self.send_json(200, {"id": cid, "object": "chat.completion", "created": created, "model": model,
                                        "choices": [{"index": 0, "message": msg, "finish_reason": finish}],
                                        "usage": usage})
        self.start_stream()
        base = {"id": cid, "object": "chat.completion.chunk", "created": created, "model": model}
        self.send_chunk(dict(base, choices=[{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]))
        if tool_calls:
            for i, c in enumerate(tool_calls):
                self.send_chunk(dict(base, choices=[{"index": 0, "delta": {"tool_calls": [
                    {"index": i, "id": c["id"], "type": "function",
                     "function": {"name": c["function"]["name"], "arguments": c["function"]["arguments"]}}]},
                    "finish_reason": None}]))
        else:
            self.send_chunk(dict(base, choices=[{"index": 0, "delta": {"content": text}, "finish_reason": None}]))
        self.send_chunk(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": finish}]))
        if include_usage:
            self.send_chunk(dict(base, choices=[], usage=usage))
        self.send_chunk("data: [DONE]\n\n")
        self.end_stream()

    def slow_args(self, entry, cid, created, model, tools, usage, include_usage):
        name = tools[0]["function"]["name"] if tools else "lookup"
        base = {"id": cid, "object": "chat.completion.chunk", "created": created, "model": model}
        self.start_stream()
        sent = {}
        sent["role"] = self.send_chunk(dict(base, choices=[{"index": 0, "delta": {
            "role": "assistant", "content": None,
            "tool_calls": [{"index": 0, "id": "call_slow_1", "type": "function",
                            "function": {"name": name, "arguments": ""}}]}, "finish_reason": None}]))
        pieces = ["{\"q\":\"P1-alpha", "P2-beta", "P3-gamma\"}"]
        for i, piece in enumerate(pieces):
            if i:
                time.sleep(5)
            sent["P%d" % (i + 1)] = self.send_chunk(dict(base, choices=[{"index": 0, "delta": {
                "tool_calls": [{"index": 0, "function": {"arguments": piece}}]}, "finish_reason": None}]))
            update_record(entry, sent=dict(sent))
        sent["finish"] = self.send_chunk(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": "tool_calls"}]))
        if include_usage:
            self.send_chunk(dict(base, choices=[], usage=usage))
        self.send_chunk("data: [DONE]\n\n")
        self.end_stream()
        update_record(entry, sent=sent)

    def stream_idle(self, entry, cid, created, model, secs, usage, include_usage):
        base = {"id": cid, "object": "chat.completion.chunk", "created": created, "model": model}
        self.start_stream()
        t0 = self.send_chunk(dict(base, choices=[{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]))
        update_record(entry, idle_start=t0)
        time.sleep(secs)
        t1 = self.send_chunk(dict(base, choices=[{"index": 0, "delta": {"content": "after-idle"}, "finish_reason": None}]))
        self.send_chunk(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": "stop"}]))
        if include_usage:
            self.send_chunk(dict(base, choices=[], usage=usage))
        self.send_chunk("data: [DONE]\n\n")
        self.end_stream()
        update_record(entry, idle_end=t1)


def main():
    port = int(os.environ.get("PORT", "8081" if ROLE == "stub" else "8080"))
    srv = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    srv.daemon_threads = True
    sys.stderr.write("spike %s listening on %d\n" % (ROLE, port))
    srv.serve_forever()


if __name__ == "__main__":
    main()
