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
    children: Snippet;
  } = $props();

  let el = $state<HTMLDivElement | null>(null);
  let ready = $state(false);

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

<div
  bind:this={el}
  class="popover absolute z-40 {beside
    ? 'bottom-0 left-full ml-1.5'
    : `${up ? 'bottom-[42px]' : 'top-[38px]'} ${align === 'right' ? 'right-0' : 'left-0'}`}"
  style={fill ? "left:0;right:0" : `width:${width}px`}
>
  {@render children()}
</div>
