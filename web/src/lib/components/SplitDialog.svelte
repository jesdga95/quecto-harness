<script lang="ts">
  import X from "phosphor-svelte/lib/X";
  import type { Snippet } from "svelte";
  import Dialog from "./Dialog.svelte";

  let {
    label,
    title,
    onclose,
    rail,
    detail,
  }: {
    /** What the dialog is, for the screen reader. */
    label: string;
    /** The name on the rail. */
    title: string;
    onclose: () => void;
    /** The list on the left, one row per record or category. */
    rail: Snippet;
    /** The record on the right. It keeps its own three parts: a shrink-0
        header, a min-h-0 flex-1 overflow-y-auto body and a shrink-0 footer. */
    detail: Snippet;
  } = $props();
</script>

<Dialog {label} width={880} height={620} row {onclose}>
  <div class="rail w-[216px] shrink-0">
    <p class="rail-head">{title}</p>
    {@render rail()}
  </div>

  <div class="relative flex min-w-0 flex-1 flex-col">
    <button class="icon-btn absolute top-3.5 right-4 z-10" aria-label="Close" onclick={onclose}>
      <X size={16} />
    </button>
    {@render detail()}
  </div>
</Dialog>
