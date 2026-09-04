<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Brain from "phosphor-svelte/lib/Brain";
  import Check from "phosphor-svelte/lib/Check";
  import Popover from "./Popover.svelte";
  import type { Effort } from "../api";
  import { app, setEffort } from "../state/app.svelte";
  import { effortHint, effortLabel } from "../format";

  let { up = false }: { up?: boolean } = $props();
  let open = $state(false);

  const chosen = $derived(app.config?.effort ?? "medium");

  // Which levels sit between auto and off comes from the model's template. A
  // stored level this model does not have is still listed, so the chip and the
  // menu never disagree about what is selected.
  const levels = $derived.by((): Effort[] => {
    const list: Effort[] = ["auto", "off", ...app.effortLevels];
    return list.includes(chosen) ? list : [...list, chosen];
  });

  function pick(level: Effort) {
    setEffort(level);
    open = false;
  }
</script>

<div class="relative">
  <button class="chip" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    <Brain />
    Effort: {effortLabel(chosen)}
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
          {#if chosen === level}
            <Check size={14} class="text-accent" />
          {/if}
        </button>
      {/each}
    </Popover>
  {/if}
</div>
