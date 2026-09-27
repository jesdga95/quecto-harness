import { ApiError, api } from "../api";
import { dismissSticky, toast } from "./toast.svelte";
import type {
  Agent,
  AgentPatch,
  Attached,
  Changes,
  Child,
  Config,
  HarnessEvent,
  Model,
  Answer,
  PermMode,
  PermissionDecision,
  Probe,
  Protocol,
  Project,
  ProjectFile,
  ProjectDir,
  SearchConfig,
  SearchPatch,
  SearchProbe,
  ServerMode,
  Session,
  Steer,
  Todo,
  TranscriptItem,
  TurnStatus,
  Usage,
  WorkMode,
} from "../api";
import { applyEvent, nextId, outcomeLabel, pushUser } from "../transcript";
import {
  fileArtifactId,
  projectArtifacts,
  planArtifacts,
  writtenPreviews,
  type Artifact,
} from "../artifacts";
import { inProject } from "../files";
import type { Plan } from "../plan";
import { confirm } from "./confirm.svelte";

// Every screen reads this one object. It holds what the server would hold plus
// the little the browser owns, like which panel is open and whether the last
// call to the harness got through.
export const app = $state({
  ready: false,
  mode: "router" as ServerMode,
  config: null as Config | null,
  search: null as SearchConfig | null,
  agents: [] as Agent[],
  // Whether the agents dialog is open. It is reached from the composer, four
  // components down, so the flag lives here rather than being threaded.
  agentsOpen: false,
  projects: [] as Project[],
  models: [] as Model[],
  sessions: {} as Record<string, Session[]>,
  projectPath: "",
  sessionId: "",
  // The session last fetched, which is the only record of a subagent run: the
  // sidebar lists conversations somebody started and nothing else.
  opened: null as Session | null,
  items: [] as TranscriptItem[],
  // A message typed while the turn runs, waiting for it to pick it up.
  steer: null as Steer | null,
  todos: [] as Todo[],
  // The mode the next turn runs in. With no session open it is held here and
  // sent with the message that creates one.
  workMode: "execute" as WorkMode,
  // The plan plan mode is writing, or the one last approved.
  draft: null as Plan | null,
  changes: { files: [], added: 0, removed: 0 } as Changes,
  // Whether the tabbed panel beside the transcript is open.
  panel: false,
  // The subagent runs this session launched, newest first, with what each is
  // doing now. The chip counts them and the panel lists them.
  runs: [] as Child[],
  // The run the panel is showing and the steps it has taken. They are fetched
  // when a run is opened, so a panel nobody opened costs no request and holds
  // no stream.
  runId: "",
  runItems: [] as TranscriptItem[],
  // The artifact the panel shows. Pinned means the reader chose it, and the
  // panel stops moving to each new one. Dismissed means the panel was closed
  // during this turn, so a new artifact does not open it again. Wide hides
  // the transcript.
  artifactId: "",
  artifactPinned: false,
  artifactsDismissed: false,
  artifactsWide: false,
  // Files in the project as last read, by the path the harness named them
  // under, and the ones the reader opened, in the order they were opened.
  files: {} as Record<string, ProjectFile | undefined>,
  fileErrors: {} as Record<string, string>,
  openFiles: [] as string[],
  // The panel's tabs, by artifact id, in the order they were opened, and the
  // folder its file browser is showing.
  tabs: [] as string[],
  browseDir: null as ProjectDir | null,
  browseError: "",
  usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: 0 } as Usage,
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
});


// The artifacts are read from the transcript, so they follow it without being
// kept in step by hand. Files in the project follow them: the drawings, pages
// and images a turn wrote, and whatever the reader opened.
const written = $derived(writtenPreviews(app.items, app.projectPath));
const projectPaths = $derived.by(() => {
  const paths = [...written.keys()];
  for (const path of app.openFiles) if (!paths.includes(path)) paths.push(path);
  return paths;
});
const artifactList = $derived([
  ...planArtifacts(app.draft),
  ...projectArtifacts(projectPaths, app.files, written),
]);

export function artifacts(): Artifact[] {
  return artifactList;
}

// Artifacts already on screen when a session was opened or resynced. Only one
// that appears after that, in a live stream, opens the panel.
let announced = new Set<string>();

