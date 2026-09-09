<script lang="ts">
  import { answer, dialog } from "../state/confirm.svelte";
  import { pushOverlay } from "../overlay";

  let confirmButton = $state<HTMLButtonElement | null>(null);

  // The destructive action is focused, so Enter answers and Escape cancels.
  $effect(() => {
    if (dialog.current) confirmButton?.focus();
  });

  $effect(() => {
    if (!dialog.current) return;
    return pushOverlay(() => answer(false));
  });
</script>

{#if dialog.current}
  <div
    class="fixed inset-0 z-60 grid place-items-center bg-[rgb(15_23_42/0.42)] p-6"
    role="presentation"
    onclick={(e) => e.target === e.currentTarget && answer(false)}
  >
    <div class="card w-full max-w-[400px] p-5" role="alertdialog" aria-modal="true">
      <h2 class="text-[15px] font-medium">{dialog.current.title}</h2>
      {#if dialog.current.body}
        <p class="mt-1.5 text-[13px] text-ink-2">{dialog.current.body}</p>
      {/if}

      <div class="mt-5 flex justify-end gap-2">
        <button class="btn btn-ghost" onclick={() => answer(false)}>Cancel</button>
        <button
          bind:this={confirmButton}
          class="btn"
          class:btn-danger={dialog.current.destructive !== false}
          class:btn-primary={dialog.current.destructive === false}
          onclick={() => answer(true)}
        >
          {dialog.current.confirmLabel ?? "Confirm"}
        </button>
      </div>
    </div>
  </div>
{/if}
