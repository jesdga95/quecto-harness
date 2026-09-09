import type {
  Agent,
  AgentPatch,
  Answer,
  Attached,
  Breakdown,
  Changes,
  Config,
  Effort,
  FileChange,
  EventHandler,
  HarnessApi,
  HarnessEvent,
  Model,
  ModelStatus,
  PermMode,
  PermissionDecision,
  Probe,
  Project,
  Sent,
  ServerMode,
  Session,
  State,
  Steer,
  Todo,
  Transcript,
  TranscriptItem,
  TurnCost,
  TurnStatus,
  Usage,
} from "./types";

const BASE = "/api";

// Every failed call arrives as this. Status 0 means the request never reached
// the harness at all; 401 means the session cookie is gone and nothing will
// work until the printed URL is opened again.
export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// Server payloads. Every field is optional because a response is a guess
// until it has been read, and a missing field must not become a zero value
// that the interface then presents as fact.

interface WireModel {
  id?: string;
  status?: string;
  modalities?: string[];
  ctx_hint?: number;
  ctx_source?: string;
}

interface WireConfig {
  agent?: string;
  permissions?: string;
  compact_at?: number;
  max_tool_calls?: number;
  shell?: string;
  command_timeout_s?: number;
  output_limit_bytes?: number;
  listen?: string;
}

interface WireAgent {
  id?: string;
  name?: string;
  url?: string;
  key?: string;
  model?: string;
  effort?: string;
  preserve_reasoning?: boolean;
  temperature?: number | null;
  top_p?: number | null;
  top_k?: number | null;
  repeat_penalty?: number | null;
  max_tokens?: number | null;
  seed?: number | null;
  instructions?: string;
}

/** What every agent write answers with: the list as it now stands, which one
    answers, and the record that was written. */
interface WireAgents {
  agents?: WireAgent[];
  agent?: string;
  saved?: WireAgent;
}

interface WireProbe {
  mode?: string;
  models?: WireModel[];
  detect_error?: string;
  detect_kind?: string;
  models_error?: string;
  models_kind?: string;
}

interface WireState {
  mode?: string;
  agents?: WireAgent[];
  config?: WireConfig;
  projects?: string[];
  models?: WireModel[];
  detect_error?: string;
  detect_kind?: string;
  models_kind?: string;
  models_error?: string;
}

interface WireSummary {
  id?: string;
  project?: string;
  model?: string;
  title?: string;
  created?: string;
}

interface WireToolCall {
  id?: string;
  index?: number;
  function?: { name?: string; arguments?: string };
}

interface WirePermission {
  id?: string;
  tool?: string;
  command?: string;
  path?: string;
  prefix?: string;
  args?: string;
  decision?: string;
  reason?: string;
  content?: string;
}

interface WireEntry {
  seq?: number;
  kind?: string;
  content?: string;
  reasoning?: string;
  tool_calls?: WireToolCall[];
  tool_call_id?: string;
  tool_name?: string;
  exit_code?: number | null;
  truncated?: boolean;
  permission?: WirePermission | null;
  attachments?: { path?: string; kind?: string; bytes?: number; file?: string }[] | null;
  edited?: boolean;
  visible?: boolean;
  meta?: Record<string, unknown> | null;
}

interface WireAttachment {
  path?: string;
  kind?: string;
  bytes?: number;
  file?: string;
}

interface WireSteer {
  text?: string;
  attachments?: WireAttachment[];
}

// A message either became an entry or joined the queue, and the two answers
// carry different things.
type WireSent =
  | { queued: true; steer: WireSteer }
  | { queued: false; seq: number; attachments?: WireAttachment[] };

interface WireSnapshot {
  id?: string;
  project?: string;
  model?: string;
  title?: string;
  created?: string;
  n_ctx?: number;
  fill?: number;
  entries?: WireEntry[];
  todos?: { text?: string; status?: string }[] | null;
  pending?: WirePermission[];
  question?: { id?: string; call_id?: string } | null;
  state?: string;
  steer?: WireSteer | null;
  last_seq?: number;
  last_usage?: { prompt_tokens?: number; completion_tokens?: number } | null;
  breakdown?: { system?: number; tools?: number; messages?: number } | null;
}

