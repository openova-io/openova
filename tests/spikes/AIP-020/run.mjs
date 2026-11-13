// Spike AIP-020: can the agent kit be turned inside out? Each test starts one kit
// run (one child process) against the scripted model endpoint in mock.mjs, with a
// low-level in-process tool server whose handlers never execute anything and are
// answered later by this script. Output: TEST lines, REQ lines, RESULT_JSON.
import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { isDeepStrictEqual as deq } from "node:util";
import { query, createSdkMcpServer } from "@anthropic-ai/claude-agent-sdk";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { ListToolsRequestSchema, CallToolRequestSchema } from "@modelcontextprotocol/sdk/types.js";
import { startMock } from "./mock.mjs";

const MODEL = "claude-sonnet-4-6";
const T0 = Date.now();
const PARK_MS = +(process.env.SPIKE_PARK_MS || 600000);
const OUT = path.resolve(process.env.SPIKE_OUT || "out");
fs.mkdirSync(OUT, { recursive: true });
process.on("uncaughtException", (e) => console.log(`WARN uncaught: ${e?.stack || e}`));
process.on("unhandledRejection", (e) => console.log(`WARN unhandled rejection: ${e?.stack || e}`));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const shm = (p) => fs.mkdtempSync(`/dev/shm/${p}-`);
const sha = (p) => crypto.createHash("sha256").update(fs.readFileSync(p)).digest("hex").slice(0, 16);
const tok = (k) => `sk-ant-oat01-spike-${k}-${crypto.randomBytes(16).toString("hex")}`;
const tokens = { A: tok("A"), B: tok("B"), C: tok("C") };
const secretMarker = `SPIKE-SECRET-${crypto.randomBytes(6).toString("hex")}`;
const secretPath = path.join(shm("secret"), "secret.txt");
fs.writeFileSync(secretPath, `${secretMarker}\n`);
const mock = await startMock({ mainModel: MODEL, tokens, secretPath, secretMarker });
function dump() {
  for (const e of mock.log) console.log(`REQ ${e.n} +${Math.round((e.at - T0) / 1000)}s ${e.kind} ${e.method} ${e.path}${e.viaProxy ? " (via proxy)" : ""} sc=${e.scenario ?? "-"} ph=${e.phase ?? "-"} model=${e.model ?? "-"} stream=${e.stream ?? "-"} auth=${e.auth.scheme}/${e.auth.token} tools=[${(e.tools || []).map((t) => t.name + (t.defer ? "(deferred)" : "")).join(",")}] results=${JSON.stringify(e.toolResults || [])} reply=${JSON.stringify(e.reply?.blocks?.map((b) => b.tool ?? "text") ?? [])}`);
  for (const x of mock.egress) console.log(`EGRESS +${Math.round((x.at - T0) / 1000)}s ${x.via} ${x.host}`);
  fs.writeFileSync(path.join(OUT, "result.json"), JSON.stringify({ ...R, requests: mock.log, egress: mock.egress }, null, 1));
  console.log(`RESULT_JSON ${JSON.stringify(R)}`);
}
process.on("SIGTERM", () => { console.log("WARN SIGTERM: dumping partial result"); dump(); process.exit(1); });
const beat = setInterval(() => console.log(`BEAT +${Math.round((Date.now() - T0) / 1000)}s requests=${mock.log.length} egress=${mock.egress.length} tests=${R.tests.map((t) => t.id).join(",")}`), 60000);
beat.unref();
const TOOLS = [
  { name: "lookup", description: "Look up the weather forecast for a city.",
    parameters: { type: "object", properties: { city: { type: "string", description: "City name" }, days: { type: "integer", minimum: 1, maximum: 7 } }, required: ["city"], additionalProperties: false } },
  { name: "convert", description: "Convert an amount between currencies.",
    parameters: { type: "object", properties: { amount: { type: "number" }, from: { type: "string", enum: ["OMR", "USD"] }, to: { type: "string", enum: ["OMR", "USD"] } }, required: ["amount", "from", "to"] } },
];
const R = { tests: [], values: {}, info: {} };
const rec = (id, ok, ev) => { const result = ok === null ? "not-run" : ok ? "pass" : "fail"; R.tests.push({ id, result, evidence: ev }); console.log(`TEST ${id} ${result} :: ${ev}`); };
const until = async (pred, ms, what) => { const t = Date.now(); while (!pred()) { if (Date.now() - t > ms) throw new Error(`timeout ${ms} ms waiting for ${what}`); await sleep(100); } };
const resultOf = (r) => r.msgs.find((x) => x.m.type === "result")?.m;
const initOf = (r) => r.msgs.find((x) => x.m.type === "system" && x.m.subtype === "init")?.m;
const reqs = (sc) => mock.log.filter((e) => e.kind === "messages" && e.scenario === sc);
const brief = (r) => { const x = resultOf(r); return x ? `${x.subtype}${x.is_error ? "/error" : ""}: ${String(x.result ?? x.errors ?? "").slice(0, 160)}` : `no result; error=${r.error}; stderr=${r.stderr.join("").slice(-300)}`; };

