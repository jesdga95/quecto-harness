<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import Popover from "./Popover.svelte";
  import {
    maxToolCalls,
    nextToolCallLimit,
    preserveReasoning,
    setPreserveReasoning,
  } from "../state/app.svelte";

  let { up = false }: { up?: boolean } = $props();
  let open = $state(false);

  const preserve = $derived(preserveReasoning());
  const limit = $derived(maxToolCalls());
  const limitLabel = $derived(limit === 0 ? "∞" : String(limit));
</script>

<div class="relative">
  <button
    class="round"
    aria-label="Add"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <Plus />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} {up} width={292}>
      <div class="popover-head">Thinking</div>
      <label class="popover-item cursor-pointer">
        <span class="min-w-0 flex-1">
          <span class="block">Preserve reasoning</span>
          <span class="block text-[12px] text-muted">
            Keep thinking from earlier turns in the prompt
          </span>
        </span>
        <input
          class="switch"
          type="checkbox"
          checked={preserve}
          onchange={(e) => setPreserveReasoning(e.currentTarget.checked)}
        />
      </label>

      <div class="popover-head">Tools</div>
      <button
        class="popover-item"
        aria-label={limit === 0 ? "Tool calls per turn, no limit" : `Tool calls per turn, ${limit}`}
        onclick={() => nextToolCallLimit()}
      >
        <span class="min-w-0 flex-1">
          <span class="block">Tool calls per turn</span>
          <span class="block text-[12px] text-muted">
            After the limit the model answers with no tools
          </span>
        </span>
        <span class="pill pill-mute num w-[46px] shrink-0 justify-center">{limitLabel}</span>
      </button>
    </Popover>
  {/if}
</div>
