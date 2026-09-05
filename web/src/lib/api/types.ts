// Shapes of the harness HTTP API and its SSE stream. Components read these
// types only, so a real client can replace the mock without touching the UI.

export type ModelStatus =
  | "unloaded"
  | "loading"
  | "loaded"
  | "sleeping"
  | "downloading";
export type Effort = "auto" | "off" | "low" | "medium" | "high";
export type TurnStatus =
  | "idle"
  | "thinking"
  | "streaming"
  | "running"
  | "waiting"
  | "compacting";

export interface Model {
  id: string;
  status: ModelStatus;
  /** The model reads images as well as text. */
  vision: boolean;
  /** Per slot context window. A hint parsed from the launch args until loaded. */
  ctx: number;
  ctxIsHint: boolean;
}

export interface Project {
  path: string;
  name: string;
}

export interface Session {
  id: string;
  project: string;
  model: string;
  title: string;
  created: string;
}

export interface ServerConfig {
  url: string;
  token: string;
}

/** The permission policy. ask shows a card for everything, allowlist runs
    writes and allowlisted command prefixes, full runs every tool call with
    nothing checked. */
export type PermMode = "ask" | "allowlist" | "full";

export interface Config {
  listen: string;
  server: ServerConfig;
  model: string;
  effort: Effort;
  /** Keeps thinking from earlier turns in the prompt. */
  preserve_reasoning: boolean;
  permissions: PermMode;
  compact_at: number;
  /** Tool calls one turn may make. Zero is no limit. */
  max_tool_calls: number;
  shell: string;
  command_timeout_s: number;
  output_limit_bytes: number;
  projects: string[];
}

export interface State {
  mode: "router" | "single";
  /** Empty when the model server answered. */
  detectError?: string;
  /** Error taxonomy: "unreachable" means nothing answered at all. */
  detectKind?: string;
  /** From GET /v1/models, which is what proves this is a llama.cpp server. */
  modelsError?: string;
  modelsKind?: string;
  config: Config;
  projects: Project[];
  models: Model[];
}

/** One step of the plan the model keeps with write_todos. */
export interface Todo {
  text: string;
  status: "todo" | "doing" | "done";
}

export type PermissionDecision =
  | { decision: "once" }
  | { decision: "always"; prefix: string }
  | { decision: "deny"; reason: string };

export type HarnessEvent =
  | { type: "delta"; text: string }
  | { type: "reasoning"; text: string }
  | { type: "tool_call"; id: string; name: string; args: Record<string, unknown> }
  /** Arguments stream in after the call opens, so they land in a second event. */
  | { type: "tool_args"; id: string; name: string; args: Record<string, unknown> }
  | {
      type: "tool_output";
      id: string;
      output: string;
      exit_code: number | null;
      truncated: boolean;
    }
  | {
      type: "permission";
      id: string;
      tool: string;
      command: string;
      prefix: string;
    }
  /** The card was answered, or a stop answered it, and every tab closes it. */
  | { type: "permission_done"; id: string; outcome: string }
  | {
      type: "usage";
      prompt_tokens: number;
      completion_tokens: number;
      n_ctx: number;
      tok_per_s?: number;
      cached_pct?: number;
      breakdown?: Breakdown;
    }
  | { type: "status"; status: TurnStatus }
  /** A line the harness writes into the transcript, such as a model change. */
  | { type: "status_entry"; text: string }
  /** The model named the session after its first turn. */
  | { type: "title"; title: string }
  | { type: "compacted"; before: number; after: number; summary: string }
  /** The model rewrote the plan. It replaces the one held. */
  | { type: "todos"; todos: Todo[] }
  /** A message is waiting for the running turn, as it now stands. */
  | { type: "queued"; steer: Steer }
  /** The harness appended a user message, which is a queued one being picked
      up by the turn it was typed against. */
  | { type: "message"; seq: number; text: string; files: Attached[] }
  | { type: "error"; message: string }
  | { type: "done" }
  /** The stream fell too far behind to replay, so it carries a fresh snapshot. */
  | { type: "resync"; transcript: Transcript }
  | { type: "connection"; connected: boolean; message?: string };

export type TranscriptItem =
  /** seq names the entry to the harness, which is how a message is sent
      again. It is 0 until the harness has stored the message. */
  | { kind: "user"; id: string; seq: number; text: string; files: Attached[]; edited: boolean }
  | { kind: "assistant"; id: string; text: string; streaming: boolean }
  | { kind: "reasoning"; id: string; text: string; tokens: number; open: boolean }
  | {
      kind: "tool";
      id: string;
      name: string;
      args: Record<string, unknown>;
      output: string;
      exitCode: number | null;
      truncated: boolean;
      running: boolean;
    }
  | {
      kind: "permission";
      id: string;
      tool: string;
      command: string;
      prefix: string;
      outcome: string | null;
    }
  | { kind: "compaction"; id: string; before: number; after: number; summary: string }
  | { kind: "status"; id: string; text: string }
  | { kind: "error"; id: string; message: string };

