#!/usr/bin/env python3
"""Lab test PRD-060: vector search with row-level trimming on the platform's PostgreSQL.

Runs only inside the CI job, against the PostgreSQL container that pg.sh starts
on 127.0.0.1:5432. Subcommands, in workflow order:

  facts      the image's `vector` extension: present? version? iterative index scans?
  schema     the store's tables, roles and row-level security (schema.sql), plus a
             small check of how PostgreSQL treats a policy on `document` that reads
             `document` itself
  load       one million real 1,024-dimension chunks (Wikipedia paragraphs embedded
             by a 1,024-dimension multilingual model, a public dataset), their
             documents (articles), 1,000 sources and the grants; 1,000 held-out
             query vectors
  build      the design's two indexes, timed; relation sizes
  measure    recall at 10 against an exact scan and the latency of the design's
             search transaction, for two principals that each read 1% of documents
  summarize  summary.md and the outcome letter, computed from the JSON files

Principals (both read one percent of documents):
  p1  through source grants: group:team-a is reader on 10 of the 1,000 sources;
      documents inherit (the design's normal read path)
  p2  through per-document reader lists: user:p2 is on the own reader list of 1%
      of documents, spread over all sources, and holds no source grant (the
      upload-with-reader-fields path; the source filter then trims almost nothing
      and row-level security does the trimming)
"""
import argparse
import hashlib
import json
import re
import sys
import time
import uuid
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

DIM = 1024
SOURCES = 1000
P1_SOURCES = [f"src-{i:04d}" for i in range(10)]
PRINCIPALS = {"p1": "{user:p1,group:team-a}", "p2": "{user:p2}"}
K = 10
RECALL_MIN = 0.95
P95_MAX_MS = 200.0

# The search statement of the design, all values bound.
SEARCH = (
    "SELECT c.id, c.document_id, c.source_id, c.page, c.text, 1 - (c.embedding <=> %(q)s) AS score\n"
    "FROM chunk c\n"
    "WHERE c.source_id = ANY (%(src)s)\n"
    "ORDER BY c.embedding <=> %(q)s\n"
    "LIMIT %(k)s"
)
SEARCH_LITERAL = (SEARCH.replace("%(q)s", "{q}::vector")
                  .replace("%(src)s", "{src}::text[]").replace("%(k)s", "{k}"))
SET_PRINCIPALS = "SELECT set_config('cortex.principals', %s, true)"


def connect(db="cortex", user="postgres", vector=False):
    import psycopg
    c = psycopg.connect(f"host=127.0.0.1 port=5432 dbname={db} user={user}", autocommit=True)
    if vector:
        from pgvector.psycopg import register_vector
        register_vector(c)
    return c


def h32(s):
    return int(hashlib.md5(s.encode()).hexdigest()[:8], 16)


def sha(s):
    return hashlib.sha256(s.encode()).hexdigest()


def first_line(e):
    return str(e).strip().splitlines()[0] if str(e).strip() else type(e).__name__


def save(out, name, obj, show=True):
    text = json.dumps(obj, indent=2, default=str)
    (Path(out) / name).write_text(text)
    if show:
        print(f"===== {name}")
        print(text)


def stats(xs):
    import numpy as np
    a = np.asarray(xs, dtype=float)
    if a.size == 0:
        return {"n": 0}
    r = lambda v: round(float(v), 2)
    return {"n": int(a.size), "mean": r(a.mean()), "p50": r(np.percentile(a, 50)),
            "p95": r(np.percentile(a, 95)), "p99": r(np.percentile(a, 99)), "max": r(a.max())}


