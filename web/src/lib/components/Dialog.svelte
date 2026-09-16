<script lang="ts">
  import type { Snippet } from "svelte";
  import { isTop, pushOverlay } from "../overlay";

  let {
    label,
    width = 540,
    height,
    row = false,
    onclose,
    children,
  }: {
    /** What the dialog is, for the screen reader. The visible title is drawn
        by the caller, since a split dialog has one per pane rather than one
        per dialog. */
    label: string;
    width?: number;
    /** A height of its own, for a dialog whose panes scroll. Without one the
        card is as tall as what is in it. */
    height?: number;
    /** The panes sit side by side rather than stacked. */
    row?: boolean;
    onclose: () => void;
    children: Snippet;
  } = $props();

  let card = $state<HTMLDivElement | null>(null);
  const escape = () => onclose();

  $effect(() => pushOverlay(escape));

  // Focus moves onto the dialog and back to whatever opened it, so closing
  // does not leave the page with nothing focused.
  $effect(() => {
    const before = document.activeElement as HTMLElement | null;
    card?.focus();
    return () => before?.focus();
  });

  const FOCUSABLE =
    'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

  // Tab wraps inside the dialog, unless something is open above it: a confirm
  // draws over this one from outside its markup and has to stay reachable.
  function trap(event: KeyboardEvent) {
    if (event.key !== "Tab" || !card || !isTop(escape)) return;
    const items = [...card.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
      (el) => el.offsetParent !== null,
    );
    const first = items[0];
    const last = items[items.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }
</script>

<div
  class="fixed inset-0 z-50 grid place-items-center bg-[rgb(15_23_42/0.42)] p-6"
  role="presentation"
  onclick={(e) => e.target === e.currentTarget && onclose()}
>
  <div
    bind:this={card}
    class="card flex max-h-full w-full overflow-hidden"
    class:flex-col={!row}
    class:flex-row={row}
    style="max-width:{width}px{height ? `;height:${height}px` : ''}"
    role="dialog"
    aria-modal="true"
    aria-label={label}
    tabindex="-1"
    onkeydown={trap}
  >
    {@render children()}
  </div>
</div>
