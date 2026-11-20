// The cortex routes the screens call, each with the signed-in user's token. None of them changes
// a definition: sources and assistants are Application parameters (application.ts).

import { readJobs, type JobEvent } from "./events.js";
import { call, callJSON, type Fetch } from "./http.js";

export interface SourceStatus {
  id: string;
  title: string;
  state: string; // idle, syncing or error
  documents: number;
  chunks: number;
  lastSyncAt: string | null;
  lastError: string | null;
}
export interface DocumentInfo {
  id: string;
  sourceId: string;
  title: string;
  uri: string;
  state: string;
  updatedAt: string;
}
export interface Hit {
  documentId: string;
  sourceId: string;
  title: string;
  uri: string;
  chunk: { id: number; text: string; page: number | null; score: number };
}
export interface Citation {
  index: number;
  documentId: string;
  title: string;
  uri: string;
  page: number | null;
}
export interface Answer {
  content: string;
  citations: Citation[];
}

export class Cortex {
  constructor(
    private readonly base: string,
    private readonly token: () => string,
    private readonly fetchFn: Fetch,
  ) {}

  private url(path: string): string {
    return `${this.base}${path}`;
  }

  async sources(): Promise<SourceStatus[]> {
    return (await callJSON<{ items: SourceStatus[] }>(this.fetchFn, this.token, this.url("/v1/sources"))).items;
  }

  // upload sends one file; with readers the document reads only for them and the source's managers.
  async upload(sourceId: string, file: Blob, filename: string, readers: string[]): Promise<{ documentId: string; jobId: string }> {
    const form = new FormData();
    form.append("file", file, filename);
    for (const r of readers) form.append("reader", r);
    const path = `/v1/sources/${encodeURIComponent(sourceId)}/documents`;
    return callJSON(this.fetchFn, this.token, this.url(path), { method: "POST", body: form });
  }

  async document(id: string): Promise<DocumentInfo> {
    return callJSON(this.fetchFn, this.token, this.url(`/v1/documents/${encodeURIComponent(id)}`));
  }

  async content(id: string): Promise<Blob> {
    return (await call(this.fetchFn, this.token, this.url(`/v1/documents/${encodeURIComponent(id)}/content`))).blob();
  }

  async remove(id: string): Promise<{ jobId: string }> {
    return callJSON(this.fetchFn, this.token, this.url(`/v1/documents/${encodeURIComponent(id)}`), { method: "DELETE" });
  }

  async search(query: string): Promise<Hit[]> {
    const init = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ query }) };
    return (await callJSON<{ results: Hit[] }>(this.fetchFn, this.token, this.url("/v1/search"), init)).results;
  }

  // models lists the assistants the user may call.
  async models(): Promise<string[]> {
    const list = await callJSON<{ data: { id: string }[] }>(this.fetchFn, this.token, this.url("/v1/models"));
    return list.data.map((m) => m.id);
  }

  async ask(assistant: string, question: string): Promise<Answer> {
    const body = JSON.stringify({ model: assistant, messages: [{ role: "user", content: question }] });
    const init = { method: "POST", headers: { "Content-Type": "application/json" }, body };
    const res = await callJSON<{ choices: { message: { content: string } }[]; citations?: Citation[] }>(
      this.fetchFn,
      this.token,
      this.url("/v1/chat/completions"),
      init,
    );
    const first = res.choices[0];
    if (!first) throw new Error("the answer holds no message");
    // An answer without passages carries citations null: nothing was cited.
    return { content: first.message.content, citations: res.citations ?? [] };
  }

  // events reads the job stream until the server ends it or signal aborts it.
  async events(signal: AbortSignal, onJob: (e: JobEvent) => void): Promise<void> {
    const res = await call(this.fetchFn, this.token, this.url("/v1/events"), { signal, headers: { Accept: "text/event-stream" } });
    if (res.body) await readJobs(res.body, onJob);
  }
}
