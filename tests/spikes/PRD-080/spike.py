#!/usr/bin/env python3
"""Lab test PRD-080 (never merged): CPU images for embeddings with rerank,
document conversion and transcription.

    python3 spike.py embeddings|conversion|transcription

Runs on a GitHub-hosted runner. Each server under test runs in Docker on an
internal network (no route out), first with the hardening the serving rules
ask for: a non-root user, a read-only root file system, all capabilities
dropped and no-new-privileges. Only if that start fails are weaker profiles
tried, one at a time, so the record says which rule the image breaks. A small
client container on the same network sends the requests (client.py).

Every measured value is printed as a `PRD080-RESULT {json}` line and written
to $OUT/results.jsonl.
"""
import collections
import glob
import html
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import unicodedata
import urllib.request
import zipfile

SPIKE = os.path.dirname(os.path.abspath(__file__))
DATA = os.environ["DATA"]
OUT = os.environ["OUT"]
MODELS = os.path.join(DATA, "models")
NET = "prd080"
CLI = "prd080-cli"
SRV = "prd080-srv"
CLIENT_IMAGE = "python:3.12.7-slim-bookworm"

# Floors applied by this lab test. The plan entry names a floor "written in the
# ticket"; no ticket text carries one, so these are stated here and in the record.
FLOORS = {
    "embed_texts_per_s": 2.0,      # ~500-token chunks, batches of 16, one replica, four cores
    "query_p95_ms": 500.0,         # one short query embedded
    "rerank8_p50_s": 2.0,          # one query against 8 chunks of ~500 tokens (topK default 8)
    "pages_per_min": 2.0,          # the twenty-page scanned file
    "audio_s_per_s": 1.0,          # seconds of audio per wall second
}
# Quality bars for the right-to-left (Arabic) sample, also applied here.
RTL = {"embed_r1": 0.8, "rerank_top1": 0.8, "ocr_cer": 0.15, "asr_wer_ar": 0.40, "asr_wer_en": 0.15}

FLEURS = "google/fleurs"


