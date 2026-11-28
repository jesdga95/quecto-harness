<script lang="ts">
  import { onMount } from "svelte";
  import { api, type TrashFailure } from "../api";
  import CopyButton from "./CopyButton.svelte";
  import { heading, notice } from "../storage";

  let failures = $state<TrashFailure[] | null>(null);
  let error = $state("");

  onMount(() => {
    let closed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const latest = await api.trashFailures();
        if (closed) return;
        failures = latest;
        error = "";
      } catch (err) {
        if (closed) return;
        error = err instanceof Error ? err.message : "Could not read storage status";
      }
      timer = setTimeout(refresh, 15_000);
    }
    void refresh();
    return () => { closed = true; clearTimeout(timer); };
  });
</script>

<div class="field-card">
  <h4 class="field-head">Storage</h4>
  {#if error}
    <p class="text-[12px] text-muted">{error}</p>
  {/if}
  {#if failures?.length}
    <p class="text-[12px] text-muted">{heading(failures)}</p>
    {#each failures as failure, i (i)}
      {@const lines = notice(failure)}
      <div>
        <div class="flex items-center gap-1">
          <code class="min-w-0 select-all break-all">{failure.path}</code>
          <CopyButton text={failure.path} label={lines.copy} />
        </div>
        <p class="break-words text-[12px] text-muted">{lines.status}</p>
        {#if failure.trash}
          <p class="text-[12px] text-muted">{lines.trash}</p>
          <div class="flex items-center gap-1">
            <code class="min-w-0 select-all break-all">{failure.trash}</code>
            <CopyButton text={failure.trash} label="Copy trash path" />
          </div>
        {/if}
        {#if lines.advice}
          <p class="text-[12px] text-muted">{lines.advice}</p>
        {/if}
        {#if lines.record && failure.record}
          <p class="text-[12px] text-muted">{lines.record}</p>
          <div class="flex items-center gap-1">
            <code class="min-w-0 select-all break-all">{failure.record}</code>
            <CopyButton text={failure.record} label="Copy record path" />
          </div>
        {/if}
      </div>
    {/each}
  {:else if !error}
    <p class="text-[12px] text-muted">{failures ? "No folders to check." : "Checking folder status…"}</p>
  {/if}
</div>
