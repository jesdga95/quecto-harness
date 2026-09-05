<script lang="ts">
  import type { DiffHunk } from "../api";
  import { codeHtml, languageOfPath } from "../highlight";

  let {
    hunks,
    omitted,
    path = "",
  }: { hunks: DiffHunk[]; omitted: number; path?: string } = $props();

  const sign = { added: "+", removed: "−", context: " " };
  // A diff line is coloured on its own, since the lines around it may not be
  // in the file any more.
  const language = $derived(languageOfPath(path));
</script>

<div class="diff">
  {#each hunks as hunk, h (h)}
    {#if h > 0 || hunk.old_start > 1}
      <div class="diff-hunk">Line {hunk.new_start}</div>
    {/if}
    {#each hunk.lines as line, i (i)}
      <div
        class="diff-line"
        class:is-added={line.kind === "added"}
        class:is-removed={line.kind === "removed"}
      >
        <span class="diff-sign">{sign[line.kind]}</span>
        <span class="diff-code">{@html codeHtml(line.text || " ", language)}</span>
      </div>
    {/each}
  {/each}
  {#if omitted > 0}
    <div class="diff-foot">{omitted} more lines, not shown</div>
  {/if}
</div>
