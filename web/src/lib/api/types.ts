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
  permissions: PermMode;
  compact_at: number;
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
    }
  | { type: "status"; status: TurnStatus }
  | { type: "compacted"; before: number; after: number }
  | { type: "error"; message: string }
  | { type: "done" }
  /** The stream fell too far behind to replay, so it carries a fresh snapshot. */
  | { type: "resync"; transcript: Transcript }
  | { type: "connection"; connected: boolean; message?: string };

export type TranscriptItem =
  | { kind: "user"; id: string; text: string }
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
  | { kind: "compaction"; id: string; before: number; after: number }
  | { kind: "error"; id: string; message: string };

export interface Usage {
  prompt_tokens: number;
  completion_tokens: number;
  n_ctx: number;
  tok_per_s?: number;
  cached_pct?: number;
}

export interface Transcript {
  session: Session;
  items: TranscriptItem[];
  usage: Usage;
}

export type EventHandler = (event: HarnessEvent) => void;

export interface HarnessApi {
  getState(): Promise<State>;
  listModels(refresh?: boolean): Promise<Model[]>;
  /** Opens the host's folder dialog. Resolves to null when it is dismissed. */
  pickDirectory(): Promise<string | null>;
  addProject(path: string): Promise<Project[]>;
  removeProject(path: string): Promise<Project[]>;
  listSessions(project: string): Promise<Session[]>;
  createSession(project: string, model: string): Promise<Session>;
  getSession(id: string): Promise<Transcript>;
  deleteSession(id: string): Promise<void>;
  renameSession(id: string, title: string): Promise<void>;
  sendMessage(id: string, text: string): Promise<void>;
  stop(id: string): Promise<void>;
  compact(id: string): Promise<void>;
  decide(permissionId: string, decision: PermissionDecision): Promise<void>;
  updateConfig(patch: Partial<Config>): Promise<Config>;
  listAllowlist(project: string): Promise<string[]>;
  removeAllowed(project: string, prefix: string): Promise<string[]>;
  /** Subscribes to the session stream. The returned function closes it. */
  events(session: string, onEvent: EventHandler): () => void;
}