# ============================================================== helpers
def log(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


def sh(cmd, timeout=None, check=False):
    p = subprocess.run(cmd, shell=isinstance(cmd, str), capture_output=True, text=True, timeout=timeout)
    if check and p.returncode != 0:
        raise RuntimeError(f"{cmd} -> {p.returncode}: {p.stderr[-800:]}")
    return p.returncode, (p.stdout + p.stderr)


def result(obj):
    line = json.dumps(obj, ensure_ascii=False, sort_keys=True)
    print("PRD080-RESULT " + line, flush=True)
    with open(os.path.join(OUT, "results.jsonl"), "a") as f:
        f.write(line + "\n")


def get_json(url):
    for i in range(4):
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                return json.load(r)
        except Exception as e:
            log("retry", url, e)
            time.sleep(5 * (i + 1))
    return {}


def download(url, path):
    if os.path.exists(path) and os.path.getsize(path) > 0:
        return path
    os.makedirs(os.path.dirname(path), exist_ok=True)
    for i in range(4):
        try:
            with urllib.request.urlopen(url, timeout=120) as r, open(path + ".part", "wb") as f:
                shutil.copyfileobj(r, f, 1 << 20)
            os.replace(path + ".part", path)
            return path
        except Exception as e:
            log("retry", url, e)
            time.sleep(5 * (i + 1))
    raise RuntimeError("download failed: " + url)


def hf_model_licence(repo):
    d = get_json(f"https://huggingface.co/api/models/{repo}")
    return (d.get("cardData") or {}).get("license"), d.get("gated")


def snapshot(repo, rev, local=None, cache=None, allow=None, ignore=None):
    from huggingface_hub import snapshot_download
    t0 = time.time()
    if cache:
        path = snapshot_download(repo, revision=rev, cache_dir=cache, allow_patterns=allow, ignore_patterns=ignore)
        refs = os.path.join(cache, "models--" + repo.replace("/", "--"), "refs")
        os.makedirs(refs, exist_ok=True)
        open(os.path.join(refs, "main"), "w").write(rev)  # offline lookups of "main" resolve to the pinned commit
    else:
        path = snapshot_download(repo, revision=rev, local_dir=local, allow_patterns=allow, ignore_patterns=ignore)
    size = sum(os.path.getsize(p) for p in glob.glob(os.path.join(path, "**"), recursive=True) if os.path.isfile(p))
    lic, gated = hf_model_licence(repo)
    info = {"repo": repo, "revision": rev, "licence": lic, "gated": gated, "bytes": size,
            "download_s": round(time.time() - t0, 1)}
    result({"kind": "model", **info})
    return path


# ============================================================== docker
def pull(image):
    for i in range(3):
        rc, out = sh(["docker", "pull", "-q", image], timeout=1800)
        if rc == 0:
            break
        log("pull retry", image, out[-300:])
        time.sleep(10)
    rc, out = sh(["docker", "image", "inspect", image])
    d = json.loads(out)[0] if rc == 0 else {}
    cfg = d.get("Config") or {}
    labels = cfg.get("Labels") or {}
    info = {
        "kind": "image", "image": image,
        "digest": (d.get("RepoDigests") or ["?"])[0],
        "user": cfg.get("User") or "(empty = root)",
        "size_gb": round((d.get("Size") or 0) / 1e9, 2),
        "created": d.get("Created", "")[:19],
        "entrypoint": cfg.get("Entrypoint"), "cmd": cfg.get("Cmd"),
        "label_licence": labels.get("org.opencontainers.image.licenses") or labels.get("license"),
        "label_version": labels.get("org.opencontainers.image.version"),
    }
    result(info)
    return info


def image_runs_nonroot(info):
    u = (info.get("user") or "").split(":")[0]
    return u not in ("", "(empty = root)", "root", "0")


def rm(name):
    sh(["docker", "rm", "-f", name])


def setup_network():
    sh(["docker", "network", "create", "--internal", NET])
    rm(CLI)
    pull(CLIENT_IMAGE)
    sh(["docker", "run", "-d", "--name", CLI, "--network", NET, "-v", f"{SPIKE}:/w:ro", "-v", f"{DATA}:/data:ro",
        "-v", f"{OUT}:/out", CLIENT_IMAGE, "sleep", "infinity"], check=True)


def client(cmd, spec, timeout=7200):
    rc, out = sh(["docker", "exec", CLI, "python3", "/w/client.py", cmd, json.dumps(spec)], timeout=timeout)
    for line in reversed(out.strip().splitlines()):
        if line.startswith("{"):
            try:
                return json.loads(line)
            except Exception:
                break
    return {"error": f"client rc={rc}: {out[-600:]}"}


PROFILES = ["hardened", "writable-root", "image-default", "egress"]


def start(info, profile, args, env=None, mounts=(), entrypoint=None):
    rm(SRV)
    a = ["docker", "run", "-d", "--name", SRV, "--cpus", str(min(4, os.cpu_count() or 4)), "--memory", "13g"]
    if profile in ("hardened", "writable-root"):
        if not image_runs_nonroot(info):
            a += ["--user", "10001"]
            env = dict(env or {}, HOME="/tmp")
        a += ["--cap-drop", "ALL", "--security-opt", "no-new-privileges"]
    if profile == "hardened":
        a += ["--read-only", "--tmpfs", "/tmp:rw,exec,size=6g,mode=1777"]
    a += ["--network", "bridge" if profile == "egress" else NET]
    for k, v in (env or {}).items():
        a += ["-e", f"{k}={v}"]
    for src, dst in mounts:
        a += ["-v", f"{src}:{dst}:ro"]
    if entrypoint is not None:
        a += ["--entrypoint", entrypoint]
    a += [info["image"]] + list(args)
    rc, out = sh(a)
    if rc != 0:
        return False, out[-500:]
    if profile == "egress":
        sh(["docker", "network", "connect", NET, SRV])
    return True, ""


def running():
    rc, out = sh(["docker", "inspect", "-f", "{{.State.Status}}", SRV])
    return out.strip() in ("running", "created", "restarting")


def wait_ready(paths, port, timeout, ok=(200,)):
    urls = [f"http://{SRV}:{port}{p}" for p in paths]
    t0 = time.time()
    while time.time() - t0 < timeout:
        if not running():
            return False, round(time.time() - t0, 1), "container exited"
        r = client("probe", {"urls": urls, "ok": list(ok)}, timeout=30)
        if r.get("ready"):
            return True, round(time.time() - t0, 1), r.get("url")
        time.sleep(2)
    return False, timeout, "not ready in time"


def serve(info, label, args, port, health, timeout, env=None, mounts=(), entrypoint=None, ok=(200,)):
    """Start under the strictest profile that works. Returns the profile used or None."""
    tried = []
    for prof in PROFILES:
        okstart, err = start(info, prof, args, env, mounts, entrypoint)
        if okstart:
            ready, secs, why = wait_ready(health, port, timeout, ok)
        else:
            ready, secs, why = False, 0, "docker run failed: " + err
        if ready:
            result({"kind": "start", "label": label, "image": info["image"], "profile": prof, "ready_s": secs,
                    "failed_before": tried})
            return prof
        _, logs = sh(["docker", "logs", "--tail", "25", SRV])
        tried.append({"profile": prof, "why": why, "after_s": secs, "log": logs[-1500:]})
        log(f"{label}: profile {prof} failed: {why}\n{logs[-1500:]}")
    result({"kind": "start", "label": label, "image": info["image"], "profile": None, "failed_before": tried})
    rm(SRV)
    return None


class MemPeak:
    def __init__(self):
        self.peak, self.stop = 0.0, False
        self.t = threading.Thread(target=self.run, daemon=True)
        self.t.start()

    def run(self):
        units = {"B": 1e-9, "KiB": 1024 / 1e9, "MiB": 1024 ** 2 / 1e9, "GiB": 1024 ** 3 / 1e9}
        while not self.stop:
            rc, out = sh(["docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", SRV], timeout=30)
            m = re.match(r"\s*([\d.]+)\s*([KMG]?i?B)", out)
            if rc == 0 and m:
                self.peak = max(self.peak, float(m.group(1)) * units.get(m.group(2), 0))
            time.sleep(2)

    def done(self):
        self.stop = True
        return round(self.peak, 2)


# ============================================================== text
AR_MARKS = re.compile(r"[ؐ-ًؚ-ٰٟۖ-ۭـ]")


def norm(text, lang):
    t = unicodedata.normalize("NFKC", text or "")
    if lang == "ar":
        t = AR_MARKS.sub("", t)
        t = re.sub("[إأآٱ]", "ا", t).replace("ى", "ي").replace("ة", "ه").replace("ؤ", "و").replace("ئ", "ي")
        t = t.translate(str.maketrans("٠١٢٣٤٥٦٧٨٩", "0123456789"))
    t = re.sub(r"[^\w\s]", " ", t.lower()).replace("_", " ")
    return " ".join(t.split())


def scores(hyp, ref, lang):
    from rapidfuzz.distance import Levenshtein
    h, r = norm(hyp, lang), norm(ref, lang)
    hw, rw = h.split(), r.split()
    cer = Levenshtein.distance(h, r) / max(1, len(r))
    wer = Levenshtein.distance(hw, rw) / max(1, len(rw))
    hc, rc = collections.Counter(hw), collections.Counter(rw)
    bag = sum(min(c, hc[w]) for w, c in rc.items()) / max(1, len(rw))
    out = {"cer": round(cer, 4), "wer": round(wer, 4), "word_recall": round(bag, 4), "ref_words": len(rw)}
    if lang == "ar":  # diagnostic: words written in visual (reversed) order
        hr = collections.Counter(w[::-1] for w in hw)
        out["word_recall_if_reversed"] = round(sum(min(c, hr[w]) for w, c in rc.items()) / max(1, len(rw)), 4)
    return out


def fleurs(with_audio=False):
    """FLEURS test splits (CC-BY-4.0): English (en_us) and Arabic (ar_eg)."""
    import pyarrow.parquet as pq
    rev = get_json(f"https://huggingface.co/api/datasets/{FLEURS}/revision/refs%2Fconvert%2Fparquet").get("sha") \
        or "refs%2Fconvert%2Fparquet"
    rows = {}
    for lang, cfg in (("en", "en_us"), ("ar", "ar_eg")):
        url = f"https://huggingface.co/datasets/{FLEURS}/resolve/{rev}/{cfg}/test/0000.parquet"
        path = download(url, os.path.join(DATA, "fleurs", f"{cfg}-test.parquet"))
        cols = ["id", "num_samples", "transcription", "raw_transcription"] + (["audio"] if with_audio else [])
        t = pq.read_table(path, columns=cols).to_pylist()
        seen, uniq = set(), []
        for r in t:
            if r["id"] not in seen:
                seen.add(r["id"])
                uniq.append(r)
        rows[lang] = uniq
    result({"kind": "dataset", "name": FLEURS, "revision": rev, "en_unique": len(rows["en"]),
            "ar_unique": len(rows["ar"]), "licence": "cc-by-4.0"})
    return rows


def parallel_pairs(rows, n):
    ar = {r["id"]: r for r in rows["ar"]}
    pairs = [{"id": r["id"], "en": r["raw_transcription"], "ar": ar[r["id"]]["raw_transcription"]}
             for r in sorted(rows["en"], key=lambda r: r["id"]) if r["id"] in ar]
    return pairs[:n]


# ============================================================== embeddings
EMBED_MODELS = {
    "bge-m3": dict(repo="BAAI/bge-m3", rev="5617a9f61b028005a4858fdac845db406aefb181", kind="embed", dims=1024,
                   ignore=["imgs/*", "*.md", "colbert_linear.pt", "sparse_linear.pt", "long.jpg"]),
    "granite-107m-ml": dict(repo="ibm-granite/granite-embedding-107m-multilingual",
                            rev="d6cffd338414d6a1c1f5decfad5fec62eebc90d5", kind="embed", dims=384,
                            ignore=["pytorch_model.bin", "*.md"]),
    "bge-reranker-v2-m3": dict(repo="BAAI/bge-reranker-v2-m3", rev="953dc6f6f85a1b2dbfca4c34a2796e7dde08d41e",
                               kind="rerank", ignore=["*.md", "assets/*"]),
    "mmarco-ml-minilm": dict(repo="cross-encoder/mmarco-mMiniLMv2-L12-H384-v1",
                             rev="1427fd652930e4ba29e8149678df786c240d8825", kind="rerank",
                             ignore=["openvino/*", "onnx/model_O*", "onnx/model_q*", "pytorch_model.bin", "*.md"]),
    "bge-m3-q8": dict(repo="gpustack/bge-m3-GGUF", rev="2d48f1737679ad900d5c26c5aad5410e9c70fdca", kind="embed",
                      dims=1024, allow=["bge-m3-Q8_0.gguf"], file="bge-m3-Q8_0.gguf"),
    "bge-reranker-v2-m3-q8": dict(repo="gpustack/bge-reranker-v2-m3-GGUF",
                                  rev="3093af03b1a635e67b084b1d8c03c5f5e020fd05", kind="rerank",
                                  allow=["bge-reranker-v2-m3-Q8_0.gguf"], file="bge-reranker-v2-m3-Q8_0.gguf"),
}

EMBED_CANDIDATES = [
    dict(name="tei", image="ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.4",
         source="github.com/huggingface/text-embeddings-inference", source_licence="Apache-2.0", port=8080,
         health=["/health"],
         models=[("bge-m3", ""), ("granite-107m-ml", ""), ("bge-reranker-v2-m3", ""), ("mmarco-ml-minilm", "")],
         args=lambda m, k, v: ["--model-id", f"/models/{m}", "--hostname", "0.0.0.0", "--port", "8080",
                               "--auto-truncate"],
         env={"HF_HUB_OFFLINE": "1"}, embed_path="/v1/embeddings", rerank_api="tei", rerank_path="/rerank"),
    dict(name="infinity", image="michaelf34/infinity:0.0.78-cpu", source="github.com/michaelfeil/infinity",
         source_licence="MIT", port=7997, health=["/v1/health", "/health"],
         models=[("bge-m3", "torch"), ("bge-m3", "optimum"), ("granite-107m-ml", "torch"),
                 ("granite-107m-ml", "optimum"), ("bge-reranker-v2-m3", "torch"), ("mmarco-ml-minilm", "torch"),
                 ("mmarco-ml-minilm", "optimum")],
         args=lambda m, k, v: ["v2", "--model-id", f"/models/{m}", "--served-model-name", m, "--port", "7997",
                               "--url-prefix", "/v1", "--device", "cpu", "--engine", v],
         env={"HF_HUB_OFFLINE": "1", "INFINITY_ANONYMOUS_USAGE_STATS": "0", "DO_NOT_TRACK": "1"},
         embed_path="/v1/embeddings", rerank_api="n1", rerank_path="/v1/rerank"),
    dict(name="llama.cpp", image="ghcr.io/ggml-org/llama.cpp:server-b11718", source="github.com/ggml-org/llama.cpp",
         source_licence="MIT", port=8080, health=["/health"], models=[("bge-m3-q8", ""), ("bge-reranker-v2-m3-q8", "")],
         args=lambda m, k, v: ["-m", f"/models/{m}/{EMBED_MODELS[m]['file']}", "--host", "0.0.0.0", "--port", "8080",
                            "-t", "4", "-c", "8192", "-np", "8", "-b", "8192", "-ub", "8192"]
         + (["--embeddings", "--pooling", "cls"] if k == "embed" else ["--reranking"]),
         env={}, embed_path="/v1/embeddings", rerank_api="n1", rerank_path="/v1/rerank"),
]


def prep_embed(rows):
    from tokenizers import Tokenizer
    tok = Tokenizer.from_file(os.path.join(MODELS, "bge-m3", "tokenizer.json"))
    import random
    rnd = random.Random(80)
    passages, ptoks = [], []
    for lang in ("en", "ar"):
        sents = [r["raw_transcription"] for r in rows[lang]]
        made = 0
        while made < 80:
            rnd.shuffle(sents)
            cur, n = [], 0
            for s in sents:
                k = len(tok.encode(s, add_special_tokens=False).ids)
                if n + k > 505:
                    break
                cur.append(s)
                n += k
            passages.append(" ".join(cur))
            ptoks.append(len(tok.encode(passages[-1]).ids))
            made += 1
    rnd.shuffle(passages)
    queries = [r["raw_transcription"] for r in rows["en"][:20]] + [r["raw_transcription"] for r in rows["ar"][:20]]
    pairs = parallel_pairs(rows, 100)
    json.dump({"passages": passages, "queries": queries, "pairs": pairs},
              open(os.path.join(DATA, "embed.json"), "w"), ensure_ascii=False)
    result({"kind": "embed-data", "passages": len(passages), "passage_tokens_mean": round(sum(ptoks) / len(ptoks), 1),
            "passage_tokens_min": min(ptoks), "passage_tokens_max": max(ptoks), "queries": len(queries),
            "pairs": len(pairs)})


def run_embeddings():
    rows = fleurs()
    for m, d in EMBED_MODELS.items():
        try:
            snapshot(d["repo"], d["rev"], local=os.path.join(MODELS, m), allow=d.get("allow"), ignore=d.get("ignore"))
        except Exception as e:
            result({"kind": "model", "repo": d["repo"], "error": str(e)[:300]})
    prep_embed(rows)
    setup_network()
    for c in EMBED_CANDIDATES:
        info = pull(c["image"])
        result({"kind": "candidate", "task": "embeddings", "name": c["name"], "image": c["image"],
                "source": c["source"], "source_licence": c["source_licence"], "digest": info["digest"],
                "image_user": info["user"], "image_nonroot": image_runs_nonroot(info), "size_gb": info["size_gb"]})
        for m, variant in c["models"]:
            d = EMBED_MODELS[m]
            label = f"{c['name']}/{m}" + (f"@{variant}" if variant else "")
            try:
                prof = serve(info, label, c["args"](m, d["kind"], variant), c["port"], c["health"], 600,
                             env=c["env"], mounts=[(MODELS, "/models")])
                if not prof:
                    continue
                mem = MemPeak()
                base = f"http://{SRV}:{c['port']}"
                spec = {"base": base, "model": m, "embed_path": c["embed_path"], "rerank_api": c["rerank_api"],
                        "rerank_path": c["rerank_path"], "cap": 300}
                r = client("embed" if d["kind"] == "embed" else "rerank", spec)
                if d["kind"] == "rerank":
                    n1 = client("probe", {"urls": [base + "/v1/rerank"], "ok": [200, 405]})
                    r["n1_route_present"] = bool(n1.get("ready"))
                r.update({"kind": "measure", "task": "embeddings", "candidate": c["name"], "model": m,
                          "variant": variant, "model_kind": d["kind"], "profile": prof, "mem_peak_gb": mem.done(),
                          "declared_dims": d.get("dims")})
                result(r)
            except Exception as e:
                result({"kind": "measure", "task": "embeddings", "candidate": c["name"], "model": m,
                        "variant": variant, "error": str(e)[:400]})
            finally:
                rm(SRV)
        sh(["docker", "rmi", "-f", c["image"]])


# ============================================================== conversion
TESSDATA_FAST = "87416418657359cb625c412a48b6e1d6d41c29bd"
EASYOCR_ARABIC = "https://github.com/JaidedAI/EasyOCR/releases/download/pre-v1.1.6/arabic.zip"

CONV_CANDIDATES = [
    dict(name="docling-serve", image="quay.io/docling-project/docling-serve-cpu:v1.38.2",
         source="github.com/docling-project/docling-serve", source_licence="MIT", port=5001, health=["/health"],
         api="docling", args=[], env={"DOCLING_SERVE_MAX_SYNC_WAIT": "3600", "DOCLING_SERVE_ENABLE_UI": "false",
                                      "HF_HUB_OFFLINE": "1", "OMP_NUM_THREADS": "4",
                                      # writable places under a read-only root: all on the /tmp emptyDir
                                      "HOME": "/tmp", "XDG_CACHE_HOME": "/tmp/.cache", "MPLCONFIGDIR": "/tmp/mpl",
                                      "EASYOCR_MODULE_PATH": "/tmp/easyocr"},
         configs=[("rapidocr", "rapidocr"), ("tesserocr", "tesserocr"), ("tesseract", "tesseract"),
                  ("easyocr", "easyocr")],
         ar_configs=[("rapidocr", "rapidocr"), ("tesserocr", "tesserocr"), ("tesseract", "tesseract"),
                     ("easyocr", "easyocr")]),
    dict(name="tika", image="apache/tika:3.3.3.0-full", source="github.com/apache/tika-docker",
         source_licence="Apache-2.0", port=9998, health=["/version"], api="tika",
         args=["-c", "/config/tika-config.xml"], env={}, configs=[("tesseract", "eng")],
         ar_configs=[("tesseract", "ara")]),
    dict(name="unstructured-api", image="quay.io/unstructured-io/unstructured-api:0.1.16",
         source="github.com/Unstructured-IO/unstructured-api", source_licence="Apache-2.0", port=8000,
         health=["/healthcheck"], api="unstructured", args=[],
         env={"HF_HUB_OFFLINE": "1", "DO_NOT_TRACK": "true", "SCARF_NO_ANALYTICS": "true",
              # numba caches compiled functions next to the source unless told otherwise (read-only root)
              "NUMBA_CACHE_DIR": "/tmp/numba"},
         configs=[("hi_res", "eng")], ar_configs=[("hi_res", "ara")]),
]

TIKA_CONFIG = """<?xml version="1.0" encoding="UTF-8"?>
<properties>
  <server>
    <params>
      <taskTimeoutMillis>3600000</taskTimeoutMillis>
      <forkedJvmArgs><arg>-Xmx6g</arg></forkedJvmArgs>
    </params>
  </server>
</properties>
"""

CSS = """
@page { size: A4; margin: 18mm 20mm; }
body { font-family: 'Noto Serif', 'DejaVu Serif', serif; font-size: 11pt; line-height: 1.35; }
body.ar { font-family: 'Noto Naskh Arabic', 'Noto Sans Arabic', serif; font-size: 12pt; }
section { page-break-after: always; break-after: page; }
section:last-child { page-break-after: auto; }
h1 { font-size: 17pt; margin: 0 0 8pt; } h2 { font-size: 13.5pt; margin: 12pt 0 6pt; }
table { border-collapse: collapse; margin: 10pt 0; } td, th { border: 1px solid #000; padding: 3pt 8pt; }
"""


def build_pages(sents, lang, n):
    """Pages with headings, paragraphs, a list and a table. Returns (html, truth per page)."""
    import random
    rnd = random.Random(80 + n)
    rows = [s for s in sents if 40 <= len(s) <= 260]
    i, pages, truth = 0, [], []
    branches = ["Muscat", "Sohar", "Salalah", "Nizwa", "Sur", "Ibri"] if lang == "en" else \
        ["مسقط", "صحار", "صلالة", "نزوى", "صور", "عبري"]
    head = ["Account", "Branch", "Balance"] if lang == "en" else ["الحساب", "الفرع", "الرصيد"]

    def take(k):
        nonlocal i
        out = [rows[(i + j) % len(rows)] for j in range(k)]
        i += k
        return out

    for p in range(1, n + 1):
        t1 = " ".join(take(1)[0].split()[:6]).rstrip(".,،")
        p1 = " ".join(take(3))
        t2 = " ".join(take(1)[0].split()[:5]).rstrip(".,،")
        p2 = " ".join(take(3))
        items = take(3)
        tab = [[f"{rnd.randint(10**9, 10**10 - 1)}", rnd.choice(branches), f"{rnd.randint(100, 99999)}.{rnd.randint(0, 999):03d}"]
               for _ in range(4)]
        p3 = " ".join(take(2))
        h1, h2 = f"{p}. {t1}", f"{p}.1 {t2}"
        trs = "".join("<tr>" + "".join(f"<td>{html.escape(c)}</td>" for c in r) + "</tr>" for r in tab)
        pages.append(
            f"<section><h1>{html.escape(h1)}</h1><p>{html.escape(p1)}</p><h2>{html.escape(h2)}</h2>"
            f"<p>{html.escape(p2)}</p><ul>{''.join(f'<li>{html.escape(x)}</li>' for x in items)}</ul>"
            f"<table><tr>{''.join(f'<th>{h}</th>' for h in head)}</tr>{trs}</table><p>{html.escape(p3)}</p></section>")
        truth.append(" ".join([h1, p1, h2, p2] + items + head + [c for r in tab for c in r] + [p3]))
    d = ' dir="rtl" lang="ar"' if lang == "ar" else ' lang="en"'
    cls = "ar" if lang == "ar" else "en"
    doc = f"<!doctype html><html{d}><head><meta charset='utf-8'><style>{CSS}</style></head>" \
          f"<body class='{cls}'>{''.join(pages)}</body></html>"
    return doc, truth


def scan(html_doc, name):
    """Digital PDF from Chrome, then a simulated scan: 200 dpi grey, slight skew, noise, JPEG."""
    import numpy as np
    from PIL import Image, ImageFilter
    import img2pdf
    work = os.path.join(DATA, "docs", name)
    os.makedirs(work, exist_ok=True)
    src = os.path.join(work, "src.html")
    open(src, "w").write(html_doc)
    chrome = shutil.which("google-chrome") or shutil.which("chromium") or shutil.which("chromium-browser")
    digital = os.path.join(work, "digital.pdf")
    sh([chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-pdf-header-footer",
        f"--print-to-pdf={digital}", "file://" + src], timeout=180, check=True)
    sh(["pdftoppm", "-r", "200", "-gray", "-png", digital, os.path.join(work, "pg")], timeout=600, check=True)
    rnd = np.random.default_rng(80)
    jpgs = []
    for k, png in enumerate(sorted(glob.glob(os.path.join(work, "pg*.png")))):
        im = Image.open(png).convert("L")
        im = im.rotate(0.6 if k % 2 else -0.4, resample=Image.BICUBIC, fillcolor=255)
        a = np.asarray(im).astype(np.float32) + rnd.normal(0, 14, (im.height, im.width))
        im = Image.fromarray(np.clip(a, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(0.6))
        jp = png[:-4] + ".jpg"
        im.save(jp, quality=70, dpi=(200, 200))
        jpgs.append(jp)
    out = os.path.join(DATA, "docs", name + ".pdf")
    open(out, "wb").write(img2pdf.convert(jpgs, layout_fun=img2pdf.get_fixed_dpi_layout_fun((200, 200))))
    _, txt = sh(["pdftotext", out, "-"])
    _, info = sh(["pdfinfo", out])
    pages = int(re.search(r"Pages:\s+(\d+)", info).group(1))
    return out, pages, len(txt.strip())


def parse_output(api, raw):
    """Text in reading order plus structure counts, from each converter's own output."""
    s = {"headings": 0, "tables": 0, "list_items": 0, "pages_marked": None}
    if api == "docling":
        d = json.loads(raw)
        doc = d.get("document") or {}
        md = doc.get("md_content") or ""
        s["status"], s["errors"] = d.get("status"), (d.get("errors") or [])[:3]
        s["server_processing_s"] = d.get("processing_time")
        lines = md.splitlines()
        s["headings"] = sum(1 for l in lines if l.startswith("#"))
        s["tables"] = sum(1 for l in lines if re.match(r"^\|?\s*:?-{3,}", l))
        s["list_items"] = sum(1 for l in lines if re.match(r"^\s*[-*•]\s", l))
        s["pages_marked"] = md.count("<!-- PAGEBREAK -->") + 1 if md else 0
        text = re.sub(r"<!--.*?-->", " ", md)
        text = re.sub(r"^\|?\s*:?-{3,}.*$", " ", text, flags=re.M)
    elif api == "tika":
        x = raw.decode("utf-8", "replace")
        s["pages_marked"] = x.count('<div class="page">')
        body = re.sub(r"(?s)<head>.*?</head>", " ", x)
        text = html.unescape(re.sub(r"<[^>]+>", " ", body))
    else:
        els = json.loads(raw)
        if isinstance(els, dict):
            return str(els)[:2000], dict(s, error=str(els)[:300])
        types = collections.Counter(e.get("type") for e in els)
        s.update({"headings": types.get("Title", 0), "tables": types.get("Table", 0),
                  "list_items": types.get("ListItem", 0), "element_types": dict(types)})
        pages = {(e.get("metadata") or {}).get("page_number") for e in els}
        s["pages_marked"] = len(pages - {None})
        text = "\n".join(e.get("text") or "" for e in els)
    return text, s


def tess_dir(image):
    rc, out = sh(["docker", "run", "--rm", "--network", "none", "--entrypoint", "tesseract", image, "--list-langs"],
                 timeout=300)
    m = re.search(r'"([^"]+)"', out)
    path = m.group(1).rstrip("/") if m else None
    if not path:  # older tesseract does not print the path: use the image's TESSDATA_PREFIX
        _, env = sh(["docker", "image", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", image])
        m = re.search(r"^TESSDATA_PREFIX=(.+)$", env, re.M)
        path = m.group(1).strip().rstrip("/") if m else None
    return path, out.strip().replace("\n", " ")[:300]


def run_conversion():
    rows = fleurs()
    en = [r["raw_transcription"] for r in rows["en"]]
    ar = [r["raw_transcription"] for r in rows["ar"]]
    docs = {}
    for name, sents, lang, n in (("en20", en, "en", 20), ("ar3", ar, "ar", 3), ("warm1", en[::-1], "en", 1)):
        h, truth = build_pages(sents, lang, n)
        path, pages, textlayer = scan(h, name)
        docs[name] = {"path": path, "pages": pages, "truth": " ".join(truth), "lang": lang}
        result({"kind": "doc", "name": name, "pages": pages, "text_layer_chars": textlayer,
                "truth_words": len(norm(" ".join(truth), lang).split()), "bytes": os.path.getsize(path)})
    ara = download(f"https://github.com/tesseract-ocr/tessdata_fast/raw/{TESSDATA_FAST}/ara.traineddata",
                   os.path.join(MODELS, "tessdata", "ara.traineddata"))
    z = download(EASYOCR_ARABIC, os.path.join(MODELS, "easyocr", "arabic.zip"))
    zipfile.ZipFile(z).extractall(os.path.join(MODELS, "easyocr"))
    arabic_pth = os.path.join(MODELS, "easyocr", "arabic.pth")
    cfgdir = os.path.join(DATA, "tika-config")
    os.makedirs(cfgdir, exist_ok=True)
    open(os.path.join(cfgdir, "tika-config.xml"), "w").write(TIKA_CONFIG)
    os.makedirs(os.path.join(OUT, "conv"), exist_ok=True)
    os.chmod(os.path.join(OUT, "conv"), 0o777)
    setup_network()
    for c in CONV_CANDIDATES:
        info = pull(c["image"])
        tdir, langs = tess_dir(c["image"])
        result({"kind": "candidate", "task": "conversion", "name": c["name"], "image": c["image"],
                "source": c["source"], "source_licence": c["source_licence"], "digest": info["digest"],
                "image_user": info["user"], "image_nonroot": image_runs_nonroot(info), "size_gb": info["size_gb"],
                "tessdata_dir": tdir, "tesseract_langs": langs})
        mounts = [(ara, f"{tdir}/ara.traineddata")] if tdir else []
        if c["name"] == "docling-serve":
            art = c["env"].get("DOCLING_SERVE_ARTIFACTS_PATH", "/opt/app-root/src/.cache/docling/models")
            mounts.append((arabic_pth, f"{art}/EasyOcr/arabic.pth"))
            _, ls = sh(["docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", c["image"], "-c",
                        f"ls {art}; ls {art}/EasyOcr {art}/RapidOcr 2>/dev/null | head -40; "
                        "pip list 2>/dev/null | grep -i -E '^(docling|easyocr|rapidocr|tesserocr|torch) '"])
            repos = sorted({x.replace("--", "/") for x in re.findall(r"\b([\w.-]+--[\w.-]+)\b", ls)})
            result({"kind": "docling-models", "listing": ls[-1500:],
                    "hf_licences": {r: hf_model_licence(r)[0] for r in repos}})
        if c["name"] == "tika":
            mounts.append((cfgdir, "/config"))
            _, fonts = sh(["docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", c["image"], "-c",
                           "dpkg -l | grep -i -E 'mscorefonts|tesseract-ocr ' | awk '{print $2, $3}'"])
            result({"kind": "tika-packages", "packages": fonts.strip()[:500]})
        label = c["name"]
        try:
            prof = serve(info, label, c["args"], c["port"], c["health"], 900, env=c["env"], mounts=mounts)
            if not prof:
                continue
            base = f"http://{SRV}:{c['port']}"
            if c["api"] == "docling":
                cap = client("probe", {"urls": [base + "/v1/capabilities"], "body": True, "timeout": 30})
                result({"kind": "docling-capabilities", "ready": cap.get("ready"), "body": cap.get("body")})
            mem = MemPeak()
            for doc, cfgs in (("en20", c["configs"]), ("ar3", c["ar_configs"])):
                for clabel, val in cfgs:
                    if not running():  # a failed engine took the server down: start it again
                        result({"kind": "restart", "candidate": c["name"], "before": f"{clabel}-{doc}",
                                "log": sh(["docker", "logs", "--tail", "30", SRV])[1][-2000:]})
                        prof = serve(info, label, c["args"], c["port"], c["health"], 900, env=c["env"],
                                     mounts=mounts)
                        if not prof:
                            break
                    lang = docs[doc]["lang"]
                    spec = {"base": base, "api": c["api"]}
                    if c["api"] == "docling":
                        spec.update({"ocr": val, "lang": [lang], "ocr_field": "ocr_engine"})
                    elif c["api"] == "tika":
                        spec["lang"] = val
                    else:
                        spec["lang"] = [val]
                    tag = f"{c['name']}-{clabel}-{doc}"
                    warm = client("convert", dict(spec, file="/data/docs/warm1.pdf", out=f"/out/conv/{tag}.warm"))
                    if warm.get("code") != 200 and c["api"] == "docling":
                        m0 = sh(["docker", "logs", "--tail", "30", SRV])[1][-2500:]
                        result({"kind": "docling-ocr-field-retry", "tag": tag, "code": warm.get("code"),
                                "head": warm.get("head"), "server_log": m0})
                        spec["ocr_field"] = "ocr_preset"
                        warm = client("convert", dict(spec, file="/data/docs/warm1.pdf",
                                                      out=f"/out/conv/{tag}.warm"))
                    r = client("convert", dict(spec, file=f"/data/docs/{doc}.pdf", out=f"/out/conv/{tag}.raw"))
                    m = {"kind": "measure", "task": "conversion", "candidate": c["name"], "config": clabel,
                         "doc": doc, "pages": docs[doc]["pages"], "profile": prof, "warmup_s": warm.get("seconds"),
                         "warmup_code": warm.get("code"), **{k: r.get(k) for k in ("code", "seconds", "head", "error")}}
                    if r.get("code") == 200:
                        raw = open(os.path.join(OUT, "conv", f"{tag}.raw"), "rb").read()
                        try:
                            text, st = parse_output(c["api"], raw)
                            m.update(st)
                            m.update(scores(text, docs[doc]["truth"], lang))
                            m["sample"] = norm(text, lang)[:160]
                        except Exception as e:
                            m["parse_error"] = str(e)[:300]
                        m["pages_per_min"] = round(docs[doc]["pages"] / (r["seconds"] / 60.0), 2)
                    if r.get("code") != 200:
                        m["server_log"] = sh(["docker", "logs", "--tail", "30", SRV])[1][-2500:]
                    result(m)
            result({"kind": "memory", "task": "conversion", "candidate": c["name"], "mem_peak_gb": mem.done()})
        except Exception as e:
            result({"kind": "measure", "task": "conversion", "candidate": c["name"], "error": str(e)[:400]})
        finally:
            rm(SRV)
            sh(["docker", "rmi", "-f", c["image"]])


# ============================================================== transcription
ASR_MODELS = {
    "small": dict(ct2="Systran/faster-whisper-small", ct2_rev="536b0662742c02347bc0e980a01041f333bce120",
                  ggml="ggml-small-q8_0.bin"),
    "large-v3-turbo": dict(ct2="mobiuslabsgmbh/faster-whisper-large-v3-turbo",
                           ct2_rev="0a363e9161cbc7ed1431c9597a8ceaf0c4f78fcf", ggml="ggml-large-v3-turbo-q8_0.bin",
                           # the same weights in the repository speaches lists as supported
                           ct2_alt="deepdml/faster-whisper-large-v3-turbo-ct2",
                           ct2_alt_rev="4df90f75321148c3a29a9e2351b7ddf8f5b115a8"),
}
GGML_REPO, GGML_REV = "ggerganov/whisper.cpp", "5359861c739e955e79d9a303bcbc70fb988958b1"

ASR_CANDIDATES = [
    dict(name="speaches", image="ghcr.io/speaches-ai/speaches:0.8.3-cpu", source="github.com/speaches-ai/speaches",
         source_licence="MIT", port=8000, health=["/health"], api="openai", per_model=False,
         env=lambda m: {"HF_HUB_CACHE": "/models/hf", "HF_HUB_OFFLINE": "1", "WHISPER__COMPUTE_TYPE": "int8",
                        "WHISPER__INFERENCE_DEVICE": "cpu", "ENABLE_UI": "false", "OMP_NUM_THREADS": "4"},
         args=lambda m: [], entrypoint=None, model_id=lambda m: ASR_MODELS[m].get("ct2_alt", ASR_MODELS[m]["ct2"]),
         variants=[("default", []), ("no-vad", [("vad_filter", "false")])]),
    dict(name="whisper-asr-webservice", image="onerahmet/openai-whisper-asr-webservice:v1.10.0",
         source="github.com/ahmetoner/whisper-asr-webservice", source_licence="MIT", port=9000, health=["/docs"],
         api="asrws", per_model=True,
         env=lambda m: {"ASR_ENGINE": "faster_whisper", "ASR_MODEL": m, "ASR_MODEL_PATH": "/models/hf",
                        "ASR_DEVICE": "cpu", "ASR_QUANTIZATION": "int8", "HF_HUB_OFFLINE": "1"},
         args=lambda m: [], entrypoint=None, model_id=lambda m: m, variants=[("default", [])]),
    dict(name="whisper.cpp", image="ghcr.io/ggml-org/whisper.cpp:main-d1be6fde11ac6e0407606b4e42fe72d34add8037",
         source="github.com/ggml-org/whisper.cpp", source_licence="MIT", port=8080, health=["/"], api="openai",
         per_model=True, env=lambda m: {},
         args=lambda m: [f"whisper-server -m /models/ggml/{ASR_MODELS[m]['ggml']} --host 0.0.0.0 --port 8080 -t 4 "
                         "--inference-path /v1/audio/transcriptions"],
         entrypoint=None, model_id=lambda m: m, variants=[("default", [])]),
]


def prep_audio(rows):
    out = {}
    os.makedirs(os.path.join(OUT, "asr"), exist_ok=True)
    adir = os.path.join(DATA, "audio")
    os.makedirs(adir, exist_ok=True)
    for lang in ("en", "ar"):
        picked = [r for r in sorted(rows[lang], key=lambda r: r["id"]) if 5 * 16000 <= r["num_samples"] <= 16 * 16000][:20]
        lst = os.path.join(adir, f"{lang}.txt")
        with open(lst, "w") as f:
            for k, r in enumerate(picked):
                p = os.path.join(adir, f"{lang}-{k:02d}.wav")
                open(p + ".src", "wb").write(r["audio"]["bytes"])
                sh(["ffmpeg", "-y", "-i", p + ".src", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", p], check=True)
                f.write(f"file '{p}'\nfile '{adir}/gap.wav'\n")
        sh(["ffmpeg", "-y", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "0.4", "-ar", "16000", "-ac", "1",
            "-c:a", "pcm_s16le", f"{adir}/gap.wav"], check=True)
        dst = os.path.join(adir, f"{lang}.wav")
        sh(["ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", lst, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
            dst], check=True, timeout=600)
        _, d = sh(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", dst])
        out[lang] = {"file": f"/data/audio/{lang}.wav", "seconds": float(d.strip()), "clips": len(picked),
                     "ref": " ".join(r["raw_transcription"] for r in picked),
                     "ref_fleurs_norm": " ".join(r["transcription"] for r in picked)}
        open(os.path.join(OUT, "asr", f"ref-{lang}.txt"), "w").write(out[lang]["ref"])
        print("PRD080-TEXT", f"ref-{lang}", json.dumps(out[lang]["ref"], ensure_ascii=False), flush=True)
        result({"kind": "audio", "lang": lang, "clips": len(picked), "seconds": round(out[lang]["seconds"], 1),
                "ref_words": len(norm(out[lang]["ref"], lang).split())})
    sh(["ffmpeg", "-y", "-i", f"{adir}/en-00.wav", "-t", "5", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le",
        f"{adir}/warm.wav"], check=True)
    return out


def run_transcription():
    rows = fleurs(with_audio=True)
    audio = prep_audio(rows)
    del rows
    for m, d in ASR_MODELS.items():
        snapshot(d["ct2"], d["ct2_rev"], cache=os.path.join(MODELS, "hf"))
        if d.get("ct2_alt"):
            snapshot(d["ct2_alt"], d["ct2_alt_rev"], cache=os.path.join(MODELS, "hf"))
    snapshot(GGML_REPO, GGML_REV, local=os.path.join(MODELS, "ggml"), allow=[d["ggml"] for d in ASR_MODELS.values()])
    for p in glob.glob(os.path.join(MODELS, "**"), recursive=True):
        os.chmod(p, 0o755 if os.path.isdir(p) else 0o644)
    setup_network()
    for c in ASR_CANDIDATES:
        info = pull(c["image"])
        result({"kind": "candidate", "task": "transcription", "name": c["name"], "image": c["image"],
                "source": c["source"], "source_licence": c["source_licence"], "digest": info["digest"],
                "image_user": info["user"], "image_nonroot": image_runs_nonroot(info), "size_gb": info["size_gb"]})
        groups = [[m] for m in ASR_MODELS] if c["per_model"] else [list(ASR_MODELS)]
        for group in groups:
            label = f"{c['name']}/{'+'.join(group)}"
            try:
                prof = serve(info, label, c["args"](group[0]), c["port"], c["health"], 600, env=c["env"](group[0]),
                             mounts=[(MODELS, "/models")], entrypoint=c["entrypoint"])
                if not prof:
                    continue
                base = f"http://{SRV}:{c['port']}"
                mem = MemPeak()
                for m, (variant, extra) in [(m, v) for m in group for v in c["variants"]]:
                    spec = {"base": base, "api": c["api"], "model": c["model_id"](m), "extra": extra}
                    warm = client("transcribe", dict(spec, file="/data/audio/warm.wav", language="en"))
                    for lang in ("en", "ar"):
                        r = client("transcribe", dict(spec, file=audio[lang]["file"], language=lang))
                        x = {"kind": "measure", "task": "transcription", "candidate": c["name"], "model": m,
                             "model_id": spec["model"], "variant": variant,
                             "lang": lang, "profile": prof, "warmup_s": warm.get("seconds"),
                             "warmup_code": warm.get("code"), "audio_s": round(audio[lang]["seconds"], 1),
                             **{k: r.get(k) for k in ("code", "seconds", "head", "error")}}
                        if r.get("code") == 200 and r.get("seconds"):
                            x["audio_s_per_s"] = round(audio[lang]["seconds"] / r["seconds"], 3)
                            x.update(scores(r.get("text", ""), audio[lang]["ref"], lang))
                            x["wer_vs_fleurs_normalised"] = scores(r.get("text", ""), audio[lang]["ref_fleurs_norm"],
                                                                   lang)["wer"]
                            x["sample"] = (r.get("text") or "")[:160]
                            tag = f"{c['name']}-{m}-{variant}-{lang}"
                            open(os.path.join(OUT, "asr", tag + ".txt"), "w").write(r.get("text") or "")
                            print("PRD080-TEXT", tag, json.dumps(r.get("text") or "", ensure_ascii=False), flush=True)
                        result(x)
                result({"kind": "memory", "task": "transcription", "candidate": c["name"], "models": group,
                        "mem_peak_gb": mem.done()})
            except Exception as e:
                result({"kind": "measure", "task": "transcription", "candidate": c["name"], "error": str(e)[:400]})
            finally:
                rm(SRV)
        sh(["docker", "rmi", "-f", c["image"]])


# ============================================================== summary
def summarise(task):
    rs = [json.loads(l) for l in open(os.path.join(OUT, "results.jsonl"))]
    print("\n===== PRD-080 summary:", task, "=====")
    print("applied floors:", json.dumps(FLOORS), "rtl bars:", json.dumps(RTL))
    for r in rs:
        if r["kind"] in ("candidate", "start", "measure", "memory"):
            keep = {k: v for k, v in r.items() if k not in ("failed_before", "sample", "listing")}
            if r.get("failed_before"):
                keep["failed_profiles"] = [f"{f['profile']}: {f['why']}" for f in r["failed_before"]]
            print(json.dumps(keep, ensure_ascii=False, sort_keys=True))


def main():
    task = sys.argv[1]
    os.makedirs(OUT, exist_ok=True)
    os.makedirs(MODELS, exist_ok=True)
    result({"kind": "floors", "floors": FLOORS, "rtl": RTL})
    try:
        {"embeddings": run_embeddings, "conversion": run_conversion, "transcription": run_transcription}[task]()
    finally:
        rm(SRV)
        rm(CLI)
        summarise(task)


if __name__ == "__main__":
    main()
