<script lang="ts">
  import Warning from "phosphor-svelte/lib/Warning";
  import WifiSlash from "phosphor-svelte/lib/WifiSlash";
  import X from "phosphor-svelte/lib/X";
  import { dismiss, toasts } from "../state/toast.svelte";
</script>

<div class="pointer-events-none fixed right-5 bottom-5 z-50 flex w-[380px] flex-col gap-2">
  {#each toasts.items as item (item.id)}
    <div class="toast pointer-events-auto" class:is-warn={item.kind === "warn"}>
      {#if item.sticky}
        <WifiSlash />
      {:else}
        <Warning />
      {/if}
      <p class="min-w-0 flex-1">{item.text}</p>
      <button class="toast-x" aria-label="Dismiss" onclick={() => dismiss(item.id)}>
        <X />
      </button>
    </div>
  {/each}
</div>
