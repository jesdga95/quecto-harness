// Shapes of the harness HTTP API and its SSE stream. Components read these
// types only, so a real client can replace the mock without touching the UI.

export type ModelStatus =
  | "unloaded"
  | "loading"
  | "loaded"
  | "sleeping"
  | "downloading";
export type Effort = "auto" | "off" | "low" | "medium" | "high" | "xhigh";

/** The wire protocol an address speaks. It is named rather than found: two of
    the three answer the same model list at the same address. */
export type Protocol = "llamacpp" | "chat" | "responses";
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
  /** When the session last changed, which is what the sidebar orders by. */
  updated: string;
  /** The session that launched this one, for a subagent run. Empty is a
      conversation somebody started, which is the only kind the sidebar lists. */
  parent: string;
  /** The subagent runs this session launched, which are reachable nowhere
      else. */
  children: Child[];
}

/** One subagent run, as its launcher lists it. */
export interface Child {
  id: string;
  title: string;
  agent: string;
  /** What the run is doing now. Idle is one that has stopped working, whether
      it finished, failed or was stopped. */
  status: TurnStatus;
}

/** A named connection: where a model runs, what key opens it, which model, and
    how that model is asked. One agent answers for the whole harness. */
export interface Agent {
  id: string;
  name: string;
  /** Whether the agent may answer. A disabled one keeps its settings, cannot
      be selected, and is not offered to the agents that hand out work. */
  enabled: boolean;
  /** The agents this one may hand work to, by id. Empty is an agent that
      answers the person only: it is on no caller's list. */
  calls: string[];
  url: string;
  /** Empty is no key. */
  key: string;
  /** What the address speaks. Nothing probes for it. */
  protocol: Protocol;
  model: string;
  effort: Effort;
  /** Keeps thinking from earlier turns in the prompt. */
  preserve_reasoning: boolean;
  /** Sampling. Null is sent as no field at all, so the model server's own
      value stands. top_k and repeat_penalty reach a llama.cpp server alone,
      since neither OpenAI protocol defines them. */
  temperature: number | null;
  top_p: number | null;
  top_k: number | null;
  repeat_penalty: number | null;
  /** Caps one reply. Null leaves the length to the model server. */
  max_tokens: number | null;
  /** The window the context meter and automatic compaction measure against.
      Null takes what the model server reports, and a server that reports
      nothing leaves the window unknown, which is no limit. */
  max_context: number | null;
  /** Makes a reply repeatable. Null is a new one each turn. */
  seed: number | null;
  /** How many tasks this agent runs at once, its own conversation counted. A
      subagent call naming it past that is refused. */
  concurrency: number;
  /** What this agent is for, in one line, as the agents that may hand it work
      are told. Empty falls back to the first line of the instructions. */
  description: string;
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

/** Which tools the harness offers a turn, for every session. */
export type Toolset = "full" | "minimal";

/** One argument a tool takes, as the settings pane lists it. */
export interface ToolParam {
  name: string;
  type: string;
  description: string;
  required: boolean;
  minimum?: number;
}

/** One tool with its parameters, as the settings pane lists it. */
export interface ToolSpec {
  name: string;
  description: string;
  params: ToolParam[];
}

/** What the harness pane's debug view draws: every tool, which tools each
    toolset sends, and the system prompt each one renders. */
export interface ToolsetDebug {
  tools: ToolSpec[];
  sent: Record<Toolset, string[]>;
  prompts: Record<Toolset, string>;
}

export interface Config {
  listen: string;
  /** The id of the agent answering. Empty when none is chosen. */
  agent: string;
  permissions: PermMode;
  toolset: Toolset;
  compact_at: number;
  /** Tool calls one turn may make. Zero is no limit. */
  max_tool_calls: number;
  shell: string;
  command_timeout_s: number;
  output_limit_bytes: number;
  projects: string[];
}

/** The address and credential of one search provider. */
export interface SearchAccount {
  name: string;
  url: string;
  /** Empty is no key. */
  key: string;
}

/** The search block as the settings dialog draws it. */
export interface SearchConfig {
  /** The provider a search goes to. Empty is off: web_search is not offered. */
  provider: string;
  /** How many results one search may ask for. */
  max_hits: number;
  /** What one search may carry in total, shared across its results rather
      than allowed to each. */
  max_chars: number;
  /** How many seconds a search may take. */
  timeout_s: number;
  /** One account per provider, so naming another and coming back does not
      cost the first one its key. */
  providers: SearchAccount[];
  /** Every provider name there is, so the dialog offers a list it did not
      have to know. */
  known: string[];
  /** Whether a turn is offered web_search. */
  ready: boolean;
}

/** What a write may carry. A field left out keeps the value it has. */
export type SearchPatch = Partial<Omit<SearchConfig, "known" | "ready">>;

/** The answer to a key test. A failure is reported here rather than thrown,
    so a form can draw it. */
export interface SearchProbe {
  ok: boolean;
  error?: string;
}

/** What the harness is talking to: a llama.cpp router, one llama.cpp server,
    an OpenAI-compatible endpoint, or nothing that answered yet. */
export type ServerMode = "router" | "single" | "openai" | "responses" | "unknown";

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
  search: SearchConfig;
  agents: Agent[];
  projects: Project[];
  /** What the active agent's address holds. */
  models: Model[];
}

