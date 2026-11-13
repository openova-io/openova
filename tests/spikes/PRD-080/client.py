#!/usr/bin/env python3
"""Lab test PRD-080 (never merged): the HTTP client.

It runs inside a container on the same internal Docker network as the server
under test, so the server never needs a route out. Standard library only.

    python3 client.py <command> '<json spec>'

Every command prints one JSON object on stdout. Data files are read from
/data and large outputs are written to /out.
"""
import json
import math
import random
import statistics
import sys
import time
import urllib.error
import urllib.request
import uuid


def http(method, url, body=None, headers=None, timeout=3600):
    h = dict(headers or {})
    data = None
    if body is not None and not isinstance(body, (bytes, bytearray)):
        data = json.dumps(body).encode()
        h.setdefault("Content-Type", "application/json")
    elif body is not None:
        data = bytes(body)
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw, code = r.read(), r.status
    except urllib.error.HTTPError as e:
        raw, code = e.read(), e.code
    return code, raw, time.perf_counter() - t0


def multipart(fields, files):
    b = uuid.uuid4().hex
    out = []
    for k, v in fields:
        out.append(f'--{b}\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode())
    for k, fname, content, ctype in files:
        out.append(f'--{b}\r\nContent-Disposition: form-data; name="{k}"; filename="{fname}"\r\n'
                   f'Content-Type: {ctype}\r\n\r\n'.encode() + content + b"\r\n")
    out.append(f"--{b}--\r\n".encode())
    return b"".join(out), f"multipart/form-data; boundary={b}"


def pct(xs, p):
    if not xs:
        return None
    s = sorted(xs)
    k = max(0, min(len(s) - 1, int(math.ceil(p / 100.0 * len(s))) - 1))
    return round(s[k], 4)


def short(raw):
    return raw[:400].decode("utf-8", "replace")


# ---------------------------------------------------------------- probe
def cmd_probe(spec):
    for url in spec["urls"]:
        try:
            code, raw, _ = http("GET", url, timeout=spec.get("timeout", 3))
        except Exception as e:  # connection refused, timeout
            code, raw = 0, str(e).encode()
        if code in spec.get("ok", [200]):
            return {"ready": True, "url": url, "code": code}
    return {"ready": False, "code": code, "body": short(raw)}


# ---------------------------------------------------------------- embeddings
def embed(spec, texts):
    code, raw, dt = http("POST", spec["base"] + spec["embed_path"], {"model": spec["model"], "input": texts})
    if code != 200:
        raise RuntimeError(f"embeddings HTTP {code}: {short(raw)}")
    d = json.loads(raw)
    vecs = [x["embedding"] for x in sorted(d["data"], key=lambda x: x["index"])]
    toks = (d.get("usage") or {}).get("prompt_tokens")
    return vecs, toks, dt


def unit(v):
    n = math.sqrt(sum(x * x for x in v)) or 1.0
    return [x / n for x in v]


def recall_at_1(qs, ds):
    hit = 0
    for i, q in enumerate(qs):
        best, bi = -2.0, -1
        for j, d in enumerate(ds):
            s = sum(a * b for a, b in zip(q, d))
            if s > best:
                best, bi = s, j
        hit += bi == i
    return round(hit / len(qs), 4)


def cmd_embed(spec):
    data = json.load(open("/data/embed.json"))
    res = {"model": spec["model"]}
    vecs, _, _ = embed(spec, data["passages"][:2])  # warm-up
    res["dims"] = len(vecs[0])
    # Bulk: sequential batches of passages of about 500 tokens, time-boxed.
    batch, cap = spec.get("batch", 16), spec.get("cap", 300)
    done, toks, t0, lat = 0, 0, time.perf_counter(), []
    while done < len(data["passages"]) and time.perf_counter() - t0 < cap:
        chunk = data["passages"][done:done + batch]
        _, t, dt = embed(spec, chunk)
        lat.append(dt)
        toks += t or 0
        done += len(chunk)
    secs = time.perf_counter() - t0
    res["bulk"] = {"texts": done, "seconds": round(secs, 2), "texts_per_s": round(done / secs, 3),
                   "batch": batch, "reported_tokens": toks,
                   "tokens_per_text": round(toks / done, 1) if toks else None,
                   "batch_p50_s": pct(lat, 50)}
    # Single short queries, one request each.
    lat = []
    for q in data["queries"]:
        _, _, dt = embed(spec, [q])
        lat.append(dt)
    res["query"] = {"n": len(lat), "p50_ms": round(pct(lat, 50) * 1000, 1), "p95_ms": round(pct(lat, 95) * 1000, 1)}
    # Right-to-left sample: FLEURS parallel sentences, Arabic <-> English.
    en, ar = [], []
    for i in range(0, len(data["pairs"]), 16):
        part = data["pairs"][i:i + 16]
        en += [unit(v) for v in embed(spec, [p["en"] for p in part])[0]]
        ar += [unit(v) for v in embed(spec, [p["ar"] for p in part])[0]]
    res["rtl"] = {"pairs": len(en), "ar_to_en_r1": recall_at_1(ar, en), "en_to_ar_r1": recall_at_1(en, ar)}
    return res


# ---------------------------------------------------------------- rerank
def rerank(spec, query, docs):
    if spec["rerank_api"] == "tei":
        body = {"query": query, "texts": docs, "truncate": True}
    else:
        body = {"model": spec["model"], "query": query, "documents": docs, "top_n": len(docs)}
    code, raw, dt = http("POST", spec["base"] + spec["rerank_path"], body)
    if code != 200:
        raise RuntimeError(f"rerank HTTP {code}: {short(raw)}")
    d = json.loads(raw)
    rows = d if isinstance(d, list) else d["results"]
    scores = {r["index"]: r.get("relevance_score", r.get("score")) for r in rows}
    order = sorted(scores, key=lambda i: -scores[i])
    return order, dt


def cmd_rerank(spec):
    data = json.load(open("/data/embed.json"))
    P, Q = data["passages"], data["queries"]
    rerank(spec, Q[0], P[:2])  # warm-up
    res = {"model": spec["model"]}
    for n, reps in ((8, spec.get("reps8", 10)), (32, spec.get("reps32", 3))):
        lat, t0 = [], time.perf_counter()
        for r in range(reps):
            docs = [P[(r * n + k) % len(P)] for k in range(n)]
            _, dt = rerank(spec, Q[r % len(Q)], docs)
            lat.append(dt)
            if time.perf_counter() - t0 > spec.get("cap", 300):
                break
        res[f"docs{n}"] = {"n": len(lat), "p50_s": pct(lat, 50), "p95_s": pct(lat, 95),
                           "pairs_per_s": round(n * len(lat) / sum(lat), 3)}
    # Right-to-left sample: an Arabic query against ten English candidates, one of which is its translation.
    rnd = random.Random(80)
    pairs = data["pairs"]
    hits = {"ar_q": 0, "en_q": 0}
    nq = min(spec.get("rtl_queries", 20), len(pairs))
    t0 = time.perf_counter()
    done = 0
    for i in range(nq):
        others = rnd.sample([j for j in range(len(pairs)) if j != i], 9)
        cand = [i] + others
        rnd.shuffle(cand)
        order, _ = rerank(spec, pairs[i]["ar"], [pairs[j]["en"] for j in cand])
        hits["ar_q"] += cand[order[0]] == i
        order, _ = rerank(spec, pairs[i]["en"], [pairs[j]["ar"] for j in cand])
        hits["en_q"] += cand[order[0]] == i
        done += 1
        if time.perf_counter() - t0 > spec.get("cap", 300):
            break
    res["rtl"] = {"queries": done, "ar_query_top1": round(hits["ar_q"] / done, 4),
                  "en_query_top1": round(hits["en_q"] / done, 4)}
    return res


# ---------------------------------------------------------------- conversion
def cmd_convert(spec):
    pdf = open(spec["file"], "rb").read()
    api, base = spec["api"], spec["base"]
    if api == "docling":
        fields = [("to_formats", "md"), ("to_formats", "json"), ("do_ocr", "true"), ("force_ocr", "true"),
                  ("image_export_mode", "placeholder"), ("include_images", "false"),
                  ("md_page_break_placeholder", "<!-- PAGEBREAK -->"), ("abort_on_error", "false")]
        fields += [(spec.get("ocr_field", "ocr_engine"), spec["ocr"])]
        fields += [("ocr_lang", x) for x in spec["lang"]]
        body, ctype = multipart(fields, [("files", "doc.pdf", pdf, "application/pdf")])
        code, raw, dt = http("POST", base + "/v1/convert/file", body, {"Content-Type": ctype})
    elif api == "tika":
        hdr = {"Content-Type": "application/pdf", "Accept": "text/html", "X-Tika-PDFOcrStrategy": "ocr_only",
               "X-Tika-OCRLanguage": spec["lang"], "X-Tika-OCRtimeoutSeconds": "1800"}
        code, raw, dt = http("PUT", base + "/tika", pdf, hdr)
    else:  # unstructured
        fields = [("strategy", "hi_res"), ("pdf_infer_table_structure", "true"), ("output_format", "application/json")]
        fields += [("languages", x) for x in spec["lang"]]
        body, ctype = multipart(fields, [("files", "doc.pdf", pdf, "application/pdf")])
        code, raw, dt = http("POST", base + "/general/v0/general", body, {"Content-Type": ctype})
    open(spec["out"], "wb").write(raw)
    return {"code": code, "seconds": round(dt, 2), "bytes": len(raw), "head": short(raw) if code != 200 else ""}


# ---------------------------------------------------------------- transcription
def cmd_transcribe(spec):
    audio = open(spec["file"], "rb").read()
    if spec["api"] == "openai":
        fields = [("model", spec["model"]), ("response_format", "json")]
        if spec.get("language"):
            fields.append(("language", spec["language"]))
        body, ctype = multipart(fields, [("file", "audio.wav", audio, "audio/wav")])
        code, raw, dt = http("POST", spec["base"] + spec.get("path", "/v1/audio/transcriptions"), body,
                             {"Content-Type": ctype})
    else:  # whisper-asr-webservice
        q = f"?task=transcribe&output=json&encode=true&language={spec['language']}"
        body, ctype = multipart([], [("audio_file", "audio.wav", audio, "audio/wav")])
        code, raw, dt = http("POST", spec["base"] + "/asr" + q, body, {"Content-Type": ctype})
    text = ""
    if code == 200:
        try:
            text = json.loads(raw).get("text", "")
        except Exception:
            text = raw.decode("utf-8", "replace")
    return {"code": code, "seconds": round(dt, 2), "text": text, "head": short(raw) if code != 200 else ""}


def main():
    cmd, spec = sys.argv[1], json.loads(sys.argv[2])
    fn = {"probe": cmd_probe, "embed": cmd_embed, "rerank": cmd_rerank,
          "convert": cmd_convert, "transcribe": cmd_transcribe}[cmd]
    try:
        out = fn(spec)
    except Exception as e:
        out = {"error": f"{type(e).__name__}: {e}"[:600]}
    print(json.dumps(out, ensure_ascii=False))


if __name__ == "__main__":
    main()
