"""Stub OpenAI-compatible endpoint for spike PRD-070.

It prints the Authorization header of every request it receives and appends
the full request (method, path, headers, body, receive time) to a JSON-lines
file. It answers chat completions with a fixed reply, streamed or not.
Standard library only.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])
CAPTURE = sys.argv[2]
REPLY = "stub reply from spike PRD-070"


def shorten(value):
    """Show an Authorization value as credential then scheme.

    A JWT credential is shown by its start and length only. The scheme is
    printed last and quoted, because the CI log masker hides any
    "Bearer <word>" span.
    """
    scheme, _, cred = value.partition(" ")
    if not cred:
        scheme, cred = "", value
    if cred.count(".") == 2 and len(cred) > 60:
        cred = f"{cred[:12]}... (JWT, len {len(cred)})"
    return f"credential={cred!r} scheme={scheme!r}"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, fmt, *args):
        pass

    def _record(self, body_text):
        entry = {
            "received_at": time.time(),
            "method": self.command,
            "path": self.path,
            "headers": {k: v for k, v in self.headers.items()},
            "body": body_text,
        }
        with open(CAPTURE, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(entry) + "\n")
        auth = self.headers.get("Authorization")
        print(
            f"STUB {self.command} {self.path} Authorization header: "
            f"{shorten(auth) if auth is not None else '(absent)'}",
            flush=True,
        )

    def _json(self, code, obj):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self._record("")
        if self.path.rstrip("/").endswith("/models"):
            self._json(200, {"object": "list", "data": [{"id": "stub-model", "object": "model", "owned_by": "spike"}]})
        else:
            self._json(200, {"ok": True})

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        text = raw.decode("utf-8", "replace")
        self._record(text)
        if not self.path.rstrip("/").endswith("/chat/completions"):
            self._json(404, {"error": {"message": "stub serves chat completions only"}})
            return
        try:
            req = json.loads(text or "{}")
        except ValueError:
            req = {}
        model = req.get("model", "stub-model")
        now = int(time.time())
        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            base = {"id": "chatcmpl-spike", "object": "chat.completion.chunk", "created": now, "model": model}
            chunks = [
                {**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]},
                {**base, "choices": [{"index": 0, "delta": {"content": REPLY}, "finish_reason": None}]},
                {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                 "usage": {"prompt_tokens": 5, "completion_tokens": 6, "total_tokens": 11}},
            ]
            for chunk in chunks:
                self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        else:
            self._json(200, {
                "id": "chatcmpl-spike", "object": "chat.completion", "created": now, "model": model,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": REPLY}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 5, "completion_tokens": 6, "total_tokens": 11},
            })


if __name__ == "__main__":
    print(f"stub listening on :{PORT}, capturing to {CAPTURE}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
