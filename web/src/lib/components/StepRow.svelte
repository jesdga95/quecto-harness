<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Brain from "phosphor-svelte/lib/Brain";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import FileText from "phosphor-svelte/lib/FileText";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import type { TranscriptItem } from "../api";
  import { describeCommand, firstSentence, k } from "../format";

  let { item }: { item: TranscriptItem } = $props();
  let open = $state(false);

  const command = $derived(
    item.kind === "tool" && typeof item.args.command === "string" ? item.args.command : "",
  );
  const path = $derived(
    item.kind === "tool" && typeof item.args.path === "string" ? item.args.path : "",
  );

  const name = $derived.by(() => {
    if (item.kind === "reasoning") return "Think";
    if (item.kind === "permission") return "Permission";
    if (item.kind === "compaction") return "Compacted";
    if (item.kind !== "tool") return "Step";
    if (item.name === "read_file") return "Read";
    if (item.name === "write_file") return "Write";
    return "Shell";
  });

  const desc = $derived.by(() => {
    if (item.kind === "reasoning") return firstSentence(item.text);
    if (item.kind === "permission") return item.command;
    if (item.kind === "compaction") return `${k(item.before)} to ${k(item.after)}`;
    if (item.kind !== "tool") return "";
    return path || describeCommand(command);
  });

  const isPath = $derived(item.kind === "tool" && path !== "");
  // A compaction with no summary is a divider and nothing more.
  const expandable = $derived(item.kind !== "compaction" || item.summary !== "");
</script>

<div>
  <button class="step" disabled={!expandable} onclick={() => (open = !open)}>
    {#if item.kind === "reasoning"}
      <Brain />
    {:else if item.kind === "permission"}
      <ShieldCheck />
    {:else if item.kind === "compaction"}
      <ArrowsIn />
    {:else if item.kind === "tool" && item.name === "read_file"}
      <FileText />
    {:else if item.kind === "tool" && item.name === "write_file"}
      <PencilSimple />
    {:else}
      <Terminal />
    {/if}

    <span class="name">{name}</span>
    <span class="sep">&middot;</span>
    <span class="desc" class:path={isPath}>{desc}</span>

    {#if item.kind === "permission" && item.outcome}
      <span class="text-[12px] text-muted">{item.outcome}</span>
    {/if}
    {#if item.kind === "tool" && item.running}
      <span class="spinner"></span>
    {/if}
    {#if expandable}
      <span class="caret" class:is-open={open}><CaretRight /></span>
    {/if}
  </button>

  {#if open && expandable}
    <div class="mt-1 mb-2 ml-8 flex flex-col gap-2">
      {#if item.kind === "reasoning"}
        <p class="text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2">{item.text}</p>
      {:else if item.kind === "compaction"}
        <p class="text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2">{item.summary}</p>
      {:else if item.kind === "permission"}
        <pre class="mono-block">{item.command}</pre>
      {:else if item.kind === "tool"}
        {#if command}
          <pre class="mono-block">{command}</pre>
        {/if}
        {#if item.output}
          <pre class="mono-block">{item.output}</pre>
        {/if}
        <div class="flex items-center gap-2">
          {#if item.exitCode !== null}
            <span
              class="code"
              class:code-ok={item.exitCode === 0}
              class:code-bad={item.exitCode !== 0}>Exit {item.exitCode}</span
            >
          {/if}
          {#if item.truncated}
            <span class="text-[11.5px] text-muted">
              Output truncated to 16 kb, head and tail kept
            </span>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>
