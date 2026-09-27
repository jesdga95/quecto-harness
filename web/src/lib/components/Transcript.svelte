<script lang="ts">
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import ArrowDown from "phosphor-svelte/lib/ArrowDown";
  import ArrowsLeftRight from "phosphor-svelte/lib/ArrowsLeftRight";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import CheckCircle from "phosphor-svelte/lib/CheckCircle";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import Robot from "phosphor-svelte/lib/Robot";
  import Warning from "phosphor-svelte/lib/Warning";
  import CopyButton from "./CopyButton.svelte";
  import PermissionCard from "./PermissionCard.svelte";
  import Prose from "./Prose.svelte";
  import PlanCard from "./PlanCard.svelte";
  import QuestionCard from "./QuestionCard.svelte";
  import StepRow from "./StepRow.svelte";
  import TurnFiles from "./TurnFiles.svelte";
  import TurnFooter from "./TurnFooter.svelte";
  import type { Attached, TranscriptItem } from "../api";
  import { app, canResend, resend } from "../state/app.svelte";
  import { bytes, fileKind, fileName, timeOf } from "../format";
  import { followEnd } from "../stream.svelte";

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);

  // Seconds spent on the current wait. A wait can run for minutes and draws
  // nothing while it does, and a spinner on its own reads as a page that has
  // stopped.
  let waited = $state(0);
  $effect(() => {
    if (!app.awaitingModel) {
      waited = 0;
      return;
    }
    const started = Date.now();
    const tick = setInterval(() => {
      waited = Math.floor((Date.now() - started) / 1000);
    }, 1000);
    return () => clearInterval(tick);
  });
  const clock = $derived(
    waited < 60 ? `${waited}s` : `${Math.floor(waited / 60)}m ${waited % 60}s`,
  );

  // The message being rewritten, by id, and the words as they stand.
  let editing = $state("");
  let draft = $state("");

  function edit(item: TranscriptItem): void {
    if (item.kind !== "user") return;
    editing = item.id;
    draft = item.text;
  }

  function commit(item: TranscriptItem): void {
    if (item.kind !== "user") return;
    // The state can turn while the message is open for editing, and the send
    // is a click later than the button that offered it.
    if (!canResend(item)) return;
    const text = draft;
    if (!text.trim() && item.files.length === 0) return;
    editing = "";
    void resend(item.seq, text);
  }

  // Enter sends, Shift and Enter makes a line, Escape leaves the message as
  // it was. The composer reads the same way.
  function onkeydown(event: KeyboardEvent, item: TranscriptItem) {
    if (event.key === "Escape") {
      editing = "";
      return;
    }
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      commit(item);
    }
  }
  const count = $derived(app.items.length);
  const tail = $derived(app.items.at(-1));

  // A turn is the message that started it and everything after it up to the
  // next one. An approved plan and a compaction start one too: the work after
  // either reads as its own, and a compaction stays in view. Once a turn is over, everything before its answer
  // folds under one line, and the answer and what it wrote stay out. A turn
  // still running, or one holding a card that waits on a person, shows all.
  function pending(item: TranscriptItem): boolean {
    if (item.kind === "permission") return item.outcome === null;
    return item.kind === "tool" && item.running && !!item.askId;
  }
  function isProcess(item: TranscriptItem): boolean {
    return item.kind === "tool" || item.kind === "reasoning" || item.kind === "permission";
  }
  function lastIndex(items: TranscriptItem[], test: (item: TranscriptItem) => boolean): number {
    for (let i = items.length - 1; i >= 0; i--) if (test(items[i]!)) return i;
    return -1;
  }
  type Turn = {
    key: string;
    head: TranscriptItem | null;
    body: TranscriptItem[];
    fold: TranscriptItem[];
    rest: TranscriptItem[];
  };
  const turns = $derived.by(() => {
    const out: Turn[] = [];
    let cur: Turn | null = null;
    for (const item of app.items) {
      const starts = item.kind === "user" || item.kind === "mode" || item.kind === "compaction";
      if (starts || !cur) {
        cur = { key: item.id, head: null, body: [], fold: [], rest: [] };
        out.push(cur);
        if (starts) {
          cur.head = item;
          continue;
        }
      }
      cur.body.push(item);
    }
    out.forEach((turn, i) => {
      const live = (i === out.length - 1 && app.status !== "idle") || turn.body.some(pending);
      let cut = 0;
      if (!live) {
        const lastProcess = lastIndex(turn.body, isProcess);
        const answer = lastIndex(turn.body, (item) => item.kind === "assistant");
        cut = answer > lastProcess ? answer : lastProcess + 1;
        // Planning ends in the plan being approved rather than in a reply, and
        // the approval line under it says so, so all of it folds.
        const next = out[i + 1]?.head;
        if (next?.kind === "mode" && next.kickoff) cut = turn.body.length;
      }
      turn.fold = turn.body.slice(0, cut);
      turn.rest = turn.body.slice(cut);
    });
    return out;
  });

  // Which folded turns the reader has opened. It sticks until closed.
  const openTurns = $state<Record<string, boolean>>({});

  function foldLabel(items: TranscriptItem[]): string {
    const tools = items.filter((i) => i.kind === "tool").length;
    const messages = items.filter((i) => i.kind === "assistant").length;
    const parts: string[] = [];
    if (tools > 0) parts.push(`Used ${tools} ${tools === 1 ? "tool" : "tools"}`);
    if (messages > 0) parts.push(`${messages} ${messages === 1 ? "note" : "notes"} on the way`);
    if (parts.length === 0) {
      const thoughts = items.filter((i) => i.kind === "reasoning").length;
      if (thoughts > 0) parts.push(`${thoughts} ${thoughts === 1 ? "thought" : "thoughts"}`);
      else parts.push(`${items.length} ${items.length === 1 ? "step" : "steps"}`);
    }
    return parts.join(", ");
  }

  // The writes each finished turn made, keyed by the reply that ends it, so the
  // reply can list them above its footer.
  const turnWrites = $derived.by(() => {
    const out = new Map<string, TranscriptItem[]>();
    for (const turn of turns) {
      const end = turn.rest[lastIndex(turn.rest, (i) => i.kind === "assistant" && !!i.cost)];
      if (end) out.set(end.id, turn.body);
    }
    return out;
  });

  // How much the last item has to draw. A reply, a thinking block and a command
  // still running all grow line by line, and an expanded one moves the end of
  // the page as it does.
  const tailLength = $derived.by(() => {
    if (!tail) return 0;
    if (tail.kind === "assistant" || tail.kind === "reasoning") return tail.text.length;
    if (tail.kind === "tool") return tail.output.length;
    return 0;
  });

  // Follows the stream while the reader is at the end, and stops following the
  // moment they scroll up.
  $effect(() => {
    void count;
    void app.steer?.text.length;
    void tailLength;
    if (scroller && atBottom) scroller.scrollTop = scroller.scrollHeight;
  });

  // A compaction can start while the reader is scrolled up, and it is the one
  // wait worth seeing, so it is pulled into view when it starts.
  $effect(() => {
    if (app.status !== "compacting" || !scroller) return;
    atBottom = true;
    scroller.scrollTop = scroller.scrollHeight;
  });

  function onscroll() {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  }

  function toBottom() {
    atBottom = true;
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }

  // A reply is split around the tool calls the model made in the middle of
  // it, so the copy button takes the blocks before the one it sits under as
  // well. Keyed by the id of the last block of each turn.
  const replies = $derived.by(() => {
    const out = new Map<string, string>();
    let parts: string[] = [];
    let last = "";
    for (const item of app.items) {
      if (item.kind === "user") {
        if (last) out.set(last, parts.join("\n\n"));
        parts = [];
        last = "";
      } else if (item.kind === "assistant") {
        parts.push(item.text);
        last = item.id;
      }
    }
    if (last) out.set(last, parts.join("\n\n"));
    return out;
  });

  // The box opens with the caret at the end of the message and grows with it,
  // so a long message is edited whole rather than through a slot three lines
  // high.
  function editBox(node: HTMLTextAreaElement) {
    const fit = () => {
      const line = parseFloat(getComputedStyle(node).lineHeight) || 20;
      const pad = node.offsetHeight - node.clientHeight + 8;
      node.style.height = "auto";
      node.style.height = Math.min(node.scrollHeight, Math.round(line * 12 + pad)) + "px";
    };
    fit();
    node.focus();
    node.setSelectionRange(node.value.length, node.value.length);
    node.addEventListener("input", fit);
    return { destroy: () => node.removeEventListener("input", fit) };
  }
