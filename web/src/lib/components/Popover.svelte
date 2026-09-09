<script lang="ts">
  import type { Snippet } from "svelte";
  import { pushOverlay } from "../overlay";

  let {
    onclose,
    align = "left",
    up = false,
    beside = false,
    width = 300,
    fill = false,
    min = 0,
    float = false,
    children,
  }: {
    onclose: () => void;
    align?: "left" | "right";
    up?: boolean;
    /** Opens to the right of the anchor, level with its bottom edge, which is
        what a row inside another popover needs. */
    beside?: boolean;
    width?: number;
    /** Spans the anchor rather than taking a width of its own, which is what a
        list under a field needs. */
    fill?: boolean;
    /** How narrow a filled list may get. A field beside its heading is only as
        wide as its value, and a list of sentences needs more room than that. */
    min?: number;
    /** Places itself against the viewport rather than against the anchor, so a
        pane that scrolls cannot clip it. It follows the anchor while anything
        scrolls, and turns upward when there is no room below. */
    float?: boolean;
    children: Snippet;
  } = $props();

  let el = $state<HTMLDivElement | null>(null);
  let ready = $state(false);
  let box = $state<{ top: number; left: number; width: number } | null>(null);

  // A floating popover is measured against the viewport, so it is placed after
  // it has a size and placed again whenever anything under it moves. A list
  // that grows or shrinks is watched too, since its height is what decides
  // whether it opens downward.
  $effect(() => {
    if (!float || !el) return;
    place();
    const again = () => place();
    const watch = new ResizeObserver(again);
    watch.observe(el);
    window.addEventListener("scroll", again, true);
    window.addEventListener("resize", again);
    return () => {
      watch.disconnect();
      window.removeEventListener("scroll", again, true);
      window.removeEventListener("resize", again);
    };
  });

  const GAP = 4;
  const EDGE = 8;

  function place() {
    const anchor = el?.parentElement;
    if (!el || !anchor) return;
    const r = anchor.getBoundingClientRect();
    const w = fill ? Math.max(r.width, min) : width;
    // The width decides where the rows wrap, so it is set before the height is
    // read. Measuring an unconstrained box answers with a height the popover
    // never has, and the answer is always too short to turn it upward.
    el.style.width = w + "px";
    const h = el.offsetHeight;
    // Below the anchor unless it was asked to open upward or there is no room.
    const above = up || (h > 0 && r.bottom + GAP + h > window.innerHeight - EDGE);
    const top = above ? r.top - h - GAP : r.bottom + GAP;
    const left = align === "right" ? r.right - w : r.left;
    box = {
      top,
      left: Math.max(EDGE, Math.min(left, window.innerWidth - w - EDGE)),
      width: w,
    };
  }

  // The click that opened this is still travelling, so outside clicks only
  // count from the next task on.
  $effect(() => {
    const timer = setTimeout(() => (ready = true), 0);
    return () => clearTimeout(timer);
  });

  // Escape goes through the shared stack, so a popover inside a dialog closes
  // itself and leaves the dialog open.
  $effect(() => pushOverlay(onclose));

  function away(event: MouseEvent) {
    if (!ready || !el) return;
    if (!el.parentElement?.contains(event.target as Node)) onclose();
  }
</script>

<svelte:window onclick={away} />

{#if float}
  <div
    bind:this={el}
    class="popover fixed z-40"
    style={box
      ? `top:${box.top}px;left:${box.left}px;width:${box.width}px`
      : "top:0;left:0;visibility:hidden"}
  >
    {@render children()}
  </div>
{:else}
  <div
    bind:this={el}
    class="popover absolute z-40 {beside
      ? 'bottom-0 left-full ml-1.5'
      : `${up ? 'bottom-[42px]' : 'top-[38px]'} ${align === 'right' ? 'right-0' : 'left-0'}`}"
    style={fill ? "left:0;right:0" : `width:${width}px`}
  >
    {@render children()}
  </div>
{/if}
