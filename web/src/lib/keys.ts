/* Sidebar shortcuts. A browser keeps Cmd+N for a new window, so New session
   takes Option+N there and Cmd+N only in the desktop window, which owns it. */

const mac = /Mac|iPhone|iPad/.test(navigator.platform);
const desktop = () => document.documentElement.classList.contains("desktop-mac");

/** The labels the sidebar shows beside its controls. */
export function keyLabels(): { newSession: string; search: string } {
  return {
    newSession: desktop() ? "⌘N" : mac ? "⌥N" : "Alt+N",
    search: mac ? "⌘K" : "Ctrl+K",
  };
}

export function isNewSessionKey(e: KeyboardEvent): boolean {
  if (e.code !== "KeyN" || e.shiftKey || e.repeat) return false;
  if (desktop()) return e.metaKey && !e.altKey && !e.ctrlKey;
  return e.altKey && !e.metaKey && !e.ctrlKey;
}

export function isSearchKey(e: KeyboardEvent): boolean {
  if (e.key.toLowerCase() !== "k" || e.shiftKey || e.altKey || e.repeat) return false;
  return mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
}

/** Asks the sidebar to focus its search field. */
export const SEARCH_EVENT = "quecto:search";

/** Asks the composer to take the caret, as a new session starts. */
export const COMPOSE_EVENT = "quecto:compose";
