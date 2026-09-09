<script lang="ts">
  import type { Snippet } from "svelte";
  import Check from "phosphor-svelte/lib/Check";

  let {
    label,
    hint = "",
    checked = false,
    onpick,
    meta,
  }: {
    label: string;
    /** The sentence saying what choosing this means. It sits on the row rather
        than under the field, so every answer is read while choosing. */
    hint?: string;
    checked?: boolean;
    onpick: () => void;
    /** What is known about the row, drawn at its right edge. For a list long
        enough to be searched this is where the detail goes, since a second
        line on every row would halve what fits on screen. */
    meta?: Snippet;
  } = $props();
</script>

<button class="popover-item" class:items-start={hint !== ""} onclick={onpick}>
  <span class="min-w-0 flex-1">
    <span class="block truncate text-left">{label}</span>
    {#if hint !== ""}
      <span class="block text-left text-[12px] leading-snug text-muted">{hint}</span>
    {/if}
  </span>
  {@render meta?.()}
  {#if checked}
    <Check size={14} class="shrink-0 text-accent {hint !== '' ? 'mt-0.5' : ''}" />
  {/if}
</button>
