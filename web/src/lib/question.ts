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

/** The call's questions, in order. A call asks one, or several together that
    the card answers as a set. */
export function asked(args: Record<string, unknown>): Asked[] {
  const raw = Array.isArray(args.questions) ? args.questions : [];
  return raw
    .filter((q): q is Record<string, unknown> => typeof q === "object" && q !== null)
    .map((q): Asked => {
      const opts = Array.isArray(q.options) ? q.options : [];
      const options = opts
        .filter((o): o is Record<string, unknown> => typeof o === "object" && o !== null)
        .map((o): Option => ({
          label: typeof o.label === "string" ? o.label : "",
          description: typeof o.description === "string" ? o.description : "",
        }))
        .filter((o) => o.label !== "");
      return {
        question: typeof q.question === "string" ? q.question : "",
        options,
        multi: q.multi_select === true,
      };
    })
    .filter((q) => q.question !== "");
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
