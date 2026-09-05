<script lang="ts">
  import AddMenu from "./AddMenu.svelte";
  import ArrowUp from "phosphor-svelte/lib/ArrowUp";
  import EffortChip from "./EffortChip.svelte";
  import Paperclip from "phosphor-svelte/lib/Paperclip";
  import Square from "phosphor-svelte/lib/Square";
  import ModelChip from "./ModelChip.svelte";
  import PermissionChip from "./PermissionChip.svelte";
  import X from "phosphor-svelte/lib/X";
  import { app, pendingPermission, pickAttachment, send, stop } from "../state/app.svelte";
  import { fileName } from "../format";

  let { placeholder }: { placeholder: string } = $props();

  let text = $state("");
  let files = $state<string[]>([]);
  let field = $state<HTMLTextAreaElement | null>(null);

  // Every attachment is inlined into the prompt, which is what the harness
  // caps at five.
  const maxFiles = 5;

  // Grows with the message and stops at six lines, after which it scrolls.
  const maxLines = 6;

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
  const running = $derived(app.status !== "idle");
  const disabled = $derived(blocked || !app.sessionId);

  async function attach() {
    const path = await pickAttachment();
    if (path && !files.includes(path)) files = [...files, path];
  }

  function submit() {
    if (disabled || running || (!text.trim() && files.length === 0)) return;
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
      <div class="mb-2 flex flex-wrap gap-1.5">
        {#each files as path (path)}
          <span class="chip chip-solid cursor-default" title={path}>
            <Paperclip />
            <span class="max-w-[220px] truncate">{fileName(path)}</span>
            <button
              class="icon-btn -mr-1.5 h-5 w-5"
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
      {disabled}
    ></textarea>

    <div class="mt-2 flex items-center gap-2">
      <AddMenu up onattach={attach} canAttach={!disabled && files.length < maxFiles} />
      <PermissionChip />
      <EffortChip up />

      <div class="ml-auto flex items-center gap-2">
        {#if running}
          <span class="spinner" aria-label="Working"></span>
        {/if}
        <ModelChip />
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
