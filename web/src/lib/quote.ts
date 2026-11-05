// A quoted passage travels in the message as a markdown blockquote ahead of
// what was typed, so the model reads it as an ordinary quote and the session
// log needs no field of its own. The bubble reads the same blocks back.

/** The message as sent: each quote as its own blockquote, then the text. */
export function withQuotes(quotes: string[], text: string): string {
  const blocks = quotes.map((q) =>
    q
      .split("\n")
      .map((line) => (line.trim() ? `> ${line}` : ">"))
      .join("\n"),
  );
  return [...blocks, text.trim()].filter(Boolean).join("\n\n");
}

/** The blockquotes a message opens with, and what follows them. */
export function splitQuotes(text: string): { quotes: string[]; body: string } {
  const lines = text.split("\n");
  const quotes: string[] = [];
  let current: string[] | null = null;
  let i = 0;
  for (; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.startsWith(">")) {
      current ??= [];
      current.push(line.replace(/^> ?/, ""));
      continue;
    }
    if (line.trim() === "" && (current || quotes.length > 0)) {
      if (current) quotes.push(current.join("\n").trim());
      current = null;
      continue;
    }
    break;
  }
  if (current) quotes.push(current.join("\n").trim());
  return { quotes: quotes.filter(Boolean), body: lines.slice(i).join("\n").trim() };
}