import type { Plan } from "../plan";
export type { Plan } from "../plan";

/** What a session is doing: planning a change or carrying one out. Each sends
    its own system prompt and its own tools. */
export type WorkMode = "plan" | "execute";

/** One step of the plan the model keeps with todo_write. */
export interface Todo {
  content: string;
  status: "pending" | "in_progress" | "completed";
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
  /** partial marks arguments read out of JSON that is still arriving. */
  | { type: "tool_args"; id: string; name: string; args: Record<string, unknown>; partial?: boolean }
  /** The call left the permission card and began running. timeout_s is the
      deadline a shell command was given, and is null for every other tool. */
  | { type: "tool_start"; id: string; name: string; timeout_s: number | null }
  | {
      type: "tool_output";
      id: string;
      output: string;
      exit_code: number | null;
      truncated: boolean;
      /** What the tool reports that its result does not say, such as the
          hits of a search. */
      meta?: Record<string, unknown> | null;
    }
  | {
      type: "permission";
      id: string;
      tool: string;
      command: string;
      prefix: string;
      agent: string;
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
      compactable?: boolean;
    }
  | { type: "status"; status: TurnStatus }
  /** The session's mode changed. kickoff marks the switch an approved plan
      made, which also starts the work. */
  | { type: "mode"; mode: WorkMode; kickoff: boolean; seq: number; title: string }
  /** Plan mode wrote sections of the draft. It carries the whole draft. */
  | { type: "plan_draft"; plan: Plan }
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
  | { type: "message"; seq: number; text: string; files: Attached[]; from: string }
  /** The files this session is answerable for moved for a reason its own
      stream does not carry, such as a subagent writing one. */
  | { type: "changed" }
  /** A subagent this session launched moved. The row the subagent call drew
      follows it without mirroring its stream. */
  | {
      type: "child";
      session: string;
      status: TurnStatus;
      callId?: string;
      agent?: string;
      title?: string;
    }
  | { type: "error"; message: string }
  | { type: "done"; cost?: TurnCost }
  /** The stream fell too far behind to replay, so it carries a fresh snapshot. */
  | { type: "resync"; transcript: Transcript }
  | { type: "connection"; connected: boolean; message?: string };

export type TranscriptItem =
  /** seq names the entry to the harness, which is how a message is sent
      again. It is 0 until the harness has stored the message. */
  | {
      kind: "user";
      id: string;
      seq: number;
      text: string;
      files: Attached[];
      edited: boolean;
      /** The agent that sent it. Empty is the person. */
      from: string;
      /** When it was sent, in epoch milliseconds. */
      at?: number;
    }
  /** at is when the block was stored, which for the one that ends a turn is
      when the turn ended. */
  | {
      kind: "assistant";
      id: string;
      text: string;
      streaming: boolean;
      cost?: TurnCost;
      at?: number;
    }
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
      /** The arguments are still arriving, and args holds what has come. */
      writing?: boolean;
      /** When the call began running, and the seconds a shell command was
          given. A reloaded transcript has neither. */
      startedAt?: number;
      timeoutS?: number | null;
      /** How long the call took, once it is over. */
      ranMs?: number;
      /** Set on an ask_user_question call that is still waiting, and is the id its
          answer is posted to. */
      askId?: string;
      /** Set on a subagent call: the subagent it started and where that
          subagent has got to. */
      child?: { session: string; agent: string; status: TurnStatus };
      /** What the tool reports that its result does not say, such as the
          hits of a search. */
      meta?: Record<string, unknown> | null;
    }
  | {
      kind: "permission";
      id: string;
      tool: string;
      command: string;
      prefix: string;
      outcome: string | null;
      /** The subagent that asked. Empty is this conversation's own turn, and
          only that one blocks the composer. */
      agent: string;
    }
  | { kind: "compaction"; id: string; before: number; after: number; summary: string }
  /** Where the conversation moved to another mode. kickoff is an approved
      plan starting its execution, and title is that plan's. */
  | { kind: "mode"; id: string; mode: WorkMode; kickoff: boolean; title: string }
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
  /** The agent that sent it. Empty is the person. */
  from: string;
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

/** The four parts of the context, each measured rather than estimated. It is
 *  absent until the fixed parts have been measured against the model. */