function rss(name) { // sum of VmRSS over every process whose environment carries SPIKE_RUN=<name>
  let kb = 0, n = 0;
  for (const p of fs.readdirSync("/proc").filter((d) => /^\d+$/.test(d))) {
    try {
      if (!fs.readFileSync(`/proc/${p}/environ`, "latin1").split("\0").includes(`SPIKE_RUN=${name}`)) continue;
      const m = fs.readFileSync(`/proc/${p}/status`, "utf8").match(/VmRSS:\s+(\d+)/);
      if (m) { kb += +m[1]; n++; }
    } catch {}
  }
  return { mi: Math.round((kb / 1024) * 10) / 10, procs: n };
}

function findPaths(obj, needle, p = "", seen = new Set(), out = []) {
  if (typeof obj === "string" && obj.includes(needle)) { out.push(p + (obj === needle ? "" : " (contains)")); return out; }
  if (!obj || typeof obj !== "object" || seen.has(obj) || p.split(".").length > 7) return out;
  seen.add(obj);
  for (const k of Object.keys(obj)) { let v; try { v = obj[k]; } catch { continue; } findPaths(v, needle, p ? `${p}.${k}` : k, seen, out); }
  return out;
}

function toolServer(run, annotate) { // the kit's server constructor around a low-level list/call server
  let cfg;
  try { cfg = createSdkMcpServer({ name: "t", version: "1.0.0", tools: [] }); run.serverHow = "createSdkMcpServer+instance.server"; }
  catch (e) { cfg = { type: "sdk", name: "t", instance: new McpServer({ name: "t", version: "1.0.0" }) }; run.serverHow = `manual (${e.message})`; }
  const low = cfg.instance.server;
  try { low.registerCapabilities({ tools: {} }); } catch (e) { run.capErr = e.message; }
  low.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS.map((t) => ({ name: t.name, description: t.description,
    inputSchema: t.parameters ?? { type: "object" }, ...(annotate ? { annotations: { readOnlyHint: true } } : {}) })) }));
  low.setRequestHandler(CallToolRequestSchema, (req, extra) => new Promise((res) => {
    run.calls.push({ at: Date.now(), name: req.params.name, args: req.params.arguments, req, extra, resolve: (text) => { res({ content: [{ type: "text", text }] }); } });
  }));
  return cfg;
}

function startRun(name, { prompt, tools = true, annotate = false, extraEnv = {}, token = "A", credFile = null } = {}) {
  const home = shm(`home-${name}`), cwd = shm(`cwd-${name}`);
  const run = { name, home, calls: [], msgs: [], stderr: [], t0: Date.now(), done: false, ac: new AbortController() };
  if (credFile) { fs.mkdirSync(path.join(home, ".claude"), { recursive: true }); fs.writeFileSync(path.join(home, ".claude", ".credentials.json"), JSON.stringify(credFile)); }
  const env = { ...process.env, HOME: home, ANTHROPIC_BASE_URL: mock.url, HTTPS_PROXY: mock.url, HTTP_PROXY: mock.url, NO_PROXY: "127.0.0.1,localhost", SPIKE_RUN: name, ...extraEnv };
  delete env.CLAUDECODE; delete env.ANTHROPIC_API_KEY; delete env.CLAUDE_CODE_OAUTH_TOKEN;
  if (token) env.CLAUDE_CODE_OAUTH_TOKEN = tokens[token];
  const q = query({ prompt, options: { model: MODEL, systemPrompt: "You are a helpful assistant.", tools: [], settingSources: [],
    mcpServers: tools ? { t: toolServer(run, annotate) } : {}, allowedTools: tools ? ["mcp__t"] : [], permissionMode: "dontAsk",
    persistSession: false, includePartialMessages: true, env, cwd, abortController: run.ac,
    debugFile: path.join(OUT, `debug-${name}.log`), stderr: (d) => run.stderr.push(d) } });
  run.loop = (async () => {
    try { for await (const m of q) { run.msgs.push({ at: Date.now(), m }); if (m.type === "result") break; } }
    catch (e) { run.error = String(e?.message || e); }
    finally { run.done = true; run.endAt = Date.now(); }
  })();
  return run;
}

