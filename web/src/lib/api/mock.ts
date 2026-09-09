import type {
  Agent,
  AgentPatch,
  Answer,
  Attached,
  Changes,
  Config,
  EventHandler,
  FileChange,
  HarnessApi,
  HarnessEvent,
  Model,
  PermissionDecision,
  Probe,
  Project,
  Sent,
  Session,
  State,
  Todo,
  Transcript,
  TranscriptItem,
  Usage,
} from "./types";
import { applyEvent, nextId, outcomeLabel, pushUser } from "../transcript";
import { answerText, type Asked } from "../question";

// What a measurement of the fixed parts comes back with. The harness measures
// them after the first turn, so a session with no turn behind it has no split.
const MOCK_SYSTEM = 154;
const MOCK_TOOLS = 1026;

/** The split as the harness reports it: two measured parts and the rest. */
function split(usage: Usage): Usage {
  const messages = usage.prompt_tokens + usage.completion_tokens - MOCK_SYSTEM - MOCK_TOOLS;
  if (messages < 0) return usage;
  return { ...usage, breakdown: { system: MOCK_SYSTEM, tools: MOCK_TOOLS, messages } };
}

// One of every row the panel has to draw, so the states with no diff are
// exercised as well as the ones with one.
function mockChanges(): FileChange[] {
  return [
    {
      path: "internal/server/hub.go",
      status: "changed",
      added: 9,
      removed: 3,
      omitted: 0,
      hunks: [
        {
          old_start: 41,
          new_start: 41,
          lines: [
            { kind: "context", text: "func (h *Hub) Publish(session string, ev agent.Event) {" },
            { kind: "context", text: "\th.mu.RLock()" },
            { kind: "removed", text: "\tch := h.subs[session]" },
            { kind: "removed", text: "\th.mu.RUnlock()" },
            { kind: "removed", text: "\tch <- ev" },
            { kind: "added", text: "\tsubs := append([]chan agent.Event(nil), h.subs[session]...)" },
            { kind: "added", text: "\th.mu.RUnlock()" },
            { kind: "added", text: "\tfor _, ch := range subs {" },
            { kind: "added", text: "\t\tselect {" },
            { kind: "added", text: "\t\tcase ch <- ev:" },
            { kind: "added", text: "\t\tdefault:" },
            { kind: "added", text: "\t\t\th.drop(session, ch)" },
            { kind: "added", text: "\t\t}" },
            { kind: "added", text: "\t}" },
            { kind: "context", text: "}" },
          ],
        },
      ],
    },
    {
      path: "internal/server/fanout_test.go",
      status: "new",
      added: 4,
      removed: 0,
      omitted: 0,
      hunks: [
        {
          old_start: 1,
          new_start: 1,
          lines: [
            { kind: "added", text: "package server" },
            { kind: "added", text: "" },
            { kind: "added", text: "func TestTwoTabsBothGetTheEvent(t *testing.T) {" },
            { kind: "added", text: "}" },
          ],
        },
      ],
    },
    { path: "docs/scratch.md", status: "deleted", added: 0, removed: 0, omitted: 0, hunks: [] },
    { path: "web/src/app.css", status: "same", added: 0, removed: 0, omitted: 0, hunks: [] },
    { path: "internal/web/dist/logo.png", status: "binary", added: 0, removed: 0, omitted: 0, hunks: [] },
    { path: "web/package-lock.json", status: "large", added: 0, removed: 0, omitted: 0, hunks: [] },
  ];
}

class Cancelled extends Error {}