/** A message as the harness took it: the entry it became, or the queue it
    joined behind a turn that was already running. */
export interface Sent {
  queued: boolean;
  seq: number;
  files: Attached[];
  steer: Steer | null;
}

/** A message typed while a turn was running, waiting for it. */
export interface Steer {
  text: string;
  files: Attached[];
}

/** One file attached to a message, as the transcript shows it. An image is
    fetched back from the harness by its id; text is already in the prompt. */
export interface Attached {
  path: string;
  kind: "text" | "image";
  bytes: number;
  file?: string;
}

/** What a file this session wrote became. Only changed and new carry hunks;
    the rest name why there is nothing to draw. */
export type ChangeStatus =
  | "changed"
  | "new"
  | "deleted"
  | "same"
  | "binary"
  | "large"
  | "lost";

export type DiffKind = "context" | "added" | "removed";

/** One row of a hunk. The text carries no sign; the kind is the sign. */
export interface DiffLine {
  kind: DiffKind;
  text: string;
}

export interface DiffHunk {
  old_start: number;
  new_start: number;
  lines: DiffLine[];
}

export interface FileChange {
  path: string;
  status: ChangeStatus;
  added: number;
  removed: number;
  /** Lines past the server's limit that were counted but not sent. */
  omitted: number;
  hunks: DiffHunk[];
}

export interface Changes {
  files: FileChange[];
  added: number;
  removed: number;
}

/** The three parts of the context, each measured rather than estimated. It is
 *  absent until the fixed parts have been measured against the model. */
export interface Breakdown {
  system: number;
  tools: number;
  messages: number;
}

export interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  n_ctx: number;
  tok_per_s?: number;
  cached_pct?: number;
  breakdown?: Breakdown;
}

export interface Transcript {
  session: Session;
  items: TranscriptItem[];
  usage: Usage;
  todos: Todo[];
  /** What the session is doing. A snapshot is what a page reloaded mid-turn
      has to read this from. */
  status: TurnStatus;
  /** The message waiting for a turn that is still running. */
  steer: Steer | null;
}

export type EventHandler = (event: HarnessEvent) => void;

export interface HarnessApi {
  getState(): Promise<State>;
  listModels(refresh?: boolean): Promise<Model[]>;
  /** Opens the host's folder dialog. Resolves to null when it is dismissed. */
  pickDirectory(): Promise<string | null>;
  /** Opens the host's file dialog, which takes several files at once. Empty
      when it is dismissed. */
  pickFiles(): Promise<string[]>;
  /** Stores bytes the browser has no path for, a pasted image or block of
      text, and answers with the path a message attaches it by. */
  pasteFile(name: string, data: string): Promise<string>;
  addProject(path: string): Promise<Project[]>;
  removeProject(path: string): Promise<Project[]>;
  listSessions(project: string): Promise<Session[]>;
  createSession(project: string, model: string): Promise<Session>;
  /** Points an idle session at another model, keeping its history. */
  setSessionModel(id: string, model: string): Promise<void>;
  getSession(id: string): Promise<Transcript>;
  /** Every file the session wrote, against the copy kept before its first
      write and the file on disk now. */
  getChanges(id: string): Promise<Changes>;
  deleteSession(id: string): Promise<void>;
  renameSession(id: string, title: string): Promise<void>;
  /** Files are read by the harness and inlined into the message. What comes
      back is the attachments as it stored them, ids included, and the
      sequence number the message was stored under. */
  sendMessage(id: string, text: string, files?: string[]): Promise<Sent>;
  /** Runs the last message again, with text in place of the words it was sent
      with. The reply to it is discarded. */
  resend(id: string, seq: number, text?: string): Promise<void>;
  stop(id: string): Promise<void>;
  compact(id: string): Promise<void>;
  decide(permissionId: string, decision: PermissionDecision): Promise<void>;
  updateConfig(patch: Partial<Config>): Promise<Config>;
  listAllowlist(project: string): Promise<string[]>;
  removeAllowed(project: string, prefix: string): Promise<string[]>;
  /** Subscribes to the session stream. The returned function closes it. */
  events(session: string, onEvent: EventHandler): () => void;
}
