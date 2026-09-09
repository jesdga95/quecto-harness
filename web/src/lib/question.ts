/** A question as the card and the answered row read it, out of the arguments
    of the call that asked. The model writes them, so every field is checked
    rather than trusted. */
export interface Option {
  label: string;
  description: string;
}

export interface Asked {
  question: string;
  options: Option[];
  multi: boolean;
}

export function asked(args: Record<string, unknown>): Asked {
  const raw = Array.isArray(args.options) ? args.options : [];
  const options = raw
    .filter((o): o is Record<string, unknown> => typeof o === "object" && o !== null)
    .map<Option>((o) => ({
      label: typeof o.label === "string" ? o.label : "",
      description: typeof o.description === "string" ? o.description : "",
    }))
    .filter((o) => o.label !== "");
  return {
    question: typeof args.question === "string" ? args.question : "",
    options,
    multi: args.multi_select === true,
  };
}

/** The answer as the harness hands it to the model: the labels that were
    chosen, or the words typed instead. */
export function answerText(options: Option[], chosen: number[], typed: string): string {
  const labels = chosen.map((i) => options[i]?.label ?? "").filter((l) => l !== "");
  const text = typed.trim();
  if (labels.length > 0 && text !== "") return `${labels.join(", ")}: ${text}`;
  if (labels.length > 0) return labels.join(", ");
  return text || "no answer";
}
