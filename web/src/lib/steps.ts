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

/** What grep and web_search were asked for, which is the text unless the
    call only named files. */
export function searchQuery(item: TranscriptItem): string {
  if (item.kind !== "tool") return "";
  if (item.name === "grep") return stringArg(item, "text") || stringArg(item, "name");
  if (item.name === "web_search") return stringArg(item, "query");
  return "";
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

/** The word a step row opens with. */
export function stepName(item: TranscriptItem): string {
  if (item.kind === "reasoning") return "Think";
  if (item.kind === "permission") return "Permission";
  if (item.kind === "compaction") return "Compacted";
  if (item.kind === "error") return "Error";
  if (item.kind !== "tool") return "Step";
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
  switch (item.name) {
    case "todo_write": {
      const todos = todosOf(item);
      const done = todos.filter((t) => t.status === "completed").length;
      return `${done} of ${todos.length} done`;
    }
    case "grep":
    case "web_search":
      return searchQuery(item);
    case "ask_user_question":
      return asked(item.args).question;
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
