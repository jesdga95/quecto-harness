import type {
  Config,
  EventHandler,
  HarnessApi,
  HarnessEvent,
  Model,
  PermissionDecision,
  Project,
  Session,
  State,
  Transcript,
  TranscriptItem,
} from "./types";
import { applyEvent, nextId, outcomeLabel, pushUser } from "../transcript";

class Cancelled extends Error {}

const MODELS: Model[] = [
  { id: "Gemma4-12B", status: "loaded", ctx: 131072, ctxIsHint: false },
  { id: "Muse Glimmer 30B Q5", status: "unloaded", ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.6-35B-A3B Q5", status: "unloaded", ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-27B Q5", status: "unloaded", ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-27B Q8", status: "unloaded", ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-Flash-Next IQ4", status: "unloaded", ctx: 65536, ctxIsHint: true },
];

interface ScriptedTurn {
  reasoning: string;
  preface: string;
  tool: string;
  args: Record<string, unknown>;
  command: string;
  output: string;
  exit: number;
  truncated: boolean;
  reply: string;
  denied: string;
}

const LONG_OUTPUT = [
  "internal/agent/loop.go:41: undefined: compactIfNeeded",
  "internal/agent/loop.go:88: cannot use req (variable of type *chat.Request) as chat.Request",
  "internal/llama/client.go:12: imported and not used: \"time\"",
  "internal/server/sse.go:64: missing return",
  "internal/tools/shell.go:30: declared and not used: cancel",
].join("\n");

const SCRIPT: ScriptedTurn[] = [
  {
    reasoning:
      "The user wants the layout of the project. I should not guess. " +
      "List the top level first, then decide what to read. ls is cheap and " +
      "tells me whether this is a Go module or something else.",
    preface: "Let me look at the project root first.\n\n",
    tool: "run_shell",
    args: { command: "ls -la", timeout_s: 120 },
    command: "ls -la",
    output: [
      "total 48",
      "drwxr-xr-x  12 jay  staff   384 sep  4 10:12 .",
      "drwxr-xr-x   9 jay  staff   288 sep  3 18:40 ..",
      "-rw-r--r--   1 jay  staff   162 sep  4 09:55 .gitignore",
      "-rw-r--r--   1 jay  staff  1204 sep  4 09:55 AGENTS.md",
      "-rw-r--r--   1 jay  staff   398 sep  4 09:55 Makefile",
      "drwxr-xr-x   3 jay  staff    96 sep  4 09:55 cmd",
      "-rw-r--r--   1 jay  staff    82 sep  4 09:55 go.mod",
      "drwxr-xr-x  10 jay  staff   320 sep  4 10:12 internal",
      "drwxr-xr-x   8 jay  staff   256 sep  4 10:12 web",
    ].join("\n"),
    exit: 0,
    truncated: false,
    reply:
      "The project is a Go module.\n\n" +
      "## Layout\n" +
      "- `cmd` holds the entry point\n" +
      "- `internal` holds the agent loop, the llama client and the tools\n" +
      "- `web` holds this interface\n\n" +
      "Tell me which part to change and I will read it before touching anything.",
    denied:
      "No shell then. Paste the output of ls or name the file you want me to " +
      "read and I will work from that.",
  },
  {
    reasoning:
      "Before changing the loop I need to know it builds. go build is the " +
      "shortest check and its errors name the file and line.",
    preface: "Checking that it builds before I change anything.\n\n",
    tool: "run_shell",
    args: { command: "go build ./...", timeout_s: 120 },
    command: "go build ./...",
    output: LONG_OUTPUT,
    exit: 2,
    truncated: true,
    reply:
      "The build is broken in four places, all under `internal`.\n\n" +
      "## What failed\n" +
      "- `internal/agent/loop.go` calls compactIfNeeded, which does not exist\n" +
      "- `internal/llama/client.go` imports time and never uses it\n" +
      "- `internal/server/sse.go` has a path with no return\n\n" +
      "The first one explains the rest. Say the word and I will fix them in order.",
    denied:
      "Denied, so I cannot see the build state. I can still read files if you " +
      "point me at one.",
  },
  {
    reasoning:
      "The tests are the only thing that says whether the parser still works " +
      "after the change. Run the package tests, not the whole tree.",
    preface: "Running the tests for the package I touched.\n\n",
    tool: "run_shell",
    args: { command: "go test ./internal/llama", timeout_s: 120 },
    command: "go test ./internal/llama",
    output: ["ok  \tquecto-harness/internal/llama\t0.412s"].join("\n"),
    exit: 0,
    truncated: false,
    reply:
      "Tests pass for the parser.\n\n" +
      "```\nok  quecto-harness/internal/llama  0.412s\n```\n\n" +
      "Nothing else in the tree touches it.",
    denied: "Not run. The change is unverified until those tests go through.",
  },
];