# ---------------------------------------------------------------- facts
def cmd_facts(a):
    f = {"image": a.image}
    with connect("postgres") as c:
        f["server_version"] = c.execute("SHOW server_version").fetchone()[0]
        f["version"] = c.execute("SELECT version()").fetchone()[0]
        r = c.execute("SELECT default_version FROM pg_available_extensions WHERE name='vector'").fetchone()
        f["vector_available"] = r is not None
        f["vector_default_version"] = r[0] if r else None
        f["vector_versions_installable"] = [x[0] for x in c.execute(
            "SELECT version FROM pg_available_extension_versions WHERE name='vector' ORDER BY 1").fetchall()]
        f["iterative_capable"] = False
        if r:
            c.execute("CREATE EXTENSION vector")
            f["vector_installed"] = c.execute(
                "SELECT extversion FROM pg_extension WHERE extname='vector'").fetchone()[0]
            c.execute("SELECT '[1,2,3]'::vector <=> '[1,2,4]'::vector")  # loads the library
            try:
                c.execute("SET hnsw.iterative_scan = relaxed_order")
                f["iterative_scan_after_set"] = c.execute("SHOW hnsw.iterative_scan").fetchone()[0]
            except Exception as e:  # older pgvector: the setting does not exist
                f["iterative_scan_error"] = first_line(e)
            f["hnsw_settings"] = {n: s for n, s in c.execute(
                "SELECT name, setting FROM pg_settings WHERE name LIKE 'hnsw.%' ORDER BY 1").fetchall()}
            ver = tuple(int(x) for x in re.findall(r"\d+", f["vector_installed"])[:3])
            f["iterative_capable"] = ver >= (0, 8, 0) and f.get("iterative_scan_after_set") == "relaxed_order"
        f["other_extensions_available"] = [x[0] for x in c.execute(
            "SELECT name FROM pg_available_extensions WHERE name IN ('pgaudit','pg_failover_slots','pg_stat_statements') ORDER BY 1").fetchall()]
    save(a.out, "facts.json", f)


# ---------------------------------------------------------------- schema
POLICYCHECK_SETUP = """
CREATE TABLE source (id text PRIMARY KEY);
CREATE TABLE source_acl (
  source_id text NOT NULL REFERENCES source ON DELETE CASCADE,
  principal text NOT NULL, role text NOT NULL CHECK (role IN ('reader','manager')),
  PRIMARY KEY (source_id, principal, role));
CREATE TABLE document (id uuid PRIMARY KEY, source_id text NOT NULL REFERENCES source ON DELETE CASCADE,
  acl_inherit boolean NOT NULL DEFAULT true);
CREATE TABLE document_acl (document_id uuid NOT NULL REFERENCES document ON DELETE CASCADE,
  principal text NOT NULL, PRIMARY KEY (document_id, principal));
CREATE TABLE chunk (id bigserial PRIMARY KEY,
  document_id uuid NOT NULL REFERENCES document ON DELETE CASCADE, source_id text NOT NULL);
GRANT USAGE ON SCHEMA public TO cortex_query;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO cortex_query;
INSERT INTO source VALUES ('s1'), ('s2');
INSERT INTO source_acl VALUES ('s1', 'user:a', 'reader');
INSERT INTO document VALUES ('00000000-0000-0000-0000-000000000001', 's1', true),
                            ('00000000-0000-0000-0000-000000000002', 's2', true);
INSERT INTO chunk (document_id, source_id) VALUES ('00000000-0000-0000-0000-000000000001', 's1'),
                                                  ('00000000-0000-0000-0000-000000000002', 's2');
ALTER TABLE chunk ENABLE ROW LEVEL SECURITY;
ALTER TABLE chunk FORCE ROW LEVEL SECURITY;
ALTER TABLE document ENABLE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
"""

# Literal reading of "the same pair of policies on document, with document.id in
# place of chunk.document_id": the chunk policy's body with that one substitution.
DOCUMENT_READ_LITERAL = """CREATE POLICY document_read ON document FOR SELECT TO cortex_query USING (
  EXISTS (SELECT 1 FROM document d WHERE d.id = document.id AND (
    EXISTS (SELECT 1 FROM source_acl s
        WHERE s.source_id = d.source_id
          AND (d.acl_inherit OR s.role = 'manager')
          AND s.principal = ANY (current_setting('cortex.principals')::text[]))
    OR EXISTS (SELECT 1 FROM document_acl a
        WHERE a.document_id = d.id
          AND a.principal = ANY (current_setting('cortex.principals')::text[])))))"""


