<script lang="ts">
  import CheckCircle from "phosphor-svelte/lib/CheckCircleIcon";
  import Info from "phosphor-svelte/lib/InfoIcon";
  import Warning from "phosphor-svelte/lib/WarningIcon";
  import WifiSlash from "phosphor-svelte/lib/WifiSlashIcon";
  import X from "phosphor-svelte/lib/XIcon";
  import { dismiss, toasts } from "../state/toast.svelte";
</script>

<div class="pointer-events-none fixed right-5 bottom-5 z-50 flex w-[380px] flex-col gap-2">
  {#each toasts.items as item (item.id)}
    <div
      class="toast pointer-events-auto"
      class:is-warn={item.kind === "warn"}
      class:is-ok={item.kind === "ok"}
      class:is-info={item.kind === "info"}
    >
      {#if item.sticky}
        <WifiSlash />
      {:else if item.kind === "ok"}
        <CheckCircle />
      {:else if item.kind === "info"}
        <Info />
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