function markAnnounced(): void {
  announced = new Set(artifactList.map((a) => a.id));
}

let unsubscribe: (() => void) | null = null;
// The stream of the run the panel is showing. One at a time: a reader follows
// one subagent, and the rest report their state on the launcher's own stream.
let runStream: (() => void) | null = null;

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

/** Reads the harness and copies what it said over the local copy. Three things
    need it: starting up, saving an agent, and choosing one. */
export async function refreshState(): Promise<boolean> {
  const state = await guard(() => api.getState());
  if (!state) return false;
  app.mode = state.mode;
  app.detectError = state.detectError ?? "";
  app.detectKind = state.detectKind ?? "";
  app.modelsError = state.modelsError ?? "";
  app.modelsKind = state.modelsKind ?? "";
  app.config = state.config;
  app.search = state.search;
  app.agents = state.agents;
  app.projects = state.projects;
  app.models = state.models;
  return true;
}

export async function init(): Promise<void> {
  if (!(await refreshState())) return;
  await loadSessions();
  app.ready = true;
  // The first project is named so the empty screen has one, and its most
  // recent conversation is opened if it has one.
  const first = app.projects[0];
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
  const listed = sessionsOf(app.projectPath).find((s) => s.id === app.sessionId);
  if (listed) return listed;
  // A subagent run is not in the list, so what was fetched when it was opened
  // is the only record of it on the page.
  return app.opened?.id === app.sessionId ? app.opened : undefined;
}

export async function selectProject(path: string): Promise<void> {
  app.projectPath = path;
  app.allowlist = (await guard(() => api.listAllowlist(path))) ?? [];
  const first = sessionsOf(path)[0];
  if (first) await selectSession(first.id);
  else clearSession();
}

/** Empties the screen, leaving the project. Nothing is open, so what the last
    session was showing goes with it. */
function clearSession(): void {
  unsubscribe?.();
  unsubscribe = null;
  app.sessionId = "";
  app.opened = null;
  app.items = [];
  app.todos = [];
  app.steer = null;
  app.workMode = "execute";
  app.draft = null;
  app.status = "idle";
  app.awaitingModel = false;
  app.changes = { files: [], added: 0, removed: 0 };
  closePanel();
  app.runs = [];
  resetArtifacts();
  // Nothing is known about the next session's context until its model has
  // been read, and a zero window draws no chip.
  app.usage = { prompt_tokens: 0, completion_tokens: 0, n_ctx: 0 };
  // Nothing is watching the harness now, so a warning that it cannot be
  // reached has nothing left to clear it. A call that fails raises its own.
  if (!app.connected) {
    app.connected = true;
    app.error = "";
    dismissSticky();
  }
}

export async function selectSession(id: string): Promise<void> {
  unsubscribe?.();
  unsubscribe = null;
  app.sessionId = id;
  const t = await guard(() => api.getSession(id));
  if (!t) return;
  // A session opened from another project's list makes that project the one
  // a new session, a file path and an @ search go to.
  const project = t.session.project;
  if (project && project !== app.projectPath) {
    app.projectPath = project;
    app.allowlist = (await guard(() => api.listAllowlist(project))) ?? [];
    if (app.sessionId !== id) return;
  }
  app.opened = t.session;
  app.items = t.items;
  app.usage = t.usage;
  app.todos = t.todos;
  app.steer = t.steer;
  app.workMode = t.mode;
  app.draft = t.draft;
  // A session opened while its turn runs is running, and the snapshot is the
  // only thing that says so. A model being read and a compaction both draw
  // nothing until they land, so the wait is taken from the status.
  app.status = t.status;
  app.awaitingModel = WAITING.has(t.status);
  app.changes = { files: [], added: 0, removed: 0 };
  // A reloaded page reads which subagents are still working from the snapshot,
  // since their state only reaches the launcher's stream when it changes.
  // The changes tab stays open across sessions; the others were this one's.
  closeRuns();
  const keepChanges = app.panel && app.artifactId === CHANGES_ID;
  app.runs = t.session.children;
  resetArtifacts();
  if (keepChanges) showTab(CHANGES_ID);
  else app.panel = false;
  markAnnounced();
  unsubscribe = api.events(id, onEvent);
  void loadChanges();
}