function usageCheck(r) { // T9: raw message_start / message_delta usage, per model message, against what the endpoint sent
  const out = []; let cur = null;
  for (const { m } of r.msgs) {
    if (m.type !== "stream_event") continue;
    if (m.event.type === "message_start") out.push((cur = { id: m.event.message.id, start: m.event.message.usage }));
    if (m.event.type === "message_delta" && cur) cur.delta = m.event.usage;
  }
  return out.map((x) => { const s = mock.sent[x.id];
    return { id: x.id, ok: !!s && x.start?.input_tokens === s.input && x.start?.cache_read_input_tokens === s.cacheRead && x.start?.cache_creation_input_tokens === s.cacheWrite && x.delta?.output_tokens === s.output,
      start: x.start, delta: x.delta }; });
}
function assembled(r) { // T10: assembled assistant messages per model message id
  const g = {};
  for (const { m } of r.msgs) if (m.type === "assistant") (g[m.message.id] ??= []).push(m);
  return Object.entries(g).map(([id, ms]) => ({ id, count: ms.length, blocks: ms.map((x) => x.message.content.map((b) => b.type).join("+")),
    sameUsage: ms.every((x) => JSON.stringify(x.message.usage) === JSON.stringify(ms[0].message.usage)), usage: ms[0].message.usage }));
}
async function guard(id, fn) { try { await fn(); } catch (e) { rec(id, false, `error: ${e.message}`); } }

// ---------- T3 and T3x start first: they park for PARK_MS while the other tests run ----------
const t3 = startRun("t3", { prompt: "SPIKE:t3 What is the weather in Muscat?" });
const t3x = startRun("t3x", { prompt: "SPIKE:t3x What is the weather in Muscat?", extraEnv: { MCP_TOOL_TIMEOUT: String(PARK_MS + 60000) } });
const parked = [];
let sampler;
const t3ready = until(() => (t3.calls.length || t3.done) && (t3x.calls.length || t3x.done), 180000, "t3 handlers")
  .then(() => { const at = t3.calls[0]?.at ?? Date.now();
    sampler = setInterval(() => parked.push({ s: Math.round((Date.now() - at) / 1000), t3: rss("t3"), t3x: rss("t3x") }), 30000); })
  .catch((e) => { R.info.t3ready = e.message; });

await guard("T1", async () => { // T1 + T2 + T9 + T10 on one run
  const r = startRun("t1", { prompt: "SPIKE:t1 What is the weather in Muscat for the next three days?" });
  await until(() => r.calls.length >= 1 || r.done, 180000, "t1 handler");
  const c = r.calls[0];
  if (!c) throw new Error(`no handler call; ${brief(r)}`);
  const first = reqs("t1").find((e) => e.phase === "first" && e.model === MODEL);
  const sentTu = first.reply.blocks[0];
  const offered = first.tools.find((t) => t.name === "mcp__t__lookup");
  const tu = r.msgs.filter((x) => x.m.type === "assistant").flatMap((x) => x.m.message.content).find((b) => b.type === "tool_use");
  const idPaths = findPaths({ request: c.req, extra: c.extra }, sentTu.id);
  R.values.toolUseIdField = idPaths.length ? idPaths[0] : "none";
  R.info.toolUseIdPaths = idPaths;
  R.info.extraKeys = Object.keys(c.extra || {});
  c.resolve("Sunny, 31C, light wind.");
  await until(() => r.done, 120000, "t1 result");
  const fin = reqs("t1").find((e) => e.phase === "final");
  const res = resultOf(r);
  const checks = { offeredNames: first.tools.map((t) => t.name), schemaDeepEqual: deq(offered?.schema, TOOLS[0].parameters),
    schemaBytesEqual: JSON.stringify(offered?.schema) === JSON.stringify(TOOLS[0].parameters), kitToolUseName: tu?.name,
    kitToolUseInputEqual: deq(tu?.input, sentTu.input), handlerName: c.name, handlerArgsEqual: deq(c.args, sentTu.input),
    resultReachedModel: fin?.toolResults?.[0]?.id === sentTu.id && fin.toolResults[0].text.includes("Sunny, 31C"), final: brief(r) };
  rec("T1", !!offered && checks.schemaDeepEqual && tu?.name === "mcp__t__lookup" && checks.kitToolUseInputEqual && c.name === "lookup"
    && checks.handlerArgsEqual && checks.resultReachedModel && res?.subtype === "success" && res.result === "Final answer for t1.", JSON.stringify(checks));
  rec("T2", R.values.toolUseIdField !== "none", `tool-use id ${sentTu.id} found at: ${JSON.stringify(idPaths)}; extra keys: ${R.info.extraKeys.join(",")}`);
  const u = usageCheck(r);
  const expected = reqs("t1").filter((e) => e.model === MODEL && e.stream).map((e) => e.reply.id);
  rec("T9", expected.length > 0 && expected.every((id) => u.find((x) => x.id === id)?.ok), JSON.stringify({ expectedModelMessages: expected, seen: u }));
  R.info.assembledT1 = assembled(r);
  R.info.initT1 = (({ apiKeySource, tools, mcp_servers, model, claude_code_version, permissionMode }) => ({ apiKeySource, tools, mcp_servers, model, claude_code_version, permissionMode }))(initOf(r) || {});
  R.info.serverHow = r.serverHow; R.info.capErr = r.capErr;
});