function words(text: string): string[] {
  return text.match(/\S+\s*|\s+/g) ?? [];
}

function baseName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

// The whole harness backend, faked in memory. Turns follow a fixed script and
// every delay is a timer, so the flow can be clicked through end to end.
export class MockApi implements HarnessApi {
  private config: Config = {
    listen: "127.0.0.1:7331",
    server: { url: "http://192.168.2.205:8080", token: "" },
    model: "Gemma4-12B",
    effort: "medium",
    preserve_reasoning: true,
    permissions: "ask",
    compact_at: 0.8,
    max_tool_calls: 50,
    shell: "/bin/sh",
    command_timeout_s: 120,
    output_limit_bytes: 16384,
    projects: [
      "/Users/jay/Projects/Personal/quecto-harness",
      "/Users/jay/Projects/Personal/quecto-proxy",
    ],
  };

  private models: Model[] = MODELS.map((m) => ({ ...m }));
  private sessions: Session[] = [];
  private transcripts = new Map<string, Transcript>();
  private handlers = new Map<string, EventHandler>();
  private allowlist = new Map<string, string[]>([
    ["/Users/jay/Projects/Personal/quecto-harness", ["git status", "go vet"]],
  ]);
  private turnIndex = new Map<string, number>();
  private pending:
    | { id: string; session: string; settle: (d: PermissionDecision) => void }
    | null = null;
  private running: { session: string; cancelled: boolean } | null = null;

  constructor() {
    this.seed();
  }

  // reads

  async getState(): Promise<State> {
    await this.tick();
    return {
      mode: "router",
      config: { ...this.config },
      projects: this.config.projects.map((p) => ({ path: p, name: baseName(p) })),
      models: this.models.map((m) => ({ ...m })),
    };
  }

  async listModels(_refresh?: boolean): Promise<Model[]> {
    await this.tick();
    return this.models.map((m) => ({ ...m }));
  }

  async pickDirectory(): Promise<string | null> {
    await this.tick();
    const answer = window.prompt(
      "The real harness opens the host folder dialog here.",
      "/Users/jay/Projects/Personal/new-project",
    );
    return answer?.trim() ? answer.trim() : null;
  }

  async listSessions(project: string): Promise<Session[]> {
    await this.tick();
    return this.sessions.filter((s) => s.project === project).map((s) => ({ ...s }));
  }

  /** A copy, the way a fetched transcript would be, so the tab owns its items. */
  async getSession(id: string): Promise<Transcript> {
    await this.tick();
    const t = this.transcripts.get(id);
    if (!t) throw new Error(`session ${id} not found`);
    return {
      session: { ...t.session },
      items: t.items.map((i) => ({ ...i })),
      usage: { ...t.usage },
    };
  }

  async setSessionModel(id: string, model: string): Promise<void> {
    await this.tick();
    const t = this.transcripts.get(id);
    if (!t) throw new Error(`session ${id} not found`);
    t.session.model = model;
    this.emit(id, { type: "status_entry", text: "Model changed to " + model });
  }

  async renameSession(id: string, title: string): Promise<void> {
    await this.tick();
    const session = this.sessions.find((s) => s.id === id);
    if (session) session.title = title;
  }

  async deleteSession(id: string): Promise<void> {
    await this.tick();
    this.sessions = this.sessions.filter((s) => s.id !== id);
    this.transcripts.delete(id);
  }

  // writes

  async addProject(path: string): Promise<Project[]> {
    await this.tick();
    if (!this.config.projects.includes(path)) this.config.projects.push(path);
    return this.config.projects.map((p) => ({ path: p, name: baseName(p) }));
  }