def policy_from(schema_text, name):
    m = re.search(r"CREATE POLICY %s ON .*?\);\n" % name, schema_text, re.S)
    return m.group(0).strip().rstrip(";")


def count_as_query_role(c, table, principals="{user:a}"):
    try:
        with c.transaction():
            c.execute("SET LOCAL ROLE cortex_query")
            c.execute(SET_PRINCIPALS, (principals,))
            return {"rows": c.execute(f"SELECT count(*) FROM {table}").fetchone()[0]}
    except Exception as e:
        return {"error": first_line(e)}


def cmd_schema(a):
    schema_text = Path(a.sql).read_text()
    with connect("postgres") as c:
        c.execute("CREATE ROLE cortex_query LOGIN")
        c.execute("CREATE ROLE cortex_index LOGIN")
        c.execute("CREATE DATABASE cortex")
        c.execute("CREATE DATABASE policycheck")
    with connect("cortex") as c:
        c.execute(schema_text)
        pols = c.execute("SELECT tablename, policyname, cmd, roles::text FROM pg_policies ORDER BY 1, 2").fetchall()
    res = {"policies_in_store": [list(p) for p in pols]}
    with connect("policycheck") as c:
        c.execute(POLICYCHECK_SETUP)
        c.execute(policy_from(schema_text, "chunk_read"))
        c.execute(DOCUMENT_READ_LITERAL)
        res["literal_document_policy"] = {
            "document": count_as_query_role(c, "document"), "chunk": count_as_query_role(c, "chunk")}
        c.execute("DROP POLICY document_read ON document")
        c.execute(policy_from(schema_text, "document_read"))
        res["self_column_document_policy"] = {
            "document": count_as_query_role(c, "document"), "chunk": count_as_query_role(c, "chunk"),
            "expected_rows": 1,
            "empty_principal_list_chunk": count_as_query_role(c, "chunk", "{}")}
    with connect("policycheck", user="cortex_query") as c:  # fresh session, setting never set
        try:
            c.execute("SELECT count(*) FROM chunk")
            res["no_setting_chunk"] = "returned a result"
        except Exception as e:
            res["no_setting_chunk"] = {"error": first_line(e)}
    save(a.out, "policycheck.json", res)


