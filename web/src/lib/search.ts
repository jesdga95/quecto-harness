import { safeUrl } from "./markdown";

/** One result, as an opened search row draws it. */
export interface SearchHit {
  title: string;
  /** The address, or null when it did not pass the gate every link goes
      through. A hit refused there is named and not linked. */
  url: string | null;
  host: string;
  /** The day the page carries, or empty when it carried none. */
  published: string;
  /** What the page contributed, as the separate passages it came back as. */
  body: string[];
}

// What a cut leaves behind, and the count of results that arrived with
// nothing to identify them. Both end a body rather than joining one.
const cut = /^\.\.\. \d+ bytes omitted \.\.\.$/;
const droppedNote = /^\d+ further results? had no address and (was|were) dropped\.$/;

/** Hits reads the results of one search off a tool call. Titles and addresses
    come from the meta the call recorded; the passages come from the output,
    which holds them once already. */
export function hits(meta: Record<string, unknown> | null | undefined, output: string): SearchHit[] {
  const raw = meta?.hits;
  if (!Array.isArray(raw)) return [];

  const found = raw
    .filter((h): h is Record<string, unknown> => typeof h === "object" && h !== null)
    .map((h) => ({
      title: typeof h.title === "string" ? h.title : "",
      address: typeof h.url === "string" ? h.url : "",
      published: typeof h.published === "string" ? h.published : "",
    }))
    .filter((h) => h.address !== "" || h.title !== "");

  const passages = bodies(
    output,
    found.map((h) => h.address),
  );

  return found.map((h, i) => ({
    title: h.title,
    url: safeUrl(h.address),
    host: host(h.address),
    published: day(h.published),
    body: passages[i] ?? [],
  }));
}

/** Host is the site an address belongs to, without the prefix that every
    second one carries. An address too malformed to parse is its own host. */
export function host(address: string): string {
  if (address === "") return "";
  try {
    return new URL(address).host.replace(/^www\./, "");
  } catch {
    return address;
  }
}

/** Day is the date out of a timestamp, since a row has room for the day and
    no use for the hour. */
function day(published: string): string {
  return published.length > 10 ? published.slice(0, 10) : published;
}

// Each result's address is written on a line of its own, above the passages
// that came with it, so the lines between one address and the next result's
// title are that result's body. An address the output does not carry, because
// the cut landed on it, leaves its body empty rather than borrowing another's.
function bodies(output: string, addresses: string[]): string[][] {
  const lines = output.split("\n");
  const at = addresses.map(() => -1);
  let from = 0;
  addresses.forEach((address, i) => {
    if (address === "") return;
    const line = lines.indexOf(address, from);
    if (line === -1) return;
    at[i] = line;
    from = line + 1;
  });

  return addresses.map((_, i) => {
    if (at[i] === -1) return [];
    let end = lines.length;
    for (let next = i + 1; next < addresses.length; next += 1) {
      if (at[next] !== -1) {
        // The title sits above the address, and a blank line above that.
        end = at[next] - 1;
        break;
      }
    }
    return passages(lines.slice(at[i] + 1, end));
  });
}

// One result's lines as the passages they are. A blank line and a line of
// nothing but an ellipsis both separate two passages, and the date line the
// address is followed by belongs to neither.
function passages(lines: string[]): string[] {
  const start = lines[0]?.startsWith("published ") ? 1 : 0;
  const out: string[] = [];
  let held: string[] = [];

  const flush = () => {
    const text = held.join("\n").trim();
    if (text !== "") out.push(text);
    held = [];
  };

  for (const line of lines.slice(start)) {
    const text = line.trim();
    if (cut.test(text) || droppedNote.test(text)) break;
    if (text === "" || text === "...") {
      flush();
      continue;
    }
    held.push(line);
  }
  flush();
  return out;
}
