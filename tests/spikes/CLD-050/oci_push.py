#!/usr/bin/env python3
"""Spike CLD-050: push OpenTofu artifacts to an OCI registry over the
distribution API, with the exact manifest shapes OpenTofu 1.10 documents:

  module   -> image manifest, artifactType application/vnd.opentofu.modulepkg,
              one layer of media type archive/zip
  provider -> image index, artifactType application/vnd.opentofu.provider,
              one manifest per platform with artifactType
              application/vnd.opentofu.provider-target and one archive/zip layer

Usage:
  oci_push.py module   REGISTRY CAFILE REPO TAG DIR
  oci_push.py provider REGISTRY CAFILE REPO VERSION ZIP OS ARCH
"""
import hashlib
import io
import json
import os
import ssl
import sys
import urllib.error
import urllib.request
import zipfile

EMPTY = b"{}"


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


class Registry:
    def __init__(self, base, cafile):
        self.base = base.rstrip("/")
        self.ctx = ssl.create_default_context(cafile=cafile)

    def call(self, method, url, data=None, headers=None):
        req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
        return urllib.request.urlopen(req, context=self.ctx)

    def blob(self, repo, data):
        d = digest(data)
        try:
            self.call("HEAD", f"{self.base}/v2/{repo}/blobs/{d}")
            return d
        except urllib.error.HTTPError as e:
            if e.code != 404:
                raise
        r = self.call("POST", f"{self.base}/v2/{repo}/blobs/uploads/", data=b"",
                      headers={"Content-Length": "0"})
        loc = r.headers["Location"]
        if loc.startswith("/"):
            loc = self.base + loc
        sep = "&" if "?" in loc else "?"
        self.call("PUT", f"{loc}{sep}digest={d}", data=data,
                  headers={"Content-Type": "application/octet-stream",
                           "Content-Length": str(len(data))})
        return d

    def manifest(self, repo, ref, obj, media_type):
        body = json.dumps(obj, separators=(",", ":")).encode()
        self.call("PUT", f"{self.base}/v2/{repo}/manifests/{ref}", data=body,
                  headers={"Content-Type": media_type})
        return digest(body), len(body)


def artifact_manifest(reg, repo, zipdata, artifact_type):
    cfg = reg.blob(repo, EMPTY)
    layer = reg.blob(repo, zipdata)
    return {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "artifactType": artifact_type,
        "config": {"mediaType": "application/vnd.oci.empty.v1+json", "digest": cfg, "size": len(EMPTY)},
        "layers": [{"mediaType": "archive/zip", "digest": layer, "size": len(zipdata)}],
    }


def zip_dir(path):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for root, _, files in os.walk(path):
            for f in sorted(files):
                full = os.path.join(root, f)
                z.write(full, os.path.relpath(full, path))
    return buf.getvalue()


def main(argv):
    kind = argv[1]
    reg = Registry(argv[2], argv[3])
    repo = argv[4]
    if kind == "module":
        tag, src = argv[5], argv[6]
        m = artifact_manifest(reg, repo, zip_dir(src), "application/vnd.opentofu.modulepkg")
        d, _ = reg.manifest(repo, tag, m, "application/vnd.oci.image.manifest.v1+json")
        print(f"pushed module {repo}:{tag} {d}")
    elif kind == "provider":
        version, zpath, os_, arch = argv[5], argv[6], argv[7], argv[8]
        data = open(zpath, "rb").read()
        m = artifact_manifest(reg, repo, data, "application/vnd.opentofu.provider-target")
        body = json.dumps(m, separators=(",", ":")).encode()
        md, ms = reg.manifest(repo, digest(body), m, "application/vnd.oci.image.manifest.v1+json")
        index = {
            "schemaVersion": 2,
            "mediaType": "application/vnd.oci.image.index.v1+json",
            "artifactType": "application/vnd.opentofu.provider",
            "manifests": [{
                "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "artifactType": "application/vnd.opentofu.provider-target",
                "digest": md,
                "size": ms,
                "platform": {"os": os_, "architecture": arch},
            }],
        }
        d, _ = reg.manifest(repo, version, index, "application/vnd.oci.image.index.v1+json")
        print(f"pushed provider {repo}:{version} index {d} manifest {md}")
    else:
        raise SystemExit("unknown kind " + kind)


if __name__ == "__main__":
    main(sys.argv)
