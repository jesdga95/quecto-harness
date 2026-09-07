import { ApiError, api } from "../api";
import { dismissSticky, toast } from "./toast.svelte";
import type {
  Attached,
  Changes,
  Config,
  Effort,
  HarnessEvent,
  Model,
  PermMode,
  PermissionDecision,
  Project,
  ServerMode,
  Session,
  Steer,
  Todo,
  TranscriptItem,
  TurnStatus,
  Usage,
} from "../api";
import { applyEvent, outcomeLabel, pushUser } from "../transcript";
import { confirm } from "./confirm.svelte";

// Every screen reads this one object. It holds what the server would hold plus
// the little the browser owns, like which panel is open and whether the last
// call to the harness got through.
export const app = $state({
  ready: false,
  mode: "router" as ServerMode,
  config: null as Config | null,
  projects: [] as Project[],
  models: [] as Model[],
  sessions: {} as Record<string, Session[]>,
  projectPath: "",
  sessionId: "",
  items: [] as TranscriptItem[],
  // A message typed while the turn runs, waiting for it to pick it up.
  steer: null as Steer | null,
  todos: [] as Todo[],
  changes: { files: [], added: 0, removed: 0 } as Changes,
  changesOpen: false,
  usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: 131072 } as Usage,
  status: "idle" as TurnStatus,
  // The model has been asked and nothing has come back yet. True from the
  // moment a turn starts, again after each tool result while the model reads
  // it, and for the whole of a compaction, which are the stretches that draw
  // nothing. Not the "waiting" turn status, which is a permission card waiting
  // on a person.
  awaitingModel: false,
  allowlist: [] as string[],
  sidebar: true,
  error: "",
  connected: true,
  // Empty once a model server has answered.
  detectError: "",
  detectKind: "",
  modelsError: "",
  modelsKind: "",
  probing: false,
});

let unsubscribe: (() => void) | null = null;

/** Runs one call and reports failure once, in the bar at the top of the page. */
async function guard<T>(work: () => Promise<T>): Promise<T | null> {
  try {
    const value = await work();
    app.error = "";
    return value;
  } catch (err) {
    fail(err);
    return null;
  }
}

function fail(err: unknown): void {
  const message = err instanceof Error ? err.message : String(err);
  app.error = message;
  if (err instanceof ApiError && err.status === 401) {
    toast(message, "warn", true);
    return;
  }
  toast(message);
  app.status = "idle";
}

export async function init(): Promise<void> {
  const state = await guard(() => api.getState());
  if (!state) return;
  app.mode = state.mode;
  app.detectError = state.detectError ?? "";
  app.detectKind = state.detectKind ?? "";
  app.modelsError = state.modelsError ?? "";
  app.modelsKind = state.modelsKind ?? "";
  app.config = state.config;
  app.projects = state.projects;
  app.models = state.models;
  await loadSessions();
  app.ready = true;
  const first = state.projects[0];
  if (first) await selectProject(first.path);
}

async function loadSessions(): Promise<void> {
  const lists = await guard(() =>
    Promise.all(app.projects.map((p) => api.listSessions(p.path))),
  );
  if (!lists) return;
  const next: Record<string, Session[]> = {};
  app.projects.forEach((p, i) => (next[p.path] = lists[i] ?? []));
  app.sessions = next;
}

export function sessionsOf(path: string): Session[] {
  return app.sessions[path] ?? [];
}

export function activeSession(): Session | undefined {
  return sessionsOf(app.projectPath).find((s) => s.id === app.sessionId);
}

export async function selectProject(path: string): Promise<void> {
  app.projectPath = path;
  app.allowlist = (await guard(() => api.listAllowlist(path))) ?? [];
  const first = sessionsOf(path)[0];
  if (first) await selectSession(first.id);
  else await newSession();
}

export async function selectSession(id: string): Promise<void> {
  unsubscribe?.();
  unsubscribe = null;
  app.sessionId = id;
  const t = await guard(() => api.getSession(id));
  if (!t) return;
  app.items = t.items;
  app.usage = t.usage;
  app.todos = t.todos;
  app.steer = t.steer;
  // A session opened while its turn runs is running, and the snapshot is the
  // only thing that says so. A compaction draws nothing until it lands, so the
  // wait it is in is taken from the status.
  app.status = t.status;
  app.awaitingModel = t.status === "compacting";
  app.changes = { files: [], added: 0, removed: 0 };
  unsubscribe = api.events(id, onEvent);
  void loadChanges();
}

