<script lang="ts">
  import AddMenu from "./AddMenu.svelte";
  import ArrowUp from "phosphor-svelte/lib/ArrowUp";
  import Square from "phosphor-svelte/lib/Square";
  import AgentChip from "./AgentChip.svelte";
  import ModeChip from "./ModeChip.svelte";
  import PermissionChip from "./PermissionChip.svelte";
  import X from "phosphor-svelte/lib/X";
  import FileIcon from "./FileIcon.svelte";
  import {
    app,
    pasteFile,
    blockingPermission,
    pendingQuestion,
    pickAttachments,
    findFiles,
    setWorkMode,
    send,
    stop,
  } from "../state/app.svelte";
  import { fileKind, fileName } from "../format";

  let { placeholder }: { placeholder: string } = $props();

  // Each mode asks for something different, so the empty field says what.
  const modeHint = $derived(app.workMode === "plan" ? "Describe the change to plan" : placeholder);

  let text = $state("");
  let files = $state<string[]>([]);
  let field = $state<HTMLTextAreaElement | null>(null);

  // Every attachment is inlined into the prompt, which is what the harness
  // caps at five.
  const maxFiles = 5;

  // Grows with the message and stops at six lines, after which it scrolls.
  const maxLines = 6;

  // Pasted text longer than this is attached as a file rather than left in
  // the field, where it would be too long to read or edit.
  const maxPastedText = 2048;

  // The name a pasted image is stored under, by what the clipboard says it
  // is, rather than by a name that may not match the bytes. Anything else is
  // refused when the message is sent.
  const imageNames: Record<string, string> = {
    "image/png": "Pasted image.png",
    "image/jpeg": "Pasted image.jpg",
    "image/gif": "Pasted image.gif",
    "image/webp": "Pasted image.webp",
  };

  function grow() {
    const el = field;
    if (!el) return;
    const line = parseFloat(getComputedStyle(el).lineHeight) || 20;
    const pad = el.offsetHeight - el.clientHeight + 8;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, Math.round(line * maxLines + pad)) + "px";
  }

  $effect(() => {
    text;
    grow();
  });
  // Both cards take their answer on the card itself, so the field waits for
  // whichever one is open.
  const asking = $derived(pendingQuestion() !== undefined);
  const blocked = $derived(asking || blockingPermission() !== undefined);
  // A compaction holds the session but is not offered a stop: it draws nothing
  // to take back. The send button stays, and what it takes joins the queue.
  const running = $derived(app.status !== "idle" && app.status !== "compacting");
  // Sending with no session creates one, so the field only needs a project.
  const disabled = $derived(blocked || (!app.sessionId && !app.projectPath));

  // The dialog takes several files at once, and the harness caps a message at
  // five, so what comes back past the cap is dropped rather than sent.
  async function attach() {
    const picked = await pickAttachments();
    const next = [...files];
    for (const path of picked) {
      if (!next.includes(path) && next.length < maxFiles) next.push(path);
    }
    files = next;
  }

  // A paste the field cannot hold: an image, which has no path of its own,
  // and text long enough to be a document. Both are stored by the harness and
  // attached by the path it gives back.
  async function onpaste(event: ClipboardEvent) {
    const data = event.clipboardData;
    if (!data || disabled) return;

    const images = [...data.items].filter(
      (item) => item.kind === "file" && imageNames[item.type] !== undefined,
    );
    if (images.length > 0) {
      event.preventDefault();
      for (const item of images) {
        const file = item.getAsFile();
        if (file) await addPasted(imageNames[item.type], file);
      }
      return;
    }

    const pasted = data.getData("text/plain");
    if (pasted.length <= maxPastedText) return;
    event.preventDefault();
    await addPasted("Pasted text.txt", new Blob([pasted], { type: "text/plain" }));
  }

  async function addPasted(name: string, blob: Blob) {
    if (files.length >= maxFiles) return;
    const path = await pasteFile(name, blob);
    if (path && !files.includes(path)) files = [...files, path];
  }

  // An @ before the caret, and the word after it, is a file being named. The
  // list under it comes from the project, and a choice puts the path in the
  // message for the model to read.
  let mention = $state<{ start: number; query: string } | null>(null);
  let hits = $state<string[]>([]);
  let pick = $state(0);
  let asked = 0;

  function readMention() {
    const el = field;
    if (!el) return;
    const before = text.slice(0, el.selectionStart ?? text.length);
    const match = /(^|\s)@([^\s@]*)$/.exec(before);
    if (!match) {
      mention = null;
      return;
    }
    const query = match[2] ?? "";
    if (mention && mention.query === query) return;
    mention = { start: before.length - query.length - 1, query };
    const ticket = ++asked;
    setTimeout(async () => {
      if (ticket !== asked) return;
      const found = await findFiles(query);
      if (ticket !== asked || !mention) return;
      hits = found;
      pick = 0;
    }, 90);
  }

  function choose(path: string) {
    const el = field;
    if (!mention || !el) return;
    const end = mention.start + 1 + mention.query.length;
    const insert = `@${path} `;
    text = text.slice(0, mention.start) + insert + text.slice(end);
    const caret = mention.start + insert.length;
    mention = null;
    hits = [];
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(caret, caret);
    });
  }

  function submit() {
    if (disabled || (!text.trim() && files.length === 0)) return;
    const value = text;
    const attached = files;
    mention = null;
    hits = [];
    text = "";
    files = [];
    void send(value, attached);
  }

  // Enter sends, Shift and Enter makes a line. While the file list is open,
  // the arrows move through it and Enter or Tab takes the one marked.
  function onkeydown(event: KeyboardEvent) {
    // Shift and Tab moves between planning and carrying out.
    if (event.key === "Tab" && event.shiftKey) {
      event.preventDefault();
      void setWorkMode(app.workMode === "plan" ? "execute" : "plan");
      return;
    }
    if (mention && hits.length > 0) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        pick = (pick + step + hits.length) % hits.length;
        return;
      }
      if (event.key === "Enter" || event.key === "Tab") {
        event.preventDefault();
        const path = hits[pick];
        if (path) choose(path);
        return;
      }
    }
    if (mention && event.key === "Escape") {
      event.preventDefault();
      mention = null;
      hits = [];
      return;
    }
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      submit();
    }
  }
