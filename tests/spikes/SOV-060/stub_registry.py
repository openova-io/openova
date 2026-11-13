#!/usr/bin/env python3
"""SOV-060 spike: a TLS endpoint that answers every registry call with one status.

Usage: stub_registry.py PORT MODE LOGFILE
  MODE 503: every request gets 503 Service Unavailable.
  MODE 401: every request gets 401 with a Bearer challenge whose token
            endpoint (on this same server) also answers 401.
Each request is appended to LOGFILE in the access-log form the other
servers use: <epoch> "<METHOD> <path> HTTP/1.1" <status> port=<PORT>
"""
import http.server
import ssl
import sys
import time

PORT = int(sys.argv[1])
MODE = sys.argv[2]
LOGFILE = sys.argv[3]
CERT = "/etc/sov060/server.crt"
KEY = "/etc/sov060/server.key"


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _answer(self):
        if MODE == "503":
            status = 503
            body = b'{"errors":[{"code":"UNAVAILABLE","message":"service unavailable"}]}'
            extra = {}
        else:
            status = 401
            body = b'{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}'
            extra = {}
            if not self.path.startswith("/token"):
                extra["WWW-Authenticate"] = (
                    'Bearer realm="https://stub.local.test:%d/token",service="stub"' % PORT
                )
        with open(LOGFILE, "a") as f:
            f.write('%.3f "%s %s HTTP/1.1" %d port=%d\n' % (time.time(), self.command, self.path, status, PORT))
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        for k, v in extra.items():
            self.send_header(k, v)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    do_GET = _answer
    do_HEAD = _answer
    do_POST = _answer

    def log_message(self, *args):
        pass


def main():
    srv = http.server.ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(CERT, KEY)
    srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