export async function newSession(): Promise<void> {
  if (!app.projectPath || !app.config) return;
  const empty = sessionsOf(app.projectPath).find((s) => s.title === "New session");
  if (empty) {
    await selectSession(empty.id);
    return;
  }
  const session = await guard(() => api.createSession(app.projectPath));
  if (!session) return;
  await loadSessions();
  await selectSession(session.id);
}

/** Asks the host for a folder and adds it. Does nothing if the dialog is dismissed. */
export async function pickAndAddProject(): Promise<void> {
  const path = await guard(() => api.pickDirectory());
  if (path) await addProject(path);
}

export async function addProject(path: string): Promise<void> {
  const known = new Set(app.projects.map((p) => p.path));
  const projects = await guard(() => api.addProject(path));
  if (!projects) return;
  app.projects = projects;
  await loadSessions();
  // The server resolves and cleans the path it stores, so the entry it did not
  // have before is the one to select. The raw path may not match it.
  const added = projects.find((p) => !known.has(p.path));
  await selectProject(added?.path ?? path);
}

export async function removeProject(path: string): Promise<void> {
  const projects = await guard(() => api.removeProject(path));
  if (!projects) return;
  app.projects = projects;
  if (app.projectPath === path) {
    unsubscribe?.();
    app.sessionId = "";
    app.items = [];
    app.todos = [];
    app.changes = { files: [], added: 0, removed: 0 };
    const next = projects[0];
    if (next) await selectProject(next.path);
    else app.projectPath = "";
  }
  await loadSessions();
}

export async function renameSession(id: string, title: string): Promise<void> {
  if ((await guard(() => api.renameSession(id, title))) === null) return;
  await loadSessions();
}

export async function deleteSession(id: string): Promise<void> {
  if ((await guard(() => api.deleteSession(id))) === null) return;
  const wasActive = app.sessionId === id;
  if (wasActive) {
    unsubscribe?.();
    app.sessionId = "";
    app.items = [];
    app.todos = [];
    app.changes = { files: [], added: 0, removed: 0 };
  }
  await loadSessions();
  if (wasActive) {
    const next = sessionsOf(app.projectPath)[0];
    if (next) await selectSession(next.id);
    else await newSession();
  }
}

/** What the composer draws before the harness has read the file. The harness
    decides what a file really is when the message arrives. */
export function looksLikeImage(path: string): boolean {
  return /\.(png|jpe?g|gif|webp)$/i.test(path);
}

/** Asks the host for files to attach. Empty when the dialog is dismissed. */
export async function pickAttachments(): Promise<string[]> {
  return (await guard(() => api.pickFiles())) ?? [];
}

/** Stores a pasted file with the harness, which is what gives it the path a
    message attaches it by. Empty when the harness refused it. */
export async function pasteFile(name: string, blob: Blob): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return (await guard(() => api.pasteFile(name, btoa(binary)))) ?? "";
}

export async function send(text: string, files: string[] = []): Promise<void> {
  const message = text.trim();
  if (!app.sessionId || (!message && files.length === 0)) return;
  const attached: Attached[] = files.map((path) => ({
    path,
    kind: looksLikeImage(path) ? "image" : "text",
    bytes: 0,
  }));
  // A model still loading has no turn for the message to join. The composer
  // keeps what was typed, so this is the backstop rather than the check.
  if (app.status === "loading") return;
  // A message typed while the turn runs waits for it, and shows as the queue
  // rather than as a message the model has already been given.
  if (app.status !== "idle") {
    const queued = await guard(() => api.sendMessage(app.sessionId, message, files));
    if (queued) app.steer = queued.steer;
    return;
  }
  pushUser(app.items, message, attached);
  // The first status event is a round trip away, and the wait starts now.
  app.awaitingModel = true;
  // The harness reads the attachments, so a file it refuses refuses the whole
  // message. The bubble comes back off the transcript when that happens.
  const item = app.items[app.items.length - 1];
  const sent = await guard(() => api.sendMessage(app.sessionId, message, files));
  if (sent === null) {
    app.items = app.items.filter((i) => i.id !== item?.id);
    return;
  }
  // A turn that started between the check and the call takes the message as a
  // queued one, so the bubble it was drawn as comes back off the transcript.
  if (sent.queued) {
    app.items = app.items.filter((i) => i.id !== item?.id);
    app.steer = sent.steer;
    return;
  }
  // The harness stored the files and gave each one an id, which is what the
  // bubble needs to show a picture rather than the name of one. Its sequence
  // number is what an edit of this message names it by.
  if (item?.kind === "user") {
    item.seq = sent.seq;
    if (sent.files.length > 0) item.files = sent.files;
  }
  await loadSessions();
}