# ---------------------------------------------------------------- load
def cmd_load(a):
    import numpy as np
    import pyarrow.compute as pc
    import pyarrow.parquet as pq

    t0 = time.time()
    files = sorted(Path(a.corpus).glob("en-*.parquet"))
    urls, titles, taken = [], {}, []
    for f in files:
        t = pq.read_table(f, columns=["url", "title"])
        n = min(t.num_rows, a.rows - len(urls))
        u = t.column("url").to_pylist()[:n]
        ti = t.column("title").to_pylist()[:n]
        for x, y in zip(u, ti):
            titles.setdefault(x, y)
        urls.extend(u)
        taken.append((str(f), n, t.num_rows))
        if len(urls) >= a.rows:
            break
    if len(urls) < a.rows:
        sys.exit(f"corpus has only {len(urls)} rows")
    per_doc = Counter(urls)
    docs = list(titles)
    src = {u: h32("src:" + u) % SOURCES for u in docs}
    own = {u: h32("own:" + u) % 100 == 0 for u in docs}
    did = {u: uuid.uuid5(uuid.NAMESPACE_URL, u) for u in docs}
    t_scan = time.time()

    now = datetime.now(timezone.utc)
    with connect(vector=True) as c, c.cursor() as cur:
        with cur.copy("COPY source (id, title, definition_sha256, state) FROM STDIN") as cp:
            for i in range(SOURCES):
                sid = f"src-{i:04d}"
                cp.write_row((sid, f"Source {i}", sha(sid), "idle"))
        with cur.copy("COPY source_acl (source_id, principal, role) FROM STDIN") as cp:
            for i in range(SOURCES):
                sid = f"src-{i:04d}"
                cp.write_row((sid, f"group:g{i % 100:02d}", "reader"))
                cp.write_row((sid, f"user:m{i:04d}", "manager"))
                if i < len(P1_SOURCES):
                    cp.write_row((sid, "group:team-a", "reader"))
        with cur.copy("COPY document (id, source_id, external_id, title, uri, content_sha256, object_key,"
                      " state, acl_inherit, updated_at) FROM STDIN") as cp:
            for u in docs:
                s = f"src-{src[u]:04d}"
                d = sha(u)
                cp.write_row((did[u], s, u, titles[u] or u, u, d, f"raw/{s}/{d}", "indexed", not own[u], now))
        with cur.copy("COPY document_acl (document_id, principal) FROM STDIN") as cp:
            for u in docs:
                if own[u]:
                    cp.write_row((did[u], "user:p2"))
                    cp.write_row((did[u], "user:owner"))
        t_meta = time.time()
        print(f"[{time.strftime('%H:%M:%S')}] metadata loaded: {len(docs)} documents; loading chunks", flush=True)

        ordinal = Counter()
        written = 0
        with cur.copy("COPY chunk (document_id, source_id, ordinal, page, text, embedding)"
                      " FROM STDIN WITH (FORMAT BINARY)") as cp:
            cp.set_types(["uuid", "text", "int4", "int4", "text", "vector"])
            for f, n, _ in taken:
                left = n
                for b in pq.ParquetFile(f).iter_batches(batch_size=20000, columns=["url", "text", "emb"]):
                    if left <= 0:
                        break
                    b = b.slice(0, min(left, b.num_rows))
                    left -= b.num_rows
                    emb = b.column("emb")
                    mm = pc.min_max(pc.list_value_length(emb)).as_py()
                    if mm["min"] != DIM or mm["max"] != DIM:
                        sys.exit(f"embedding length {mm} in {f}")
                    vecs = emb.flatten().to_numpy(zero_copy_only=False).astype(np.float32).reshape(-1, DIM)
                    for u, txt, v in zip(b.column("url").to_pylist(), b.column("text").to_pylist(), vecs):
                        cp.write_row((did[u], f"src-{src[u]:04d}", ordinal[u], None,
                                      (txt or "").replace("\x00", ""), v))
                        ordinal[u] += 1
                        written += 1
        t_chunks = time.time()
        n_chunk = c.execute("SELECT count(*) FROM chunk").fetchone()[0]

    # 1,000 query vectors from a file of the same dataset that is not loaded.
    qt = pq.read_table(a.query_file, columns=["url", "emb"])
    step = qt.num_rows // a.queries
    pick = np.arange(a.queries) * step
    qemb = qt.column("emb").combine_chunks()
    mm = pc.min_max(pc.list_value_length(qemb)).as_py()
    Q = qemb.flatten().to_numpy(zero_copy_only=False).astype(np.float32).reshape(-1, DIM)[pick]
    qurls = [qt.column("url")[int(i)].as_py() for i in pick]
    np.save(Path(a.out) / "queries.npy", Q)

    sel1 = [u for u in docs if src[u] < len(P1_SOURCES) and not own[u]]
    sel2 = [u for u in docs if own[u]]
    norms = np.linalg.norm(Q, axis=1)
    res = {
        "files": [{"file": Path(f).name, "rows_taken": n, "rows_in_file": m} for f, n, m in taken],
        "chunks_written": written, "chunks_in_table": n_chunk,
        "documents": len(docs), "sources": SOURCES,
        "chunks_per_document": {"mean": round(a.rows / len(docs), 2), "max": max(per_doc.values())},
        "documents_with_own_reader_list": len(sel2),
        "expected": {
            "p1": {"documents": len(sel1), "chunks": sum(per_doc[u] for u in sel1)},
            "p2": {"documents": len(sel2), "chunks": sum(per_doc[u] for u in sel2)},
        },
        "queries": {"file": Path(a.query_file).name, "count": int(len(Q)), "rows_in_file": qt.num_rows,
                    "dims": mm, "query_articles_in_corpus": sum(1 for u in qurls if u in titles),
                    "norm_min": round(float(norms.min()), 4), "norm_max": round(float(norms.max()), 4)},
        "seconds": {"scan": round(t_scan - t0, 1), "metadata": round(t_meta - t_scan, 1),
                    "chunks": round(t_chunks - t_meta, 1)},
    }
    for p in ("p1", "p2"):
        e = res["expected"][p]
        e["document_fraction"] = round(e["documents"] / len(docs), 5)
        e["chunk_fraction"] = round(e["chunks"] / a.rows, 5)
    save(a.out, "load.json", res)