interface WireEvent {
  type?: string;
  seq?: number;
  state?: string;
  text?: string;
  index?: number;
  id?: string;
  name?: string;
  arguments?: string;
  tool?: string;
  command?: string;
  path?: string;
  prefix?: string;
  args?: string;
  output?: string;
  exit_code?: number | null;
  truncated?: boolean;
  decision?: string;
  reason?: string;
  ask?: string;
  content?: string;
  attachments?: WireAttachment[];
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  n_ctx?: number;
  before?: number;
  after?: number;
  summary?: string;
  title?: string;
  todos?: { text?: string; status?: string }[] | null;
  message?: string;
  breakdown?: { system?: number; tools?: number; messages?: number } | null;
  tokens?: number;
  ms?: number;
  tok_per_s?: number;
  requests?: number;
}

// transport

async function call<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin" };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    init.headers = { "content-type": "application/json" };
  }

  let res: Response;
  try {
    res = await fetch(BASE + path, init);
  } catch {
    throw new ApiError("The harness is not reachable", 0);
  }

  if (!res.ok) throw new ApiError(await failure(res), res.status);
  const text = await res.text();
  if (!text) return {} as T;
  try {
    return JSON.parse(text) as T;
  } catch {
    throw new ApiError("The harness sent a reply that is not JSON", res.status);
  }
}

async function failure(res: Response): Promise<string> {
  if (res.status === 401) return "Not authorized: run quecto-harness open";
  // A dev proxy in front of a harness that is not listening answers for it,
  // so a gateway status means the same thing as a refused connection.
  if (res.status >= 502 && res.status <= 504) return "The harness is not reachable";
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) return body.error;
  } catch {
    // A body that is not JSON leaves only the status line to report.
  }
  return `${res.status} ${res.statusText || "Request failed"}`;
}

// mapping

const STATUSES: ModelStatus[] = [
  "unloaded",
  "loading",
  "loaded",
  "sleeping",
  "downloading",
];
const EFFORTS: Effort[] = ["auto", "off", "low", "medium", "high"];
const PERM_MODES: PermMode[] = ["ask", "allowlist", "full"];

/** The harness reports where a context number came from. These are the ones
    the server stated; anything else was worked out from its launch arguments. */
const MEASURED_CTX = new Set(["props", "meta", "models"]);

// The server's turn states are finer than the six the interface draws, so
// several collapse onto one. Anything terminal reads as idle.
const TURN_STATES: Record<string, TurnStatus> = {
  idle: "idle",
  loading: "loading",
  preflight: "thinking",
  compacting: "compacting",
  streaming: "streaming",
  deciding: "waiting",
  executing: "running",
  final: "streaming",
  stopped: "idle",
  failed: "idle",
};

function baseName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

function toProjects(paths: string[]): Project[] {
  return paths.map((p) => ({ path: p, name: baseName(p) }));
}

function toModel(m: WireModel): Model {
  const status = STATUSES.find((s) => s === m.status) ?? "unloaded";
  return {
    id: m.id ?? "",
    status,
    vision: (m.modalities ?? []).includes("image"),
    ctx: m.ctx_hint ?? 0,
    ctxIsHint: !MEASURED_CTX.has(m.ctx_source ?? ""),
  };
}

function toAgent(a: WireAgent): Agent {
  return {
    id: a.id ?? "",
    name: a.name || "Agent",
    url: a.url ?? "",
    key: a.key ?? "",
    model: a.model ?? "",
    effort: toEffort(a.effort),
    preserve_reasoning: a.preserve_reasoning ?? true,
    temperature: a.temperature ?? null,
    top_p: a.top_p ?? null,
    top_k: a.top_k ?? null,
    repeat_penalty: a.repeat_penalty ?? null,
    max_tokens: a.max_tokens ?? null,
    seed: a.seed ?? null,
    instructions: a.instructions ?? "",
  };
}

