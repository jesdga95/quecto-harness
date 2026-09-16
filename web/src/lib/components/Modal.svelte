<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import type { Snippet } from "svelte";
  import Dialog from "./Dialog.svelte";

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
</script>

<Dialog label={title} {width} {height} {onclose}>
  <header class="flex shrink-0 items-center justify-between border-b border-hair px-5 py-3.5">
    <h2 class="text-[15px] font-medium">{title}</h2>
    <button class="icon-btn" aria-label="Close" onclick={onclose}><X size={16} /></button>
  </header>
  {@render children()}
</Dialog>
