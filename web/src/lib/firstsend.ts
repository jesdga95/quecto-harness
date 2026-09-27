/* The first message's composer drops from where the empty screen had it to
   where the session keeps it. The empty screen records its box as the message
   goes, and the session's composer plays back from there as it mounts. */

let from: DOMRect | null = null;
let timer = 0;

/** Called by the empty screen as its first message is sent. */
export function leave(composer: HTMLElement): void {
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  from = composer.getBoundingClientRect();
  // A send that never opens a session has nothing to land.
  clearTimeout(timer);
  timer = window.setTimeout(() => (from = null), 4000);
}

/** Action for the session's composer. */
export function arriveComposer(node: HTMLElement): void {
  const start = from;
  if (!start) return;
  from = null;
  clearTimeout(timer);
  const to = node.getBoundingClientRect();
  node.animate(
    [
      { transform: `translate(${start.left - to.left}px, ${start.top - to.top}px)` },
      { transform: "none" },
    ],
    { duration: 980, easing: "cubic-bezier(.3,1.22,.45,1)" },
  );
}
