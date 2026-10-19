<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import Brain from "phosphor-svelte/lib/Brain";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import FileText from "phosphor-svelte/lib/FileText";
  import ImageSquare from "phosphor-svelte/lib/ImageSquare";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import PaperPlaneTilt from "phosphor-svelte/lib/PaperPlaneTilt";
  import Question from "phosphor-svelte/lib/Question";
  import Robot from "phosphor-svelte/lib/Robot";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import WarningCircle from "phosphor-svelte/lib/WarningCircle";
  import FileBlock from "./FileBlock.svelte";
  import SearchResults from "./SearchResults.svelte";
  import TerminalCard from "./TerminalCard.svelte";
  import TodoList from "./TodoList.svelte";
  import StreamText from "./StreamText.svelte";
  import WriteDiff from "./WriteDiff.svelte";
  import type { TranscriptItem } from "../api";
  import { clockLabel, statusLabel } from "../format";
  import { asked } from "../question";
  import { stepDesc, stepFailed, stepName, stringArg, todosOf } from "../steps";
  import { app, showProcesses } from "../state/app.svelte";

  // live marks the row the model is still writing into.
  let { item, live = false }: { item: TranscriptItem; live?: boolean } = $props();
  let open = $state(false);

  // A subagent row names the agent it started and follows it, since what that
  // agent is doing is in a transcript of its own.
  const child = $derived(item.kind === "tool" ? item.child : undefined);
  const task = $derived(stringArg(item, "task"));

  // What a send_message call was given, so expanding the row shows the message
  // rather than the word the model got back for sending it.
  const message = $derived(
    item.kind === "tool" && item.name === "send_message" ? stringArg(item, "text") : "",
  );

  // read_image keeps its own copy of the picture, which the row shows rather
  // than the file as it stands now.
  const image = $derived(
    item.kind === "tool" && typeof item.meta?.image === "string" ? item.meta.image : "",
  );

  const command = $derived(stringArg(item, "command"));
  const path = $derived(stringArg(item, "path"));
  const failed = $derived(stepFailed(item));
  const name = $derived(stepName(item));
  const desc = $derived(stepDesc(item, live, app.draft?.title ?? ""));
  const todos = $derived(todosOf(item));

  // The questions are in the call's own arguments and the answer is the result,
  // so the row draws each choice as it was put and what came back.
  const questions = $derived(
    item.kind === "tool" && item.name === "ask_user_question" ? asked(item.args) : [],
  );

  // The result is the labels that were chosen, or words nobody offered, so a
  // row is marked on an exact match and a typed answer marks nothing.
  const chosen = $derived(new Set(item.kind === "tool" ? item.output.split(", ") : []));

  // A command says how long it has been going against the limit it was given,
  // since a slow run and a stuck one look the same without them. A run under
  // five seconds says nothing once it is over.
  let now = $state(Date.now());
  $effect(() => {
    if (item.kind !== "tool" || !item.running || item.startedAt === undefined) return;
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  const clock = $derived.by(() => {
    if (item.kind !== "tool" || item.name !== "bash") return "";
    if (!item.running) {
      return item.ranMs !== undefined && item.ranMs >= 5000 ? clockLabel(item.ranMs / 1000) : "";
    }
    if (item.startedAt === undefined) return "";
    const on = clockLabel((now - item.startedAt) / 1000);
    return item.timeoutS ? `${on} of ${clockLabel(item.timeoutS)}` : on;
  });

  // What a write or edit did to the file, as the text it wrote and the lines
  // it counted. The harness counts against the file as it was; a call still
  // running, or one stored before counts were kept, counts its own arguments.
  const edit = $derived.by(() => {
    if (item.kind !== "tool" || (item.name !== "write" && item.name !== "edit")) return null;
    const text = (key: string) => (typeof item.args[key] === "string" ? (item.args[key] as string) : "");
    // An edit carries a list of replacements; one stored before it did names
    // a single one at the top level.
    const list = Array.isArray(item.args.edits) ? (item.args.edits as Record<string, unknown>[]) : null;
    const side = (key: string) =>
      list
        ? list.map((e) => (e && typeof e[key] === "string" ? (e[key] as string) : "")).join("\n")
        : text(key);
    const added = item.name === "write" ? text("content") : side("new_string");
    const removed = item.name === "write" ? "" : side("old_string");
    const count = (s: string) => (s === "" ? 0 : s.replace(/\n$/, "").split("\n").length);
    const meta = item.meta;
    const counts =
      typeof meta?.added === "number" && typeof meta?.removed === "number"
        ? { added: meta.added, removed: meta.removed }
        : { added: count(added), removed: count(removed) };
    return { added, removed, counts };
  });
  // A write that failed carries the error as its output, which is what the
  // row opens on instead of the text it did not write.
  const wrote = $derived(
    item.kind === "tool" &&
      (item.running || /^(wrote|replaced|edited) /.test(item.output)),
  );

  const isPath = $derived(item.kind === "tool" && item.name !== "grep" && path !== "");
  // A background bash call has no output to show, only the process it started.
  const proc = $derived(
    item.kind === "tool" && item.name === "bash" && typeof item.meta?.process === "string"
      ? item.meta.process
      : "",
  );
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
    {:else if item.kind === "error"}
      <WarningCircle class="text-crit" />
    {:else if item.kind === "tool" && item.name === "read"}
      <FileText />
    {:else if item.kind === "tool" && item.name === "read_image"}
      <ImageSquare />
    {:else if item.kind === "tool" && item.name === "grep"}
      <MagnifyingGlass />
    {:else if item.kind === "tool" && item.name === "web_search"}
      <MagnifyingGlass />
    {:else if item.kind === "tool" && (item.name === "write" || item.name === "edit")}
      <PencilSimple />
    {:else if item.kind === "tool" && (item.name === "todo_write" || item.name === "exit_plan_mode" || item.name === "write_plan")}
      <ListChecks />
    {:else if item.kind === "tool" && item.name === "ask_user_question"}
      <Question />
    {:else if item.kind === "tool" && item.name === "subagent"}
      <Robot />
    {:else if item.kind === "tool" && item.name === "send_message"}
      <PaperPlaneTilt />
    {:else}
      <Terminal />
    {/if}

    <span class="name">{name}</span>
    <span class="sep">&middot;</span>
    <span class="desc" class:path={isPath && !failed} class:text-crit={failed}>{desc}</span>

    {#if proc}
      <span class="text-[12px] text-muted">Background</span>
    {/if}

    {#if edit && wrote && (edit.counts.added > 0 || edit.counts.removed > 0)}
      <span class="num text-[12px]">
        <span class="stat-add">+{edit.counts.added}</span>
        <span class="stat-del">&minus;{edit.counts.removed}</span>
      </span>
    {/if}
    {#if item.kind === "permission" && item.outcome}
      <span class="text-[12px] text-muted">{item.outcome}</span>
    {/if}
    {#if item.kind === "tool" && item.name === "exit_plan_mode" && !item.running}
      <span class="text-[12px] text-muted">
        {item.output === "approved" ? "Approved" : item.output.startsWith("not approved") ? "Revised" : ""}
      </span>
    {/if}
    {#if item.kind === "tool" && item.exitCode !== null && item.exitCode !== 0}
      <span class="text-[12px] text-crit">Exit {item.exitCode}</span>
    {/if}
    {#if clock}
      <span class="text-[12px] text-muted">{clock}</span>
    {/if}
    {#if child && child.status !== "idle"}
      <span class="text-[12px] text-muted">{statusLabel(child.status)}</span>
      <span class="spinner"></span>
    {:else if item.kind === "tool" && item.running}
      <span class="spinner"></span>
    {/if}
    {#if expandable}
      <span class="caret" class:is-open={open}><CaretRight /></span>
    {/if}
  </button>

  {#if open && expandable}
    <div class="step-body">
      {#if item.kind === "reasoning"}
        <StreamText class="step-text" text={item.text} {live} />
      {:else if item.kind === "compaction"}
        <p class="step-text">{item.summary}</p>
      {:else if item.kind === "error"}
        <p class="step-text">{item.message}</p>
      {:else if item.kind === "permission"}
        <pre class="mono-block">{item.command}</pre>
      {:else if failed && item.kind === "tool"}
        <pre class="mono-block">{item.output}</pre>
      {:else if item.kind === "tool" && item.name === "todo_write"}
        <TodoList {todos} />
      {:else if item.kind === "tool" && item.name === "ask_user_question"}
        {#if questions.length > 1}
          {#if item.output}
            <div class="text-[13px] text-ink-2 whitespace-pre-wrap">{item.output}</div>
          {/if}
        {:else}
          <ul class="flex flex-col gap-1">
            {#each (questions[0]?.options ?? []) as option (option.label)}
              <li class="todo" class:is-doing={chosen.has(option.label)}>
                <span>{option.label}</span>
              </li>
            {/each}
          </ul>
          {#if item.output}
            <p class="text-[13px] text-ink-2">Answered {item.output}</p>
          {/if}
        {/if}
      {:else if item.kind === "tool" && item.name === "exit_plan_mode"}
        {#if item.output.startsWith("not approved: ")}
          <p class="text-[13px] text-ink-2">Asked for: {item.output.slice(14)}</p>
        {:else if item.output}
          <p class="step-text">{item.output}</p>
        {/if}
      {:else if item.kind === "tool" && item.name === "bash"}
        {#if proc}
          <p class="step-text">
            Running in the background.
            <button class="proc-more" onclick={() => showProcesses(proc)}>Open its tail</button>
          </p>
        {:else}
          <TerminalCard
            {command}
            output={item.output}
            exitCode={item.exitCode}
            truncated={item.truncated}
          />
        {/if}
      {:else if item.kind === "tool" && item.name === "subagent"}
        {#if task}
          <p class="step-text">{task}</p>
        {/if}
        {#if item.output}
          <p class="step-text">{item.output}</p>
        {/if}
      {:else if item.kind === "tool" && item.name === "send_message"}
        {#if message}
          <p class="step-text">{message}</p>
        {/if}
        {#if item.output}
          <span class="text-[11.5px] text-muted">{item.output}</span>
        {/if}
      {:else if item.kind === "tool" && item.name === "read_image" && image}
        <a class="step-shot" href="/api/files/{image}" target="_blank" rel="noopener noreferrer" title={path}>
          <img src="/api/files/{image}" alt={path} />
        </a>
      {:else if item.kind === "tool" && item.name === "read" && item.output}
        <FileBlock {path} output={item.output} />
      {:else if item.kind === "tool" && item.name === "web_search"}
        <SearchResults item={item} />
      {:else if edit && wrote && path}
        <WriteDiff {path} added={edit.added} removed={edit.removed} counts={edit.counts} />
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
