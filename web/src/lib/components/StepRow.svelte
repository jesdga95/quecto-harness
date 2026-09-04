<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Brain from "phosphor-svelte/lib/Brain";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import FileText from "phosphor-svelte/lib/FileText";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import TerminalCard from "./TerminalCard.svelte";
  import TodoList from "./TodoList.svelte";
  import type { Todo, TranscriptItem } from "../api";
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
    if (item.name === "write_todos") return "Plan";
    return "Shell";
  });

  const desc = $derived.by(() => {
    if (item.kind === "reasoning") return firstSentence(item.text);
    if (item.kind === "permission") return item.command;
    if (item.kind === "compaction") return `${k(item.before)} to ${k(item.after)}`;
    if (item.kind !== "tool") return "";
    if (item.name === "write_todos") {
      const done = todos.filter((t) => t.status === "done").length;
      return `${done} of ${todos.length} done`;
    }
    return path || describeCommand(command);
  });

  // The plan is in the call's own arguments, which is what the row draws
  // rather than the result the model was handed back.
  const todos = $derived.by<Todo[]>(() => {
    if (item.kind !== "tool" || item.name !== "write_todos") return [];
    const raw = item.args.todos;
    if (!Array.isArray(raw)) return [];
    return raw
      .filter((t): t is Record<string, unknown> => typeof t === "object" && t !== null)
      .map<Todo>((t) => ({
        text: typeof t.text === "string" ? t.text : "",
        status: t.status === "doing" || t.status === "done" ? t.status : "todo",
      }))
      .filter((t) => t.text !== "");
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
    {:else if item.kind === "tool" && item.name === "write_todos"}
      <ListChecks />
    {:else}
      <Terminal />
    {/if}

    <span class="name">{name}</span>
    <span class="sep">&middot;</span>
    <span class="desc" class:path={isPath}>{desc}</span>

    {#if item.kind === "permission" && item.outcome}
      <span class="text-[12px] text-muted">{item.outcome}</span>
    {/if}
    {#if item.kind === "tool" && item.exitCode !== null && item.exitCode !== 0}
      <span class="text-[12px] text-crit">Exit {item.exitCode}</span>
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
      {:else if item.kind === "tool" && item.name === "write_todos"}
        <TodoList {todos} />
      {:else if item.kind === "tool" && item.name === "run_shell"}
        <TerminalCard
          {command}
          output={item.output}
          exitCode={item.exitCode}
          truncated={item.truncated}
        />
      {:else if item.kind === "tool"}
        {#if item.output}
          <pre class="mono-block">{item.output}</pre>
        {/if}
        {#if item.truncated}
          <span class="text-[11.5px] text-muted">
            Truncated to 16 kb, head and tail kept
          </span>
        {/if}
      {/if}
    </div>
  {/if}
</div>