# ---------------------------------------------------------------- build
def sizes(c):
    q = """SELECT relname, pg_relation_size(oid) FROM pg_class
           WHERE relname IN ('chunk','document','document_acl','source_acl','chunk_embedding','chunk_source')"""
    s = {n: v for n, v in c.execute(q).fetchall()}
    s["chunk_toast"] = c.execute(
        "SELECT pg_relation_size(reltoastrelid) FROM pg_class WHERE relname='chunk'").fetchone()[0]
    s["database"] = c.execute("SELECT pg_database_size('cortex')").fetchone()[0]
    return {k: {"bytes": v, "GiB": round(v / 2**30, 3)} for k, v in s.items()}


def cmd_build(a):
    notices = []
    with connect() as c:
        c.add_notice_handler(lambda d: notices.append(f"{d.severity}: {d.message_primary}"))
        c.execute("SET client_min_messages = debug1")
        t0 = time.time()
        print(f"[{time.strftime('%H:%M:%S')}] building chunk_source, then chunk_embedding (hnsw)", flush=True)
        c.execute("CREATE INDEX chunk_source ON chunk (source_id)")
        t1 = time.time()
        c.execute("CREATE INDEX chunk_embedding ON chunk USING hnsw (embedding vector_cosine_ops)")
        t2 = time.time()
        c.execute("SET client_min_messages = notice")
        c.execute("VACUUM ANALYZE")
        t3 = time.time()
        res = {
            "seconds": {"chunk_source": round(t1 - t0, 1), "chunk_embedding_hnsw": round(t2 - t1, 1),
                        "vacuum_analyze": round(t3 - t2, 1)},
            "indexdef": c.execute("SELECT pg_get_indexdef('chunk_embedding'::regclass)").fetchone()[0],
            "settings": {n: s for n, s in c.execute(
                "SELECT name, setting || coalesce(unit, '') FROM pg_settings WHERE name IN "
                "('maintenance_work_mem','max_parallel_maintenance_workers','shared_buffers')").fetchall()},
            "sizes": sizes(c),
            "notices": [n for n in notices if "hnsw" in n.lower() or "parallel" in n.lower()][:30],
        }
    save(a.out, "build.json", res)


# ---------------------------------------------------------------- measure
def readable(plist, Q):
    import numpy as np
    r = {}
    with connect(user="cortex_query", vector=True) as c:
        with c.transaction():
            c.execute(SET_PRINCIPALS, (plist,))
            t = time.time()
            r["documents"] = c.execute("SELECT count(*) FROM document").fetchone()[0]
            r["chunks"] = c.execute("SELECT count(*) FROM chunk").fetchone()[0]
            sources = [x[0] for x in c.execute("SELECT DISTINCT source_id FROM document ORDER BY 1").fetchall()]
            r["count_seconds"] = round(time.time() - t, 1)
        with c.transaction():
            c.execute(SET_PRINCIPALS, (plist,))
            rows = c.cursor(binary=True).execute(
                "SELECT id, embedding FROM chunk WHERE source_id = ANY (%s)", (sources,)).fetchall()
    r["sources_in_filter"] = len(sources)
    r["rows_fetched_for_exact"] = len(rows)
    ids = np.array([x[0] for x in rows], dtype=np.int64)
    X = np.stack([np.asarray(x[1].to_numpy() if hasattr(x[1], "to_numpy") else x[1], dtype=np.float32)
                  for x in rows])
    Xn = X / np.linalg.norm(X, axis=1, keepdims=True)
    Qn = Q / np.linalg.norm(Q, axis=1, keepdims=True)
    S = Qn @ Xn.T
    top = np.argsort(-S, axis=1)[:, :K + 1]
    edge = S[np.arange(len(Q))[:, None], top]
    r["queries_with_near_tie_at_rank_10"] = int(np.sum(np.abs(edge[:, K - 1] - edge[:, K]) < 1e-6))
    exact = [[int(ids[j]) for j in row[:K]] for row in top]
    return r, sources, exact, set(ids.tolist())


