// Scripted model endpoint for spike AIP-020. It speaks the Messages API that the
// agent kit's child process calls, so the kit can be tested with no login and no
// network. The same listener is the child's HTTPS proxy: every CONNECT is logged
// as an egress attempt and refused (the job runs with the network switched off).
import http from "node:http";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const textOf = (c) => (typeof c === "string" ? c : Array.isArray(c) ? c.map((b) => b.text ?? "").join("") : "");

export function startMock({ mainModel, tokens, secretPath, secretMarker }) {
  const log = [], egress = [], sent = {};
  let n = 0;
  const authTag = (h) => {
    const a = h.authorization || "";
    const v = a.replace(/^Bearer\s+/i, "");
    const tag = Object.entries(tokens).find(([, t]) => t === v)?.[0];
    return { scheme: a ? a.split(" ")[0] : "none", token: a ? tag ?? "unknown" : "none", xApiKey: !!h["x-api-key"] };
  };
  let self = "";
  const server = http.createServer(async (req, res) => {
    let viaProxy = false;
    if (/^https?:\/\//.test(req.url)) { // a request sent to us as a proxy
      const u = new URL(req.url);
      if (![`127.0.0.1:${self}`, `localhost:${self}`].includes(u.host)) { egress.push({ at: Date.now(), via: "http-proxy", host: u.host }); res.writeHead(403); return res.end(); }
      req.url = u.pathname + u.search; viaProxy = true;
    }
    let body = "";
    for await (const c of req) body += c;
    let j = {};
    try { j = JSON.parse(body || "{}"); } catch {}
    const e = { n: ++n, at: Date.now(), method: req.method, path: req.url, viaProxy, auth: authTag(req.headers),
      beta: req.headers["anthropic-beta"] ?? null, ua: req.headers["user-agent"] ?? null, app: req.headers["x-app"] ?? null };
    log.push(e);
    if (req.method !== "POST" || !req.url.startsWith("/v1/messages")) {
      e.kind = "other";
      res.writeHead(404, { "content-type": "application/json" });
      return res.end(JSON.stringify({ type: "error", error: { type: "not_found_error", message: "spike mock" } }));
    }
    if (req.url.startsWith("/v1/messages/count_tokens")) {
      e.kind = "count";
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({ input_tokens: 10 }));
    }
    const msgs = j.messages || [];
    const all = JSON.stringify(msgs);
    const last = msgs[msgs.length - 1];
    const results = (Array.isArray(last?.content) ? last.content : []).filter((b) => b.type === "tool_result");
    Object.assign(e, {
      kind: "messages", model: j.model, stream: !!j.stream,
      scenario: (all.match(/SPIKE:(\w+)/) || [])[1] || "none",
      phase: results.length ? "final" : "first",
      tools: (j.tools || []).map((t) => ({ name: t.name, type: t.type ?? null, defer: t.defer_loading ?? null, schema: t.input_schema })),
      system: (Array.isArray(j.system) ? j.system.map((b) => b.text) : [j.system]).filter(Boolean).map((s) => s.slice(0, 90)),
      toolResults: results.map((r) => ({ id: r.tool_use_id, isError: !!r.is_error, text: textOf(r.content).slice(0, 200) })),
      leak: secretMarker ? body.includes(secretMarker) : false,
    });
    const main = j.model === mainModel;
    const id = `msg_spike_${n}`;
    const tid = (i) => `toolu_spike_${n}_${i}`;
    let blocks;
    if (main && e.phase === "first" && ["t1", "t3", "t3x"].includes(e.scenario)) {
      blocks = [{ tool: "mcp__t__lookup", input: { city: "Muscat", days: 3 } }];
    } else if (main && e.phase === "first" && ["t6", "t6r"].includes(e.scenario)) {
      blocks = [{ text: ["Let me check ", "both."] }, { tool: "mcp__t__lookup", input: { city: "Salalah" } },
        { tool: "mcp__t__convert", input: { amount: 10, from: "OMR", to: "USD" } }];
    } else if (main && e.phase === "first" && ["t5", "t5b"].includes(e.scenario)) {
      blocks = [{ tool: "Read", input: { file_path: secretPath } }];
    } else if (main && e.phase === "first" && e.scenario === "slow") {
      blocks = [{ text: Array.from({ length: 30 }, (_, i) => `word${i} `), delayMs: 500 }];
    } else {
      blocks = [{ text: [`Final answer for ${e.scenario}.`] }];
    }
    const hasTool = blocks.some((b) => b.tool);
    sent[id] = { input: 1000 + n, cacheWrite: 2000 + n, cacheRead: 3000 + n, output: 4000 + n, scenario: e.scenario, phase: e.phase };
    e.reply = { id, blocks: blocks.map((b, i) => (b.tool ? { tool: b.tool, id: tid(i), input: b.input } : { text: b.text.join("") })) };
    const usage0 = { input_tokens: 1000 + n, cache_creation_input_tokens: 2000 + n, cache_read_input_tokens: 3000 + n, output_tokens: 1 };
    if (!j.stream) {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(JSON.stringify({ id, type: "message", role: "assistant", model: j.model,
        content: blocks.map((b, i) => (b.tool ? { type: "tool_use", id: tid(i), name: b.tool, input: b.input } : { type: "text", text: b.text.join("") })),
        stop_reason: hasTool ? "tool_use" : "end_turn", stop_sequence: null, usage: { ...usage0, output_tokens: 4000 + n } }));
    }
    res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache", "request-id": `req_spike_${n}` });
    const ev = (type, data) => res.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...data })}\n\n`);
    ev("message_start", { message: { id, type: "message", role: "assistant", model: j.model, content: [], stop_reason: null, stop_sequence: null, usage: usage0 } });
    for (const [i, b] of blocks.entries()) {
      if (b.tool) {
        ev("content_block_start", { index: i, content_block: { type: "tool_use", id: tid(i), name: b.tool, input: {} } });
        ev("content_block_delta", { index: i, delta: { type: "input_json_delta", partial_json: JSON.stringify(b.input) } });
      } else {
        ev("content_block_start", { index: i, content_block: { type: "text", text: "" } });
        for (const piece of b.text) {
          ev("content_block_delta", { index: i, delta: { type: "text_delta", text: piece } });
          if (b.delayMs) await sleep(b.delayMs);
        }
      }
      ev("content_block_stop", { index: i });
    }
    ev("message_delta", { delta: { stop_reason: hasTool ? "tool_use" : "end_turn", stop_sequence: null }, usage: { output_tokens: 4000 + n } });
    ev("message_stop", {});
    e.streamEndAt = Date.now();
    res.end();
  });
  server.on("connect", (req, socket) => { egress.push({ at: Date.now(), via: "connect", host: req.url }); socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"); });
  return new Promise((ok) => server.listen(0, "127.0.0.1", () => { self = String(server.address().port);
    ok({ url: `http://127.0.0.1:${self}`, log, egress, sent, close: () => server.close() }); }));
}
