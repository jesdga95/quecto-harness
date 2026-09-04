// Transient messages. A failure the user can act on becomes a toast; a state
// that persists, like a lost connection, stays until it clears.

export type ToastKind = "error" | "warn" | "info";

export type Toast = {
  id: number;
  kind: ToastKind;
  text: string;
  sticky: boolean;
};

let next = 1;

export const toasts = $state<{ items: Toast[] }>({ items: [] });

/** Server messages are plain lowercase sentences, so they get a capital here. */
function sentence(text: string): string {
  const t = text.trim();
  if (t === "") return t;
  return t[0]!.toUpperCase() + t.slice(1);
}

export function toast(text: string, kind: ToastKind = "error", sticky = false): number {
  const clean = sentence(text);
  const same = toasts.items.find((t) => t.text === clean && t.kind === kind);
  if (same) return same.id;

  const id = next++;
  toasts.items = [...toasts.items, { id, kind, text: clean, sticky }];
  if (!sticky) setTimeout(() => dismiss(id), 6000);
  return id;
}

export function dismiss(id: number): void {
  toasts.items = toasts.items.filter((t) => t.id !== id);
}

/** Drops every toast raised for a condition that has now cleared. */
export function dismissSticky(): void {
  toasts.items = toasts.items.filter((t) => !t.sticky);
}