def search_pass(plist, sources, Q, iterative=None, extra=None, max_seconds=0):
    lat_tx, lat_q, got, short, misordered = [], [], [], 0, 0
    with connect(user="cortex_query", vector=True) as c:
        start = time.perf_counter()
        for q in Q:
            if max_seconds and time.perf_counter() - start > max_seconds:
                break
            t0 = time.perf_counter()
            with c.transaction():
                c.execute(SET_PRINCIPALS, (plist,))
                if iterative:
                    c.execute(f"SET LOCAL hnsw.iterative_scan = {iterative}")
                if extra:
                    c.execute(extra)
                t1 = time.perf_counter()
                rows = c.execute(SEARCH, {"q": q, "src": sources, "k": K}).fetchall()
                t2 = time.perf_counter()
            t3 = time.perf_counter()
            lat_tx.append((t3 - t0) * 1000)
            lat_q.append((t2 - t1) * 1000)
            got.append([int(x[0]) for x in rows])
            sc = [float(x[5]) for x in rows]
            short += len(rows) < K
            misordered += any(sc[i] < sc[i + 1] - 1e-6 for i in range(len(sc) - 1))
        prepared = [{"statement": s.split("\n")[0][:60], "generic_plans": g, "custom_plans": cu}
                    for s, g, cu in c.execute(
                        "SELECT statement, generic_plans, custom_plans FROM pg_prepared_statements").fetchall()]
    return {"lat_tx": lat_tx, "lat_q": lat_q, "got": got, "short": short, "misordered": misordered,
            "prepared": prepared, "seconds": round(time.perf_counter() - start, 1)}


def plan_walk(node, chain, scan):
    label = node["Node Type"] + (f" ({node['Index Name']})" if node.get("Index Name") else "")
    chain.append(label)
    if node["Node Type"] in ("Index Scan", "Bitmap Heap Scan", "Seq Scan", "Index Only Scan"):
        scan["rows_removed_by_filter"] = node.get("Rows Removed by Filter", 0)
        scan["rows_out"] = node.get("Actual Rows")
    for ch in node.get("Plans", []):
        if ch.get("Parent Relationship") in ("SubPlan", "InitPlan"):
            continue
        plan_walk(ch, chain, scan)


def explain_sample(plist, sources, Q, n, iterative="relaxed_order"):
    from psycopg import sql
    out, first_text = [], None
    arr = "{" + ",".join(sources) + "}"
    with connect(user="cortex_query") as c:
        for i, q in enumerate(Q[:n]):
            vec = "[" + ",".join(repr(float(x)) for x in q) + "]"
            body = sql.SQL(SEARCH_LITERAL).format(q=sql.Literal(vec), src=sql.Literal(arr), k=sql.Literal(K))
            with c.transaction():
                c.execute(SET_PRINCIPALS, (plist,))
                c.execute(f"SET LOCAL hnsw.iterative_scan = {iterative}")
                js = c.execute(sql.SQL("EXPLAIN (ANALYZE, BUFFERS, SETTINGS, FORMAT JSON) ") + body).fetchone()[0]
                if i == 0:
                    first_text = "\n".join(x[0] for x in c.execute(
                        sql.SQL("EXPLAIN (ANALYZE, BUFFERS, SETTINGS) ") + body).fetchall())
            top = js[0]
            chain, scan = [], {}
            plan_walk(top["Plan"], chain, scan)
            out.append({"chain": " -> ".join(chain), "execution_ms": round(top["Execution Time"], 2),
                        "planning_ms": round(top["Planning Time"], 2),
                        "shared_hit": top["Plan"].get("Shared Hit Blocks"),
                        "shared_read": top["Plan"].get("Shared Read Blocks"), **scan})
    chains = Counter(x["chain"] for x in out)
    import numpy as np
    return {"n": len(out), "plan_shapes": dict(chains),
            "execution_ms": stats([x["execution_ms"] for x in out]),
            "rows_removed_by_filter_median": float(np.median([x.get("rows_removed_by_filter", 0) for x in out])) if out else None,
            "shared_read_median": float(np.median([x["shared_read"] or 0 for x in out])) if out else None,
            "shared_hit_median": float(np.median([x["shared_hit"] or 0 for x in out])) if out else None,
            "first_plan_text": first_text}


