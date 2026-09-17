import type { Attached, HarnessEvent, PermissionDecision, TranscriptItem } from "./api/types";

let seq = 0;

/** An id minted here must never equal one built from a stored entry, since a
    repeated key aborts the whole keyed list and the transcript stops updating
    until the page is reloaded. Stored ids are the prefix and the entry sequence
    number with nothing between them, so the dash is what keeps the two apart. */
export function nextId(prefix = "i"): string {
  seq += 1;
  return `${prefix}-${seq}`;
}

/** Ids that arrive from the model or the server are not guaranteed unique, and
    a repeated key aborts the whole keyed list, so collisions are renamed. */
function freshId(items: TranscriptItem[], id: string): string {
  if (!items.some((i) => i.id === id)) return id;
  let n = 2;
  while (items.some((i) => i.id === `${id}#${n}`)) n++;
  return `${id}#${n}`;
}

/** Rough count for the thinking block, four characters to a token. */
function tokens(text: string): number {
  return Math.max(1, Math.ceil(text.length / 4));
}

/** Event types that put a new item below whatever text is being streamed. */
const APPENDING = new Set([
  "status_entry",
  "reasoning",
  "tool_call",
  "permission",
  "compacted",
  "error",
]);

// Folds one stream event into the transcript. The store and the mock server
// both use it, so a replayed session looks like a live one.
export function applyEvent(items: TranscriptItem[], event: HarnessEvent): void {
  const last = items[items.length - 1];

  // Anything landing under the text ends the block above it, so the caret comes
  // off there rather than only at the end of the turn. A later delta opens a new
  // block, which is what a message split around a tool call is.
  if (APPENDING.has(event.type) && last && last.kind === "assistant") {
    last.streaming = false;
  }

  switch (event.type) {
    case "delta": {
      if (last && last.kind === "assistant" && last.streaming) {
        last.text += event.text;
      } else {
        items.push({
          kind: "assistant",
          id: nextId("a"),
          text: event.text,
          streaming: true,
        });
      }
      break;
    }
    case "status_entry": {
      items.push({ kind: "status", id: nextId("s"), text: event.text });
      break;
    }
    case "reasoning": {
      if (last && last.kind === "reasoning") {
        last.text += event.text;
        last.tokens = tokens(last.text);
      } else {
        items.push({
          kind: "reasoning",
          id: nextId("r"),
          text: event.text,
          tokens: tokens(event.text),
          open: false,
        });
      }
      break;
    }
    case "tool_call": {
      items.push({
        kind: "tool",
        id: freshId(items, event.id || nextId("tool")),
        name: event.name,
        args: event.args,
        output: "",
        exitCode: null,
        truncated: false,
        running: true,
      });
      break;
    }
    // A subagent this session launched moved. The row that started it follows
    // along, since what the subagent is doing is a transcript of its own.
    case "child": {
      const row = [...items]
        .reverse()
        .find(
          (i) =>
            i.kind === "tool" &&
            (i.child?.session === event.session ||
              i.id === event.callId ||
              i.id.startsWith(`${event.callId}#`)),
        );
      if (row?.kind === "tool") {
        row.child = {
          session: event.session,
          agent: event.agent || row.child?.agent || "",
          status: event.status,
        };
      }
      break;
    }
    case "tool_args": {
      // A renamed collision keeps the original id as its prefix, and the card
      // being filled is always the most recent one, so the search runs backwards.
      const item = [...items]
        .reverse()
        .find(
          (i) =>
            i.kind === "tool" && (i.id === event.id || i.id.startsWith(`${event.id}#`)),
        );
      if (item && item.kind === "tool") {
        item.args = event.args;
        item.name = event.name;
      }
      break;
    }
    case "tool_start": {
      // A renamed collision keeps the original id as its prefix, and the card
      // being filled is always the most recent one, so the search runs backwards.
      const item = [...items]
        .reverse()
        .find(
          (i) =>
            i.kind === "tool" && (i.id === event.id || i.id.startsWith(`${event.id}#`)),
        );
      if (item && item.kind === "tool") {
        item.startedAt = Date.now();
        item.timeoutS = event.timeout_s;
      }
      break;
    }
    case "tool_output": {
      // A renamed collision keeps the original id as its prefix, and the card
      // being filled is always the most recent one, so the search runs backwards.
      const item = [...items]
        .reverse()
        .find(
          (i) =>
            i.kind === "tool" && (i.id === event.id || i.id.startsWith(`${event.id}#`)),
        );
      if (item && item.kind === "tool") {
        item.output = event.output;
        item.exitCode = event.exit_code;
        item.truncated = event.truncated;
        item.running = false;
        if (item.startedAt !== undefined) item.ranMs = Date.now() - item.startedAt;
        if (event.meta) item.meta = event.meta;
      }
      break;
    }
    case "question": {
      // The call's own row is the card, so the answer needs the id it is
      // posted to and nothing else. A renamed collision keeps the original id
      // as its prefix, and the row being filled is the most recent one.
      const item = [...items]
        .reverse()
        .find(
          (i) => i.kind === "tool" && (i.id === event.id || i.id.startsWith(`${event.id}#`)),
        );
      if (item && item.kind === "tool") item.askId = event.ask;
      break;
    }
    case "permission": {
      items.push({
        kind: "permission",
        id: freshId(items, event.id || nextId("perm")),
        tool: event.tool,
        command: event.command,
        prefix: event.prefix,
        outcome: null,
        agent: event.agent,
      });
      break;
    }
    case "permission_done": {
      // A card is answered by one tab and closed in all of them, and a stop
      // answers every card it finds, so the outcome arrives on the stream
      // rather than only from the click that caused it.
      const item = [...items]
        .reverse()
        .find(
          (i) =>
            i.kind === "permission" && (i.id === event.id || i.id.startsWith(`${event.id}#`)),
        );
      if (item && item.kind === "permission") item.outcome = event.outcome;
      break;
    }
    case "compacted": {
      items.push({
        kind: "compaction",
        id: nextId("c"),
        before: event.before,
        after: event.after,
        summary: event.summary,
      });
      break;
    }
    case "error": {
      items.push({ kind: "error", id: nextId("e"), message: event.message });
      break;
    }
    case "done": {
      if (last && last.kind === "assistant") {
        last.streaming = false;
        if (event.cost) last.cost = event.cost;
      }
      break;
    }
  }
}

/** What a card says once it has been answered. */
export function outcomeLabel(decision: PermissionDecision): string {
  if (decision.decision === "once") return "Allowed once";
  if (decision.decision === "always") return `Always allowed: ${decision.prefix}`;
  return decision.reason ? `Denied: ${decision.reason}` : "Denied";
}

/** seq is 0 until the harness answers for the message, which is what keeps a
    message the harness has not stored yet out of reach of an edit. */
export function pushUser(
  items: TranscriptItem[],
  text: string,
  files: Attached[] = [],
  seq = 0,
  edited = false,
  from = "",
): void {
  items.push({ kind: "user", id: nextId("u"), seq, text, files, edited, from });
}
