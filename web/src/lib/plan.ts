/** A plan as the card and the answered row read it, out of the arguments of
    the submit_plan call. Every field is optional on the wire. */
export interface Planned {
  title: string;
  goal: string;
  files: string[];
  steps: string[];
  check: string;
  open: string[];
}

function strings(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === "string" && v.trim() !== "");
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

export function planned(args: Record<string, unknown>): Planned {
  return {
    title: text(args.title),
    goal: text(args.goal),
    files: strings(args.files),
    steps: strings(args.steps),
    check: text(args.check),
    open: strings(args.open),
  };
}

/** The plan as a markdown document, which is how the panel shows it. The
    title is left out: the panel's header already carries it. */
export function planMarkdown(p: Planned): string {
  const out: string[] = [];
  if (p.goal) out.push(p.goal);
  if (p.files.length > 0) out.push("## Files", p.files.map((f) => `- ${f}`).join("\n"));
  if (p.steps.length > 0) out.push("## Steps", p.steps.map((s, i) => `${i + 1}. ${s}`).join("\n"));
  if (p.check) out.push("## Check", p.check);
  if (p.open.length > 0) out.push("## Open", p.open.map((q) => `- ${q}`).join("\n"));
  return out.join("\n\n") + "\n";
}