const MODELS: Model[] = [
  { id: "Gemma4-12B", status: "loaded", vision: true, ctx: 131072, ctxIsHint: false },
  { id: "Muse Glimmer 30B Q5", status: "unloaded", vision: false, ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.6-35B-A3B Q5", status: "unloaded", vision: true, ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-27B Q5", status: "unloaded", vision: true, ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-27B Q8", status: "unloaded", vision: true, ctx: 131072, ctxIsHint: true },
  { id: "Qwen3.8-Flash-Next IQ4", status: "unloaded", vision: false, ctx: 65536, ctxIsHint: true },
];

// What each fake address holds. Keyed by the address rather than by the agent,
// so typing an address nothing is behind shows the unreachable state at once.
const ADDRESSES: Record<string, Model[]> = {
  "http://192.168.2.205:8080": MODELS,
  "http://10.0.0.14:1234": MODELS.slice(3, 5),
  "https://api.example.com/v1": [
    { id: "gpt-oss-120b", status: "loaded", vision: false, ctx: 131072, ctxIsHint: false },
  ],
};

const AGENTS: Agent[] = [
  {
    id: "agt_local", name: "Local llama.cpp", url: "http://192.168.2.205:8080",
    key: "", model: "Gemma4-12B", effort: "medium", preserve_reasoning: true,
    temperature: null, top_p: null, top_k: null, repeat_penalty: null,
    max_tokens: null, seed: null, instructions: "",
  },
  {
    id: "agt_studio", name: "Studio box", url: "http://10.0.0.14:1234",
    key: "sk-studio", model: "Qwen3.8-27B Q5", effort: "high", preserve_reasoning: false,
    temperature: 0.7, top_p: 0.9, top_k: 40, repeat_penalty: 1.1,
    max_tokens: 8192, seed: null, instructions: "Answer in British English.",
  },
  {
    id: "agt_cloud", name: "Cloud endpoint", url: "https://api.example.com/v1",
    key: "", model: "gpt-oss-120b", effort: "medium", preserve_reasoning: true,
    temperature: null, top_p: null, top_k: null, repeat_penalty: null,
    max_tokens: null, seed: null, instructions: "",
  },
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
  /** A choice put to the user before the tool runs, on the turns that have
      one, so the card is on screen with no harness behind it. */
  question?: Asked;
}

const MOCK_TODOS: Todo[] = [
  { text: "Read the hub and the routes", status: "done" },
  { text: "Keep a set of subscribers per session", status: "doing" },
  { text: "Drop a slow reader instead of blocking", status: "todo" },
];

const MOCK_SUMMARY = [
  "Task: find why two tabs on one session each see half the events.",
  "Found: subscribe hands both tabs the same channel, so an event goes to",
  "whichever tab reads first. Files: internal/server/hub.go.",
  "Next: keep a set of subscribers per session and fan out on publish.",
].join(" ");

const LONG_OUTPUT = [
  "internal/agent/loop.go:41: undefined: compactIfNeeded",
  "internal/agent/loop.go:88: cannot use req (variable of type *chat.Request) as chat.Request",
  "internal/llm/client.go:12: imported and not used: \"time\"",
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
      "- `internal` holds the agent loop, the model client and the tools\n" +
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
      "- `internal/llm/client.go` imports time and never uses it\n" +
      "- `internal/server/sse.go` has a path with no return\n\n" +
      "The first one explains the rest. Say the word and I will fix them in order.",
    denied:
      "Denied, so I cannot see the build state. I can still read files if you " +
      "point me at one.",
  },
  {
    reasoning:
      "The tests are the only thing that says whether the parser still works " +
      "after the change. How much to run is the user's call, so ask.",
    preface: "One thing before I run them.\n\n",
    question: {
      question: "How much of the suite should I run?",
      options: [
        { label: "The package I touched", description: "Seconds, and covers the change" },
        { label: "The whole tree", description: "A minute, and catches what the change reached" },
      ],
      multi: false,
    },
    tool: "run_shell",
    args: { command: "go test ./internal/llm", timeout_s: 120 },
    command: "go test ./internal/llm",
    output: ["ok  \tquecto-harness/internal/llm\t0.412s"].join("\n"),
    exit: 0,
    truncated: false,
    reply:
      "Tests pass for the parser.\n\n" +
      "```\nok  quecto-harness/internal/llm  0.412s\n```\n\n" +
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
    agent: "agt_local",
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

  private agents: Agent[] = AGENTS.map((a) => ({ ...a }));
  private models: Model[] = MODELS.map((m) => ({ ...m }));
  private sessions: Session[] = [];
  private transcripts = new Map<string, Transcript>();
  private handlers = new Map<string, EventHandler>();
  private allowlist = new Map<string, string[]>([
    ["/Users/jay/Projects/Personal/quecto-harness", ["git status", "go vet"]],
  ]);
  private turnIndex = new Map<string, number>();
  private lastSeq = new Map<string, number>();
  private pending:
    | { id: string; session: string; settle: (d: PermissionDecision) => void }
    | null = null;
  private openQuestion:
    | { id: string; session: string; settle: (a: Answer) => void }
    | null = null;
  // The work in flight. A compaction holds the session the way a turn does, so
  // a message typed against it waits, but a stop does not reach it.
  private running: { session: string; cancelled: boolean; compacting?: boolean } | null = null;
  // When the running turn began, so the cost line under the answer is real
  // wall clock rather than a made-up number.
  private turnStart = 0;

  constructor() {
    this.seed();
  }

  // reads

  async getState(): Promise<State> {
    await this.tick();
    const probe = this.probe(this.active());
    this.models = probe.models;
    return {
      mode: probe.mode,
      detectError: probe.detectError,
      detectKind: probe.detectKind,
      modelsError: probe.modelsError,
      modelsKind: probe.modelsKind,
      config: { ...this.config },
      agents: this.agents.map((a) => ({ ...a })),
      projects: this.config.projects.map((p) => ({ path: p, name: baseName(p) })),
      models: probe.models.map((m) => ({ ...m })),
    };
  }

  async listModels(_refresh?: boolean): Promise<Model[]> {
    await this.tick();
    this.models = this.probe(this.active()).models;
    return this.models.map((m) => ({ ...m }));
  }

  // agents

  /** The agent answering, or an empty one when there is none. */
  private active(): Agent | undefined {
    return this.agents.find((a) => a.id === this.config.agent);
  }

  /** What one address holds. An address nothing is behind is unreachable, and
      the cloud endpoint wants a key before it will say anything. */
  private probe(at: { key?: string; url: string } | undefined): Probe {
    const empty = { models: [], detectError: "", detectKind: "", modelsError: "", modelsKind: "" };
    if (!at) {
      return { ...empty, mode: "unknown", detectError: "No agent is selected: create one in the agents dialog" };
    }
    const held = ADDRESSES[at.url];
    if (!held) {
      return { ...empty, mode: "unknown", detectError: "Nothing answered at that address", detectKind: "unreachable" };
    }
    if (at.url.startsWith("https://") && !at.key) {
      return { ...empty, mode: "openai", modelsError: "The key was rejected", modelsKind: "auth" };
    }
    return { ...empty, mode: at.url.startsWith("https://") ? "openai" : "router", models: held.map((m) => ({ ...m })) };
  }

  async listAgents(): Promise<Agent[]> {
    await this.tick();
    return this.agents.map((a) => ({ ...a }));
  }

  async createAgent(patch: AgentPatch): Promise<Agent> {
    await this.tick(160);
    const name = (patch.name ?? "").trim();
    if (name === "") throw new Error("Name the agent: an agent needs a name");
    if (this.agents.some((a) => a.name.toLowerCase() === name.toLowerCase())) {
      throw new Error(`An agent named "${name}" already exists: choose another name`);
    }
    const agent: Agent = {
      id: nextId("agt"), url: "", key: "", model: "",
      effort: "medium", preserve_reasoning: true, temperature: null, top_p: null,
      top_k: null, repeat_penalty: null, max_tokens: null, seed: null,
      instructions: "", ...patch, name,
    };
    this.agents.push(agent);
    if (this.config.agent === "") this.config.agent = agent.id;
    return { ...agent };
  }

  async updateAgent(id: string, patch: AgentPatch): Promise<Agent> {
    await this.tick(160);
    const at = this.agents.findIndex((a) => a.id === id);
    const current = this.agents[at];
    if (!current) throw new Error("No agent with that id: it may have been deleted");
    const name = (patch.name ?? current.name).trim();
    if (name === "") throw new Error("Name the agent: an agent needs a name");
    if (this.agents.some((a) => a.id !== id && a.name.toLowerCase() === name.toLowerCase())) {
      throw new Error(`An agent named "${name}" already exists: choose another name`);
    }
    const next: Agent = { ...current, ...patch, name };
    this.agents[at] = next;
    if (id === this.config.agent) this.repoint(next.model);
    return { ...next };
  }

  async deleteAgent(id: string): Promise<Agent[]> {
    await this.tick(160);
    this.agents = this.agents.filter((a) => a.id !== id);
    if (this.config.agent === id) {
      this.config.agent = this.agents[0]?.id ?? "";
      this.repoint(this.active()?.model ?? "");
    }
    return this.agents.map((a) => ({ ...a }));
  }

  async selectAgent(id: string): Promise<Agent[]> {
    await this.tick(160);
    if (!this.agents.some((a) => a.id === id)) {
      throw new Error("No agent with that id: it may have been deleted");
    }
    this.config.agent = id;
    this.repoint(this.active()?.model ?? "");
    return this.agents.map((a) => ({ ...a }));
  }

  async probeAgent(
    at: { agent?: string; url: string; key?: string },
    _refresh?: boolean,
  ): Promise<Probe> {
    await this.tick(400);
    const stored = this.agents.find((a) => a.id === at.agent);
    return this.probe({ url: at.url, key: at.key ?? stored?.key });
  }

  /** One agent answers, so its model is every session's model. */
  private repoint(model: string): void {
    for (const t of this.transcripts.values()) t.session.model = model;
  }

  async pickDirectory(): Promise<string | null> {
    await this.tick();
    const answer = window.prompt(
      "The real harness opens the host folder dialog here.",
      "/Users/jay/Projects/Personal/new-project",
    );
    return answer?.trim() ? answer.trim() : null;
  }

  async pickFiles(): Promise<string[]> {
    await this.tick();
    const answer = window.prompt(
      "The real harness opens the host file dialog here. Several paths, comma separated.",
      "/Users/jay/Projects/Personal/quecto-harness/AGENTS.md,/Users/jay/Pictures/tree.jpg",
    );
    return (answer ?? "")
      .split(",")
      .map((p) => p.trim())
      .filter(Boolean);
  }

  async pasteFile(name: string, data: string): Promise<string> {
    await this.tick();
    if (!data) throw new Error("The pasted file is empty");
    return `/Users/jay/.quecto-harness/uploads/${crypto.randomUUID()}/${name}`;
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
    const run = this.running?.session === id ? this.running : null;
    return {
      session: { ...t.session },
      items: t.items.map((i) => ({ ...i })),
      usage: { ...t.usage },
      todos: t.todos.map((todo) => ({ ...todo })),
      steer: t.steer ? { text: t.steer.text, files: [...t.steer.files] } : null,
      status: run ? (run.compacting ? "compacting" : "streaming") : "idle",
    };
  }

  async getChanges(id: string): Promise<Changes> {
    await this.tick();
    if (!this.transcripts.has(id)) throw new Error(`session ${id} not found`);
    const files = mockChanges();
    return {
      files,
      added: files.reduce((n, f) => n + f.added, 0),
      removed: files.reduce((n, f) => n + f.removed, 0),
    };
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

  async createSession(project: string): Promise<Session> {
    await this.tick();
    const model = this.active()?.model ?? "";
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
      todos: [],
      steer: null,
      status: "idle",
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

  async answer(questionId: string, chosen: Answer): Promise<void> {
    await this.tick();
    if (!this.openQuestion || this.openQuestion.id !== questionId) return;
    this.settleQuestion(chosen);
  }

  async stop(session: string): Promise<void> {
    await this.tick();
    const run = this.running;
    // A compaction is not stoppable, the way the harness refuses it.
    if (run && run.session === session && !run.compacting) run.cancelled = true;
    if (this.pending) this.resolvePending({ decision: "deny", reason: "stopped" });
    // The turn is unwound by the cancel the settle carries, the way a stop
    // under a permission card is.
    if (this.openQuestion) this.settleQuestion({ options: [], text: "" });
  }

  async compact(session: string): Promise<void> {
    const t = this.transcripts.get(session);
    if (!t) return;
    const run = { session, cancelled: false, compacting: true };
    this.running = run;
    this.emit(session, { type: "status", status: "compacting" });
    const before = t.usage.prompt_tokens + t.usage.completion_tokens;
    const after = Math.min(before, Math.max(600, Math.round(before * 0.062)));
    await this.tick(1100);
    if (this.running === run) this.running = null;
    t.usage = split({ prompt_tokens: after, completion_tokens: 0, n_ctx: t.usage.n_ctx });
    this.send(session, t, {
      type: "compacted",
      before,
      after,
      summary: MOCK_SUMMARY,
    });
    this.emit(session, { type: "usage", ...t.usage });
    this.emit(session, { type: "status", status: "idle" });
    this.flushSteer(session, t);
  }

  // stream

  events(session: string, onEvent: EventHandler): () => void {
    this.handlers.set(session, onEvent);
    return () => {
      if (this.handlers.get(session) === onEvent) this.handlers.delete(session);
    };
  }

  async sendMessage(session: string, text: string, files: string[] = []): Promise<Sent> {
    const t = this.transcripts.get(session);
    if (!t) return { queued: false, seq: 0, files: [], steer: null };
    const attached: Attached[] = files.map((path) => ({
      path,
      kind: /\.(png|jpe?g|gif|webp)$/i.test(path) ? "image" : "text",
      bytes: 2400,
    }));
    // A message typed while a turn runs waits for it, joined to whatever is
    // already waiting.
    if (this.running?.session === session) {
      const held = t.steer;
      t.steer = {
        text: held && held.text ? `${held.text}\n${text}` : text,
        files: [...(held?.files ?? []), ...attached],
      };
      const queued = { text: t.steer.text, files: [...t.steer.files] };
      this.emit(session, { type: "queued", steer: queued });
      return { queued: true, seq: 0, files: [], steer: queued };
    }
    const seq = this.mintSeq(session);
    pushUser(t.items, text, attached, seq);
    if (t.session.title === "New session") t.session.title = title(text);
    void this.runTurn(session, t);
    return { queued: false, seq, files: attached, steer: null };
  }

  // The harness rewrites the history and tells every tab to refetch, which is
  // the resync here. The files ride along: an edit changes the words only.
  async resend(session: string, seq: number, text?: string): Promise<void> {
    await this.tick();
    const t = this.transcripts.get(session);
    if (!t) return;
    let at = -1;
    for (let i = t.items.length - 1; i >= 0; i--) {
      if (t.items[i]?.kind === "user") {
        at = i;
        break;
      }
    }
    const message = t.items[at];
    if (!message || message.kind !== "user" || message.seq !== seq) {
      throw new Error("That is not the last message: only the last one can be sent again");
    }
    t.items.length = at;
    const words = text ?? message.text;
    pushUser(t.items, words, message.files, this.mintSeq(session), words !== message.text);
    this.emit(session, { type: "resync", transcript: await this.getSession(session) });
    void this.runTurn(session, t);
  }

  /** Entry numbers are never reused, so an edit cannot land on a number the
      transcript already showed. */
  private mintSeq(session: string): number {
    const next = (this.lastSeq.get(session) ?? 0) + 1;
    this.lastSeq.set(session, next);
    return next;
  }

  // internals

  private async runTurn(session: string, t: Transcript): Promise<void> {
    const turn = { session, cancelled: false };
    this.running = turn;
    this.turnStart = Date.now();
    const index = this.turnIndex.get(session) ?? 0;
    const script = SCRIPT[index % SCRIPT.length]!;
    this.turnIndex.set(session, index + 1);

    try {
      // A model the mock does not hold is loaded by the first turn that needs
      // it, and nothing else in the turn happens until it answers.
      if (this.load(t.session.model)) {
        this.emit(session, { type: "status", status: "loading" });
        await this.sleep(900, turn);
      }
      this.emit(session, { type: "status", status: "thinking" });
      // The plan the real harness keeps with write_todos, one step further on
      // each turn so the panel has something to show.
      t.todos = MOCK_TODOS.map((todo, i) => ({
        text: todo.text,
        status: i < index ? "done" : i === index ? "doing" : "todo",
      }));
      this.emit(session, { type: "todos", todos: t.todos });
      await this.sleep(420, turn);
      this.settle(t.session.model, t);

      if ((this.active()?.effort ?? "medium") !== "off") {
        for (const chunk of chunks(script.reasoning, 5)) {
          this.send(session, t, { type: "reasoning", text: chunk });
          await this.sleep(120, turn);
        }
      }

      this.emit(session, { type: "status", status: "streaming" });
      await this.stream(session, t, script.preface, turn);

      if (script.question) {
        const askCall = nextId("t");
        const askId = nextId("ask");
        this.send(session, t, {
          type: "tool_call",
          id: askCall,
          name: "ask_question",
          args: {
            question: script.question.question,
            options: script.question.options,
            multi_select: script.question.multi,
          },
        });
        this.emit(session, { type: "status", status: "waiting" });
        this.send(session, t, { type: "question", id: askCall, ask: askId });
        const chosen = await this.askQuestion(askId, session, turn);
        this.emit(session, { type: "status", status: "running" });
        this.send(session, t, {
          type: "tool_output",
          id: askCall,
          output: answerText(script.question.options, chosen.options, chosen.text),
          exit_code: null,
          truncated: false,
        });
        await this.sleep(260, turn);
      }

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
      t.usage = split({
        prompt_tokens: t.usage.prompt_tokens + grown,
        completion_tokens: t.usage.completion_tokens + 240,
        n_ctx: this.ctxOf(t.session.model),
        tok_per_s: 52,
        cached_pct: Math.min(94, 61 + index * 6),
      });
      this.emit(session, { type: "usage", ...t.usage });
      if (this.absorb(session, t)) return;
      this.finish(session, t);
      if (index === 0) this.nameSession(session, t);
    } catch (err) {
      if (!(err instanceof Cancelled)) {
        this.send(session, t, { type: "error", message: String(err) });
      } else {
        this.send(session, t, { type: "error", message: "Turn stopped by user" });
      }
      this.finish(session, t);
    } finally {
      if (this.running === turn) this.running = null;
      this.flushSteer(session, t);
    }
  }

  /** Sends a message the work it was typed against ended without picking up.
      A stop leaves one behind, and so does a compaction. */
  private flushSteer(session: string, t: Transcript): void {
    const held = t.steer;
    if (!held) return;
    t.steer = null;
    void this.sendMessage(session, held.text, held.files.map((f) => f.path));
  }

  /** Picks up a message typed against this turn, so it carries on with it. */
  private absorb(session: string, t: Transcript): boolean {
    const held = t.steer;
    if (!held) return false;
    t.steer = null;
    const seq = this.mintSeq(session);
    pushUser(t.items, held.text, held.files, seq);
    this.emit(session, { type: "message", seq, text: held.text, files: held.files });
    if (this.running?.session === session) this.running = null;
    void this.runTurn(session, t);
    return true;
  }

  // The harness asks the model for a title after the first turn, and the
  // sidebar picks it up off the stream.
  private nameSession(session: string, t: Transcript): void {
    const named = "Fan out the SSE hub";
    t.session.title = named;
    const summary = this.sessions.find((s) => s.id === session);
    if (summary) summary.title = named;
    this.emit(session, { type: "title", title: named });
  }

  private finish(session: string, t: Transcript): void {
    const ms = Date.now() - this.turnStart;
    const tokens = t.items.reduce(
      (n, i) => n + (i.kind === "assistant" || i.kind === "reasoning" ? tokensIn(i.text) : 0),
      0,
    );
    this.send(session, t, {
      type: "done",
      cost: { tokens, ms, tokPerS: 52, requests: 2 },
    });
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

  private askQuestion(id: string, session: string, turn: { cancelled: boolean }): Promise<Answer> {
    return new Promise((resolve, reject) => {
      if (turn.cancelled) {
        reject(new Cancelled());
        return;
      }
      this.openQuestion = {
        id,
        session,
        settle: (a) => (turn.cancelled ? reject(new Cancelled()) : resolve(a)),
      };
    });
  }

  private settleQuestion(chosen: Answer): void {
    const open = this.openQuestion;
    if (!open) return;
    this.openQuestion = null;
    open.settle(chosen);
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

  /** Reports whether the model had to be loaded, which is the wait a turn
      opens with. */
  private load(id: string): boolean {
    const model = this.models.find((m) => m.id === id);
    if (!model || model.status !== "unloaded") return false;
    model.status = "loading";
    return true;
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
    pushUser(items, "The SSE hub drops events when two tabs are open. Find out why.",
      [], this.mintSeq(first.id));
    for (const e of seededEvents()) applyEvent(items, e);
    this.transcripts.set(first.id, {
      session: first,
      items,
      usage: split({ prompt_tokens: 39400, completion_tokens: 2600, n_ctx: 131072 }),
      todos: MOCK_TODOS.map((todo) => ({ ...todo })),
      steer: null,
      status: "idle",
    });
    for (const s of [second, third]) {
      this.transcripts.set(s.id, {
        session: s,
        items: [],
        usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: this.ctxOf(s.model) },
        todos: [],
        steer: null,
        status: "idle",
      });
    }
  }
}

/** Rough count for the mock's cost line, four characters to a token. */
function tokensIn(text: string): number {
  return Math.ceil(text.length / 4);
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

// History for the seeded session: a thought, a tool call, an answered
// question, a compaction and an error, so every row style is on screen before
// anything is clicked.
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
    {
      type: "tool_call",
      id: `${call}_edit`,
      name: "replace_in_file",
      args: {
        path: "internal/server/hub.go",
        old_string: "\tch := h.subs[session]\n\th.mu.RUnlock()\n\tch <- ev",
        new_string: "\tsubs := append([]chan agent.Event(nil), h.subs[session]...)",
      },
    },
    {
      type: "tool_output",
      id: `${call}_edit`,
      output: "edited internal/server/hub.go at line 43 (-3 +9)",
      exit_code: null,
      truncated: false,
    },
    {
      type: "tool_call",
      id: `${call}_ask`,
      name: "ask_question",
      args: {
        question: "Which tab should keep the channel when the last one closes?",
        options: [
          { label: "Close it", description: "The next tab opens a fresh one" },
          { label: "Keep it", description: "Events buffer until a tab comes back" },
        ],
      },
    },
    {
      type: "tool_output",
      id: `${call}_ask`,
      output: "Close it",
      exit_code: null,
      truncated: false,
    },
    { type: "done" },
    { type: "compacted", before: 98000, after: 6000, summary: MOCK_SUMMARY },
    { type: "error", message: "Stream closed by server: context canceled" },
  ];
}

// The annotation is what keeps this file out of a normal build. Constructing
// a class counts as a side effect, so without it the bundler keeps the whole
// module and its sample data even though nothing reaches the export.
export const mockApi: HarnessApi = /* @__PURE__ */ new MockApi();
