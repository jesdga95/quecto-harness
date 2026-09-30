<script lang="ts">
  import type { DiffHunk } from "../api";
  import { codeHtml, languageOfPath } from "../highlight";
  import { hunkHeader } from "../patch";

  let {
    hunks,
    omitted,
    path = "",
  }: { hunks: DiffHunk[]; omitted: number; path?: string } = $props();

  const sign = { added: "+", removed: "−", context: " " };
  // A diff line is coloured on its own, since the lines around it may not be
  // in the file any more.
  const language = $derived(languageOfPath(path));

  // The number beside each row: its line in the new file, or in the old one
  // for a line that was removed.
  function numbered(hunk: DiffHunk): number[] {
    let oldAt = hunk.old_start;
    let newAt = hunk.new_start;
    return hunk.lines.map((line) => {
      if (line.kind === "removed") return oldAt++;
      if (line.kind === "context") oldAt++;
      return newAt++;
    });
  }
</script>

<div class="diff">
  <!-- One track as wide as the longest line, so every row is the same width and
       a tinted one stays tinted all the way across. -->
  <div class="diff-body">
    {#each hunks as hunk, h (h)}
      {@const numbers = numbered(hunk)}
      <div class="diff-hunk">{hunkHeader(hunk)}</div>
      {#each hunk.lines as line, i (i)}
        <div
          class="diff-line"
          class:is-added={line.kind === "added"}
          class:is-removed={line.kind === "removed"}
        >
          <span class="diff-num num">{numbers[i]}</span>
          <span class="diff-sign">{sign[line.kind]}</span>
          <span class="diff-code">{@html codeHtml(line.text || " ", language)}</span>
        </div>
      {/each}
    {/each}
    {#if omitted > 0}
      <div class="diff-foot">{omitted} more lines, not shown</div>
    {/if}
  </div>
</div>