  async removeProject(path: string): Promise<Project[]> {
    await this.tick();
    this.config.projects = this.config.projects.filter((p) => p !== path);
    return this.config.projects.map((p) => ({ path: p, name: baseName(p) }));
  }

  async createSession(project: string, model: string): Promise<Session> {
    await this.tick();
    const session: Session = {
      id: nextId("s"),
      project,
      model,
      title: "New session",
      created: new Date().toISOString(),
    };
    this.sessions.unshift(session);
    this.transcripts.set(session.id, {
      session,
      items: [],
      usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: this.ctxOf(model) },
    });
    return { ...session };
  }

  async updateConfig(patch: Partial<Config>): Promise<Config> {
    await this.tick(160);
    this.config = { ...this.config, ...patch };
    return { ...this.config };
  }

  async listAllowlist(project: string): Promise<string[]> {
    await this.tick();
    return (this.allowlist.get(project) ?? []).slice();
  }

  async removeAllowed(project: string, prefix: string): Promise<string[]> {
    await this.tick();
    const list = (this.allowlist.get(project) ?? []).filter((p) => p !== prefix);
    this.allowlist.set(project, list);
    return list.slice();
  }

  async decide(permissionId: string, decision: PermissionDecision): Promise<void> {
    await this.tick();
    if (!this.pending || this.pending.id !== permissionId) return;
    this.resolvePending(decision);
  }

  async stop(session: string): Promise<void> {
    await this.tick();
    if (this.running && this.running.session === session) this.running.cancelled = true;
    if (this.pending) this.resolvePending({ decision: "deny", reason: "stopped" });
  }

  async compact(session: string): Promise<void> {
    const t = this.transcripts.get(session);
    if (!t) return;
    this.emit(session, { type: "status", status: "compacting" });
    const before = t.usage.prompt_tokens + t.usage.completion_tokens;
    const after = Math.min(before, Math.max(600, Math.round(before * 0.062)));
    await this.tick(1100);
    t.usage = { prompt_tokens: after, completion_tokens: 0, n_ctx: t.usage.n_ctx };
    this.emit(session, { type: "compacted", before, after });
    this.emit(session, {
      type: "usage",
      prompt_tokens: after,
      completion_tokens: 0,
      n_ctx: t.usage.n_ctx,
    });
    this.emit(session, { type: "status", status: "idle" });
  }

  // stream

  events(session: string, onEvent: EventHandler): () => void {
    this.handlers.set(session, onEvent);
    return () => {
      if (this.handlers.get(session) === onEvent) this.handlers.delete(session);
    };
  }

  async sendMessage(session: string, text: string): Promise<void> {
    const t = this.transcripts.get(session);
    if (!t) return;
    pushUser(t.items, text);
    if (t.session.title === "New session") t.session.title = title(text);
    void this.runTurn(session, t);
  }

  // internals

  private async runTurn(session: string, t: Transcript): Promise<void> {
    const turn = { session, cancelled: false };
    this.running = turn;
    const index = this.turnIndex.get(session) ?? 0;
    const script = SCRIPT[index % SCRIPT.length]!;
    this.turnIndex.set(session, index + 1);

    try {
      this.load(t.session.model);
      this.emit(session, { type: "status", status: "thinking" });
      await this.sleep(420, turn);
      this.settle(t.session.model, t);

      if (this.config.effort !== "off") {
        for (const chunk of chunks(script.reasoning, 5)) {
          this.send(session, t, { type: "reasoning", text: chunk });
          await this.sleep(120, turn);
        }
      }

      this.emit(session, { type: "status", status: "streaming" });
      await this.stream(session, t, script.preface, turn);

      const callId = nextId("t");
      this.send(session, t, {
        type: "tool_call",
        id: callId,
        name: script.tool,
        args: script.args,
      });
      await this.sleep(260, turn);

      let allowed = this.preapproved(t.session.project, script.command);
      let reason = "";
      if (!allowed) {
        const permId = nextId("p");
        this.emit(session, { type: "status", status: "waiting" });
        this.send(session, t, {
          type: "permission",
          id: permId,
          tool: script.tool,
          command: script.command,
          prefix: prefixOf(script.command),
        });
        const decision = await this.ask(permId, session, turn);
        if (decision.decision === "always") {
          const list = this.allowlist.get(t.session.project) ?? [];
          list.push(decision.prefix);
          this.allowlist.set(t.session.project, list);
        }
        allowed = decision.decision !== "deny";
        if (decision.decision === "deny") reason = decision.reason;
      }

      if (allowed) {
        this.emit(session, { type: "status", status: "running" });
        await this.sleep(900, turn);
        this.send(session, t, {
          type: "tool_output",
          id: callId,
          output: script.output,
          exit_code: script.exit,
          truncated: script.truncated,
        });
      } else {
        this.send(session, t, {
          type: "tool_output",
          id: callId,
          output: `denied by user: ${reason || "no reason given"}`,
          exit_code: 1,
          truncated: false,
        });
      }

      this.emit(session, { type: "status", status: "streaming" });
      await this.sleep(300, turn);
      await this.stream(session, t, allowed ? script.reply : script.denied, turn);

      const grown = allowed ? 2600 + script.output.length : 900;
      t.usage = {
        prompt_tokens: t.usage.prompt_tokens + grown,
        completion_tokens: t.usage.completion_tokens + 240,
        n_ctx: this.ctxOf(t.session.model),
        tok_per_s: 52,
        cached_pct: Math.min(94, 61 + index * 6),
      };
      this.emit(session, { type: "usage", ...t.usage });
      this.finish(session, t);
    } catch (err) {
      if (!(err instanceof Cancelled)) {
        this.send(session, t, { type: "error", message: String(err) });
      } else {
        this.send(session, t, { type: "error", message: "Turn stopped by user" });
      }
      this.finish(session, t);
    } finally {
      if (this.running === turn) this.running = null;
    }
  }

  private finish(session: string, t: Transcript): void {
    this.send(session, t, { type: "done" });
    this.emit(session, { type: "status", status: "idle" });
  }

  private async stream(
    session: string,
    t: Transcript,
    text: string,
    turn: { cancelled: boolean },
  ): Promise<void> {
    for (const token of words(text)) {
      this.send(session, t, { type: "delta", text: token });
      await this.sleep(28, turn);
    }
  }

  /** Records the event on the stored transcript, then hands it to the tab. */
  private send(session: string, t: Transcript, event: HarnessEvent): void {
    applyEvent(t.items, event);
    this.emit(session, event);
  }

  private emit(session: string, event: HarnessEvent): void {
    this.handlers.get(session)?.(event);
  }

  /** Answers the open card and remembers the answer on the stored transcript. */
  private resolvePending(decision: PermissionDecision): void {
    const pending = this.pending;
    if (!pending) return;
    this.pending = null;
    const t = this.transcripts.get(pending.session);
    const item = t?.items.find((i) => i.kind === "permission" && i.id === pending.id);
    if (item && item.kind === "permission") item.outcome = outcomeLabel(decision);
    pending.settle(decision);
  }

  private ask(id: string, session: string, turn: { cancelled: boolean }): Promise<PermissionDecision> {
    return new Promise((resolve, reject) => {
      if (turn.cancelled) {
        reject(new Cancelled());
        return;
      }
      this.pending = {
        id,
        session,
        settle: (d) => (turn.cancelled ? reject(new Cancelled()) : resolve(d)),
      };
    });
  }

  private sleep(ms: number, turn: { cancelled: boolean }): Promise<void> {
    return new Promise((resolve, reject) => {
      setTimeout(() => (turn.cancelled ? reject(new Cancelled()) : resolve()), ms);
    });
  }

  private tick(ms = 90): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  private preapproved(project: string, command: string): boolean {
    if (this.config.permissions === "full") return true;
    if (this.config.permissions !== "allowlist") return false;
    const list = this.allowlist.get(project) ?? [];
    return list.some((p) => command.startsWith(p));
  }

  private load(id: string): void {
    const model = this.models.find((m) => m.id === id);
    if (model && model.status === "unloaded") model.status = "loading";
  }

  private settle(id: string, t: Transcript): void {
    const model = this.models.find((m) => m.id === id);
    if (model && model.status === "loading") {
      model.status = "loaded";
      model.ctxIsHint = false;
    }
    t.usage.n_ctx = this.ctxOf(id);
  }

  private ctxOf(id: string): number {
    return this.models.find((m) => m.id === id)?.ctx ?? 131072;
  }

  /** One project with history, one empty, so both states are visible on load. */
  private seed(): void {
    const project = this.config.projects[0]!;
    const first: Session = {
      id: nextId("s"),
      project,
      model: "Gemma4-12B",
      title: "Wire the SSE hub",
      created: new Date(Date.now() - 3_600_000).toISOString(),
    };
    const second: Session = {
      id: nextId("s"),
      project,
      model: "Gemma4-12B",
      title: "New session",
      created: new Date(Date.now() - 600_000).toISOString(),
    };
    const third: Session = {
      id: nextId("s"),
      project: this.config.projects[1]!,
      model: "Qwen3.8-27B Q5",
      title: "Dashboard palette",
      created: new Date(Date.now() - 86_400_000).toISOString(),
    };
    this.sessions.push(first, second, third);
    this.turnIndex.set(first.id, 2);

    const items: TranscriptItem[] = [];
    pushUser(items, "The SSE hub drops events when two tabs are open. Find out why.");
    for (const e of seededEvents()) applyEvent(items, e);
    this.transcripts.set(first.id, {
      session: first,
      items,
      usage: { prompt_tokens: 39400, completion_tokens: 2600, n_ctx: 131072 },
    });
    for (const s of [second, third]) {
      this.transcripts.set(s.id, {
        session: s,
        items: [],
        usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: this.ctxOf(s.model) },
        });
    }
  }
}