/** Clears the screen for a new conversation. The harness is told about it when
    the first message is sent, so nothing is written and no row appears in the
    sidebar until there is something in it. */
export function newSession(): void {
  clearSession();
}

/** Creates the session the empty screen has been standing in for. It is not
    opened, since nothing is drawn from a session this screen already holds.
    False when the harness refused it, which leaves the screen empty. */
async function createSession(): Promise<boolean> {
  if (!app.projectPath || !app.config) return false;
  const session = await guard(() => api.createSession(app.projectPath));
  if (!session) return false;
  app.sessionId = session.id;
  unsubscribe = api.events(session.id, onEvent);
  return true;
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
    clearSession();
    const next = projects[0];
    if (next) await selectProject(next.path);
    else app.projectPath = "";
  }
  await loadSessions();
}

/** Moves a project to where a drop put it. The whole order goes to the
    harness, which checks it names every project once, and the row is put back
    where it was when the harness refuses. */
export async function reorderProjects(ordered: Project[]): Promise<void> {
  const before = app.projects;
  app.projects = ordered;
  const saved = await guard(() => api.reorderProjects(ordered.map((p) => p.path)));
  if (saved) app.projects = saved;
  else app.projects = before;
}

export async function renameSession(id: string, title: string): Promise<void> {
  if ((await guard(() => api.renameSession(id, title))) === null) return;
  await loadSessions();
}

