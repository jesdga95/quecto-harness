<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import FileIcon from "./FileIcon.svelte";
  import Popover from "./Popover.svelte";
  import type { TranscriptItem } from "../api";
  import { inProject } from "../files";
  import { app, openFile, showChanges } from "../state/app.svelte";

  // A card for each file a turn created, since a new file is usually what was
  // asked for. Edits to existing files are in the changes tab.
  let { items }: { items: TranscriptItem[] } = $props();

  const CARDS = 4;

  // A write that failed carries the error as its output, so only the
  // harness's own summary lines count.
  const done = $derived(
    items.filter(
      (i): i is Extract<TranscriptItem, { kind: "tool" }> =>
        i.kind === "tool" &&
        !i.running &&
        typeof i.args.path === "string" &&
        ((i.name === "write" && /^(wrote|replaced) /.test(i.output)) ||
          (i.name === "edit" && i.output.startsWith("edited "))),
    ),
  );

  const created = $derived.by(() => {
    const out: { path: string; lines: number }[] = [];
    for (const item of done) {
      if (item.name !== "write" || !item.output.startsWith("wrote ")) continue;
      const path = inProject(item.args.path as string, app.projectPath);
      if (out.some((c) => c.path === path)) continue;
      const added = item.meta?.added;
      const content = typeof item.args.content === "string" ? item.args.content : "";
      const lines = typeof added === "number" ? added : content.replace(/\n$/, "").split("\n").length;
      out.push({ path, lines });
    }
    return out.slice(0, CARDS);
  });

  let menu = $state("");

  function name(path: string): string {
    return path.split("/").pop() ?? path;
  }
</script>

{#if created.length > 0}
  <div class="file-cards">
    {#each created as file (file.path)}
      <div class="file-card">
        <span class="file-card-icon"><FileIcon path={file.path} size={20} /></span>
        <span class="flex min-w-0 flex-col">
          <span class="truncate text-[14px] font-medium text-ink" title={file.path}>{name(file.path)}</span>
          <span class="num truncate text-[12px] text-muted">
            New file, {file.lines} {file.lines === 1 ? "line" : "lines"}
          </span>
        </span>
        <div class="split-btn relative ml-auto">
          <button class="split-main" onclick={() => void openFile(file.path)}>Open</button>
          <button
            class="split-caret"
            aria-label="More for {name(file.path)}"
            aria-expanded={menu === file.path}
            onclick={() => (menu = menu === file.path ? "" : file.path)}
          >
            <CaretDown size={12} />
          </button>
          {#if menu === file.path}
            <Popover onclose={() => (menu = "")} align="right" float width={180}>
              <button
                class="popover-item"
                onclick={() => {
                  menu = "";
                  showChanges();
                }}
              >
                Show changes
              </button>
              <button
                class="popover-item"
                onclick={() => {
                  menu = "";
                  void navigator.clipboard?.writeText(file.path);
                }}
              >
                Copy path
              </button>
            </Popover>
          {/if}
        </div>
      </div>
    {/each}
  </div>
{/if}