</script>

{#snippet attachments(files: Attached[])}
  <div class="tiles">
    {#each files as file (file.path)}
      {#if file.kind === "image" && file.file}
        <a
          class="tile tile-shot"
          href="/api/files/{file.file}"
          target="_blank"
          rel="noopener noreferrer"
          title={file.path}
        >
          <img src="/api/files/{file.file}" alt={fileName(file.path)} />
        </a>
      {:else}
        <span class="tile tile-doc" title={file.path}>
          <span class="tile-kind">
            {fileKind(file.path)}
            {#if file.bytes > 0}<span class="tile-size">{bytes(file.bytes)}</span>{/if}
          </span>
          <span class="tile-name">{fileName(file.path)}</span>
        </span>
      {/if}
    {/each}
  </div>
{/snippet}

{#snippet renderItem(item: TranscriptItem)}
  {#if item.kind === "user" && editing === item.id}
    <div class="composer mt-4 px-3 pt-3 pb-2">
      {#if item.files.length > 0}
        {@render attachments(item.files)}
      {/if}
      <textarea
        class="textarea"
        rows="1"
        bind:value={draft}
        onkeydown={(event) => onkeydown(event, item)}
        use:editBox
      ></textarea>
      <div class="mt-2 flex items-center gap-2">
        {#if item.files.length > 0}
          <span class="text-[12px] text-muted">The files stay as they are</span>
        {/if}
        <div class="ml-auto flex items-center gap-2">
          <button class="btn" onclick={() => (editing = "")}>Cancel</button>
          <button
            class="btn btn-primary"
            onclick={() => commit(item)}
            disabled={!canResend(item) || (!draft.trim() && item.files.length === 0)}
          >
            Send
          </button>
        </div>
      </div>
    </div>
  {:else if item.kind === "user"}
    <div class="mt-4 flex flex-col items-end gap-1">
      {#if item.from}
        <span class="flex items-center gap-1.5 text-[12px] text-muted">
          <Robot size={13} />
          {item.from}
        </span>
      {/if}
      <div class="bubble">
        {#if item.files.length > 0}
          {@render attachments(item.files)}
        {/if}

        {#if item.text}
          <p class="bubble-text">{item.text}</p>
        {/if}
      </div>
      <div class="flex items-center gap-1">
        {#if item.edited}
          <span class="mr-1 text-[12px] text-muted">Edited</span>
        {/if}
        {#if item.at}
          <span class="num mr-1 text-[12px] text-muted" title={new Date(item.at).toLocaleString()}>
            {timeOf(item.at)}
          </span>
        {/if}
        {#if item.text && !item.from}
          <CopyButton text={item.text} label="Copy message" />
        {/if}
        {#if canResend(item)}
          <button class="icon-btn" aria-label="Edit message" onclick={() => edit(item)}>
            <PencilSimple />
          </button>
          <button class="icon-btn" aria-label="Send again" onclick={() => resend(item.seq)}>
            <ArrowClockwise />
          </button>
        {/if}
      </div>
    </div>
  {:else if item.kind === "assistant"}
    <div class="my-2">
      {#if item.text === "" && !item.streaming}
        <p class="text-[14px] text-muted">Ended without a reply.</p>
      {:else}
        <Prose text={item.text} streaming={item.streaming} />
      {/if}
      <!-- The cost arrives when the turn ends, so it is also what says
           this block is the end of one rather than a fragment between
           two tool calls. -->
      {#if item.cost}
        {@const writes = turnWrites.get(item.id)}
        {#if writes}
          <TurnFiles items={writes} />
        {/if}
        <TurnFooter text={replies.get(item.id) ?? item.text} cost={item.cost} at={item.at} />
      {/if}
    </div>
  {:else if item.kind === "mode"}
    <!-- A mode switch the reader made is shown by the composer, so only an
         approved plan, which starts work, draws a line. -->
    {#if item.kickoff}
      <p class="plan-approved">
        <CheckCircle size={15} weight="fill" class="shrink-0" />
        <span><b>Plan approved</b>{item.title ? `: ${item.title}` : ""}</span>
      </p>
    {/if}
  {:else if item.kind === "status"}
    <div class="flex items-center gap-2 py-[5px] text-[13px] text-muted">
      <ArrowsLeftRight size={15} class="shrink-0" />
      <span class="truncate">{item.text}</span>
    </div>
  {:else if item.kind === "error"}
    <div class="flex items-center gap-2 py-[5px] text-[13px] text-crit">
      <Warning size={15} class="shrink-0" />
      <span class="truncate">{item.message}</span>
    </div>
  {:else if item.kind === "permission" && item.outcome === null}
    <div class="my-2">
      <PermissionCard {item} />
    </div>
  {:else if item.kind === "tool" && item.name === "ask_question" && item.running && item.askId}
    <div class="my-2">
      <QuestionCard {item} />
    </div>
  {:else if item.kind === "tool" && item.name === "submit_plan" && item.running && item.askId}
    <div class="my-2">
      <PlanCard {item} />
    </div>
  {:else}
    <StepRow {item} live={item === app.items.at(-1) && app.status !== "idle"} />
  {/if}
{/snippet}

<div class="relative min-h-0 flex-1">
  <div bind:this={scroller} {onscroll} use:followEnd={() => atBottom} class="h-full overflow-y-auto">
    <div class="mx-auto flex w-full max-w-[820px] flex-col gap-1.5 px-6 py-6">
      {#each turns as turn (turn.key)}
        {#if turn.head}
          {@render renderItem(turn.head)}
        {/if}
        {#if turn.fold.length > 0}
          <div class="fold" class:is-open={openTurns[turn.key]}>
            <button
              class="fold-head"
              aria-expanded={openTurns[turn.key] === true}
              onclick={() => (openTurns[turn.key] = !openTurns[turn.key])}
            >
              <span>{foldLabel(turn.fold)}</span>
              <span class="caret" class:is-open={openTurns[turn.key]}><CaretRight /></span>
            </button>
            {#if openTurns[turn.key]}
              <div class="fold-list">
                {#each turn.fold as item (item.id)}
                  {@render renderItem(item)}
                {/each}
              </div>
            {/if}
          </div>
        {/if}
        {#each turn.rest as item (item.id)}
          {@render renderItem(item)}
        {/each}
      {/each}

      <!-- The stretch between asking the model and its first output draws
           nothing of its own, so the wait is shown where the reply will land,
           with the seconds it has taken. A compaction gets the squeeze mark,
           since it rewrites the conversation rather than waiting on a reply. -->
      {#if app.awaitingModel}
        <div class="step" aria-live="polite">
          {#if app.status === "compacting"}
            <span class="sq" aria-hidden="true"><i></i><i></i><i></i><i></i></span>
            <span class="text-muted">Compacting</span>
          {:else}
            <span class="spinner"></span>
            <span class="text-muted">Working on it</span>
          {/if}
          <!-- The clock is aria-hidden: a reader does not need it announced
               every second. -->
          {#if waited >= 2}
            <span class="num text-[12px] text-muted" aria-hidden="true">{clock}</span>
          {/if}
        </div>
      {/if}

      {#if app.steer}
        <div class="mt-4 flex flex-col items-end gap-1">
          <div class="bubble bubble-queued">
            {#if app.steer.files.length > 0}
              {@render attachments(app.steer.files)}
            {/if}
            {#if app.steer.text}
              <p class="bubble-text">{app.steer.text}</p>
            {/if}
          </div>
          <span class="text-[12px] text-muted">
            {app.status === "compacting" ? "Waiting for the compaction" : "Waiting for this turn"}
          </span>
        </div>
      {/if}
    </div>
  </div>

  {#if !atBottom}
    <button
      class="round absolute bottom-4 left-1/2 -translate-x-1/2 shadow-lg"
      aria-label="Scroll to the end"
      onclick={toBottom}
    >
      <ArrowDown />
    </button>
  {/if}
</div>
