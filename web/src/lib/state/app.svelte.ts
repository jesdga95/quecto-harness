import { ApiError, api } from "../api";
import { dismissSticky, toast } from "./toast.svelte";
import type {
  Config,
  Effort,
  HarnessEvent,
  Model,
  PermissionDecision,
  Project,
  Session,
  TranscriptItem,
  TurnStatus,
  Usage,
} from "../api";
import { applyEvent, outcomeLabel, pushUser } from "../transcript";

// Every screen reads this one object. It holds what the server would hold plus
// the little the browser owns, like which panel is open and whether the last
// call to the harness got through.
export const app = $state({
  ready: false,
  mode: "router" as "router" | "single",
  config: null as Config | null,
  projects: [] as Project[],
  models: [] as Model[],
  sessions: {} as Record<string, Session[]>,
  projectPath: "",
  sessionId: "",
  items: [] as TranscriptItem[],
  usage: { prompt_tokens: 0, completion_tokens: 0, n_ctx: 131072 } as Usage,
  status: "idle" as TurnStatus,
  allowlist: [] as string[],
  sidebar: true,
  saved: false,
  error: "",
  connected: true,
  // Empty once a model server has answered. Drives the setup screen.
  detectError: "",
  detectKind: "",
  modelsError: "",
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
  app.status = "idle";
  if (app.config) app.config.model = t.session.model;
  unsubscribe = api.events(id, onEvent);
}

/** Prefers a model already in memory, since an unloaded one loads on first use. */
function defaultModel(): string {
  const loaded = app.models.find((m) => m.status === "loaded");
  return loaded?.id ?? app.models[0]?.id ?? "";
}

export async function newSession(): Promise<void> {
  if (!app.projectPath || !app.config) return;
  const empty = sessionsOf(app.projectPath).find((s) => s.title === "New session");
  if (empty) {
    await selectSession(empty.id);
    return;
  }
  const model = app.config.model || defaultModel();
  const session = await guard(() => api.createSession(app.projectPath, model));
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
  }
  await loadSessions();
  if (wasActive) {
    const next = sessionsOf(app.projectPath)[0];
    if (next) await selectSession(next.id);
    else await newSession();
  }
}

export async function send(text: string): Promise<void> {
  const message = text.trim();
  if (!app.sessionId || !message) return;
  pushUser(app.items, message);
  const sent = await guard(() => api.sendMessage(app.sessionId, message));
  if (sent === null) return;
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

/** Points the harness at a model server. Any answer counts except silence and
    a refused key, since neither leaves a harness that can run a turn. */
export async function connectServer(url: string, token: string): Promise<string> {
  app.probing = true;
  try {
    const config = await guard(() => api.updateConfig({ server: { url, token } }));
    if (!config) return app.error || "the harness did not accept that address";
    app.config = config;
    const state = await guard(() => api.getState());
    if (!state) return app.error || "the harness did not answer";
    app.mode = state.mode;
    app.detectError = state.detectError ?? "";
    app.detectKind = state.detectKind ?? "";
    app.modelsError = state.modelsError ?? "";
    app.models = state.models;
    app.projects = state.projects;

    // Nothing there, and a refused key, both leave a harness that cannot run a
    // turn. Anything else the server says is its own business.
    const kinds = [app.detectKind, state.modelsKind ?? ""];
    if (kinds.includes("unreachable") || kinds.includes("canceled")) {
      return app.detectError || "nothing answered at that address";
    }
    if (kinds.includes("auth")) {
      return token.trim() === ""
        ? "that server needs an API key"
        : "that server refused the API key";
    }
    // Any web server answers a GET. Only a model list proves this is llama.cpp.
    if (app.modelsError !== "" || app.models.length === 0) {
      return "that address answered, but it did not return a model list";
    }
    return "";
  } finally {
    app.probing = false;
  }
}

export async function saveConfig(patch: Partial<Config>): Promise<void> {
  const config = await guard(() => api.updateConfig(patch));
  if (!config) return;
  app.config = config;
  app.saved = true;
  setTimeout(() => (app.saved = false), 1600);
}

export function setModel(id: string): void {
  if (app.config) app.config.model = id;
  void guard(() => api.updateConfig({ model: id }));
}

export function setEffort(effort: Effort): void {
  if (app.config) app.config.effort = effort;
  void guard(() => api.updateConfig({ effort }));
}

export function setAuto(auto: boolean): void {
  if (app.config) app.config.auto = auto;
  void guard(() => api.updateConfig({ auto }));
}

function onEvent(event: HarnessEvent): void {
  if (event.type === "resync") {
    app.items = event.transcript.items;
    app.usage = event.transcript.usage;
    return;
  }

  if (event.type === "connection") {
    app.connected = event.connected;
    if (event.connected) {
      app.error = "";
      dismissSticky();
    } else {
      const message = event.message ?? "the harness is not reachable, reconnecting";
      app.error = message;
      toast(message, "warn", true);
    }
    return;
  }

  applyEvent(app.items, event);

  if (event.type === "usage") {
    app.usage = {
      prompt_tokens: event.prompt_tokens,
      completion_tokens: event.completion_tokens,
      n_ctx: event.n_ctx,
      tok_per_s: event.tok_per_s ?? app.usage.tok_per_s,
      cached_pct: event.cached_pct ?? app.usage.cached_pct,
    };
  }

  // A turn that ends in an error still ends, so the composer comes back rather
  // than sitting on a spinner until the next status event.
  if (event.type === "done" || event.type === "error") app.status = "idle";

  if (event.type === "status") {
    app.status = event.status;
    void refreshModels();
  }
}

let refreshedAt = 0;

// Status events land several times a turn and the model list changes at most
// once, when the model finishes loading.
async function refreshModels(): Promise<void> {
  const now = Date.now();
  if (now - refreshedAt < 5000) return;
  refreshedAt = now;
  const models = await guard(() => api.listModels());
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