async function t6(name, annotate) {
  const r = startRun(name, { prompt: `SPIKE:${name} What is the weather in Salalah, and what is 10 OMR in USD?`, annotate });
  await until(() => r.calls.length >= 1 || r.done, 180000, `${name} handler`);
  if (!r.calls.length) throw new Error(`no handler call; ${brief(r)}`);
  try { await until(() => r.calls.length >= 2, 10000, "second handler"); } catch {}
  const mode = r.calls.length >= 2 ? "together" : "sequential";
  r.calls[0].resolve(`result of ${r.calls[0].name}`);
  await until(() => r.calls.length >= 2 || r.done, 60000, `${name} second handler`);
  r.calls[1]?.resolve(`result of ${r.calls[1].name}`);
  await until(() => r.done, 120000, `${name} result`);
  const fin = reqs(name).find((e) => e.phase === "final");
  const sentIds = reqs(name).find((e) => e.phase === "first" && e.model === MODEL).reply.blocks.filter((b) => b.tool).map((b) => b.id);
  return { r, mode, gapMs: r.calls[1] ? r.calls[1].at - r.calls[0].at : null, order: r.calls.map((c) => `${c.name}:${JSON.stringify(c.args)}`),
    idPaths: r.calls.map((c, i) => findPaths({ request: c.req, extra: c.extra }, sentIds[i] ?? "none")),
    resultsReturned: fin?.toolResults?.map((x) => x.id), sentIds, final: brief(r) };
}
await guard("T6", async () => {
  const a = await t6("t6", false);
  R.values.parallelHandlers = a.mode;
  rec("T6", true, `informative: ${a.mode}; order ${JSON.stringify(a.order)}; gap ${a.gapMs} ms; id paths ${JSON.stringify(a.idPaths)}; results returned ${JSON.stringify(a.resultsReturned)} for sent ${JSON.stringify(a.sentIds)}; ${a.final}`);
  const asm = assembled(a.r);
  const turn = asm.find((x) => x.blocks.join("+").includes("tool_use")) || asm[0];
  R.values.assistantMessagesPerTurn = turn?.count ?? null;
  rec("T10", true, `informative: ${JSON.stringify(asm)}`);
  const u = usageCheck(a.r);
  R.info.t9OnT6 = u;
  const b = await t6("t6r", true);
  R.info.parallelHandlersReadOnlyHint = b.mode;
  rec("T6r", true, `informative (tools annotated readOnlyHint): ${b.mode}; order ${JSON.stringify(b.order)}; gap ${b.gapMs} ms; ${b.final}`);
});