function toProbe(p: WireProbe): Probe {
  return {
    mode: toMode(p.mode),
    models: (p.models ?? []).map(toModel),
    detectError: p.detect_error ?? "",
    detectKind: p.detect_kind ?? "",
    modelsError: p.models_error ?? "",
    modelsKind: p.models_kind ?? "",
  };
}

function toEffort(value: string | undefined): Effort {
  return EFFORTS.find((e) => e === value) ?? "medium";
}

// An unreadable policy reads as ask.
function toPermMode(value: string | undefined): PermMode {
  return PERM_MODES.find((m) => m === value) ?? "ask";
}

function toStatus(value: string | undefined): TurnStatus {
  return TURN_STATES[value ?? ""] ?? "idle";
}

function toSession(s: WireSummary): Session {
  return {
    id: s.id ?? "",
    project: s.project ?? "",
    model: s.model ?? "",
    title: s.title || "New session",
    created: s.created ?? new Date().toISOString(),
  };
}

function parseArgs(raw: string | undefined): Record<string, unknown> | null {
  if (!raw) return null;
  try {
    const value: unknown = JSON.parse(raw);
    return value && typeof value === "object" ? (value as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/** A status outside the three the schema names reads as a step not started. */
function toTodos(wire: { text?: string; status?: string }[] | null | undefined): Todo[] {
  return (wire ?? [])
    .filter((t) => (t.text ?? "") !== "")
    .map((t) => ({
      text: t.text ?? "",
      status: t.status === "doing" || t.status === "done" ? t.status : "todo",
    }));
}

/** The split is dropped unless all three parts are present, since a partial
 *  one would read as a measurement it is not. */
function toBreakdown(
  wire: { system?: number; tools?: number; messages?: number } | null | undefined,
): Breakdown | undefined {
  if (!wire) return undefined;
  const { system, tools, messages } = wire;
  if (typeof system !== "number" || typeof tools !== "number" || typeof messages !== "number") {
    return undefined;
  }
  return { system, tools, messages };
}

function count(value: unknown): number {
  return typeof value === "number" ? value : 0;
}

// Both mean the call was never put to the user, so neither draws a row.
const SILENT_DECISIONS = new Set(["auto", "full"]);

/** What an answered card says, matching the labels the live flow writes. */
function outcomeOf(p: WirePermission): string {
  if (p.decision === "once") return "Allowed once";
  if (p.decision === "always") return `Always allowed: ${p.prefix ?? ""}`;
  if (p.decision === "stopped") return "Stopped";
  return p.reason ? `Denied: ${p.reason}` : "Denied";
}

function toPermission(p: WirePermission, outcome: string | null, fallback = ""): TranscriptItem {
  return {
    kind: "permission",
    id: p.id || fallback,
    tool: p.tool ?? "",
    command: p.command || p.path || p.args || "",
    prefix: p.prefix ?? "",
    outcome,
  };
}

/** Four characters to a token, the same estimate the thinking block shows. */
function thoughtTokens(text: string): number {
  return Math.max(1, Math.ceil(text.length / 4));
}

/** The turn cost, from a done event or from the meta of the entry that ended
    the turn. Absent where the turn was stopped or failed before it counted. */
function toCost(src: WireEvent | Record<string, unknown> | null | undefined): TurnCost | undefined {
  if (!src || typeof src.tokens !== "number" || typeof src.ms !== "number") return undefined;
  return {
    tokens: src.tokens,
    ms: src.ms,
    tokPerS: typeof src.tok_per_s === "number" ? src.tok_per_s : 0,
    requests: typeof src.requests === "number" ? src.requests : 0,
  };
}

// One stored entry can hold a thought, a reply and several calls at once, so
// it expands into as many rows as it carries. A tool entry fills the card its
// call opened rather than adding one.
function addEntry(items: TranscriptItem[], e: WireEntry): void {
  const seq = e.seq ?? items.length;

  if (e.kind === "user") {
    const files: Attached[] = (e.attachments ?? []).map((a) => ({
      path: a.path ?? "",
      kind: a.kind === "image" ? "image" : "text",
      bytes: a.bytes ?? 0,
      file: a.file,
    }));
    if (e.content || files.length > 0) {
      items.push({
        kind: "user",
        id: `u${seq}`,
        seq,
        text: e.content ?? "",
        files,
        edited: e.edited === true,
      });
    }
    return;
  }

  if (e.kind === "assistant") {
    if (e.reasoning) {
      items.push({
        kind: "reasoning",
        id: `r${seq}`,
        text: e.reasoning,
        tokens: thoughtTokens(e.reasoning),
        open: false,
      });
    }
    if (e.content) {
      items.push({
        kind: "assistant",
        id: `a${seq}`,
        text: e.content,
        streaming: false,
        cost: toCost(e.meta),
      });
    }
    (e.tool_calls ?? []).forEach((c, i) => {
      items.push({
        kind: "tool",
        id: c.id || `call_${c.index ?? i}`,
        name: c.function?.name ?? "tool",
        args: parseArgs(c.function?.arguments) ?? {},
        output: "",
        exitCode: null,
        truncated: false,
        running: true,
      });
    });
    return;
  }

  if (e.kind === "tool") {
    const permission = e.permission;
    if (permission && permission.decision && !SILENT_DECISIONS.has(permission.decision)) {
      items.push(toPermission(permission, outcomeOf(permission), `perm${seq}`));
    }
    const card = items.find((i) => i.kind === "tool" && i.id === e.tool_call_id);
    if (card && card.kind === "tool") {
      card.output = e.content ?? "";
      card.exitCode = e.exit_code ?? null;
      card.truncated = e.truncated ?? false;
      card.running = false;
      return;
    }
    items.push({
      kind: "tool",
      id: e.tool_call_id || `t${seq}`,
      name: e.tool_name ?? "tool",
      args: {},
      output: e.content ?? "",
      exitCode: e.exit_code ?? null,
      truncated: e.truncated ?? false,
      running: false,
    });
    return;
  }

  if (e.kind === "compaction") {
    const meta = e.meta ?? {};
    items.push({
      kind: "compaction",
      id: `c${seq}`,
      before: count(meta.before),
      after: count(meta.after),
      summary: e.content ?? "",
    });
    return;
  }

  if (e.kind === "status") {
    items.push({ kind: "status", id: `s${seq}`, text: e.content ?? "" });
  }
}

/** Keyed lists throw on a repeated key, and ids arrive from the model and the
    session file, so uniqueness is enforced here rather than assumed. */
function uniqueIds(items: TranscriptItem[]): void {
  const seen = new Set<string>();
  for (let i = 0; i < items.length; i++) {
    const item = items[i]!;
    let id = item.id || `i${i}`;
    if (seen.has(id)) {
      let n = 2;
      while (seen.has(`${id}#${n}`)) n++;
      id = `${id}#${n}`;
    }
    seen.add(id);
    item.id = id;
  }
}

/** The first user line, which is what the sidebar shows when the file has no title. */
function titleOf(entries: WireEntry[]): string {
  const first = entries.find((e) => e.kind === "user" && e.content);
  const text = (first?.content ?? "").trim().replace(/\s+/g, " ");
  if (!text) return "New session";
  return text.length > 60 ? text.slice(0, 60) : text;
}

// state carried between calls

let projectPaths: string[] = [];
const summaries = new Map<string, Session>();
const seqs = new Map<string, number>();

function toConfig(c: WireConfig): Config {
  return {
    listen: c.listen ?? "",
    agent: c.agent ?? "",
    permissions: toPermMode(c.permissions),
    compact_at: c.compact_at ?? 0.8,
    max_tool_calls: c.max_tool_calls ?? 50,
    shell: c.shell ?? "/bin/sh",
    command_timeout_s: c.command_timeout_s ?? 120,
    output_limit_bytes: c.output_limit_bytes ?? 16384,
    projects: projectPaths.slice(),
  };
}

interface WireChanges {
  files?: FileChange[];
  added?: number;
  removed?: number;
}

// Every field is optional on the wire, so a row missing its hunks still draws
// as a row rather than throwing the panel away.
function toChanges(wire: WireChanges): Changes {
  const files = (wire.files ?? []).map((f) => ({
    path: f.path ?? "",
    status: f.status ?? "changed",
    added: f.added ?? 0,
    removed: f.removed ?? 0,
    omitted: f.omitted ?? 0,
    hunks: (f.hunks ?? []).map((h) => ({
      old_start: h.old_start ?? 0,
      new_start: h.new_start ?? 0,
      lines: h.lines ?? [],
    })),
  }));
  return { files, added: wire.added ?? 0, removed: wire.removed ?? 0 };
}

function toAttached(wire: WireAttachment[] | undefined): Attached[] {
  return (wire ?? []).map((a) => ({
    path: a.path ?? "",
    kind: a.kind === "image" ? "image" : "text",
    bytes: a.bytes ?? 0,
    file: a.file,
  }));
}

function toSteer(wire: WireSteer | null | undefined): Steer | null {
  if (!wire) return null;
  return { text: wire.text ?? "", files: toAttached(wire.attachments) };
}

function toTranscript(snap: WireSnapshot): Transcript {
  const entries = (snap.entries ?? []).filter((e) => e.visible !== false);
  const items: TranscriptItem[] = [];
  for (const entry of entries) addEntry(items, entry);

  // An unanswered request has no entry of its own yet, so the card that blocks
  // the composer has to be restored from the pending list.
  for (const p of snap.pending ?? []) {
    if (!items.some((i) => i.kind === "permission" && i.id === p.id)) {
      items.push(toPermission(p, null));
    }
  }

  // The question is already on the row its call drew, since the call is still
  // running. What the page is missing is the id its answer is posted to.
  const asked = snap.question;
  if (asked?.id) {
    const row = [...items]
      .reverse()
      .find((i) => i.kind === "tool" && i.name === "ask_question" && i.running);
    if (row?.kind === "tool") row.askId = asked.id;
  }

  uniqueIds(items);

  const id = snap.id ?? "";
  const known = summaries.get(id);
  const session: Session = {
    id,
    project: snap.project ?? known?.project ?? "",
    model: snap.model ?? known?.model ?? "",
    title: snap.title || known?.title || titleOf(entries),
    created: snap.created ?? known?.created ?? new Date().toISOString(),
  };
  summaries.set(id, session);

  // The rate the chip shows is the last turn's, which is on the entry that
  // ended it rather than in the snapshot's own usage.
  const last = [...items].reverse().find((i) => i.kind === "assistant" && i.cost);
  const rate = last?.kind === "assistant" ? last.cost?.tokPerS : undefined;

  const usage: Usage = {
    prompt_tokens: snap.last_usage?.prompt_tokens ?? snap.fill ?? 0,
    completion_tokens: snap.last_usage?.completion_tokens ?? 0,
    n_ctx: snap.n_ctx ?? 0,
    tok_per_s: rate && rate > 0 ? Math.round(rate) : undefined,
    breakdown: toBreakdown(snap.breakdown),
  };

  const highest = entries.reduce((max, e) => Math.max(max, e.seq ?? 0), 0);
  seqs.set(id, snap.last_seq ?? highest);

  return {
    session,
    items,
    usage,
    todos: toTodos(snap.todos),
    steer: toSteer(snap.steer),
    status: toStatus(snap.state),
  };
}

// stream

interface OpenCall {
  id: string;
  name: string;
  args: string;
}

const EVENT_NAMES = [
  "status",
  "delta",
  "reasoning",
  "tool_call",
  "tool_output",
  "permission",
  "permission_done",
  "question",
  "usage",
  "status_entry",
  "title",
  "todos",
  "compacted",
  "queued",
  "message",
  "done",
  "resync",
];

// Argument fragments arrive one chunk at a time and join on index rather than
// id, since the id is empty until the call is nearly complete. The card is
// opened on the first fragment and its arguments filled once they parse.
function mapEvent(ev: WireEvent, calls: Map<number, OpenCall>): HarnessEvent[] {
  switch (ev.type) {
    case "status":
      return [{ type: "status", status: toStatus(ev.state) }];

    case "delta":
      return [{ type: "delta", text: ev.text ?? "" }];

    case "reasoning":
      return [{ type: "reasoning", text: ev.text ?? "" }];

    case "tool_call": {
      const index = ev.index ?? 0;
      const out: HarnessEvent[] = [];
      let open = calls.get(index);
      if (!open) {
        open = { id: ev.id || `call_${index}`, name: ev.name ?? "tool", args: "" };
        calls.set(index, open);
        out.push({ type: "tool_call", id: open.id, name: open.name, args: {} });
      }
      if (ev.name) open.name = ev.name;
      open.args += ev.arguments ?? "";
      const args = parseArgs(open.args);
      if (args) out.push({ type: "tool_args", id: open.id, name: open.name, args });
      return out;
    }

    case "tool_output": {
      const index = ev.index ?? 0;
      const open = calls.get(index);
      calls.delete(index);
      return [
        {
          type: "tool_output",
          id: open?.id ?? ev.id ?? `call_${index}`,
          output: ev.output ?? "",
          exit_code: ev.exit_code ?? null,
          truncated: ev.truncated ?? false,
        },
      ];
    }

    case "permission":
      return [
        {
          type: "permission",
          id: ev.id ?? "",
          tool: ev.tool ?? "",
          command: ev.command || ev.path || ev.args || "",
          prefix: ev.prefix ?? "",
        },
      ];

    case "permission_done":
      return [
        {
          type: "permission_done",
          id: ev.id ?? "",
          outcome: outcomeOf({ decision: ev.decision, prefix: ev.prefix, reason: ev.reason }),
        },
      ];

    case "question":
      return [{ type: "question", id: ev.id ?? "", ask: ev.ask ?? "" }];

    case "usage": {
      const prompt = ev.prompt_tokens ?? 0;
      const cached = ev.cached_tokens ?? 0;
      return [
        {
          type: "usage",
          prompt_tokens: prompt,
          completion_tokens: ev.completion_tokens ?? 0,
          n_ctx: ev.n_ctx ?? 0,
          cached_pct: prompt > 0 ? Math.round((cached / prompt) * 100) : undefined,
          breakdown: toBreakdown(ev.breakdown),
        },
      ];
    }

    case "status_entry":
      return [{ type: "status_entry", text: ev.content ?? "" }];

    case "title":
      return ev.title ? [{ type: "title", title: ev.title }] : [];

    case "todos":
      return [{ type: "todos", todos: toTodos(ev.todos) }];

    case "compacted":
      return [
        {
          type: "compacted",
          before: ev.before ?? 0,
          after: ev.after ?? 0,
          summary: ev.summary ?? "",
        },
      ];

    case "queued":
      return [{ type: "queued", steer: { text: ev.text ?? "", files: toAttached(ev.attachments) } }];

    case "message":
      return [
        {
          type: "message",
          seq: ev.seq ?? 0,
          text: ev.text ?? "",
          files: toAttached(ev.attachments),
        },
      ];

    case "error":
      return [{ type: "error", message: ev.message ?? "The turn failed" }];

    case "done":
      return [{ type: "done", cost: toCost(ev) }];

    default:
      return [];
  }
}

// The harness client. Sessions and sequence numbers are remembered between
// calls so a stream can reopen where it stopped without asking the caller.
/** The server names what it found; anything else it might say is unknown. */
function toMode(mode: string | undefined): ServerMode {
  switch (mode) {
    case "router":
    case "single":
    case "openai":
      return mode;
    default:
      return "unknown";
  }
}

class HttpApi implements HarnessApi {
  async getState(): Promise<State> {
    const wire = await call<WireState>("/state");
    projectPaths = wire.projects ?? [];
    return {
      mode: toMode(wire.mode),
      detectError: wire.detect_error || wire.models_error || "",
      detectKind: wire.detect_kind ?? "",
      modelsError: wire.models_error ?? "",
      modelsKind: wire.models_kind ?? "",
      config: toConfig(wire.config ?? {}),
      agents: (wire.agents ?? []).map(toAgent),
      projects: toProjects(projectPaths),
      models: (wire.models ?? []).map(toModel),
    };
  }

  async listModels(refresh?: boolean): Promise<Model[]> {
    const wire = await call<WireModel[] | { models?: WireModel[] }>(
      refresh ? "/models?refresh=1" : "/models",
    );
    const list = Array.isArray(wire) ? wire : (wire.models ?? []);
    return list.map(toModel);
  }

  async pickDirectory(): Promise<string | null> {
    const wire = await call<{ path?: string | null }>("/fs/pick", "POST", {});
    return wire.path ?? null;
  }

  async pickFiles(): Promise<string[]> {
    const wire = await call<{ paths?: string[] }>("/fs/pick", "POST", { kind: "file" });
    return wire.paths ?? [];
  }

  async pasteFile(name: string, data: string): Promise<string> {
    const wire = await call<{ path?: string }>("/fs/paste", "POST", { name, data });
    return wire.path ?? "";
  }

  async addProject(path: string): Promise<Project[]> {
    return this.setProjects(await call("/projects", "POST", { path }));
  }

  async removeProject(path: string): Promise<Project[]> {
    return this.setProjects(await call("/projects", "DELETE", { path }));
  }

  async listSessions(project: string): Promise<Session[]> {
    const wire = await call<{ sessions?: WireSummary[] }>(
      `/sessions?project=${encodeURIComponent(project)}`,
    );
    const list = (wire.sessions ?? []).map(toSession);
    for (const s of list) summaries.set(s.id, s);
    return list;
  }

  async createSession(project: string): Promise<Session> {
    const wire = await call<WireSummary>("/sessions", "POST", { project });
    const session = toSession(wire);
    summaries.set(session.id, session);
    return session;
  }

  async getSession(id: string): Promise<Transcript> {
    const wire = await call<WireSnapshot>(`/sessions/${encodeURIComponent(id)}`);
    return toTranscript({ ...wire, id: wire.id ?? id });
  }

  async getChanges(id: string): Promise<Changes> {
    const wire = await call<WireChanges>(`/sessions/${encodeURIComponent(id)}/changes`);
    return toChanges(wire);
  }

  async renameSession(id: string, title: string): Promise<void> {
    await call<unknown>(`/sessions/${encodeURIComponent(id)}`, "PUT", { title });
    const known = summaries.get(id);
    if (known) summaries.set(id, { ...known, title });
  }

  async deleteSession(id: string): Promise<void> {
    await call<unknown>(`/sessions/${encodeURIComponent(id)}`, "DELETE");
    summaries.delete(id);
  }

  async sendMessage(id: string, text: string, files: string[] = []): Promise<Sent> {
    const wire = await call<WireSent>(
      `/sessions/${encodeURIComponent(id)}/message`,
      "POST",
      { text, attachments: files },
    );
    if (wire.queued) {
      return { queued: true, seq: 0, files: [], steer: toSteer(wire.steer) };
    }
    return { queued: false, seq: wire.seq, files: toAttached(wire.attachments), steer: null };
  }

  // The harness rewrites the history and tells every tab to refetch, so
  // nothing here touches the transcript.
  async resend(id: string, seq: number, text?: string): Promise<void> {
    await call<unknown>(`/sessions/${encodeURIComponent(id)}/resend`, "POST", { seq, text });
  }

  async stop(id: string): Promise<void> {
    await call(`/sessions/${encodeURIComponent(id)}/stop`, "POST", {});
  }

  async compact(id: string): Promise<void> {
    await call(`/sessions/${encodeURIComponent(id)}/compact`, "POST", {});
  }

  async decide(permissionId: string, decision: PermissionDecision): Promise<void> {
    await call(`/permissions/${encodeURIComponent(permissionId)}`, "POST", decision);
  }

  async answer(questionId: string, answer: Answer): Promise<void> {
    await call(`/questions/${encodeURIComponent(questionId)}`, "POST", answer);
  }

  async updateConfig(patch: Partial<Config>): Promise<Config> {
    const { projects: _drop, ...body } = patch;
    const wire = await call<WireConfig>("/config", "PUT", body);
    return toConfig(wire);
  }

  async listAgents(): Promise<Agent[]> {
    const wire = await call<WireAgents>("/agents");
    return (wire.agents ?? []).map(toAgent);
  }

  async createAgent(patch: AgentPatch): Promise<Agent> {
    const wire = await call<WireAgents>("/agents", "POST", patch);
    return toAgent(wire.saved ?? {});
  }

  async updateAgent(id: string, patch: AgentPatch): Promise<Agent> {
    const wire = await call<WireAgents>(`/agents/${encodeURIComponent(id)}`, "PUT", patch);
    return toAgent(wire.saved ?? {});
  }

  async deleteAgent(id: string): Promise<Agent[]> {
    const wire = await call<WireAgents>(`/agents/${encodeURIComponent(id)}`, "DELETE");
    return (wire.agents ?? []).map(toAgent);
  }

  async selectAgent(id: string): Promise<Agent[]> {
    const wire = await call<WireAgents>(`/agents/${encodeURIComponent(id)}/select`, "POST", {});
    return (wire.agents ?? []).map(toAgent);
  }

  async probeAgent(
    at: { agent?: string; url: string; key?: string | null },
    refresh?: boolean,
  ): Promise<Probe> {
    const wire = await call<WireProbe>(
      refresh ? "/agents/probe?refresh=1" : "/agents/probe",
      "POST",
      at,
    );
    return toProbe(wire);
  }

  async listAllowlist(project: string): Promise<string[]> {
    const wire = await call<{ prefixes?: string[] }>(
      `/allowlist?project=${encodeURIComponent(project)}`,
    );
    return wire.prefixes ?? [];
  }

  async removeAllowed(project: string, prefix: string): Promise<string[]> {
    const wire = await call<{ prefixes?: string[] }>("/allowlist", "DELETE", {
      project,
      prefix,
    });
    return wire.prefixes ?? [];
  }

  events(session: string, onEvent: EventHandler): () => void {
    let source: EventSource | null = null;
    let closed = false;
    let failures = 0;
    const calls = new Map<number, OpenCall>();

    const deliver = (raw: string): void => {
      let wire: WireEvent;
      try {
        wire = JSON.parse(raw) as WireEvent;
      } catch {
        return;
      }
      if (typeof wire.seq === "number") seqs.set(session, wire.seq);
      if (wire.type === "resync") {
        void rebuild();
        return;
      }
      for (const event of mapEvent(wire, calls)) onEvent(event);
    };

    // The replay ring has already passed the point this stream reached, so the
    // transcript is refetched whole and the stream reopened at the new mark.
    const rebuild = async (): Promise<void> => {
      source?.close();
      source = null;
      try {
        const transcript = await this.getSession(session);
        calls.clear();
        onEvent({ type: "resync", transcript });
        open();
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        onEvent({ type: "connection", connected: false, message });
      }
    };

    const open = (): void => {
      if (closed) return;
      const from = seqs.get(session) ?? 0;
      const url = `${BASE}/events?session=${encodeURIComponent(session)}&from=${from}`;
      const es = new EventSource(url);
      source = es;

      es.onopen = () => {
        failures = 0;
        onEvent({ type: "connection", connected: true });
      };
      es.onmessage = (ev: MessageEvent<string>) => deliver(ev.data);
      for (const name of EVENT_NAMES) {
        es.addEventListener(name, (ev) => deliver((ev as MessageEvent<string>).data));
      }

      // A transport failure and a server event named error arrive on the same
      // listener, so the payload is what tells them apart.
      es.addEventListener("error", (ev) => {
        const data = (ev as MessageEvent<unknown>).data;
        if (typeof data === "string") {
          deliver(data);
          return;
        }
        failures += 1;
        // A stream the harness ended, which deleting a session does, is
        // reopened a moment later. The connection is reported lost once a
        // retry has failed too, so an ending on its own says nothing.
        if (failures > 1) onEvent({ type: "connection", connected: false });
        if (failures >= 5) {
          es.close();
          if (!closed) setTimeout(open, 3000);
        }
      });
    };

    open();
    return () => {
      closed = true;
      source?.close();
      source = null;
    };
  }

  private setProjects(wire: string[] | { projects?: string[] }): Project[] {
    projectPaths = Array.isArray(wire) ? wire : (wire.projects ?? []);
    return toProjects(projectPaths);
  }
}

export const httpApi: HarnessApi = new HttpApi();
