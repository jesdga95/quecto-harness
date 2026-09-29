/** The overlays open right now, outermost first. One key listener serves all
    of them and only the top one answers, so Escape inside a dialog closes the
    menu it was opened from and leaves the dialog standing. */
const stack: (() => void)[] = [];

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== "Escape") return;
  const top = stack[stack.length - 1];
  if (!top) return;
  event.stopPropagation();
  top();
}

/** Puts one overlay on top of the stack. The return value takes it off again,
    which is what an effect's cleanup wants. */
export function pushOverlay(onEscape: () => void): () => void {
  if (stack.length === 0) window.addEventListener("keydown", onKeydown, true);
  stack.push(onEscape);
  return () => {
    const at = stack.lastIndexOf(onEscape);
    if (at >= 0) stack.splice(at, 1);
    if (stack.length === 0) window.removeEventListener("keydown", onKeydown, true);
  };
}

/** Whether this overlay is the one a key would reach. A focus trap steps aside
    when it is not, since what is above it lives outside its own markup. */
export function isTop(onEscape: () => void): boolean {
  return stack[stack.length - 1] === onEscape;
}

/** Whether any overlay is open, so a key meant for the page can stand aside. */
export function overlayOpen(): boolean {
  return stack.length > 0;
}