def summarize_pass(p, exact, allowed):
    import numpy as np
    n = len(p["got"])
    rec = [len(set(g) & set(e)) / K for g, e in zip(p["got"], exact[:n])]
    leaks = sum(1 for g in p["got"] for x in g if x not in allowed) if allowed is not None else None
    return {"queries": n, "recall_at_10": round(float(np.mean(rec)), 4) if rec else None,
            "recall_min": round(float(np.min(rec)), 2) if rec else None,
            "queries_recall_below_0_9": int(sum(r < 0.9 for r in rec)),
            "queries_fewer_than_10_rows": p["short"], "queries_rows_out_of_order": p["misordered"],
            "rows_outside_readable_set": leaks,
            "latency_transaction_ms": stats(p["lat_tx"]), "latency_select_ms": stats(p["lat_q"]),
            "prepared_statements": p["prepared"], "wall_seconds": p["seconds"]}


def cmd_measure(a):
    import numpy as np
    out = Path(a.out)
    Q = np.load(out / "queries.npy")[: a.queries]
    load = json.loads((out / "load.json").read_text())
    res = {"profile": a.profile, "queries": int(len(Q)), "principals": {}}
    with connect() as c:
        res["settings"] = {n: s for n, s in c.execute(
            "SELECT name, setting || coalesce(unit, '') FROM pg_settings WHERE name IN "
            "('shared_buffers','effective_cache_size','work_mem','random_page_cost','jit','max_parallel_workers_per_gather',"
            "'plan_cache_mode') OR name LIKE 'hnsw.%' ORDER BY 1").fetchall()}
    gt_file = out / "groundtruth.json"
    gt = json.loads(gt_file.read_text()) if gt_file.exists() else {}
    for p, plist in PRINCIPALS.items():
        r = {"principal_list": plist}
        if a.profile == "runner":
            info, sources, exact, allowed = readable(plist, Q)
            exp = load["expected"][p]
            info["matches_expected"] = (info["documents"] == exp["documents"] and info["chunks"] == exp["chunks"])
            info["expected"] = exp
            r["readable"] = info
            gt[p] = {"sources": sources, "exact": exact, "allowed": sorted(allowed)}
        else:
            sources, exact, allowed = gt[p]["sources"], gt[p]["exact"], set(gt[p]["allowed"])
        print(f"[{time.strftime('%H:%M:%S')}] {a.profile} {p}: {len(sources)} sources in filter", flush=True)
        passes = {}
        if a.profile == "runner":
            n_sql = 50 if p == "p1" else 5
            sx = search_pass(plist, sources, Q[:n_sql], extra="SET LOCAL enable_indexscan = off")
            passes["exact_sql_no_index_scan"] = summarize_pass(sx, exact, allowed)
        names = ["relaxed_order_pass1", "relaxed_order_pass2"] + (["iterative_off"] if a.profile == "runner" else [])
        for name in names:
            it = "off" if name == "iterative_off" else "relaxed_order"
            sp = search_pass(plist, sources, Q, iterative=it, max_seconds=a.max_seconds)
            passes[name] = summarize_pass(sp, exact, allowed)
            print(f"[{time.strftime('%H:%M:%S')}] {a.profile} {p} {name}: recall={passes[name]['recall_at_10']} "
                  f"p95={passes[name]['latency_transaction_ms'].get('p95')} ms n={passes[name]['queries']}", flush=True)
        r["passes"] = passes
        r["explain_relaxed_order"] = explain_sample(plist, sources, Q, 20 if a.profile == "runner" else 5)
        res["principals"][p] = r
    if a.profile == "runner":
        save(out, "groundtruth.json", gt, show=False)
    save(out, f"measure-{a.profile}.json", res)


