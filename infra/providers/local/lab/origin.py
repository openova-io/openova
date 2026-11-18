import http.server, os, sys

R = "/srv"


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        p = self.path.split("?")[0].split("/")
        f, t = None, "application/octet-stream"
        if len(p) >= 5 and p[-2] == "manifests":
            f, t = os.path.join(R, "manifests", p[-3], p[-1]), "application/vnd.oci.image.manifest.v1+json"
        elif len(p) >= 5 and p[-2] == "blobs":
            f = os.path.join(R, "blobs", p[-1].replace("sha256:", ""))
        if f and os.path.isfile(f):
            b = open(f, "rb").read()
            self.send_response(200)
            self.send_header("Content-Type", t)
            self.send_header("Content-Length", str(len(b)))
            self.end_headers()
            self.wfile.write(b)
        else:
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()


http.server.ThreadingHTTPServer(("0.0.0.0", 5000), H).serve_forever()
