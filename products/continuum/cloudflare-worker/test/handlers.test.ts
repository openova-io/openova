// handlers.test.ts — branch-coverage tests for the lease-witness
// Worker handlers. Verifies every status code + body shape per the
// K-Cont-3 contract (see DESIGN.md).
//
// Strategy: spin up a real workerd runtime via @cloudflare/vitest-pool-
// workers (configured in vitest.config.ts) and drive it via plain
// `fetch` against the Worker's bound URL. KV is in-memory + per-test
// isolated so tests don't bleed state.

import { describe, it, expect, beforeEach } from "vitest";
import { SELF, env } from "cloudflare:test";

const BEARER = "test-token";
const SLOT = "ns/cr-main";
const SLOT_ENC = encodeURIComponent(SLOT); // "ns%2Fcr-main"

function authHeaders(extra: Record<string, string> = {}): HeadersInit {
  return { Authorization: `Bearer ${BEARER}`, ...extra };
}

async function clearKV(): Promise<void> {
  // KV doesn't expose "delete all" — we have no leftover keys per
  // test because miniflare's storage is per-test-isolation. But for
  // belt-and-suspenders within a single test scope, delete the slot
  // key.
  await env.OPENOVA_LEASES.delete(`lease:${SLOT}`);
}

describe("auth", () => {
  beforeEach(clearKV);

  it("rejects missing Authorization header with 401", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`);
    expect(r.status).toBe(401);
    const body = await r.json();
    expect(body).toEqual({ error: "unauthorized" });
  });

  it("rejects malformed Authorization header with 401", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: { Authorization: "NotBearer xxx" },
    });
    expect(r.status).toBe(401);
  });

  it("rejects wrong bearer token with 401", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: { Authorization: "Bearer wrong-token" },
    });
    expect(r.status).toBe(401);
  });

  it("accepts second token from the comma-separated allow-list", async () => {
    // GET on empty slot returns 404 (not 401) → auth passed.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: { Authorization: "Bearer second-token" },
    });
    expect(r.status).toBe(404);
  });
});

describe("routing", () => {
  it("returns 404 for non-/lease/ paths", async () => {
    const r = await SELF.fetch("https://x/some/other/path", {
      headers: authHeaders(),
    });
    expect(r.status).toBe(404);
  });

  it("returns 404 for /lease/ with no slot tail", async () => {
    const r = await SELF.fetch("https://x/lease/", {
      headers: authHeaders(),
    });
    expect(r.status).toBe(404);
  });

  it("returns 405 for unsupported methods", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PATCH",
      headers: authHeaders(),
    });
    expect(r.status).toBe(405);
  });

  it("answers OPTIONS preflight with 204 + Allow header", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "OPTIONS",
      headers: authHeaders(),
    });
    expect(r.status).toBe(204);
    expect(r.headers.get("Allow")).toContain("GET");
    expect(r.headers.get("Allow")).toContain("PUT");
    expect(r.headers.get("Allow")).toContain("DELETE");
  });
});

describe("GET", () => {
  beforeEach(clearKV);

  it("returns 404 for an empty slot", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: authHeaders(),
    });
    expect(r.status).toBe(404);
  });

  it("returns 200 + LeaseState after acquire", async () => {
    // First acquire to populate the slot.
    const acq = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({
        "If-Match": "0",
        "Content-Type": "application/json",
      }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(acq.status).toBe(200);
    // Now read it back.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: authHeaders(),
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("fsn1");
    expect(body.generation).toBe(1);
    expect(typeof body.acquiredAt).toBe("string");
    expect(typeof body.expiresAt).toBe("string");
  });
});

describe("PUT acquire", () => {
  beforeEach(clearKV);

  it("first acquire on empty slot accepts If-Match: 0 (trap #1)", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({
        "If-Match": "0",
        "Content-Type": "application/json",
      }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("fsn1");
    expect(body.generation).toBe(1);
  });

  it("rejects acquire on empty slot with non-zero If-Match (412)", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({
        "If-Match": "5",
        "Content-Type": "application/json",
      }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(412);
  });

  it("rejects acquire when slot held by another non-expired holder (412 + body)", async () => {
    // First holder acquires.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 60, op: "acquire" }),
    });
    // Second holder tries with stale If-Match.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "nbg1", ttlSeconds: 60, op: "acquire" }),
    });
    expect(r.status).toBe(412);
    // Per trap #3 the 412 SHOULD include the current state body.
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("fsn1");
    expect(body.generation).toBe(1);
  });

  it("re-acquire by same holder bumps generation (trap #2 monotonicity)", async () => {
    // First acquire.
    const r1 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    const b1 = (await r1.json()) as Record<string, unknown>;
    expect(b1.generation).toBe(1);
    // Re-acquire by same holder with current generation.
    const r2 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "1", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r2.status).toBe(200);
    const b2 = (await r2.json()) as Record<string, unknown>;
    expect(b2.generation).toBe(2);
    // acquiredAt is preserved on same-holder re-acquire.
    expect(b2.acquiredAt).toBe(b1.acquiredAt);
  });

  it("acquire by NEW holder after expiry stamps fresh acquiredAt", async () => {
    // First acquire with 1-second TTL.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 1, op: "acquire" }),
    });
    // Wait until expiry.
    await new Promise((r) => setTimeout(r, 1100));
    // New holder takes over.
    const r2 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "1", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "nbg1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r2.status).toBe(200);
    const b = (await r2.json()) as Record<string, unknown>;
    expect(b.holder).toBe("nbg1");
    expect(b.generation).toBe(2);
  });

  it("rejects PUT without If-Match header with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(400);
  });

  it("rejects PUT with malformed If-Match (non-integer) with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({
        "If-Match": "not-a-number",
        "Content-Type": "application/json",
      }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(400);
  });

  it("accepts quoted ETag-style If-Match", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({
        "If-Match": '"0"',
        "Content-Type": "application/json",
      }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(200);
  });

  it("rejects malformed JSON body with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: "not-json",
    });
    expect(r.status).toBe(400);
  });

  it("rejects body with missing holder with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ ttlSeconds: 30, op: "acquire" }),
    });
    expect(r.status).toBe(400);
  });

  it("rejects body with negative ttlSeconds with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: -1, op: "acquire" }),
    });
    expect(r.status).toBe(400);
  });

  it("rejects body with invalid op with 400", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "BOGUS" }),
    });
    expect(r.status).toBe(400);
  });
});

describe("PUT renew", () => {
  beforeEach(clearKV);

  it("rejects renew on empty slot with 412", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "renew" }),
    });
    expect(r.status).toBe(412);
  });

  it("happy-path renew bumps generation, preserves acquiredAt", async () => {
    // Acquire.
    const r1 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    const b1 = (await r1.json()) as Record<string, unknown>;
    // Renew.
    const r2 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "1", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "renew" }),
    });
    expect(r2.status).toBe(200);
    const b2 = (await r2.json()) as Record<string, unknown>;
    expect(b2.generation).toBe(2);
    expect(b2.acquiredAt).toBe(b1.acquiredAt);
  });

  it("rejects renew by wrong holder with 412", async () => {
    // Acquire by fsn1.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    // nbg1 tries to renew with the right generation but wrong holder.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "1", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "nbg1", ttlSeconds: 30, op: "renew" }),
    });
    expect(r.status).toBe(412);
  });

  it("rejects renew on expired lease with 412", async () => {
    // Acquire with 1-second TTL.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 1, op: "acquire" }),
    });
    await new Promise((r) => setTimeout(r, 1100));
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "1", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "renew" }),
    });
    expect(r.status).toBe(412);
  });
});

describe("DELETE", () => {
  beforeEach(clearKV);

  it("returns 204 on empty slot (idempotent)", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "0", "X-Holder": "fsn1" }),
    });
    expect(r.status).toBe(204);
  });

  it("happy-path release bumps generation (trap #2)", async () => {
    // Acquire.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    // Release.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "1", "X-Holder": "fsn1" }),
    });
    expect(r.status).toBe(204);
    // Read back — generation must be 2 (bumped on release).
    const r2 = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: authHeaders(),
    });
    expect(r2.status).toBe(200);
    const body = (await r2.json()) as Record<string, unknown>;
    expect(body.generation).toBe(2);
    expect(body.holder).toBe("");
  });

  it("rejects release with stale If-Match (412)", async () => {
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "0", "X-Holder": "fsn1" }),
    });
    expect(r.status).toBe(412);
  });

  it("rejects release with X-Holder mismatch (trap #5)", async () => {
    // Acquire by fsn1.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    // nbg1 tries to release with right If-Match but wrong X-Holder.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "1", "X-Holder": "nbg1" }),
    });
    expect(r.status).toBe(412);
  });

  it("rejects DELETE without If-Match (400)", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "X-Holder": "fsn1" }),
    });
    expect(r.status).toBe(400);
  });

  it("rejects DELETE without X-Holder (400)", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "0" }),
    });
    expect(r.status).toBe(400);
  });

  it("re-acquire after release sees generation+1 (NOT 0) — trap #2 cross-cycle", async () => {
    // Acquire → Release → Acquire-again. The second Acquire MUST
    // present If-Match=2 (= post-release generation), NOT 0.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "DELETE",
      headers: authHeaders({ "If-Match": "1", "X-Holder": "fsn1" }),
    });
    // If we tried If-Match=0 here, it would be 412 because the stored
    // gen is 2, not 0.
    const stale = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "nbg1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(stale.status).toBe(412);
    // Now use the correct generation = 2.
    const ok = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "2", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "nbg1", ttlSeconds: 30, op: "acquire" }),
    });
    expect(ok.status).toBe(200);
    const body = (await ok.json()) as Record<string, unknown>;
    expect(body.generation).toBe(3);
    expect(body.holder).toBe("nbg1");
  });
});

describe("TTL stamping (trap #4)", () => {
  beforeEach(clearKV);

  it("expiresAt = now + ttlSeconds (within 5s tolerance)", async () => {
    const before = Date.now();
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 60, op: "acquire" }),
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    const expiresAt = new Date(body.expiresAt as string).getTime();
    expect(expiresAt - before).toBeGreaterThanOrEqual(60_000 - 5_000);
    expect(expiresAt - before).toBeLessThanOrEqual(60_000 + 5_000);
  });

  it("Read returns stored state regardless of expiry (Worker doesn't auto-evict)", async () => {
    // Acquire with 1s TTL.
    await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 1, op: "acquire" }),
    });
    await new Promise((r) => setTimeout(r, 1100));
    // Read MUST still return the (expired) record per trap #4.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENC}`, {
      headers: authHeaders(),
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("fsn1");
    expect(body.generation).toBe(1);
  });
});

describe("slot encoding", () => {
  it("URL-decodes %2F back to /", async () => {
    // The slot has slashes; the URL parser decodes %2F.
    const r = await SELF.fetch(
      `https://x/lease/${encodeURIComponent("ns/cr-with-slash")}`,
      {
        method: "PUT",
        headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
        body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
      },
    );
    expect(r.status).toBe(200);
    // Read it back via the same encoding.
    const r2 = await SELF.fetch(
      `https://x/lease/${encodeURIComponent("ns/cr-with-slash")}`,
      { headers: authHeaders() },
    );
    expect(r2.status).toBe(200);
  });

  it("isolates different slots", async () => {
    // Acquire slot A.
    await SELF.fetch(`https://x/lease/${encodeURIComponent("ns/cr-A")}`, {
      method: "PUT",
      headers: authHeaders({ "If-Match": "0", "Content-Type": "application/json" }),
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    // Slot B is independent — empty 404 read.
    const r = await SELF.fetch(`https://x/lease/${encodeURIComponent("ns/cr-B")}`, {
      headers: authHeaders(),
    });
    expect(r.status).toBe(404);
  });
});
