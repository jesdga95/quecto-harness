<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import type { Snippet } from "svelte";
  import { isTop, pushOverlay } from "../overlay";

  let {
    title,
    width = 540,
    height,
    onclose,
    children,
  }: {
    title: string;
    width?: number;
    /** A height of its own, for a dialog whose panes scroll. Without one the
        card is as tall as what is in it. */
    height?: number;
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
    class="card flex max-h-full w-full flex-col overflow-hidden"
    style="max-width:{width}px{height ? `;height:${height}px` : ''}"
    role="dialog"
    aria-modal="true"
    aria-label={title}
    tabindex="-1"
    onkeydown={trap}
  >
    <header class="flex shrink-0 items-center justify-between border-b border-hair px-5 py-3.5">
      <h2 class="text-[15px] font-medium">{title}</h2>
      <button class="icon-btn" aria-label="Close" onclick={onclose}><X size={16} /></button>
    </header>
    {@render children()}
  </div>
</div>
