/** Every message shown to a person starts with a capital. */
export function sentence(text: string): string {
  const t = text.trim();
  return t === "" ? t : t[0]!.toUpperCase() + t.slice(1);
}

// Token counts read as thousands everywhere in the interface.
export function k(n: number): string {
  if (n < 1000) return String(n);
  const v = n / 1000;
  return (v < 10 ? v.toFixed(1) : Math.round(v).toString()) + "k";
}

export function pct(part: number, whole: number): number {
  if (whole <= 0) return 0;
  return Math.min(100, Math.round((part / whole) * 100));
}

export function level(ratio: number): "ok" | "warn" | "crit" {
  if (ratio >= 0.8) return "crit";
  if (ratio >= 0.6) return "warn";
  return "ok";
}

export function shortPath(path: string): string {
  return path.replace(/^\/Users\/[^/]+/, "~");
}

/** Age of a session, short enough for a sidebar row. */
export function since(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const min = Math.round(ms / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}min`;
  const hours = Math.round(min / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

export function effortLabel(effort: string): string {
  return effort[0]!.toUpperCase() + effort.slice(1);
}

/** What each level does to the request, in one line. */
export function effortHint(effort: string): string {
  switch (effort) {
    case "auto":
      return "Leave it to the model template";
    case "off":
      return "Ask the model not to think";
    case "low":
      return "A short budget";
    case "high":
      return "No budget cap";
    default:
      return "A working budget";
  }
}

/** First sentence of a thought, for the collapsed step row. */
export function firstSentence(text: string): string {
  const end = text.search(/[.!?]\s/);
  return end > 0 ? text.slice(0, end + 1) : text;
}

/** Plain description of a command, falling back to the command itself. */
export function describeCommand(command: string): string {
  if (/^ls\b/.test(command)) return "List the project root";
  if (/^go build/.test(command)) return "Build the module";
  if (/^go test/.test(command)) return "Run the package tests";
  if (/^git status/.test(command)) return "Check the working tree";
  return command;
}