export async function deleteSession(id: string): Promise<void> {
  if ((await guard(() => api.deleteSession(id))) === null) return;
  const wasActive = app.sessionId === id;
  if (wasActive) clearSession();
  await loadSessions();
  if (wasActive) {
    const next = sessionsOf(app.projectPath)[0];
    if (next) await selectSession(next.id);
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

/** Files in the open project matching what follows an @ in the composer. It
    fails quietly: a list that does not come is a list with nothing in it. */
export async function findFiles(q: string): Promise<string[]> {
  if (!app.projectPath) return [];
  try {
    return await api.findFiles(app.projectPath, q);
  } catch {
    return [];
  }
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
  if (!message && files.length === 0) return;
  // The empty screen holds a project and no session, so the first message is
  // what creates one.
  if (!app.sessionId && !(await createSession())) return;
  const attached: Attached[] = files.map((path) => ({
    path,
    kind: looksLikeImage(path) ? "image" : "text",
    bytes: 0,
  }));
  // A message typed while the turn runs waits for it, and shows as the queue
  // rather than as a message the model has already been given.
  if (app.status !== "idle") {
    const queued = await guard(() => api.sendMessage(app.sessionId, message, files));
    if (queued) app.steer = queued.steer;
    return;
  }
  const divider = modeChange();
  if (divider) app.items.push(divider);
  pushUser(app.items, message, attached);
  // The first status event is a round trip away, and the wait starts now.
  app.awaitingModel = true;
  // The harness reads the attachments, so a file it refuses refuses the whole
  // message. The bubble comes back off the transcript when that happens.
  const item = app.items[app.items.length - 1];
  const sent = await guard(() => api.sendMessage(app.sessionId, message, files, app.workMode));
  if (sent === null) {
    app.items = app.items.filter((i) => i.id !== item?.id && i.id !== divider?.id);
    // A refused first message leaves the session it created behind, and the
    // list is where it shows.
    await loadSessions();
    return;
  }
  // A turn that started between the check and the call takes the message as a
  // queued one, so the bubble it was drawn as comes back off the transcript.
  if (sent.queued) {
    app.items = app.items.filter((i) => i.id !== item?.id && i.id !== divider?.id);
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

/** The divider a message sent now needs, when the mode moved since the last
    one. A session that has only ever executed draws none. */
function modeChange(): TranscriptItem | null {
  let last: WorkMode = "execute";
  for (const item of app.items) if (item.kind === "mode") last = item.mode;
  if (app.workMode === last) return null;
  return { kind: "mode", id: nextId("m"), mode: app.workMode, kickoff: false, title: "" };
}

/** Sets the mode of the next turn. A turn already running keeps its own. */
export async function setWorkMode(mode: WorkMode): Promise<void> {
  const before = app.workMode;
  app.workMode = mode;
  if (!app.sessionId) return;
  if ((await guard(() => api.setMode(app.sessionId, mode))) === null) app.workMode = before;
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
  // A message another agent sent is not the reader's to rewrite.
  if (item.kind !== "user" || item.seq === 0 || item.from !== "") return false;
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

/** The answer is written onto the row before it is sent, so the card closes on
    the click rather than waiting for the turn to publish the result. */
export async function answer(item: TranscriptItem, chosen: Answer): Promise<void> {
  if (item.kind !== "tool" || !item.askId) return;
  const id = item.askId;
  item.askId = undefined;
  if ((await guard(() => api.answer(id, chosen))) === null) item.askId = id;
}

export async function removeAllowed(prefix: string): Promise<void> {
  const list = await guard(() => api.removeAllowed(app.projectPath, prefix));
  if (list) app.allowlist = list;
}

/** Whether the harness could run a turn right now: an agent answers, something
    answered at its address, and it named at least one model. */
export function agentReady(): boolean {
  if (!activeAgent()) return false;
  const fatal = ["unreachable", "canceled", "auth"];
  if (fatal.includes(app.detectKind) || fatal.includes(app.modelsKind)) return false;
  return app.models.length > 0;
}

export function activeAgent(): Agent | undefined {
  return app.agents.find((a) => a.id === app.config?.agent);
}

export function agentById(id: string): Agent | undefined {
  return app.agents.find((a) => a.id === id);
}

/** Saves one field of an agent. The local copy moves first so the field does
    not lag the typing, and goes back if the harness refused it. */
export async function saveAgent(id: string, patch: AgentPatch): Promise<Agent | null> {
  const at = app.agents.findIndex((a) => a.id === id);
  const before = app.agents[at];
  if (at >= 0 && before) app.agents[at] = { ...before, ...patch };
  const saved = await guard(() => api.updateAgent(id, patch));
  if (!saved) {
    if (at >= 0 && before) app.agents[at] = before;
    return null;
  }
  if (at >= 0) app.agents[at] = saved;
  return saved;
}

export async function addAgent(patch: AgentPatch): Promise<Agent | null> {
  const created = await guard(() => api.createAgent(patch));
  if (!created) return null;
  await refreshState();
  return created;
}

export async function removeAgent(id: string): Promise<void> {
  const agents = await guard(() => api.deleteAgent(id));
  if (!agents) return;
  await refreshState();
}

/** Chooses the agent every session answers with. Nothing is loaded by the
    choice: the next message that needs the model is what loads it. */
export async function setActiveAgent(id: string): Promise<void> {
  if (app.config?.agent === id) return;
  // The split was estimated against the previous prompt, so it goes with it
  // rather than being shown beside a total the new model reported.
  app.usage.breakdown = undefined;
  if ((await guard(() => api.selectAgent(id))) === null) return;
  await refreshState();
}

export function probeAgent(
  at: { agent?: string; url: string; key?: string | null; protocol?: Protocol },
  refresh?: boolean,
): Promise<Probe | null> {
  return guard(() => api.probeAgent(at, refresh));
}

/** Saves one field of the config. True once the harness has answered, which
    is what tells a dialog the write landed rather than merely went out. */
export async function saveConfig(patch: Partial<Config>): Promise<boolean> {
  const config = await guard(() => api.updateConfig(patch));
  if (!config) return false;
  app.config = config;
  return true;
}

/** Saves the search block. The answer is the resolved block, which is what the
    dialog compares its draft against from then on. */
export async function saveSearch(patch: SearchPatch): Promise<boolean> {
  const search = await guard(() => api.updateSearch(patch));
  if (!search) return false;
  app.search = search;
  return true;
}

/** Runs the Test key button. A key that does not work is answered on 200 and
    read in the form. A request the endpoint refuses is a 400, which guard
    raises as the app banner on top of the form's nothing-came-back line. */
export function probeSearch(at: { provider: string; url: string; key: string }): Promise<SearchProbe | null> {
  return guard(() => api.probeSearch(at));
}

/** The steps a sampling slider moves through. Null is the first of them: the
    field is left out of the request and the model server's value stands. */
function samplingSteps(max: number): (number | null)[] {
  const out: (number | null)[] = [null];
  for (let i = 0; i * 0.05 <= max + 1e-9; i++) out.push(Math.round(i * 5) / 100);
  return out;
}

/** Whole steps, for a count rather than a fraction. */
function wholeSteps(max: number, by: number): (number | null)[] {
  const out: (number | null)[] = [null];
  for (let v = 0; v <= max; v += by) out.push(v);
  return out;
}

export const TEMPERATURE_STEPS = samplingSteps(2);
export const TOP_P_STEPS = samplingSteps(1);
export const TOP_K_STEPS = wholeSteps(200, 5);
export const REPEAT_PENALTY_STEPS = samplingSteps(2);

/** How many tasks one agent runs at once. The ceiling matches the harness's,
    so a number the form takes is never one the write refuses. */
export const MAX_CONCURRENCY = 16;

/** How long the line describing an agent may be. The ceiling matches the
    harness's, so a description the form takes is never one the write refuses. */
export const MAX_DESCRIPTION = 200;

/** A reply length doubles rather than creeps, so the bar does too. */
export const MAX_TOKENS_STEPS: (number | null)[] = [
  null, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072,
];

/** The step nearest a value, so a hand-edited number still lands on the bar. */
export function samplingIndex(steps: (number | null)[], value: number | null): number {
  if (value === null) return 0;
  let best = 1;
  for (let i = 2; i < steps.length; i++) {
    if (Math.abs((steps[i] as number) - value) < Math.abs((steps[best] as number) - value)) {
      best = i;
    }
  }
  return best;
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

/** The warning every picker of full auto asks, from the composer chip and the
    settings dialog alike. Answered no, the choice does not land. */
export function confirmFullAuto(): Promise<boolean> {
  return confirm(FULL_AUTO_WARNING);
}

/** Switches the permission policy. Nothing is written and nothing is sent
    until the full auto dialog is answered. */
export async function setPermMode(mode: PermMode): Promise<void> {
  if (mode === permMode()) return;
  if (mode === "full" && !(await confirmFullAuto())) return;
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

// The panel's fixed tabs. Everything else in the tab list is an artifact id.
export const BROWSE_ID = "panel:files";
export const CHANGES_ID = "panel:changes";
export const RUNS_ID = "panel:runs";

function addTab(id: string): void {
  if (!app.tabs.includes(id)) app.tabs.push(id);
}

/** Shows one tab, opening the panel on it. */
export function showTab(id: string): void {
  addTab(id);
  app.artifactId = id;
  app.artifactPinned = true;
  app.artifactsDismissed = false;
  app.panel = true;
}

/** Closes the panel. Closing it while a turn runs keeps it closed until the
    next turn, and the agent it was following is let go. */
export function closePanel(): void {
  if (app.panel && app.status !== "idle") app.artifactsDismissed = true;
  app.panel = false;
  app.artifactsWide = false;
  closeRuns();
}

/** The header's panel button. An empty panel opens on what is newest: an
    artifact, else the changes, else the agents, else the files. */
export function togglePanel(): void {
  if (app.panel) {
    closePanel();
    return;
  }
  if (app.tabs.length === 0) {
    const last = artifactList.at(-1);
    const first = last
      ? last.id
      : app.changes.files.length > 0
        ? CHANGES_ID
        : app.runs.length > 0
          ? RUNS_ID
          : BROWSE_ID;
    showTab(first);
    if (first === BROWSE_ID) void browse("");
    return;
  }
  if (!app.tabs.includes(app.artifactId)) app.artifactId = app.tabs[0]!;
  app.artifactsDismissed = false;
  app.panel = true;
}

export function showChanges(): void {
  showTab(CHANGES_ID);
}

export function showRuns(): void {
  showTab(RUNS_ID);
}

/** Lets go of the agent the panel was following. The tab stays, and follows
    one again when it is next shown. */
function closeRuns(): void {
  runStream?.();
  runStream = null;
  app.runId = "";
  app.runItems = [];
}

/** Takes a tab out of the panel. The one beside it takes its place, and the
    panel closes with its last tab. */
export function closeTab(id: string): void {
  const at = app.tabs.indexOf(id);
  if (at === -1) return;
  app.tabs.splice(at, 1);
  if (id === BROWSE_ID) {
    app.browseDir = null;
    app.browseError = "";
  }
  if (id === RUNS_ID) closeRuns();
  if (app.artifactId !== id) return;
  const next = app.tabs[at] ?? app.tabs[at - 1];
  if (next) {
    app.artifactId = next;
    app.artifactPinned = true;
  } else {
    app.artifactId = "";
    app.artifactPinned = false;
    closePanel();
  }
}

/** Opens the file browser tab on a folder, the project root by default. */
export async function browse(path = app.browseDir?.path ?? ""): Promise<void> {
  showTab(BROWSE_ID);
  const id = app.sessionId;
  if (!id) return;
  try {
    const dir = await api.listDir(id, path);
    if (app.sessionId !== id) return;
    app.browseDir = dir;
    app.browseError = "";
  } catch (err) {
    if (app.sessionId === id) app.browseError = err instanceof Error ? err.message : String(err);
  }
}

/** Shows one artifact in the panel. Choosing the newest goes back to following
    whatever comes next. */
export function showArtifact(id: string): void {
  addTab(id);
  app.artifactId = id;
  app.artifactPinned = artifactList.at(-1)?.id !== id;
  app.artifactsDismissed = false;
  app.panel = true;
}

export function toggleArtifactsWide(): void {
  app.artifactsWide = !app.artifactsWide;
}

/** Reads a file in the project into the panel's cache. A failure is kept
    against the path for the panel to say, and toasted when asked for. */
export async function loadFile(raw: string, loud = false): Promise<ProjectFile | null> {
  const id = app.sessionId;
  if (!id) return null;
  const path = inProject(raw, app.projectPath);
  try {
    const file = await api.readFile(id, path);
    if (app.sessionId !== id) return null;
    app.files[file.path] = file;
    delete app.fileErrors[file.path];
    return file;
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (app.sessionId === id) app.fileErrors[path] = message;
    if (loud) toast(message);
    return null;
  }
}

/** Opens a file in the project in the panel, as a file link asks. */
export async function openFile(raw: string): Promise<void> {
  const file = await loadFile(raw, true);
  if (!file) return;
  if (!app.openFiles.includes(file.path)) app.openFiles.push(file.path);
  showArtifact(fileArtifactId(file.path));
}

export function fileUrl(path: string): string {
  return app.sessionId ? api.fileUrl(app.sessionId, path) : "";
}

function resetArtifacts(): void {
  app.files = {};
  app.fileErrors = {};
  app.openFiles = [];
  app.tabs = [];
  app.browseDir = null;
  app.browseError = "";
  app.artifactId = "";
  app.artifactPinned = false;
  app.artifactsDismissed = false;
  app.artifactsWide = false;
  announced = new Set();
}

/** Opens the panel on an artifact the stream has just started, unless the
    reader closed it this turn or chose another one to look at. */
function announceArtifacts(): void {
  for (const artifact of artifactList) {
    if (announced.has(artifact.id)) continue;
    announced.add(artifact.id);
    if (!app.artifactPinned) {
      addTab(artifact.id);
      app.artifactId = artifact.id;
    }
    if (!app.artifactsDismissed && !app.panel) app.panel = true;
  }
}

/** Shows one subagent run in the panel: its steps as they stand, and its
    stream from there. Nothing else is followed, so the panel holds one stream
    however many agents are working. */
export async function openRun(id: string): Promise<void> {
  runStream?.();
  runStream = null;
  app.runId = id;
  app.runItems = [];
  const t = await guard(() => api.getSession(id));
  if (!t || app.runId !== id) return;
  app.runItems = t.items;
  noteRun(id, t.status);
  runStream = api.events(id, onRunEvent);
}

/** The panel's stream. It builds the run's steps and nothing else: the
    launcher's own screen is not this session's. */
function onRunEvent(event: HarnessEvent): void {
  if (event.type === "resync") {
    app.runItems = event.transcript.items;
    return;
  }
  if (event.type === "message") {
    if (!app.runItems.some((i) => i.kind === "user" && i.seq === event.seq)) {
      pushUser(app.runItems, event.text, event.files, event.seq, false, event.from);
    }
    return;
  }
  applyEvent(app.runItems, event);
}

/** Records where a subagent run has got to. A launch is the first the launcher
    hears of one, so an id nothing holds is added rather than dropped. */
function noteRun(id: string, status: TurnStatus, agent = "", title = ""): void {
  const run = app.runs.find((r) => r.id === id);
  if (run) {
    run.status = status;
    if (agent) run.agent = agent;
    if (title) run.title = title;
    return;
  }
  app.runs.unshift({ id, title, agent, status });
}

// Statuses that mean a request is out and nothing has come back for it.
const ASKED = new Set<TurnStatus>(["loading", "thinking", "streaming", "compacting"]);

// The waits a session can be found in, which draw nothing of their own.
const WAITING = new Set<TurnStatus>(["loading", "compacting"]);

function onEvent(event: HarnessEvent): void {
  if (event.type === "resync") {
    app.items = event.transcript.items;
    app.usage = event.transcript.usage;
    app.todos = event.transcript.todos;
    app.workMode = event.transcript.mode;
    app.draft = event.transcript.draft;
    app.steer = event.transcript.steer;
    app.status = event.transcript.status;
    app.awaitingModel = WAITING.has(event.transcript.status);
    markAnnounced();
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
      pushUser(app.items, event.text, event.files, event.seq, false, event.from);
    }
    return;
  }

  if (event.type === "todos") {
    app.todos = event.todos;
    return;
  }

  // The draft is drawn in the panel, which opens on it as it is first written.
  if (event.type === "plan_draft") {
    app.draft = event.plan;
    announceArtifacts();
    return;
  }

  // An approved plan starts its execution in the same turn, and the message
  // that starts it is drawn as the divider.
  if (event.type === "mode") {
    app.workMode = event.mode;
    // A mode the person picks starts new work, and the harness drops the
    // plan with it. An approval keeps the plan it is carrying out.
    if (!event.kickoff) app.draft = null;
    if (event.kickoff) {
      app.items.push({
        kind: "mode",
        id: nextId("m"),
        mode: event.mode,
        kickoff: true,
        title: event.title,
      });
    }
    return;
  }

  if (event.type === "child") {
    noteRun(event.session, event.status, event.agent ?? "", event.title ?? "");
  }

  // The harness says the files moved for a reason this stream cannot show,
  // which is a subagent having written one.
  if (event.type === "changed") {
    void loadChanges();
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

  if (event.type === "delta" || event.type === "tool_args") announceArtifacts();

  // The event carries an id rather than a name, so the card it filled is what
  // names it. A write moves the list by definition. A command can create,
  // change or delete anything, but only a path some write already kept a copy
  // of can appear in the list, so an empty list stays empty and is not refetched.
  if (event.type === "tool_output") {
    const card = app.items.find((i) => i.id === event.id);
    const touched =
      card?.kind === "tool" &&
      (card.name === "write_file" ||
        card.name === "replace_in_file" ||
        (card.name === "run_shell" && app.changes.files.length > 0));
    if (touched) void loadChanges();
    // A file the panel holds is read again once a write to it lands.
    if (card?.kind === "tool" && (card.name === "write_file" || card.name === "replace_in_file")) {
      const path = typeof card.args.path === "string" ? inProject(card.args.path, app.projectPath) : "";
      if (path && (app.files[path] || written.has(path))) void loadFile(path);
      announceArtifacts();
    }
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
    if (app.status === "idle" && event.status !== "idle") app.artifactsDismissed = false;
    app.status = event.status;
    if (ASKED.has(event.status)) app.awaitingModel = true;
    if (event.status === "idle") app.awaitingModel = false;
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

/** An unanswered card, whoever raised it. */
export function pendingPermission(): TranscriptItem | undefined {
  return app.items.find((i) => i.kind === "permission" && i.outcome === null);
}

/** The card that blocks the composer. A subagent's card does not: this
    conversation may be idle or working on something else, and a background
    worker must not lock a person out of their own composer. */
export function blockingPermission(): TranscriptItem | undefined {
  return app.items.find((i) => i.kind === "permission" && i.outcome === null && i.agent === "");
}

/** A question blocks it the same way, on its own card. The id is what makes
    the card answerable, so a row without one blocks nothing. */
export function pendingQuestion(): TranscriptItem | undefined {
  return app.items.find(
    (i) =>
      i.kind === "tool" &&
      (i.name === "ask_question" || i.name === "submit_plan") &&
      i.running &&
      i.askId !== undefined,
  );
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
