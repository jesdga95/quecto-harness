<script lang="ts">
  import { codeHtml, languageOfPath } from "../highlight";

  let { path, output }: { path: string; output: string } = $props();

  // read numbers each line and may end with a line saying what it left
  // off. The numbers go in a gutter of their own so the code beside them is
  // one block the highlighter can read.
  const rows = $derived.by(() => {
    const numbers: string[] = [];
    const code: string[] = [];
    const trailer: string[] = [];
    for (const line of output.replace(/\n$/, "").split("\n")) {
      const match = /^\s*(\d+)\t([\s\S]*)$/.exec(line);
      if (match && trailer.length === 0) {
        numbers.push(match[1]!);
        code.push(match[2]!);
      } else {
        trailer.push(line);
      }
    }
    return { numbers, code: code.join("\n"), trailer: trailer.join("\n") };
  });

  const language = $derived(languageOfPath(path));
</script>

<div class="filecode">
  {#if rows.numbers.length > 0}
    <div class="filecode-rows">
      <div class="filecode-gutter num">
        {#each rows.numbers as n, i (i)}<span>{n}</span>{/each}
      </div>
      <pre class="filecode-body">{@html codeHtml(rows.code, language)}</pre>
    </div>
  {/if}
  {#if rows.trailer}
    <div class="filecode-foot">{rows.trailer}</div>
  {/if}
</div>