for (const [name, withServer] of [["t5", true], ["t5b", false]]) await guard(name === "t5" ? "T5" : "T5b", async () => {
  const r = startRun(name, { prompt: `SPIKE:${name} Read the file ${secretPath} and tell me what it contains.`, tools: withServer });
  await until(() => r.done, 180000, `${name} result`);
  const rs = reqs(name), first = rs.find((e) => e.phase === "first" && e.model === MODEL), fin = rs.find((e) => e.phase === "final");
  const offered = first?.tools.map((t) => t.name) ?? null;
  const ok = !!first && offered.every((n) => n.startsWith("mcp__t__")) && !!fin && fin.toolResults[0]?.isError === true && !rs.some((e) => e.leak) && r.calls.length === 0 && resultOf(r)?.subtype === "success";
  rec(name === "t5" ? "T5" : "T5b", ok, JSON.stringify({ offered, sessionTools: initOf(r)?.tools, scriptedBuiltinToolUse: "Read", kitAnswer: fin?.toolResults, secretReachedEndpoint: rs.some((e) => e.leak), handlerCalls: r.calls.length, final: brief(r) }));
});

async function t4(name, opts) {
  const r = startRun(name, { prompt: `SPIKE:${name} Say OK.`, tools: false, ...opts });
  const cp = path.join(r.home, ".claude", ".credentials.json");
  const before = fs.existsSync(cp) ? { sha: sha(cp), mtime: fs.statSync(cp).mtimeMs } : null;
  await until(() => r.done, 180000, `${name} result`);
  const main = reqs(name).filter((e) => e.model === MODEL);
  const after = fs.existsSync(cp) ? { sha: sha(cp), mtime: fs.statSync(cp).mtimeMs } : null;
  const files = []; const walk = (d) => { for (const f of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, f.name); f.isDirectory() ? walk(p) : files.push(path.relative(r.home, p)); } }; walk(r.home);
  return { r, main, before, after, files, auth: main.map((e) => `${e.auth.scheme}/${e.auth.token}${e.auth.xApiKey ? "+x-api-key" : ""}`),
    beta: main[0]?.beta, ua: main[0]?.ua, egress: [...new Set(mock.egress.filter((x) => x.at >= r.t0 && x.at <= r.endAt).map((x) => x.host))],
    apiKeySource: initOf(r)?.apiKeySource, res: resultOf(r), final: brief(r) };
}
const credDoc = (k, inMs) => ({ claudeAiOauth: { accessToken: tokens[k], refreshToken: "", expiresAt: Date.now() + inMs, scopes: ["user:inference", "user:profile"], subscriptionType: "max" } });
let a4, b4;
await guard("T4a", async () => {
  a4 = await t4("t4a", { token: "A" });
  rec("T4a", a4.res?.subtype === "success" && a4.main.length > 0 && a4.main.every((e) => e.auth.scheme === "Bearer" && e.auth.token === "A" && !e.auth.xApiKey) && !a4.after,
    JSON.stringify({ auth: a4.auth, credentialFileAfter: a4.after, homeFiles: a4.files, apiKeySource: a4.apiKeySource, egress: a4.egress, final: a4.final }));
});
await guard("T4b", async () => {
  b4 = await t4("t4b", { token: null, credFile: credDoc("B", 3600000) });
  rec("T4b", b4.res?.subtype === "success" && b4.main.length > 0 && b4.main.every((e) => e.auth.scheme === "Bearer" && e.auth.token === "B") && !!b4.after && b4.after.sha === b4.before?.sha && b4.after.mtime === b4.before?.mtime,
    JSON.stringify({ auth: b4.auth, fileBefore: b4.before, fileAfter: b4.after, homeFiles: b4.files, apiKeySource: b4.apiKeySource, egress: b4.egress, final: b4.final }));
  if (a4) R.info.t4HeaderCompare = { betaEqual: a4.beta === b4.beta, uaEqual: a4.ua === b4.ua, betaEnv: a4.beta, betaFile: b4.beta, uaEnv: a4.ua };
});
await guard("T4c", async () => {
  const c4 = await t4("t4c", { token: null, credFile: credDoc("C", 60000) });
  rec("T4c", true, `informative (file token expiring in 60 s, empty refresh token): ${JSON.stringify({ auth: c4.auth, fileBefore: c4.before, fileAfter: c4.after, egress: c4.egress, final: c4.final })}`);
});
R.values.childAuth = R.tests.find((t) => t.id === "T4a")?.result === "pass" ? "env" : R.tests.find((t) => t.id === "T4b")?.result === "pass" ? "file" : null;

