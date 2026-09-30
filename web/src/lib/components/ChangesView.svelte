<script lang="ts">
  import ArrowsInLineVertical from "phosphor-svelte/lib/ArrowsInLineVertical";
  import ArrowsOutLineVertical from "phosphor-svelte/lib/ArrowsOutLineVertical";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import GitDiff from "phosphor-svelte/lib/GitDiff";
  import CopyButton from "./CopyButton.svelte";
  import DiffBlock from "./DiffBlock.svelte";
  import type { ChangeStatus, FileChange } from "../api";
  import { unifiedPatch } from "../patch";
  import { app } from "../state/app.svelte";

  // A file is collapsed until opened by hand, except when it is the only one.
  // Opened paths are held here so a refetch after a write keeps them open.
  let opened = $state(new Set<string>());

  const files = $derived(app.changes.files);
  const patch = $derived(unifiedPatch(files));
  const drawn = $derived(files.filter(drawable));
  const allOpen = $derived(drawn.length > 0 && drawn.every(isOpen));

  function drawable(f: FileChange): boolean {
    return f.hunks.length > 0;
  }

  function isOpen(f: FileChange): boolean {
    return files.length === 1 || opened.has(f.path);
  }

  function toggle(path: string) {
    const next = new Set(opened);
    if (!next.delete(path)) next.add(path);
    opened = next;
  }

  function toggleAll() {
    opened = allOpen ? new Set() : new Set(drawn.map((f) => f.path));
  }

  const BADGE: Partial<Record<ChangeStatus, string>> = { new: "A", changed: "M", deleted: "D" };

  // What a row says where its counts would go, when there is no diff to draw.
  function note(f: FileChange): string {
    if (f.status === "deleted") return "Deleted";
    if (f.status === "same") return "No change";
    if (f.status === "binary") return "Binary, not compared";
    if (f.status === "large") return "Too large to compare";
    if (f.status === "lost") return "The kept copy is gone";
    return "";
  }

  function split(path: string): [string, string] {
    const at = path.lastIndexOf("/");
    return [path.slice(0, at + 1), path.slice(at + 1)];
  }
</script>

{#if files.length === 0}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">No changes</span></div>
  </div>
  <div class="dock-empty">
    <div class="dock-empty-icon"><GitDiff size={21} /></div>
    <h3>Nothing written yet</h3>
    <p>Every file this session writes or edits shows here with its diff.</p>
  </div>
{:else}
  <div class="panel-path">
    <div class="panel-crumbs">
      <span class="crumb is-last font-medium">{files.length} {files.length === 1 ? "file" : "files"}</span>
      <span class="num ml-2 text-[12px]">
        <span class="stat-add">+{app.changes.added}</span>
        <span class="stat-del ml-1">&minus;{app.changes.removed}</span>
      </span>
    </div>
    <div class="flex shrink-0 items-center gap-0.5">
      {#if drawn.length > 1}
        <button
          class="icon-btn"
          aria-label={allOpen ? "Collapse every file" : "Expand every file"}
          title={allOpen ? "Collapse all" : "Expand all"}
          onclick={toggleAll}
        >
          {#if allOpen}<ArrowsInLineVertical />{:else}<ArrowsOutLineVertical />{/if}
        </button>
      {/if}
      {#if patch}
        <CopyButton text={patch} label="Copy diff" />
      {/if}
    </div>
  </div>

  <div class="artifact-body">
    <div class="min-h-0 flex-1 overflow-y-auto pb-5">
      {#each files as file (file.path)}
        {@const [dir, name] = split(file.path)}
        {@const open = drawable(file) && isOpen(file)}
        <button
          class="changes-file"
          class:is-open={open}
          disabled={!drawable(file) || files.length === 1}
          title={file.path}
          onclick={() => toggle(file.path)}
        >
          <span class="change-badge is-{file.status}">{BADGE[file.status] ?? "·"}</span>
          <span class="file"><span><span class="dir">{dir}</span>{name}</span></span>
          {#if drawable(file)}
            <span class="num text-[11.5px]">
              <span class="stat-add">+{file.added}</span>
              {#if file.removed}<span class="stat-del ml-1">&minus;{file.removed}</span>{/if}
            </span>
            {#if files.length > 1}
              <span class="caret"><CaretRight /></span>
            {/if}
          {:else}
            <span class="text-[11.5px] text-muted">{note(file)}</span>
          {/if}
        </button>

        {#if open}
          <div class="changes-diff">
            <DiffBlock hunks={file.hunks} omitted={file.omitted} path={file.path} />
          </div>
        {/if}
      {/each}
    </div>
  </div>
{/if}
