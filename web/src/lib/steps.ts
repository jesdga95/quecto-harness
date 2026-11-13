// What a step row calls a transcript item, and the one line it says about it.
// A step row and the header of a turn still running both read them here, so
// the two cannot name the same call differently.

import type { Todo, TranscriptItem } from "./api";
import { describeCommand, firstLine, firstSentence, k, latestSentence } from "./format";
import { asked } from "./question";
import { sectionsOf } from "./plan";

/** A string argument of a tool call, or empty when it has none. */
export function stringArg(item: TranscriptItem, key: string): string {
  if (item.kind !== "tool") return "";
  const value = item.args[key];
  return typeof value === "string" ? value : "";
}

/** What grep and web_search were asked for. A grep names the text, then the
    file name pattern and folder it was narrowed to, so two calls that differ
    only there read differently. */
export function searchQuery(item: TranscriptItem): string {
  if (item.kind !== "tool") return "";
  if (item.name === "web_search") return stringArg(item, "query");
  if (item.name !== "grep") return "";
  const text = stringArg(item, "text");
  const name = stringArg(item, "name");
  const path = stringArg(item, "path").trim();
  let out = text && name ? `${text} in ${name}` : text || name;
  if (path) out += ` under ${path}`;
  return out;
}

/** A call that failed before it could run, or that the tool refused, carries
    the error as its output. Only bash has an exit code to say so with. */
export function stepFailed(item: TranscriptItem): boolean {
  return item.kind === "tool" && !item.running && item.meta?.error === true;
}

/** The plan a todo_write call wrote, from its own arguments rather than the
    result the model was handed back. */
export function todosOf(item: TranscriptItem): Todo[] {
  if (item.kind !== "tool" || item.name !== "todo_write") return [];
  const raw = item.args.todos;
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((t): t is Record<string, unknown> => typeof t === "object" && t !== null)
    .map<Todo>((t) => ({
      content: typeof t.content === "string" ? t.content : "",
      status: t.status === "in_progress" || t.status === "completed" ? t.status : "pending",
    }))
    .filter((t) => t.content !== "");
}

/** The server and tool of an MCP call. The row's meta names both once the
    call has answered; before that they are read from the name it was sent
    under, which is mcp__server__tool. Null for any other call. */
export function mcpParts(item: TranscriptItem): { server: string; tool: string } | null {
  if (item.kind !== "tool") return null;
  return mcpOf(item.name, item.meta);
}

export function mcpOf(name: string, meta?: Record<string, unknown> | null): { server: string; tool: string } | null {
  if (!name.startsWith("mcp__")) return null;
  if (typeof meta?.server === "string" && typeof meta?.tool === "string") {
    return { server: meta.server, tool: meta.tool };
  }
  const rest = name.slice(5);
  const at = rest.indexOf("__");
  return at < 0 ? { server: rest, tool: "" } : { server: rest.slice(0, at), tool: rest.slice(at + 2) };
}

/** The word a step row opens with. */
export function stepName(item: TranscriptItem): string {
  if (item.kind === "reasoning") return "Think";
  if (item.kind === "permission") return "Permission";
  if (item.kind === "compaction") return "Compacted";
  if (item.kind === "error") return "Error";
  if (item.kind !== "tool") return "Step";
  const mcp = mcpParts(item);
  if (mcp) return mcp.server;
  switch (item.name) {
    case "read":
      return "Read";
    case "read_image":
      return "View";
    case "grep":
      return "Find";
    case "web_search":
      return "Search";
    case "write":
      return "Write";
    case "edit":
      return "Edit";
    case "todo_write":
      return "Plan";
    case "ask_user_question":
      return "Question";
    case "exit_plan_mode":
      return "Plan";
    case "write_plan":
      return "Draft";
    case "subagent":
      return "Agent";
    case "send_message":
      return "Message";
    default:
      return "Shell";
  }
}

/** The line after the name. A thought still being written follows its newest
    paragraph; a finished one shows how it began. planTitle names the plan an
    exit_plan_mode call puts up, which is not in the call itself. */
export function stepDesc(item: TranscriptItem, live: boolean, planTitle: string): string {
  if (item.kind === "reasoning") return live ? latestSentence(item.text) : firstSentence(item.text);
  if (item.kind === "permission") return item.command;
  if (item.kind === "compaction") return `${k(item.before)} to ${k(item.after)}`;
  if (item.kind === "error") return firstSentence(item.message);
  if (item.kind !== "tool") return "";
  if (stepFailed(item)) return firstLine(item.output);
  const mcp = mcpParts(item);
  if (mcp) return mcp.tool;
  switch (item.name) {
    case "todo_write": {
      const todos = todosOf(item);
      const done = todos.filter((t) => t.status === "completed").length;
      return `${done} of ${todos.length} done`;
    }
    case "grep":
    case "web_search":
      return searchQuery(item);
    case "ask_user_question": {
      const qs = asked(item.args);
      return qs.length > 1 ? `${qs.length} questions` : (qs[0]?.question ?? "");
    }
    case "exit_plan_mode":
      return planTitle;
    case "write_plan":
      return sectionsOf(item.args).join(", ");
    case "subagent": {
      const to = stringArg(item, "agent");
      const title = stringArg(item, "title");
      return to === "" ? title : `${to}: ${title}`;
    }
    case "send_message":
      return stringArg(item, "text");
    default:
      return stringArg(item, "path") || describeCommand(stringArg(item, "command"));
  }
}
