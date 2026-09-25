<script lang="ts">
  import Browser from "phosphor-svelte/lib/Browser";
  import FileCode from "phosphor-svelte/lib/FileCode";
  import PenNib from "phosphor-svelte/lib/PenNib";
  import { artifactId, baseTitle } from "../artifacts";
  import type { Block } from "../markdown";
  import { app, showArtifact } from "../state/app.svelte";

  // Where a drawing, page or file sits in the reply. The artifact itself opens in
  // the panel beside the transcript.
  let { itemId, block }: { itemId: string; block: Block } = $props();

  const id = $derived(artifactId(itemId, block.index));
  const title = $derived(baseTitle(block));
  const lines = $derived(block.source.split("\n").length);
  const on = $derived(app.dock === "artifacts" && app.artifactId === id);

  function note(): string {
    const kind = block.kind === "svg" ? "Drawing" : block.kind === "page" ? "Page" : "File";
    if (!block.complete) return `Writing, ${lines} ${lines === 1 ? "line" : "lines"}`;
    return `${kind}, ${lines} ${lines === 1 ? "line" : "lines"}`;
  }
</script>

<button class="artifact-card" class:is-on={on} onclick={() => showArtifact(id)}>
  <span class="artifact-card-icon">
    {#if block.kind === "svg"}<PenNib />{:else if block.kind === "page"}<Browser />{:else}<FileCode />{/if}
  </span>
  <span class="flex min-w-0 flex-col">
    <span class="truncate text-[14px] font-medium text-ink">{title}</span>
    <span class="num text-[12px] text-muted">{note()}</span>
  </span>
  <span class="ml-auto shrink-0">
    {#if !block.complete}
      <span class="spinner block"></span>
    {:else}
      <span class="artifact-card-open">{on ? "Showing" : "Open"}</span>
    {/if}
  </span>
</button>
