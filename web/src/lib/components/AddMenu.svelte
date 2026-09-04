<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import Popover from "./Popover.svelte";
  import { preserveReasoning, setPreserveReasoning } from "../state/app.svelte";

  let { up = false }: { up?: boolean } = $props();
  let open = $state(false);

  const preserve = $derived(preserveReasoning());
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
    <Popover onclose={() => (open = false)} {up} width={264}>
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
    </Popover>
  {/if}
</div>
