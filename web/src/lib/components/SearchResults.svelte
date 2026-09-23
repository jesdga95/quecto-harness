<script lang="ts">
  import type { TranscriptItem } from "../api";
  import { searchProviderLabel } from "../format";
  import { hits } from "../search";
  import SearchHit from "./SearchHit.svelte";

  let { item }: { item: Extract<TranscriptItem, { kind: "tool" }> } = $props();

  let raw = $state(false);

  const found = $derived(hits(item.meta, item.output));

  const provider = $derived.by(() => {
    const name = item.meta?.provider;
    return typeof name === "string" ? searchProviderLabel(name) : "";
  });
  const dropped = $derived.by(() => {
    const n = item.meta?.dropped;
    return typeof n === "number" ? n : 0;
  });
  const count = $derived(`${found.length} ${found.length === 1 ? "result" : "results"}`);
</script>

{#if found.length > 0}
  <div class="hits">
    <div class="hits-meta">
      {#if provider !== ""}
        <span>{provider}</span>
        <span class="hits-dot">&middot;</span>
      {/if}
      <span>{count}</span>
    </div>

    {#each found as hit, i (i)}
      <SearchHit {hit} index={i} />
    {/each}

    <div class="hits-foot">
      {#if dropped > 0}
        <span class="hits-note">
          {dropped} {dropped === 1 ? "result" : "results"} came back with no address and were dropped.
        </span>
      {/if}
      {#if item.truncated}
        <span class="hits-note">Truncated, head and tail kept</span>
      {/if}
      <button class="hit-more" onclick={() => (raw = !raw)}>
        {raw ? "Hide what the model read" : "What the model read"}
      </button>
    </div>

    {#if raw}
      <pre class="mono-block">{item.output}</pre>
    {/if}
  </div>
{:else if item.output}
  <!-- A search that found nothing, or one that failed, is its own sentence. -->
  <pre class="mono-block">{item.output}</pre>
{/if}