await guard("T7", async () => {
  const r = startRun("slow", { prompt: "SPIKE:slow Write thirty words.", tools: false });
  const s = [];
  while (!r.done && Date.now() - r.t0 < 180000) { s.push({ at: Date.now(), ...rss("slow") }); await sleep(250); }
  const evs = r.msgs.filter((x) => x.m.type === "stream_event");
  const e = reqs("slow").find((x) => x.model === MODEL);
  const gen = s.filter((x) => e && x.at >= e.at && x.at <= (e.streamEndAt ?? Infinity));
  R.values.rssActiveMi = gen.length ? Math.max(...gen.map((x) => x.mi)) : null;
  R.info.rssActive = { samplesWhileGenerating: gen.length, procs: Math.max(0, ...gen.map((x) => x.procs)), firstMi: s.find((x) => x.mi > 0)?.mi, streamEvents: evs.length, final: brief(r) };
});

await t3ready;
await guard("T3", async () => {
  for (const r of [t3, t3x]) if (!r.calls.length) throw new Error(`${r.name}: no handler call; ${brief(r)}`);
  const due = Math.max(t3.calls[0].at, t3x.calls[0].at) + PARK_MS;
  while (Date.now() < due) await sleep(1000);
  const last = { t3: rss("t3"), t3x: rss("t3x") };
  clearInterval(sampler);
  const endedEarly = { t3: t3.done, t3x: t3x.done };
  const waitedMs = { t3: Date.now() - t3.calls[0].at, t3x: Date.now() - t3x.calls[0].at };
  for (const r of [t3, t3x]) r.calls[0].resolve("Sunny, 31C.");
  await until(() => t3.done && t3x.done, 180000, "t3 results");
  for (const [id, r] of [["T3", t3], ["T3x", t3x]]) {
    const fin = reqs(r.name).find((e) => e.phase === "final");
    const sentId = reqs(r.name).find((e) => e.phase === "first" && e.model === MODEL)?.reply.blocks[0].id;
    const during = r.msgs.filter((x) => x.at > r.calls[0].at && x.at < r.calls[0].at + waitedMs[r.name]).map((x) => `${x.m.type}${x.m.subtype ? "/" + x.m.subtype : ""}`);
    rec(id, !endedEarly[r.name] && fin?.toolResults?.[0]?.id === sentId && fin.toolResults[0].text.includes("Sunny") && resultOf(r)?.subtype === "success",
      `${id === "T3x" ? "informative, MCP_TOOL_TIMEOUT set; " : ""}handler answered after ${Math.round(waitedMs[r.name] / 1000)} s; ended early: ${endedEarly[r.name]}; messages during wait: ${JSON.stringify(during)}; tool result reached endpoint: ${JSON.stringify(fin?.toolResults)}; ${brief(r)}`);
  }
  R.values.rssParkedMi = last.t3.mi;
  R.info.rssParked = { atResolve: last, samples: parked };
});
rec("T8", null, "needs the real login: the number of 2,000-token calls per hour before the first upstream 429 cannot be measured against a scripted endpoint");
R.values.callsPerHour = null;

R.info.versions = { kit: JSON.parse(fs.readFileSync("node_modules/@anthropic-ai/claude-agent-sdk/package.json", "utf8")).version,
  kitClaudeCodeVersion: JSON.parse(fs.readFileSync("node_modules/@anthropic-ai/claude-agent-sdk/package.json", "utf8")).claudeCodeVersion,
  mcpSdk: JSON.parse(fs.readFileSync("node_modules/@modelcontextprotocol/sdk/package.json", "utf8")).version, node: process.version };
R.info.otherRequests = mock.log.filter((e) => e.kind !== "messages").map((e) => `${e.method} ${e.path} auth=${e.auth.scheme}/${e.auth.token}`);
R.info.egressHosts = [...new Set(mock.egress.map((x) => `${x.via}:${x.host}`))];
R.info.systemPrefixes = [...new Set(mock.log.filter((e) => e.kind === "messages" && e.model === MODEL).flatMap((e) => e.system))];
R.info.auxModels = [...new Set(mock.log.filter((e) => e.kind === "messages" && e.model !== MODEL).map((e) => e.model))];
const mandatory = ["T1", "T3", "T5", "T9"];
const res = (id) => R.tests.find((t) => t.id === id)?.result;
R.verdict = mandatory.every((id) => res(id) === "pass") && (res("T4a") === "pass" || res("T4b") === "pass") ? "pass" : "fail";
dump();
mock.close();
process.exit(0);