/** The last message the user sent, which is the only one an edit can replace. */
function lastUserItem(): TranscriptItem | undefined {
  for (let i = app.items.length - 1; i >= 0; i--) {
    const item = app.items[i];
    if (item?.kind === "user") return item;
  }
  return undefined;
}

/** A message can be sent again once the harness has stored it, the turn it
    started has finished, and no model is being read. Editing one and sending
    it again is the same act, so it reads this too. */
export function canResend(item: TranscriptItem): boolean {
  if (item.kind !== "user" || item.seq === 0) return false;
  return app.status === "idle" && lastUserItem() === item;
}

/** Tool calls in the turn a resend discards. What they wrote to disk is not
    undone, so a turn that made any is worth confirming. */
function tailToolCalls(): number {
  let calls = 0;
  for (let i = app.items.length - 1; i >= 0; i--) {
    const item = app.items[i];
    if (!item || item.kind === "user") break;
    if (item.kind === "tool") calls += 1;
  }
  return calls;
}

/** Sends the last message again, with text in place of the words it carried.
    The reply to it is discarded and the model answers the message as it now
    stands. The harness rewrites the history and every tab refetches, so
    nothing here edits the transcript. */
export async function resend(seq: number, text?: string): Promise<void> {
  if (!app.sessionId) return;
  const calls = tailToolCalls();
  if (calls > 0) {
    const ok = await confirm({
      title: text === undefined ? "Run the turn again?" : "Send the edited message?",
      body:
        `The reply to it is removed and the model answers again. That reply made ` +
        `${calls} tool ${calls === 1 ? "call" : "calls"}, and what they wrote to ` +
        `disk stays as it is.`,
      confirmLabel: text === undefined ? "Run again" : "Send",
      destructive: true,
    });
    if (!ok) return;
  }
  if ((await guard(() => api.resend(app.sessionId, seq, text))) === null) return;
  await loadSessions();
}

export async function stop(): Promise<void> {
  if (!app.sessionId) return;
  await guard(() => api.stop(app.sessionId));
}

export async function compact(): Promise<void> {
  if (!app.sessionId) return;
  await guard(() => api.compact(app.sessionId));
}

export async function decide(
  item: TranscriptItem,
  decision: PermissionDecision,
): Promise<void> {
  if (item.kind !== "permission") return;
  item.outcome = outcomeLabel(decision);
  const done = await guard(() => api.decide(item.id, decision));
  if (done === null) {
    item.outcome = null;
    return;
  }
  if (decision.decision === "always") {
    app.allowlist = (await guard(() => api.listAllowlist(app.projectPath))) ?? app.allowlist;
  }
}

export async function removeAllowed(prefix: string): Promise<void> {
  const list = await guard(() => api.removeAllowed(app.projectPath, prefix));
  if (list) app.allowlist = list;
}

/** Whether the harness could run a turn right now: something answered at the
    address, it took the key, and it named at least one model. The setup screen
    and the connect button both read this, so they cannot disagree. */
export function serverReady(): boolean {
  const fatal = ["unreachable", "canceled", "auth"];
  if (fatal.includes(app.detectKind) || fatal.includes(app.modelsKind)) return false;
  return app.models.length > 0;
}

/** Points the harness at a model server. Any answer counts except silence and
    a refused key, since neither leaves a harness that can run a turn. */
