<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Brain from "phosphor-svelte/lib/Brain";
  import Check from "phosphor-svelte/lib/Check";
  import Popover from "./Popover.svelte";
  import type { Effort } from "../api";
  import { effortHint, effortLabel } from "../format";

  let {
    value,
    onpick,
    up = false,
  }: {
    value: Effort;
    onpick: (value: Effort) => void;
    up?: boolean;
  } = $props();
  let open = $state(false);
  const levels: Effort[] = ["auto", "off", "low", "medium", "high"];

  function pick(level: Effort) {
    onpick(level);
    open = false;
  }
</script>

<div class="relative">
  <button class="chip" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    <Brain />
    Effort: {effortLabel(value)}
    <CaretDown />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} {up} width={220}>
      {#each levels as level (level)}
        <button class="popover-item" onclick={() => pick(level)}>
          <span class="min-w-0 flex-1">
            <span class="block">{effortLabel(level)}</span>
            <span class="block text-[12px] text-muted">{effortHint(level)}</span>
          </span>
          {#if value === level}
            <Check size={14} class="text-accent" />
          {/if}
        </button>
      {/each}
    </Popover>
  {/if}
</div>