export interface Breakdown {
  system: number;
  rules: number;
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
  /** Whether enough has been said since the last summary for a compaction
      to be taken. */
  compactable?: boolean;
}

/** What one turn spent. ms is the wall clock from the message to the answer,
    tool calls included; tokPerS is the rate while the model was generating,
    which excludes them. tokens is the output. The rest are summed over the
    turn's requests and are absent where the turn predates them; cached is also
    absent where the server never said. */
export interface TurnCost {
  tokens: number;
  ms: number;
  tokPerS: number;
  requests: number;
  /** Every prompt token sent, cached ones included. */
  input?: number;
  /** The context as it stood when the turn's last request was made. */
  window?: number;
  cached?: number;
  /** Time inside requests, generating, before the first token, and in tools. */
  llmMs?: number;
  genMs?: number;
  firstMs?: number;
  toolMs?: number;
  agent?: string;
  model?: string;
}

/** One folder in the project, as the panel's browser lists it. path is
    relative to root, empty for root itself. */
export interface ProjectDir {
  root: string;
  path: string;
  entries: { name: string; dir: boolean; bytes: number }[];
}

/** One file in the project, as the panel shows it. Only text carries its
    content; an image is drawn from fileUrl. */
export interface ProjectFile {
  path: string;
  kind: "text" | "image" | "binary" | "large";
  bytes: number;
  content: string;
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
  /** The mode the next turn runs in. */
  mode: WorkMode;
  /** The toolset the session was created with, which every turn of it is
      offered. */
  toolset: Toolset;
  /** The plan being written, or the one last approved. */
  draft: Plan | null;
}

export type EventHandler = (event: HarnessEvent) => void;

/** Receives what sessions are doing: the whole list of those not idle when
    whole is set, otherwise one session that changed. */
export type SessionStatesHandler = (states: Record<string, TurnStatus>, whole: boolean) => void;

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
  probeAgent(
    at: { agent?: string; url: string; key?: string | null; protocol?: Protocol },
    refresh?: boolean,
  ): Promise<Probe>;
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
  /** Sets the order of the project list. It must name every project exactly
      once, since the harness stores the list it is given. */
  reorderProjects(paths: string[]): Promise<Project[]>;
  listSessions(project: string): Promise<Session[]>;
  createSession(project: string): Promise<Session>;
  /** Points an idle session at another model, keeping its history. */
  getSession(id: string): Promise<Transcript>;
  /** Every file the session wrote, against the copy kept before its first
      write and the file on disk now. */
  getChanges(id: string): Promise<Changes>;
  /** One file in the session's project. Rejects with the harness's message
      for a path outside the project, a folder, or a file that is not there. */
  readFile(id: string, path: string): Promise<ProjectFile>;
  /** Files in a project whose path holds every word of q, best first. */
  findFiles(project: string, q: string): Promise<string[]>;
  /** One folder in the session's project, folders first. */
  listDir(id: string, path: string): Promise<ProjectDir>;
  /** Where an image in the session's project is served from. */
  fileUrl(id: string, path: string): string;
  deleteSession(id: string): Promise<void>;
  renameSession(id: string, title: string): Promise<void>;
  /** Files are read by the harness and inlined into the message. What comes
      back is the attachments as it stored them, ids included, and the
      sequence number the message was stored under. */
  sendMessage(id: string, text: string, files?: string[], mode?: WorkMode): Promise<Sent>;
  setMode(id: string, mode: WorkMode): Promise<void>;
  /** Runs the last message again, with text in place of the words it was sent
      with. The reply to it is discarded. */
  resend(id: string, seq: number, text?: string): Promise<void>;
  stop(id: string): Promise<void>;
  compact(id: string): Promise<void>;
  decide(permissionId: string, decision: PermissionDecision): Promise<void>;
  answer(questionId: string, answer: Answer): Promise<void>;
  updateConfig(patch: Partial<Config>): Promise<Config>;
  /** Saves the search block and answers with the resolved one. */
  updateSearch(patch: SearchPatch): Promise<SearchConfig>;
  /** Runs one cheap billed request against an address and key that may not be
      saved yet. A failure is reported in the answer, not thrown. */
  probeSearch(at: { provider: string; url: string; key: string }): Promise<SearchProbe>;
  listAllowlist(project: string): Promise<string[]>;
  removeAllowed(project: string, prefix: string): Promise<string[]>;
  /** Every tool the harness offers, which tools each toolset sends, and the
      system prompt each one renders, for the harness pane's debug view. */
  getTools(): Promise<ToolsetDebug>;
  /** Subscribes to the session stream. The returned function closes it. */
  events(session: string, onEvent: EventHandler): () => void;
  /** Follows every session's status, open or not. The returned function
      closes it. */
  watchSessions(onStates: SessionStatesHandler): () => void;
}
