// Helpers for markup the model is still writing, shared by drawings and pages.

/** Index just past the last complete tag. A quote counts only inside a tag, so
    an apostrophe in a label does not swallow the rest of the markup. */
export function lastTagEnd(source: string): number {
  let end = 0;
  let quote = "";
  let tag = false;
  for (let i = 0; i < source.length; i += 1) {
    const char = source[i]!;
    if (quote) {
      if (char === quote) quote = "";
    } else if (tag && (char === '"' || char === "'")) {
      quote = char;
    } else if (char === "<") {
      tag = true;
    } else if (char === ">" && tag) {
      tag = false;
      end = i + 1;
    }
  }
  return end;
}

/** The source without a tag still being written at its end. Text after the
    last complete tag is kept, so a paragraph appears as it is typed. */
export function withoutOpenTag(source: string): string {
  const end = lastTagEnd(source);
  const open = source.indexOf("<", end);
  return open < 0 ? source : source.slice(0, open);
}

const ENTITIES: Record<string, string> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
  apos: "'",
  "#39": "'",
};

/** The text of the first title element, or "" when there is none yet. */
export function titleOf(source: string): string {
  const match = /<title[^>]*>([^<]*)<\/title\s*>/i.exec(source);
  if (!match) return "";
  return match[1]!
    .replace(/&(amp|lt|gt|quot|apos|#39);/g, (_, name: string) => ENTITIES[name] ?? "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 80);
}
