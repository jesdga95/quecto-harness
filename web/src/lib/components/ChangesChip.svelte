<script lang="ts">
  import GitDiff from "phosphor-svelte/lib/GitDiff";
  import { app, toggleChanges } from "../state/app.svelte";

  const count = $derived(app.changes.files.length);
</script>

{#if count > 0}
  <button
    class="chip chip-plain"
    class:chip-solid={app.dock === "changes"}
    onclick={toggleChanges}
    title="Files this session changed"
  >
    <GitDiff />
    <span class="num text-[12px]">{count} {count === 1 ? "file" : "files"}</span>
    <span class="num stat-add text-[12px]">+{app.changes.added}</span>
    <span class="num stat-del text-[12px]">&minus;{app.changes.removed}</span>
  </button>
{/if}
