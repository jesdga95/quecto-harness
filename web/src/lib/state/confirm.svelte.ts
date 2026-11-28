// One dialog at a time, asked for imperatively so a caller reads as a question:
// `if (await confirm({ ... })) remove()`.

export type ConfirmRequest = {
  title: string;
  body?: string;
  files?: string[];
  confirmLabel?: string;
  destructive?: boolean;
  /** A second way to go ahead, drawn between Cancel and the confirm button. */
  alternateLabel?: string;
};

export type Choice = "confirm" | "alternate" | "cancel";

type Pending = ConfirmRequest & { resolve: (choice: Choice) => void };

export const dialog = $state<{ current: Pending | null }>({ current: null });

export function confirm(request: ConfirmRequest): Promise<boolean> {
  return choose(request).then((c) => c === "confirm");
}

/** Asks with up to two ways forward, and answers with the one taken. */
export function choose(request: ConfirmRequest): Promise<Choice> {
  // A second question replaces the first, which answers cancel rather than hanging.
  dialog.current?.resolve("cancel");
  return new Promise<Choice>((resolve) => {
    dialog.current = { ...request, resolve };
  });
}

export function answer(choice: boolean | Choice): void {
  const pending = dialog.current;
  dialog.current = null;
  pending?.resolve(choice === true ? "confirm" : choice === false ? "cancel" : choice);
}