</script>

<div class="w-full">
  <div class="composer px-4 pt-4 pb-3" class:is-plan={app.workMode === "plan"}>
    {#if files.length > 0}
      <div class="tiles mb-3">
        {#each files as path (path)}
          <span class="tile tile-doc" title={path}>
            <span class="tile-kind">{fileKind(path)}</span>
            <span class="tile-name">{fileName(path)}</span>
            <button
              class="tile-x"
              aria-label="Remove {fileName(path)}"
              onclick={() => (files = files.filter((f) => f !== path))}
            >
              <X />
            </button>
          </span>
        {/each}
      </div>
    {/if}

    <div class="relative">
    {#if mention && hits.length > 0}
      <div class="popover mention-list absolute right-0 bottom-full left-0 z-40 mb-3" role="listbox">
        {#each hits as path, i (path)}
          <button
            class="popover-item"
            class:is-on={i === pick}
            role="option"
            aria-selected={i === pick}
            onmousedown={(e) => {
              e.preventDefault();
              choose(path);
            }}
            onmouseenter={() => (pick = i)}
          >
            <FileIcon {path} size={15} />
            <span class="truncate">{path.split("/").pop()}</span>
            {#if path.includes("/")}
              <span class="ml-auto truncate pl-3 text-[12px] text-muted">{path.slice(0, path.lastIndexOf("/"))}</span>
            {/if}
          </button>
        {/each}
      </div>
    {/if}
    <textarea
      bind:this={field}
      class="textarea"
      rows="1"
      placeholder={asking
        ? "Answer the card above first"
        : blocked
          ? "Answer the permission card first"
          : modeHint}
      bind:value={text}
      {onkeydown}
      {onpaste}
      oninput={readMention}
      onclick={readMention}
      onblur={() => (mention = null)}
      {disabled}
    ></textarea>
    </div>

    <div class="mt-2 flex items-center gap-2">
      <AddMenu up onattach={attach} canAttach={!disabled && files.length < maxFiles} />
      <ModeChip />
      <PermissionChip />

      <div class="ml-auto flex items-center gap-2">
        <AgentChip />
        <!-- Stop keeps its place while a turn runs, and a message typed
             against that turn is sent from the button beside it. -->
        {#if running && (text.trim() || files.length > 0)}
          <button class="round" aria-label="Send" onclick={submit}>
            <ArrowUp />
          </button>
        {/if}
        {#if running}
          <button class="round round-brand" aria-label="Stop" onclick={() => stop()}>
            <Square weight="fill" />
          </button>
        {:else}
          <button
            class="round round-brand"
            aria-label="Send"
            onclick={submit}
            disabled={disabled || (!text.trim() && files.length === 0)}
          >
            <ArrowUp />
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
