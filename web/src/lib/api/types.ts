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
  | "loading"
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

/** A named connection: where a model runs, what key opens it, which model, and
    how that model is asked. One agent answers for the whole harness. */
export interface Agent {
  id: string;
  name: string;
  url: string;
  /** Empty is no key. */
  key: string;
  model: string;
  effort: Effort;
  /** Keeps thinking from earlier turns in the prompt. */
  preserve_reasoning: boolean;
  /** Sampling. Null is sent as no field at all, so the model server's own
      value stands. top_k and repeat_penalty reach a llama.cpp server alone,
      since the OpenAI API defines neither. */
  temperature: number | null;
  top_p: number | null;
  top_k: number | null;
  repeat_penalty: number | null;
  /** Caps one reply. Null leaves the length to the model server. */
  max_tokens: number | null;
  /** Makes a reply repeatable. Null is a new one each turn. */
  seed: number | null;
  /** Added to the end of the built-in system prompt. */
  instructions: string;
}

/** What a write may carry. A field left out keeps the value it has. */
export type AgentPatch = Partial<Omit<Agent, "id">>;

/** What one address answered when it was asked what it holds. A failure is
    reported here rather than thrown, so a form can draw it. */
export interface Probe {
  mode: ServerMode;
  models: Model[];
  detectError: string;
  detectKind: string;
  modelsError: string;
  modelsKind: string;
}

/** The permission policy. ask shows a card for everything, allowlist runs
    writes and allowlisted command prefixes, full runs every tool call with
    nothing checked. */
export type PermMode = "ask" | "allowlist" | "full";

export interface Config {
  listen: string;
  /** The id of the agent answering. Empty when none is chosen. */
  agent: string;
  permissions: PermMode;
  compact_at: number;
  /** Tool calls one turn may make. Zero is no limit. */
  max_tool_calls: number;
  shell: string;
  command_timeout_s: number;
  output_limit_bytes: number;
  projects: string[];
}

/** What the harness is talking to: a llama.cpp router, one llama.cpp server,
    an OpenAI-compatible endpoint, or nothing that answered yet. */
export type ServerMode = "router" | "single" | "openai" | "unknown";

export interface State {
  mode: ServerMode;
  /** Empty when the model server answered. */
  detectError?: string;
  /** Error taxonomy: "unreachable" means nothing answered at all. */
  detectKind?: string;
  /** From GET /v1/models, which is what proves there is a model behind the
      address. */
  modelsError?: string;
  modelsKind?: string;
  config: Config;
  agents: Agent[];
  projects: Project[];
  /** What the active agent's address holds. */
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

/** What the user chose on a question card. options are indexes into the
    question's own list; text is an answer typed instead of chosen. */
export interface Answer {
  options: number[];
  text: string;
}

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
  /** A tool call is waiting on an answer. The row the call drew is the card,
      so this carries the id to answer rather than the question again. */
  | { type: "question"; id: string; ask: string }
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
  | { type: "done"; cost?: TurnCost }
  /** The stream fell too far behind to replay, so it carries a fresh snapshot. */
  | { type: "resync"; transcript: Transcript }
  | { type: "connection"; connected: boolean; message?: string };

export type TranscriptItem =
  /** seq names the entry to the harness, which is how a message is sent
      again. It is 0 until the harness has stored the message. */
  | { kind: "user"; id: string; seq: number; text: string; files: Attached[]; edited: boolean }
  | { kind: "assistant"; id: string; text: string; streaming: boolean; cost?: TurnCost }
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
      /** Set on an ask_question call that is still waiting, and is the id its
          answer is posted to. */
      askId?: string;
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

/** What one turn spent. ms is the wall clock from the message to the answer,
    tool calls included; tokPerS is the rate while the model was generating,
    which excludes them. */
export interface TurnCost {
  tokens: number;
  ms: number;
  tokPerS: number;
  requests: number;
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
  /** What the active agent's address holds. */
  listModels(refresh?: boolean): Promise<Model[]>;
  listAgents(): Promise<Agent[]>;
  createAgent(patch: AgentPatch): Promise<Agent>;
  updateAgent(id: string, patch: AgentPatch): Promise<Agent>;
  deleteAgent(id: string): Promise<Agent[]>;
  selectAgent(id: string): Promise<Agent[]>;
  /** Reads one address, saved or not, and reports what it said either way. An
      agent id fills in the key the browser was never given. */
  probeAgent(at: { agent?: string; url: string; key?: string | null }, refresh?: boolean): Promise<Probe>;
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
  createSession(project: string): Promise<Session>;
  /** Points an idle session at another model, keeping its history. */
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
  answer(questionId: string, answer: Answer): Promise<void>;
  updateConfig(patch: Partial<Config>): Promise<Config>;
  listAllowlist(project: string): Promise<string[]>;
  removeAllowed(project: string, prefix: string): Promise<string[]>;
  /** Subscribes to the session stream. The returned function closes it. */
  events(session: string, onEvent: EventHandler): () => void;
}
