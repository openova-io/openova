// What the screens show, and the actions they call. app.ts owns both; a screen only reads.

import type { Loaded } from "./application.js";
import type { Answer, DocumentInfo, Hit, SourceStatus } from "./cortex.js";
import type { JobEvent } from "./events.js";
import type { AssistantDef, Difference, SourceDef } from "./params.js";

export const TABS = ["sources", "upload", "documents", "jobs", "assistants"] as const;
export type Tab = (typeof TABS)[number];

export interface State {
  tab: Tab;
  busy: boolean;
  message: string; // the last outcome or refusal, one line
  app: Loaded | null; // the Application's parameters and version; null until it is read
  unread: { app?: string; models?: string }; // why the read of the Application or of the models failed, by its code
  conflict: Difference[] | null; // set when a save was refused because the Application changed
  draftSource: SourceDef | null; // a source that was not saved, offered again
  draftAssistant: AssistantDef | null; // an assistant that was not saved, offered again
  done: Record<"add-source" | "add-assistant" | "upload", number>; // saves and uploads of each form: a new count draws it afresh, empty
  sources: SourceStatus[];
  documents: DocumentInfo[]; // documents uploaded, opened or seen in a job, newest first
  hits: Hit[];
  jobs: JobEvent[]; // newest first
  live: boolean; // the job stream is open
  models: string[] | null; // the assistants this user may call; null until cortex listed them
  answer: Answer | null;
}

export interface Actions {
  show(tab: Tab): void;
  addSource(s: SourceDef): Promise<void>;
  removeSource(id: string): Promise<void>;
  addAssistant(a: AssistantDef): Promise<void>;
  removeAssistant(id: string): Promise<void>;
  upload(sourceId: string, file: File, readers: string[]): Promise<void>;
  search(query: string): Promise<void>;
  open(documentId: string): Promise<void>;
  download(documentId: string): Promise<void>;
  remove(documentId: string): Promise<void>;
  ask(assistant: string, question: string): Promise<void>;
}

export function emptyState(): State {
  return {
    tab: "sources",
    busy: false,
    message: "",
    app: null,
    unread: {},
    conflict: null,
    draftSource: null,
    draftAssistant: null,
    done: { "add-source": 0, "add-assistant": 0, upload: 0 },
    sources: [],
    documents: [],
    hits: [],
    jobs: [],
    live: false,
    models: null,
    answer: null,
  };
}
