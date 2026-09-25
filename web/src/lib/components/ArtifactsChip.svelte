<script lang="ts">
  import Shapes from "phosphor-svelte/lib/Shapes";
  import { app, artifacts, toggleArtifacts } from "../state/app.svelte";

  const list = $derived(artifacts());
  const writing = $derived(list.some((a) => !a.complete));
</script>

{#if list.length > 0}
  <button
    class="chip chip-plain"
    class:chip-solid={app.dock === "artifacts"}
    onclick={toggleArtifacts}
    title="Drawings, pages and files in this session"
  >
    <Shapes />
    <span class="num text-[12px]">{list.length} {list.length === 1 ? "artifact" : "artifacts"}</span>
    {#if writing && app.status !== "idle"}
      <span class="spinner"></span>
    {/if}
  </button>
{/if}
