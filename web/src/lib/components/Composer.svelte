<script lang="ts">
  import AddMenu from "./AddMenu.svelte";
  import ArrowUp from "phosphor-svelte/lib/ArrowUp";
  import EffortChip from "./EffortChip.svelte";
  import Square from "phosphor-svelte/lib/Square";
  import ModelChip from "./ModelChip.svelte";
  import PermissionChip from "./PermissionChip.svelte";
  import X from "phosphor-svelte/lib/X";
  import {
    app,
    pasteFile,
    pendingPermission,
    pickAttachments,
    send,
    stop,
  } from "../state/app.svelte";
  import { fileKind, fileName } from "../format";

  let { placeholder }: { placeholder: string } = $props();

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
  const blocked = $derived(pendingPermission() !== undefined);
  // Loading is the model being read, not a turn: there is nothing to stop and
  // nothing for a message to join. The field stays open, since a message can be
  // written while it loads; only sending waits.
  const loading = $derived(app.status === "loading");
  // A compaction holds the session but is not offered a stop: it draws nothing
  // to take back. The send button stays, and what it takes joins the queue.
  const running = $derived(app.status !== "idle" && !loading && app.status !== "compacting");
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

  function submit() {
    // Sending waits for the model, so the field keeps what was typed rather
    // than clearing it into a message that is refused.
    if (disabled || loading || (!text.trim() && files.length === 0)) return;
    const value = text;
    const attached = files;
    text = "";
    files = [];
    void send(value, attached);
  }

  // Enter sends, Shift and Enter makes a line.
  function onkeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      submit();
    }
  }
</script>

<div class="w-full">
  <div class="composer px-4 pt-4 pb-3">
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

    <textarea
      bind:this={field}
      class="textarea"
      rows="1"
      placeholder={blocked ? "Answer the permission card first" : placeholder}
      bind:value={text}
      {onkeydown}
      {onpaste}
      {disabled}
    ></textarea>

    <div class="mt-2 flex items-center gap-2">
      <AddMenu up onattach={attach} canAttach={!disabled && files.length < maxFiles} />
      <PermissionChip />
      <EffortChip up />

      <div class="ml-auto flex items-center gap-2">
        <ModelChip />
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
            disabled={disabled || loading || (!text.trim() && files.length === 0)}
          >
            <ArrowUp />
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
