<script lang="ts">
  import ArrowLeft from "phosphor-svelte/lib/ArrowLeftIcon";
  import CaretLeft from "phosphor-svelte/lib/CaretLeftIcon";
  import X from "phosphor-svelte/lib/XIcon";
  import type { Snippet } from "svelte";
  import Dialog from "./Dialog.svelte";

  let {
    label,
    title,
    summary,
    onclose,
    onback,
    rail,
    detail,
  }: {
    /** What the dialog is, for the screen reader. */
    label: string;
    /** The name on the rail. */
    title: string;
    /** A line under the title when the list has the dialog to itself. */
    summary?: string;
    onclose: () => void;
    /** Set when the dialog was opened from another, which back returns to. */
    onback?: () => void;
    /** The list on the left, one row per record or category. */
    rail: Snippet;
    /** The record on the right. It keeps its own three parts: a shrink-0
        header, a min-h-0 flex-1 overflow-y-auto body and a shrink-0 footer. */
    detail: Snippet;
  } = $props();

  // A narrow window shows the list or the record, not both. Picking a row in
  // the list opens its record, and the bar over the record goes back.
  const NARROW = "(max-width: 720px)";
  let narrow = $state(window.matchMedia(NARROW).matches);
  let listing = $state(true);
  $effect(() => {
    const query = window.matchMedia(NARROW);
    const update = () => (narrow = query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  });

  function picked(event: MouseEvent) {
    if (narrow && (event.target as Element).closest(".rail-row, .rail-add")) listing = false;
  }
</script>

<Dialog {label} width={880} height={620} row {onclose}>
  {#if !narrow || listing}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="rail split-rail w-[216px] shrink-0" onclick={picked}>
      {#if onback}
        <button class="rail-head rail-back" title="Back to settings" onclick={onback}>
          <ArrowLeft size={14} />
          {title}
        </button>
      {:else}
        <p class="rail-head">{title}</p>
      {/if}
      {#if narrow && summary}
        <p class="rail-summary">{summary}</p>
      {/if}
      {@render rail()}
      {#if narrow}
        <button class="icon-btn rail-close" aria-label="Close" onclick={onclose}>
          <X size={16} />
        </button>
      {/if}
    </div>
  {/if}

  {#if !narrow || !listing}
    <div class="relative flex min-h-0 min-w-0 flex-1 flex-col">
      {#if narrow}
        <div class="detail-bar">
          <button class="detail-back" onclick={() => (listing = true)}>
            <CaretLeft size={14} />
            {title}
          </button>
          <button class="icon-btn" aria-label="Close" onclick={onclose}>
            <X size={16} />
          </button>
        </div>
      {:else}
        <button class="icon-btn absolute top-3.5 right-4 z-10" aria-label="Close" onclick={onclose}>
          <X size={16} />
        </button>
      {/if}
      {@render detail()}
    </div>
  {/if}
</Dialog>
