/** A plan in its sections, as the draft and the card read it. Every field is
    optional on the wire. */
export interface Plan {
  title: string;
  goal: string;
  code: string[];
  tests: string[];
  validation: string[];
  migration: string;
  risks: string[];
  steps: string[];
  open: string[];
}

function strings(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === "string" && v.trim() !== "");
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

export function toPlan(wire: unknown): Plan | null {
  if (typeof wire !== "object" || wire === null) return null;
  const w = wire as Record<string, unknown>;
  return {
    title: text(w.title),
    goal: text(w.goal),
    code: strings(w.code),
    tests: strings(w.tests),
    validation: strings(w.validation),
    migration: text(w.migration),
    risks: strings(w.risks),
    steps: strings(w.steps),
    open: strings(w.open),
  };
}

/** The sections a write_plan call sent, in the order the plan holds them. */
export function sectionsOf(args: Record<string, unknown>): string[] {
  const order = ["title", "goal", "code", "tests", "validation", "migration", "risks", "steps", "open"];
  return order.filter((k) => k in args);
}

/** The plan as a markdown document, which is how the panel shows it. The
    title is left out: the panel's header already carries it. A section not
    written yet is left out too. */
export function planMarkdown(p: Plan): string {
  const out: string[] = [];
  const list = (items: string[]) => items.map((i) => `- ${i}`).join("\n");
  if (p.goal) out.push("## Goal", p.goal);
  if (p.code.length > 0) out.push("## Code", list(p.code));
  if (p.tests.length > 0) out.push("## Tests", list(p.tests));
  if (p.validation.length > 0) out.push("## Validation", list(p.validation));
  if (p.migration) out.push("## Migration", p.migration);
  if (p.risks.length > 0) out.push("## Risks", list(p.risks));
  if (p.steps.length > 0) out.push("## Steps", p.steps.map((s, i) => `${i + 1}. ${s}`).join("\n"));
  if (p.open.length > 0) out.push("## Open", list(p.open));
  return out.join("\n\n") + "\n";
}
