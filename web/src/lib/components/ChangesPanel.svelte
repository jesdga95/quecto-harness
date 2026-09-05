<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import DiffBlock from "./DiffBlock.svelte";
  import type { FileChange } from "../api";
  import { app } from "../state/app.svelte";

  // A file is expanded until it is collapsed by hand. Closed paths are held
  // here so a refetch after a write does not reopen them.
  let closed = $state(new Set<string>());

  const files = $derived(app.changes.files);

  function toggle(path: string) {
    const next = new Set(closed);
    if (!next.delete(path)) next.add(path);
    closed = next;
  }

  function drawable(f: FileChange): boolean {
    return f.hunks.length > 0;
  }

  // What a row says where its counts would go, when there is no diff to draw.
  function note(f: FileChange): string {
    if (f.status === "deleted") return "Deleted";
    if (f.status === "same") return "No change";
    if (f.status === "binary") return "Binary, not compared";
    if (f.status === "large") return "Too large to compare";
    if (f.status === "lost") return "The kept copy is gone";
    return "";
  }
</script>

<aside class="changes">
  <div class="changes-head">
    <h2 class="text-[13px] font-medium">Changes in this session</h2>
    <span class="num ml-auto text-[12px]">
      <span class="stat-add">+{app.changes.added}</span>
      <span class="stat-del ml-1.5">&minus;{app.changes.removed}</span>
    </span>
  </div>

  <div class="min-h-0 flex-1 overflow-y-auto px-2.5 pb-5">
    {#each files as file (file.path)}
      <div class="mb-1">
        <button
          class="changes-file"
          disabled={!drawable(file)}
          onclick={() => toggle(file.path)}
        >
          <span class="file">{file.path}</span>
          {#if drawable(file)}
            <span class="num text-[11.5px]">
              <span class="stat-add">+{file.added}</span>
              <span class="stat-del ml-1">&minus;{file.removed}</span>
            </span>
            {#if file.status === "new"}
              <span class="text-[11.5px] text-muted">New</span>
            {/if}
            <span class="caret" class:is-open={!closed.has(file.path)}><CaretRight /></span>
          {:else}
            <span class="text-[11.5px] text-muted">{note(file)}</span>
          {/if}
        </button>

        {#if !closed.has(file.path) && drawable(file)}
          <div class="mt-1 mb-2">
            <DiffBlock hunks={file.hunks} omitted={file.omitted} path={file.path} />
          </div>
        {/if}
      </div>
    {/each}
  </div>
</aside>
