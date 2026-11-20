// GET /v1/events is a server-sent event stream with one frame "event: job" per job change and a
// comment line now and then. The browser's EventSource cannot send the token, so the stream is
// read from a fetch body. A frame is a hint: the screen refetches what it shows.

export interface JobEvent {
  jobId: string;
  sourceId: string;
  documentId: string;
  kind: string;
  state: string;
  at: string;
}

export interface Frame {
  event: string;
  data: string;
}

// parseFrames takes the complete frames out of text and returns them with the incomplete rest.
export function parseFrames(text: string): { frames: Frame[]; rest: string } {
  const blocks = text.replace(/\r\n?/g, "\n").split("\n\n");
  const rest = blocks.pop() ?? "";
  const frames: Frame[] = [];
  for (const block of blocks) {
    let event = "message";
    const data: string[] = [];
    for (const line of block.split("\n")) {
      if (line.startsWith(":") || line === "") continue;
      const i = line.indexOf(":");
      const field = i < 0 ? line : line.slice(0, i);
      const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
      if (field === "event") event = value;
      else if (field === "data") data.push(value);
    }
    if (data.length > 0) frames.push({ event, data: data.join("\n") });
  }
  return { frames, rest };
}

// readJobs reads body until it ends and hands each job frame to onJob.
export async function readJobs(body: ReadableStream<Uint8Array>, onJob: (e: JobEvent) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    const text = buffer + (done ? decoder.decode() : decoder.decode(value, { stream: true }));
    // A chunk can end between the CR and the LF of one line end. The CR waits for the next chunk:
    // read alone it would be a line end, and the LF after it a second one that ends the frame early.
    const cut = !done && text.endsWith("\r") ? text.length - 1 : text.length;
    const parsed = parseFrames(text.slice(0, cut));
    buffer = parsed.rest + text.slice(cut);
    for (const f of parsed.frames) {
      if (f.event === "job") onJob(JSON.parse(f.data) as JobEvent);
    }
    if (done) return;
  }
}
