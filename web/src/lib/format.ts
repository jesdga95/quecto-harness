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

/** How long a turn took, at the resolution a reader cares about. */
export function duration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const secs = ms / 1000;
  if (secs < 60) return `${secs < 10 ? secs.toFixed(1) : Math.round(secs)}s`;
  return `${Math.floor(secs / 60)}m ${Math.round(secs % 60)}s`;
}

export function level(ratio: number): "ok" | "warn" | "crit" {
  if (ratio >= 0.8) return "crit";
  if (ratio >= 0.6) return "warn";
  return "ok";
}

/** The file name at the end of a path. */
export function fileName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

/** The extension, as the label on an attachment tile. */
export function fileKind(path: string): string {
  const name = fileName(path);
  const dot = name.lastIndexOf(".");
  const ext = dot > 0 ? name.slice(dot + 1) : "";
  return ext ? ext.slice(0, 4).toUpperCase() : "FILE";
}

/** What a file weighs, for an attachment tile. */
export function bytes(n: number): string {
  return n < 1024 ? `${n} b` : `${Math.round(n / 1024)} kb`;
}

export function shortPath(path: string): string {
  return path.replace(/^\/Users\/[^/]+/, "~");
}

/** Age of a session, short enough for a sidebar row. */
export function since(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const min = Math.round(ms / 60000);
  if (min < 1) return "Now";
  if (min < 60) return `${min}min`;
  const hours = Math.round(min / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

/** The written name of a thinking level. */
export function effortLabel(effort: string): string {
  switch (effort) {
    case "auto":
      return "Auto";
    case "off":
      return "Off";
    case "low":
      return "Low";
    case "high":
      return "High";
    case "xhigh":
      return "Extra high";
    default:
      return "Medium";
  }
}

/** How much thinking each level asks for, in one line. */
export function effortHint(effort: string): string {
  switch (effort) {
    case "auto":
      return "Leave it to the model template";
    case "off":
      return "Ask the model not to think";
    case "low":
      return "Short thinking";
    case "high":
      return "Long thinking";
    case "xhigh":
      return "As much as the model wants";
    default:
      return "A working amount";
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

/** What to say about an address that was asked what it holds. Empty when it
    answered with a model list, which is the only answer that is not news. */
export function probeMessage(probe: {
  models: { length: number };
  detectKind: string;
  detectError: string;
  modelsKind: string;
  modelsError: string;
}, hasKey: boolean): string {
  const kinds = [probe.detectKind, probe.modelsKind];
  if (kinds.includes("unreachable") || kinds.includes("canceled")) {
    return "Nothing answered at that address.";
  }
  if (kinds.includes("auth")) {
    return hasKey ? "That server refused the API key." : "That server needs an API key.";
  }
  if (probe.models.length > 0) return "";
  // A server that failed said why, and its own words name the cause.
  if (probe.modelsError !== "") return probe.modelsError;
  if (probe.detectError !== "") return probe.detectError;
  return "That address answered with no model list. Type the model id.";
}

export function protocolLabel(protocol: string): string {
  switch (protocol) {
    case "chat":
      return "Chat";
    case "responses":
      return "Responses";
    default:
      return "llama.cpp";
  }
}

/** What choosing one protocol over another means, since an address will often
    answer more than one and nothing can ask it which to use. */
export function protocolHint(protocol: string): string {
  switch (protocol) {
    case "chat":
      return "The chat completions endpoint every OpenAI-compatible server has.";
    case "responses":
      return "OpenAI's newer endpoint. Its recent models take tools and thinking together only here.";
    default:
      return "A llama.cpp server, which is also the only one that takes top k and repeat penalty.";
  }
}

/** The written name of a search provider. */
export function searchProviderLabel(name: string): string {
  switch (name) {
    case "exa":
      return "Exa";
    default:
      return name;
  }
}

/** What a search provider is, for the picker that names one. */
export function searchProviderHint(name: string): string {
  switch (name) {
    case "exa":
      return "Searches the open web. Its address and key are set in the card below.";
    default:
      return "Searches the open web.";
  }
}
