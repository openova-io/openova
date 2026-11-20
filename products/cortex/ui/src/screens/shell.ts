// The shell around the screens: the tabs, the outcome line and the screen of the chosen tab.

import { h, type VNode } from "../dom.js";
import { TABS, type Actions, type State, type Tab } from "../state.js";
import { assistantsView } from "./assistants.js";
import { documentsView } from "./documents.js";
import { jobsView } from "./jobs.js";
import { sourcesView } from "./sources.js";
import { uploadView } from "./upload.js";

const TITLES: Record<Tab, string> = { sources: "Sources", upload: "Upload", documents: "Documents", jobs: "Jobs", assistants: "Assistants" };

function screen(s: State, act: Actions): VNode {
  switch (s.tab) {
    case "sources":
      return sourcesView(s, act);
    case "upload":
      return uploadView(s, act);
    case "documents":
      return documentsView(s, act);
    case "jobs":
      return jobsView(s);
    case "assistants":
      return assistantsView(s, act);
  }
}

export function shellView(s: State, act: Actions): VNode {
  return h(
    "div",
    { class: "shell" },
    h("header", null, h("h1", null, "Knowledge hub")),
    h(
      "nav",
      null,
      TABS.map((t) => h("button", { type: "button", "data-test": `tab-${t}`, "aria-current": s.tab === t ? "page" : null, onclick: () => act.show(t) }, TITLES[t])),
    ),
    h("p", { role: "status", "data-test": "message" }, s.busy ? "Working..." : s.message),
    h("main", null, screen(s, act)),
  );
}
