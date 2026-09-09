<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Brain from "phosphor-svelte/lib/Brain";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import FileText from "phosphor-svelte/lib/FileText";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import PaperPlaneTilt from "phosphor-svelte/lib/PaperPlaneTilt";
  import Question from "phosphor-svelte/lib/Question";
  import Robot from "phosphor-svelte/lib/Robot";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import FileBlock from "./FileBlock.svelte";
  import TerminalCard from "./TerminalCard.svelte";
  import TodoList from "./TodoList.svelte";
  import type { Todo, TranscriptItem } from "../api";
  import { describeCommand, firstSentence, k } from "../format";
  import { asked } from "../question";

  let { item }: { item: TranscriptItem } = $props();
  let open = $state(false);

  // A run_agent row names the agent it started and follows it, since what that
  // agent is doing is in a transcript of its own.
  const child = $derived(item.kind === "tool" ? item.child : undefined);
  const task = $derived(
    item.kind === "tool" && typeof item.args.task === "string" ? item.args.task : "",
  );

  // What a send_message call was given, so expanding the row shows the message
  // rather than the word the model got back for sending it.
  const message = $derived(
    item.kind === "tool" && item.name === "send_message" && typeof item.args.text === "string"
      ? item.args.text
      : "",
  );

  const command = $derived(
    item.kind === "tool" && typeof item.args.command === "string" ? item.args.command : "",
  );
  const path = $derived(
    item.kind === "tool" && typeof item.args.path === "string" ? item.args.path : "",
  );

  // What find was asked for, which is the text unless the call only named files.
  const query = $derived.by(() => {
    if (item.kind !== "tool" || item.name !== "find") return "";
    const { text, name } = item.args;
    if (typeof text === "string" && text !== "") return text;
    return typeof name === "string" ? name : "";
  });

  const name = $derived.by(() => {
    if (item.kind === "reasoning") return "Think";
    if (item.kind === "permission") return "Permission";
    if (item.kind === "compaction") return "Compacted";
    if (item.kind !== "tool") return "Step";
    if (item.name === "read_file") return "Read";
    if (item.name === "find") return "Find";
    if (item.name === "write_file") return "Write";
    if (item.name === "replace_in_file") return "Edit";
    if (item.name === "write_todos") return "Plan";
    if (item.name === "ask_question") return "Question";
    if (item.name === "run_agent") return "Agent";
    if (item.name === "send_message") return "Message";
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
    if (item.name === "find") return query;
    if (item.name === "ask_question") return question.question;
    if (item.name === "run_agent") {
      const to = typeof item.args.agent === "string" ? item.args.agent : "";
      return to === "" ? task : `${to}: ${task}`;
    }
    if (item.name === "send_message") return message;
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

  // The question is in the call's own arguments and the answer is the result,
  // so the row draws the choice as it was put and what came back.
  const question = $derived(
    item.kind === "tool" && item.name === "ask_question"
      ? asked(item.args)
      : { question: "", options: [], multi: false },
  );

  // The result is the labels that were chosen, or words nobody offered, so a
  // row is marked on an exact match and a typed answer marks nothing.
  const chosen = $derived(new Set(item.kind === "tool" ? item.output.split(", ") : []));

  const isPath = $derived(item.kind === "tool" && item.name !== "find" && path !== "");
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
    {:else if item.kind === "tool" && item.name === "find"}
      <MagnifyingGlass />
    {:else if item.kind === "tool" && (item.name === "write_file" || item.name === "replace_in_file")}
      <PencilSimple />
    {:else if item.kind === "tool" && item.name === "write_todos"}
      <ListChecks />
    {:else if item.kind === "tool" && item.name === "ask_question"}
      <Question />
    {:else if item.kind === "tool" && item.name === "run_agent"}
      <Robot />
    {:else if item.kind === "tool" && item.name === "send_message"}
      <PaperPlaneTilt />
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
    {#if child && child.status !== "idle"}
      <span class="text-[12px] text-muted">{child.status}</span>
      <span class="spinner"></span>
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
      {:else if item.kind === "tool" && item.name === "ask_question"}
        <ul class="flex flex-col gap-1">
          {#each question.options as option (option.label)}
            <li class="todo" class:is-doing={chosen.has(option.label)}>
              <span>{option.label}</span>
            </li>
          {/each}
        </ul>
        {#if item.output}
          <p class="text-[13px] text-ink-2">Answered {item.output}</p>
        {/if}
      {:else if item.kind === "tool" && item.name === "run_shell"}
        <TerminalCard
          {command}
          output={item.output}
          exitCode={item.exitCode}
          truncated={item.truncated}
        />
      {:else if item.kind === "tool" && item.name === "run_agent"}
        {#if task}
          <p class="text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2">{task}</p>
        {/if}
        {#if item.output}
          <p class="text-[13px] text-ink-2">{item.output}</p>
        {/if}
      {:else if item.kind === "tool" && item.name === "send_message"}
        {#if message}
          <p class="text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2">{message}</p>
        {/if}
        {#if item.output}
          <span class="text-[11.5px] text-muted">{item.output}</span>
        {/if}
      {:else if item.kind === "tool" && item.name === "read_file" && item.output}
        <FileBlock {path} output={item.output} />
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