export async function connectServer(url: string, token: string): Promise<string> {
  app.probing = true;
  try {
    const config = await guard(() => api.updateConfig({ server: { url, token } }));
    if (!config) return app.error || "The harness did not accept that address";
    app.config = config;
    const state = await guard(() => api.getState());
    if (!state) return app.error || "The harness did not answer";
    app.mode = state.mode;
    app.detectError = state.detectError ?? "";
    app.detectKind = state.detectKind ?? "";
    app.modelsError = state.modelsError ?? "";
    app.modelsKind = state.modelsKind ?? "";
    app.models = state.models;
    app.projects = state.projects;

    if (serverReady()) return "";

    // Nothing there, and a refused key, both leave a harness that cannot run a
    // turn. Anything else the server says is its own business.
    const kinds = [app.detectKind, app.modelsKind];
    if (kinds.includes("unreachable") || kinds.includes("canceled")) {
      return app.detectError || "Nothing answered at that address";
    }
    if (kinds.includes("auth")) {
      return token.trim() === ""
        ? "That server needs an API key"
        : "That server refused the API key";
    }
    // A server that failed said why, and its own words name the cause.
    if (app.modelsError !== "") return app.modelsError;
    // Any web server answers a GET. Only a model list proves there is a model
    // behind it.
    return "That address answered, but it did not return a model list";
  } finally {
    app.probing = false;
  }
}

export async function saveConfig(patch: Partial<Config>): Promise<void> {
  const config = await guard(() => api.updateConfig(patch));
  if (!config) return;
  app.config = config;
}

/** A turn talks to the session's model, not to the configuration, so an open
    session is repointed as well as the default for the next one. */
/** There is one model. Choosing it points every session at it, and the harness
    reads it before anything is sent. */
export async function setModel(id: string): Promise<void> {
  const previous = app.config?.model ?? "";
  if (app.config) app.config.model = id;
  // The split was measured against the previous model, so it goes with it
  // rather than being shown beside a total the new model reported.
  app.usage.breakdown = undefined;
  if ((await guard(() => api.updateConfig({ model: id }))) === null) {
    if (app.config) app.config.model = previous;
  }
}

export function setEffort(effort: Effort): void {
  if (app.config) app.config.effort = effort;
  void guard(() => api.updateConfig({ effort }));
}

export function setPreserveReasoning(on: boolean): void {
  if (app.config) app.config.preserve_reasoning = on;
  void guard(() => api.updateConfig({ preserve_reasoning: on }));
}

export function preserveReasoning(): boolean {
  return app.config?.preserve_reasoning ?? true;
}

/** What the tool call limit rotates through. Zero is no limit. */
export const TOOL_CALL_STEPS = [25, 50, 100, 0];

export function maxToolCalls(): number {
  return app.config?.max_tool_calls ?? 50;
}

/** Advances to the next step. A hand-edited number lands on the next one above it. */
export function nextToolCallLimit(): void {
  const current = maxToolCalls();
  const next = TOOL_CALL_STEPS.find((v) => v > current) ?? 0;
  if (app.config) app.config.max_tool_calls = next;
  void guard(() => api.updateConfig({ max_tool_calls: next }));
}

export function permMode(): PermMode {
  return app.config?.permissions ?? "ask";
}

const FULL_AUTO_WARNING = {
  title: "Turn on full auto?",
  body:
    "Every tool call runs with nothing asked and nothing checked: the allowlist stops " +
    "applying, and the model can run any shell command as you, anywhere on this machine, " +
    "including ones that delete files or reach the network. It stays on for every project " +
    "and every session until you turn it off.",
  confirmLabel: "Turn on full auto",
};

/** Switches the permission policy. Nothing is written and nothing is sent
    until the full auto dialog is answered. */
export async function setPermMode(mode: PermMode): Promise<void> {
  if (mode === permMode()) return;
  if (mode === "full" && !(await confirm(FULL_AUTO_WARNING))) return;
  if (app.config) app.config.permissions = mode;
  void guard(() => api.updateConfig({ permissions: mode }));
}

/** Refetches the change list. It fails quietly: the panel is a second view of
    work the transcript already reported, so a stale list is better than a
    toast on every write. */
export async function loadChanges(): Promise<void> {
  const id = app.sessionId;
  if (!id) return;
  try {
    const changes = await api.getChanges(id);
    if (app.sessionId === id) app.changes = changes;
  } catch {
    // Keep what was there.
  }
}

