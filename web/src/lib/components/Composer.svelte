<script lang="ts">
  import AddMenu from "./AddMenu.svelte";
  import ArrowUp from "phosphor-svelte/lib/ArrowUp";
  import EffortChip from "./EffortChip.svelte";
  import Square from "phosphor-svelte/lib/Square";
  import ModelChip from "./ModelChip.svelte";
  import PermissionChip from "./PermissionChip.svelte";
  import { app, pendingPermission, send, stop } from "../state/app.svelte";

  let { placeholder }: { placeholder: string } = $props();

  let text = $state("");
  let field = $state<HTMLTextAreaElement | null>(null);

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

  function submit() {
    if (disabled || running || !text.trim()) return;
    const value = text;
    text = "";
    void send(value);
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
      <AddMenu up />
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
            disabled={disabled || !text.trim()}
          >
            <ArrowUp />
          </button>
        {/if}
      </div>
    </div>
  </div>
</div>
