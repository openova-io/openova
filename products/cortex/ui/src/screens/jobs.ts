// Jobs: the job changes cortex pushes over its event stream, newest first.

import { h, type VNode } from "../dom.js";
import type { State } from "../state.js";
import { table, when } from "./parts.js";

export function jobsView(s: State): VNode {
  return h(
    "section",
    { "data-test": "screen-jobs" },
    h("h2", null, "Jobs"),
    h("p", { "data-test": "live" }, s.live ? "Live: changes appear as they happen." : "Not connected: the stream reopens by itself."),
    s.jobs.length > 0
      ? table(
          "jobs",
          ["At", "Kind", "State", "Source", "Document", "Job"],
          s.jobs.map((j) => [when(j.at), j.kind, j.state, j.sourceId, j.documentId, j.jobId]),
        )
      : h("p", { "data-test": "no-jobs" }, "No job has changed since this page opened."),
  );
}