export function toggleChanges(): void {
  app.changesOpen = !app.changesOpen;
}

// Statuses that mean a request is out and nothing has come back for it.
const ASKED = new Set<TurnStatus>(["thinking", "streaming", "compacting"]);

function onEvent(event: HarnessEvent): void {
  if (event.type === "resync") {
    app.items = event.transcript.items;
    app.usage = event.transcript.usage;
    app.todos = event.transcript.todos;
    app.steer = event.transcript.steer;
    app.status = event.transcript.status;
    app.awaitingModel = event.transcript.status === "compacting";
    return;
  }

  if (event.type === "queued") {
    app.steer = event.steer;
    return;
  }

  // The turn picked the queued message up, so it becomes an ordinary bubble.
  if (event.type === "message") {
    app.steer = null;
    if (!app.items.some((i) => i.kind === "user" && i.seq === event.seq)) {
      pushUser(app.items, event.text, event.files, event.seq);
    }
    return;
  }

  if (event.type === "todos") {
    app.todos = event.todos;
    return;
  }

  if (event.type === "connection") {
    app.connected = event.connected;
    if (event.connected) {
      app.error = "";
      dismissSticky();
    } else {
      const message = event.message ?? "The harness is not reachable, reconnecting";
      app.error = message;
      toast(message, "warn", true);
    }
    return;
  }

  // The title lands in the sidebar and the header, neither of which reads the
  // transcript, so it is refetched with the session list.
  if (event.type === "title") {
    void loadSessions();
    return;
  }

  // Anything the model produces ends the wait; the next request starts it
  // again on its status event.
  if (event.type === "delta" || event.type === "reasoning" || event.type === "tool_call") {
    app.awaitingModel = false;
  }

  applyEvent(app.items, event);

  // Only the file tools change the list, and the event carries an id rather
  // than a name, so the card it filled is what names it.
  if (event.type === "tool_output") {
    const card = app.items.find((i) => i.id === event.id);
    const wrote =
      card?.kind === "tool" && (card.name === "write_file" || card.name === "replace_in_file");
    if (wrote) void loadChanges();
  }

  if (event.type === "usage") {
    app.usage = {
      prompt_tokens: event.prompt_tokens,
      completion_tokens: event.completion_tokens,
      n_ctx: event.n_ctx,
      tok_per_s: event.tok_per_s ?? app.usage.tok_per_s,
      cached_pct: event.cached_pct ?? app.usage.cached_pct,
      breakdown: event.breakdown,
    };
  }

  // A turn that ends in an error still ends, so the composer comes back rather
  // than sitting on a spinner until the next status event.
  if (event.type === "done" || event.type === "error") {
    app.status = "idle";
    app.awaitingModel = false;
  }

  if (event.type === "done" && event.cost && event.cost.tokPerS > 0) {
    app.usage.tok_per_s = Math.round(event.cost.tokPerS);
  }

  if (event.type === "status") {
    const settled = app.status === "loading" && event.status !== "loading";
    app.status = event.status;
    if (ASKED.has(event.status)) app.awaitingModel = true;
    if (event.status === "idle" || event.status === "loading") app.awaitingModel = false;
    void refreshModels(settled);
  }
}

let refreshedAt = 0;

/** Status events land several times a turn and the model list changes at most
    once, when a load finishes. That reading is the one the dots are drawn
    from, so it is taken past both caches rather than from a list fetched
    before the load began. */
async function refreshModels(settled = false): Promise<void> {
  const now = Date.now();
  if (!settled && now - refreshedAt < 5000) return;
  refreshedAt = now;
  const models = await guard(() => api.listModels(settled));
  if (models) app.models = models;
}

/** The card blocks the composer until it is answered. */
export function pendingPermission(): TranscriptItem | undefined {
  return app.items.find((i) => i.kind === "permission" && i.outcome === null);
}

/** Turns and steps, for the line under the composer. */
export function turnStats(): { turns: number; steps: number } {
  let turns = 0;
  let steps = 0;
  for (const item of app.items) {
    if (item.kind === "user") turns += 1;
    else if (item.kind !== "assistant") steps += 1;
  }
  return { turns, steps };
}
