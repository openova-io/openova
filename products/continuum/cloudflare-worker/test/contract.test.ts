// contract.test.ts — verifies the Worker satisfies the EXACT request
// shapes K-Cont-3's CFKVClient sends. If a test in this file changes,
// the corresponding Go client method changed and the contract has
// drifted — STOP and reconcile.
//
// The shapes are documented in
// `core/controllers/continuum/internal/witness/cloudflarekv/client.go`
// (search for `applyAuth`, `Read`, `write`, `Release`). The fakeWorker
// in `client_test.go` is the authoritative reference impl on the Go
// side.

import { describe, it, expect, beforeEach } from "vitest";
import { SELF, env } from "cloudflare:test";

const BEARER = "test-token";
const SLOT_ENCODED = "ns%2Fcr-main"; // CFKVClient.pathEscapeSlot output

async function clearKV(): Promise<void> {
  await env.OPENOVA_LEASES.delete(`lease:ns/cr-main`);
}

describe("contract: GET — Read()", () => {
  beforeEach(clearKV);

  it("client sends Authorization + Accept + X-Lease-Slot, expects 200|404", async () => {
    // Per CFKVClient.Read():
    //   req.Header.Set("Authorization", "Bearer "+c.APIToken)
    //   req.Header.Set("Accept", "application/json")
    //   req.Header.Set("X-Lease-Slot", c.Slot)
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        Accept: "application/json",
        "X-Lease-Slot": "ns/cr-main",
      },
    });
    // Empty slot → 404 per Read switch on http.StatusNotFound.
    expect(r.status).toBe(404);
  });

  it("populated slot returns 200 with {holder, acquiredAt, expiresAt, generation}", async () => {
    // Pre-populate via PUT.
    await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        Accept: "application/json",
        "Content-Type": "application/json",
        "If-Match": "0",
        "X-Lease-Slot": "ns/cr-main",
      },
      body: JSON.stringify({ holder: "fsn1-primary", ttlSeconds: 30, op: "acquire" }),
    });
    // Now GET.
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "GET",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        Accept: "application/json",
        "X-Lease-Slot": "ns/cr-main",
      },
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    // Field-name contract — these are the EXACT JSON keys
    // CFKVClient.kvState expects (json:"holder", "acquiredAt",
    // "expiresAt", "generation"). Any rename here BREAKS the wire.
    expect(body).toHaveProperty("holder");
    expect(body).toHaveProperty("acquiredAt");
    expect(body).toHaveProperty("expiresAt");
    expect(body).toHaveProperty("generation");
    expect(typeof body.holder).toBe("string");
    expect(typeof body.acquiredAt).toBe("string");
    expect(typeof body.expiresAt).toBe("string");
    expect(typeof body.generation).toBe("number");
    // RFC3339 — Go's time.Parse(time.RFC3339, ...) accepts the JS
    // toISOString() output (which is RFC3339 with `.SSSZ`).
    expect(body.acquiredAt).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    expect(body.expiresAt).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
  });
});

describe("contract: PUT — Acquire() and write()", () => {
  beforeEach(clearKV);

  it("client sends If-Match + Content-Type + body{holder,ttlSeconds,op}", async () => {
    // Per CFKVClient.write():
    //   req.Header.Set("Content-Type", "application/json")
    //   req.Header.Set("Accept", "application/json")
    //   req.Header.Set("If-Match", strconv.FormatInt(ifMatch, 10))
    // body = json.Marshal(kvWriteRequest{Holder, TTLSeconds, Op})
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "0",
        "X-Lease-Slot": "ns/cr-main",
      },
      body: JSON.stringify({
        holder: "fsn1-primary",
        ttlSeconds: 30,
        op: "acquire",
      }),
    });
    // Per CFKVClient.write switch on http.StatusOK | http.StatusCreated.
    // Worker returns 200 always.
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("fsn1-primary");
    expect(body.generation).toBe(1);
  });

  it("412 conflict body decodable to kvState (trap #3)", async () => {
    // Acquire by holder A.
    await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "0",
      },
      body: JSON.stringify({ holder: "A", ttlSeconds: 60, op: "acquire" }),
    });
    // B tries with stale If-Match — Worker should return 412 with
    // current state body. Per CFKVClient.write switch on 412:
    //   var k kvState; _ = json.NewDecoder(resp.Body).Decode(&k)
    //   st, _ := parseState(k)
    //   return st, witness.ErrLeaseHeldByAnother
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "0",
      },
      body: JSON.stringify({ holder: "B", ttlSeconds: 60, op: "acquire" }),
    });
    expect(r.status).toBe(412);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.holder).toBe("A");
    expect(body.generation).toBe(1);
  });

  it("op:'renew' bumps generation when sameHolder + non-expired", async () => {
    await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "0",
      },
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "1",
      },
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "renew" }),
    });
    expect(r.status).toBe(200);
    const body = (await r.json()) as Record<string, unknown>;
    expect(body.generation).toBe(2);
  });
});

describe("contract: DELETE — Release()", () => {
  beforeEach(clearKV);

  it("client sends If-Match + X-Holder, expects 204", async () => {
    // Acquire first.
    await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        Accept: "application/json",
        "If-Match": "0",
      },
      body: JSON.stringify({ holder: "fsn1", ttlSeconds: 30, op: "acquire" }),
    });
    // Per CFKVClient.Release():
    //   req.Header.Set("If-Match", strconv.FormatInt(cur.Generation, 10))
    //   req.Header.Set("X-Holder", holder)
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "DELETE",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "If-Match": "1",
        "X-Holder": "fsn1",
        "X-Lease-Slot": "ns/cr-main",
      },
    });
    // Per CFKVClient.Release switch:
    //   case http.StatusNoContent || http.StatusOK: return nil
    expect(r.status).toBe(204);
  });
});

describe("contract: 401 surface", () => {
  it("missing Authorization header → 401 (CFKVClient maps to 'auth rejected')", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "GET",
    });
    expect(r.status).toBe(401);
  });

  it("wrong bearer → 401", async () => {
    const r = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "GET",
      headers: { Authorization: "Bearer not-the-real-token" },
    });
    expect(r.status).toBe(401);
  });
});

describe("contract: full lifecycle (mirrors TestCFKV_GenerationBumpedOnRelease)", () => {
  beforeEach(clearKV);

  it("Acquire → Release → Read shows generation bumped + empty holder", async () => {
    // Acquire (gen 0 → 1).
    const a = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "Content-Type": "application/json",
        "If-Match": "0",
      },
      body: JSON.stringify({ holder: "fsn", ttlSeconds: 30, op: "acquire" }),
    });
    const a1 = (await a.json()) as Record<string, unknown>;
    expect(a1.generation).toBe(1);
    // Release (gen 1 → 2).
    const d = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "DELETE",
      headers: {
        Authorization: `Bearer ${BEARER}`,
        "If-Match": "1",
        "X-Holder": "fsn",
      },
    });
    expect(d.status).toBe(204);
    // Read shows generation 2 + holder cleared.
    const g = await SELF.fetch(`https://x/lease/${SLOT_ENCODED}`, {
      method: "GET",
      headers: { Authorization: `Bearer ${BEARER}` },
    });
    expect(g.status).toBe(200);
    const body = (await g.json()) as Record<string, unknown>;
    expect(body.generation).toBe(2);
    expect(body.holder).toBe("");
  });
});
