// Transient messages. A failure the user can act on becomes a toast; a state
// that persists, like a lost connection, stays until it clears.

// ok confirms something the user asked for went through; info is a notice
// that needs no action.
export type ToastKind = "error" | "warn" | "connection" | "info" | "ok";

export type Toast = {
  id: number;
  kind: ToastKind;
  text: string;
  sticky: boolean;
  path?: string;
};

let next = 1;

export const toasts = $state<{ items: Toast[] }>({ items: [] });

export function toast(text: string, kind: ToastKind = "error", sticky = false, path?: string): number {
  const clean = text.trim();
  const same = toasts.items.find((t) => t.text === clean && t.kind === kind && t.path === path);
  if (same) return same.id;

  const id = next++;
  toasts.items = [...toasts.items, { id, kind, text: clean, sticky, path }];
  if (!sticky) setTimeout(() => dismiss(id), 6000);
  return id;
}

export function dismiss(id: number): void {
  toasts.items = toasts.items.filter((t) => t.id !== id);
}

/** Drops every toast raised for a condition that has now cleared. */
export function dismissConnectionWarnings(): void {
  toasts.items = toasts.items.filter((t) => t.kind !== "connection");
}