function chunks(text: string, parts: number): string[] {
  const size = Math.ceil(text.length / parts);
  const out: string[] = [];
  for (let i = 0; i < text.length; i += size) out.push(text.slice(i, i + size));
  return out;
}

function prefixOf(command: string): string {
  return command.split(/\s+/).slice(0, 2).join(" ");
}

function title(text: string): string {
  const clean = text.trim().replace(/\s+/g, " ");
  return clean.length > 38 ? clean.slice(0, 38) + "..." : clean || "New session";
}

// History for the seeded session: a thought, a tool call, a compaction and an
// error, so every row style is on screen before anything is clicked.
function seededEvents(): HarnessEvent[] {
  const call = nextId("t");
  return [
    {
      type: "reasoning",
      text:
        "Two tabs means two subscribers on one channel. If the hub writes to a " +
        "single channel only one reader wins, so the second tab misses events. " +
        "Check how the hub registers clients.",
    },
    {
      type: "delta",
      text: "The hub keeps one channel per session, not one per client. Reading it.\n\n",
    },
    {
      type: "tool_call",
      id: call,
      name: "read_file",
      args: { path: "internal/server/sse.go", limit: 60 },
    },
    {
      type: "tool_output",
      id: call,
      output: [
        "  1  package server",
        "  2",
        "  3  type hub struct {",
        "  4      mu       sync.Mutex",
        "  5      channels map[string]chan Event",
        "  6  }",
        "  7",
        "  8  func (h *hub) subscribe(session string) chan Event {",
        "  9      h.mu.Lock()",
        " 10      defer h.mu.Unlock()",
        " 11      ch, ok := h.channels[session]",
        " 12      if !ok {",
        " 13          ch = make(chan Event, 64)",
        " 14          h.channels[session] = ch",
        " 15      }",
        " 16      return ch",
        "...",
      ].join("\n"),
      exit_code: 0,
      truncated: true,
    },
    {
      type: "delta",
      text:
        "Confirmed. `subscribe` hands both tabs the same channel, so each " +
        "event goes to whichever tab reads first.\n\n" +
        "## Fix\n" +
        "- keep a set of subscribers per session\n" +
        "- fan out on publish and drop a slow reader instead of blocking\n" +
        "- close the channel when the last tab goes away",
    },
    { type: "done" },
    { type: "compacted", before: 98000, after: 6000 },
    { type: "error", message: "Stream closed by server: context canceled" },
  ];
}

export const mockApi: HarnessApi = new MockApi();
