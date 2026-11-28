<script lang="ts">
  import CheckCircle from "phosphor-svelte/lib/CheckCircleIcon";
  import Info from "phosphor-svelte/lib/InfoIcon";
  import Warning from "phosphor-svelte/lib/WarningIcon";
  import WifiSlash from "phosphor-svelte/lib/WifiSlashIcon";
  import X from "phosphor-svelte/lib/XIcon";
  import CopyButton from "./CopyButton.svelte";
  import { dismiss, toasts } from "../state/toast.svelte";
</script>

<div class="pointer-events-none fixed right-5 bottom-5 z-50 flex w-[380px] flex-col gap-2">
  {#each toasts.items as item (item.id)}
    <div
      class="toast pointer-events-auto"
      class:is-warn={item.kind === "warn" || item.kind === "connection"}
      class:is-ok={item.kind === "ok"}
      class:is-info={item.kind === "info"}
    >
      {#if item.kind === "connection"}
        <WifiSlash />
      {:else if item.kind === "ok"}
        <CheckCircle />
      {:else if item.kind === "info"}
        <Info />
      {:else}
        <Warning />
      {/if}
      <div class="min-w-0 flex-1 break-words">
        <p>{item.text}</p>
        {#if item.path}
          <div class="mt-1 flex items-center gap-1">
            <code class="min-w-0 select-all break-all">{item.path}</code>
            <CopyButton text={item.path} label="Copy folder path" />
          </div>
        {/if}
      </div>
      <button class="toast-x" aria-label="Dismiss" onclick={() => dismiss(item.id)}>
        <X />
      </button>
    </div>
  {/each}
</div>
