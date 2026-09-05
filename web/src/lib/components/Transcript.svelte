<script lang="ts">
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import ArrowDown from "phosphor-svelte/lib/ArrowDown";
  import ArrowsLeftRight from "phosphor-svelte/lib/ArrowsLeftRight";
  import Copy from "phosphor-svelte/lib/Copy";
  import PencilSimple from "phosphor-svelte/lib/PencilSimple";
  import Warning from "phosphor-svelte/lib/Warning";
  import PermissionCard from "./PermissionCard.svelte";
  import Prose from "./Prose.svelte";
  import StepRow from "./StepRow.svelte";
  import type { Attached, TranscriptItem } from "../api";
  import { app, canResend, resend } from "../state/app.svelte";
  import { bytes, duration, fileKind, fileName, k } from "../format";

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);

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

  // Follows the stream while the reader is at the end, and stops following the
  // moment they scroll up.
  $effect(() => {
    void count;
    void app.steer?.text.length;
    void (tail && tail.kind === "assistant" ? tail.text.length : 0);
    if (scroller && atBottom) scroller.scrollTop = scroller.scrollHeight;
  });

  function onscroll() {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  }

  function toBottom() {
    atBottom = true;
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  }

  function copy(text: string) {
    void navigator.clipboard?.writeText(text);
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

<div class="relative min-h-0 flex-1">
  <div bind:this={scroller} {onscroll} class="h-full overflow-y-auto">
    <div class="mx-auto flex w-full max-w-[820px] flex-col gap-1.5 px-6 py-6">
      {#each app.items as item (item.id)}
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
                  disabled={!draft.trim() && item.files.length === 0}
                >
                  Send
                </button>
              </div>
            </div>
          </div>
        {:else if item.kind === "user"}
          <div class="mt-4 flex flex-col items-end gap-1">
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
              {#if item.text}
                <button class="icon-btn" aria-label="Copy message" onclick={() => copy(item.text)}>
                  <Copy />
                </button>
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
            <Prose text={item.text} streaming={item.streaming} />
            <!-- The cost arrives when the turn ends, so it is also what says
                 this block is the end of one rather than a fragment between
                 two tool calls. -->
            {#if item.cost}
              <div class="mt-0.5 flex items-center gap-2">
                <button
                  class="icon-btn"
                  aria-label="Copy reply"
                  onclick={() => copy(replies.get(item.id) ?? item.text)}
                >
                  <Copy />
                </button>
                <span class="num text-[12px] text-muted">
                  {k(item.cost.tokens)} tokens &middot; {duration(item.cost.ms)}
                  {#if item.cost.tokPerS > 0}
                    &middot; {Math.round(item.cost.tokPerS)} tok/s
                  {/if}
                </span>
              </div>
            {/if}
          </div>
        {:else if item.kind === "status"}
          <div class="flex items-center gap-2 px-2 py-1 text-[13px] text-muted">
            <ArrowsLeftRight size={15} class="shrink-0" />
            <span class="truncate">{item.text}</span>
          </div>
        {:else if item.kind === "error"}
          <div class="flex items-center gap-2 px-2 py-1 text-[13px] text-crit">
            <Warning size={15} class="shrink-0" />
            <span class="truncate">{item.message}</span>
          </div>
        {:else if item.kind === "permission" && item.outcome === null}
          <div class="my-2">
            <PermissionCard {item} />
          </div>
        {:else}
          <StepRow {item} />
        {/if}
      {/each}

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
          <span class="text-[12px] text-muted">Waiting for this turn</span>
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