# ---------------------------------------------------------------- summarize
def cmd_summarize(a):
    out = Path(a.out)
    rd = lambda n: json.loads((out / n).read_text()) if (out / n).exists() else None
    facts, pol, load, build = rd("facts.json"), rd("policycheck.json"), rd("load.json"), rd("build.json")
    mr, mc = rd("measure-runner.json"), rd("measure-chart.json")
    L = ["# PRD-060 measured values", ""]
    if facts:
        L += [f"- image: `{facts['image']}`, server {facts['server_version']}",
              f"- vector available: {facts['vector_available']}, installed version: {facts.get('vector_installed')}, "
              f"hnsw.iterative_scan after SET: {facts.get('iterative_scan_after_set') or facts.get('iterative_scan_error')}"]
    if build:
        L += [f"- HNSW build: {build['seconds']['chunk_embedding_hnsw']} s; index "
              f"{build['sizes']['chunk_embedding']['GiB']} GiB; heap {build['sizes']['chunk']['GiB']} GiB; "
              f"toast {build['sizes']['chunk_toast']['GiB']} GiB"]
    if pol:
        L += [f"- literal document policy: {pol['literal_document_policy']}",
              f"- self-column document policy: {pol['self_column_document_policy']}"]
    verdict = []
    for prof, m in (("runner", mr), ("chart", mc)):
        if not m:
            continue
        L += ["", f"## profile {prof}", "",
              "| principal | pass | n | recall@10 | min | <10 rows | out of order | leaks | p50 ms | p95 ms | p99 ms |",
              "|---|---|---|---|---|---|---|---|---|---|---|"]
        for p, r in m["principals"].items():
            for name, s in r["passes"].items():
                lt = s["latency_transaction_ms"]
                L.append(f"| {p} | {name} | {s['queries']} | {s['recall_at_10']} | {s['recall_min']} | "
                         f"{s['queries_fewer_than_10_rows']} | {s['queries_rows_out_of_order']} | "
                         f"{s['rows_outside_readable_set']} | {lt.get('p50')} | {lt.get('p95')} | {lt.get('p99')} |")
            L.append(f"| {p} | plans | {r['explain_relaxed_order']['plan_shapes']} |")
            if prof == "runner":
                s = r["passes"]["relaxed_order_pass2"]
                verdict.append((p, s["recall_at_10"], s["latency_transaction_ms"]["p95"]))
    outcome = "none"
    if facts is not None:
        if not facts["iterative_capable"]:
            outcome = "B"
        elif len(verdict) == 2:
            ok = all(rc is not None and rc >= RECALL_MIN and p95 < P95_MAX_MS for _, rc, p95 in verdict)
            outcome = "A" if ok else "C"
    L += ["", f"Outcome (computed, runner profile, warm pass, both principals): {outcome}"]
    text = "\n".join(L)
    (out / "summary.md").write_text(text)
    print(text)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("facts"); s.add_argument("--image", required=True)
    s = sub.add_parser("schema"); s.add_argument("--sql", required=True)
    s = sub.add_parser("load"); s.add_argument("--corpus", required=True); s.add_argument("--query-file", required=True)
    s.add_argument("--rows", type=int, default=1_000_000); s.add_argument("--queries", type=int, default=1000)
    sub.add_parser("build")
    s = sub.add_parser("measure"); s.add_argument("--profile", choices=["runner", "chart"], required=True)
    s.add_argument("--queries", type=int, default=1000); s.add_argument("--max-seconds", type=float, default=0)
    sub.add_parser("summarize")
    for p in sub.choices.values():
        p.add_argument("--out", required=True)
    a = ap.parse_args()
    globals()["cmd_" + a.cmd](a)


if __name__ == "__main__":
    main()
