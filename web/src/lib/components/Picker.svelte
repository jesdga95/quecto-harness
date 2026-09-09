<script lang="ts">
  import type { Snippet } from "svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Popover from "./Popover.svelte";

  let {
    open,
    label,
    inline = false,
    caretOpens = false,
    onopen,
    onclose,
    trigger,
    children,
  }: {
    open: boolean;
    /** What the caret is called, for the field it is not a control of. */
    label: string;
    /** The field sits beside its heading rather than under it, so it is only
        as wide as its value. Its list is then wider than the field and hangs
        from its right edge, since a sentence per row wants more room than the
        field has and the edge of the card is just past it. */
    inline?: boolean;
    /** Whether the caret is what opens the list. A trigger that is itself a
        button opens it on its own, so the caret is then decoration and lets
        the click through rather than being a second control saying the same
        thing. */
    caretOpens?: boolean;
    onopen: () => void;
    onclose: () => void;
    /** The field itself: a button where the value is only chosen, an input
        where it can also be typed. */
    trigger: Snippet;
    children: Snippet;
  } = $props();

  // What an inline field's list widens to. Enough for a row's sentence on two
  // lines, and still inside the card the field sits in.
  const LIST = 300;
</script>

<div class="relative">
  {@render trigger()}
  {#if caretOpens}
    <button
      class="field-caret"
      aria-label={label}
      aria-expanded={open}
      onclick={() => (open ? onclose() : onopen())}
    >
      <CaretDown size={13} />
    </button>
  {:else}
    <span class="field-caret pointer-events-none" aria-hidden="true">
      <CaretDown size={13} />
    </span>
  {/if}

  {#if open}
    <Popover {onclose} fill float min={inline ? LIST : 0} align={inline ? "right" : "left"}>
      {@render children()}
    </Popover>
  {/if}
</div>
