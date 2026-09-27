<script lang="ts">
  import CopyButton from "./CopyButton.svelte";
  import { openFile } from "../state/app.svelte";

  // What one write or edit put in the file, from the call's own arguments: a
  // write is all added lines, an edit the lines it took out and put in. A long
  // one shows its ends and folds the middle until asked.
  let {
    path,
    removed = "",
    added,
    counts,
  }: {
    path: string;
    removed?: string;
    added: string;
    counts: { added: number; removed: number };
  } = $props();

  const HEAD = 3;
  const TAIL = 4;

  function lines(text: string): string[] {
    if (text === "") return [];
    return text.replace(/\n$/, "").split("\n");
  }

  const rows = $derived([
    ...lines(removed).map((text) => ({ sign: "−", text, add: false })),
    ...lines(added).map((text) => ({ sign: "+", text, add: true })),
  ]);

  let whole = $state(false);
  const folded = $derived(!whole && rows.length > HEAD + TAIL + 1);
  const hidden = $derived(rows.length - HEAD - TAIL);
</script>

<div class="wdiff">
  <div class="wdiff-head">
    <button class="wdiff-path" title="Open {path}" onclick={() => void openFile(path)}>{path}</button>
    <CopyButton text={added} label="Copy the new text" />
  </div>
  <div class="wdiff-body">
    {#each folded ? rows.slice(0, HEAD) : rows as row, i (i)}
      <div class="wdiff-line" class:is-add={row.add} class:is-del={!row.add}>
        <span class="wdiff-sign">{row.sign}</span><span>{row.text || " "}</span>
      </div>
    {/each}
    {#if folded}
      <button class="wdiff-fold" onclick={() => (whole = true)}>… {hidden} more lines</button>
      {#each rows.slice(-TAIL) as row, i (i)}
        <div class="wdiff-line" class:is-add={row.add} class:is-del={!row.add}>
          <span class="wdiff-sign">{row.sign}</span><span>{row.text || " "}</span>
        </div>
      {/each}
    {/if}
  </div>
  <div class="wdiff-foot num">
    └ <span class="stat-add">+{counts.added}</span>
    <span class="stat-del">&minus;{counts.removed}</span> &middot; 1 file
  </div>
</div>
