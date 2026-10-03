<script lang="ts">
  import CheckSquare from "phosphor-svelte/lib/CheckSquare";
  import Square from "phosphor-svelte/lib/Square";
  import type { TranscriptItem } from "../api";
  import { answer, stop } from "../state/app.svelte";
  import { asked } from "../question";

  // The body of the composer while a call waits on the reader. The field
  // becomes the typed answer and the transcript keeps only the call's row.
  let { item }: { item: Extract<TranscriptItem, { kind: "tool" }> } = $props();

  const questions = $derived(asked(item.args));
  const many = $derived(questions.length > 1);
  // The question the turn is open on. The backend reveals one at a time, so
  // what is before it is settled and what is after it has not been asked.
  const current = $derived(Math.min(item.askIndex ?? 0, Math.max(questions.length - 1, 0)));
  const cur = $derived(questions[current] ?? { question: "", options: [], multi: false });
  const last = $derived(current >= questions.length - 1);
  // Between one answer and the next question the call has no id to answer.
  const open = $derived(item.askId !== undefined);

  let chosen = $state<number[]>([]);
  let typed = $state("");
  let input = $state<HTMLInputElement | null>(null);

  // The field takes the keyboard as each question opens, unless the reader
  // has put it somewhere else.
  $effect(() => {
    if (!open || !input) return;
    const at = document.activeElement;
    if (!at || at === document.body) input.focus();
  });

  function dotClass(i: number): string {
    if (i < current) return "h-1.5 w-1.5 rounded-full bg-accent";
    if (i === current) return "h-1.5 w-4 rounded-full bg-accent";
    return "h-1.5 w-1.5 rounded-full bg-line-strong";
  }

  function toggle(i: number): void {
    chosen = chosen.includes(i) ? chosen.filter((n) => n !== i) : [...chosen, i].sort();
  }

  function send(options: number[]): void {
    if (!open || (options.length === 0 && typed.trim() === "")) return;
    const a = { options, text: typed.trim() };
    chosen = [];
    typed = "";
    void answer(item, a);
  }
</script>

<div class:opacity-60={!open}>
  {#if many}
    <div class="flex items-center gap-2 text-[12px] text-muted">
      <span>Question {current + 1} of {questions.length}</span>
      <span class="ml-auto flex items-center gap-1">
        {#each questions as _q, i (i)}
          <span class={dotClass(i)}></span>
        {/each}
      </span>
    </div>
  {/if}

  <div class="text-[15px] font-medium" class:pt-1={many}>{cur.question}</div>

  <div class="-mx-[9px] flex flex-col pt-1.5">
    {#each cur.options as option, i (i)}
      <button
        class="popover-item"
        aria-pressed={cur.multi ? chosen.includes(i) : undefined}
        disabled={!open}
        onclick={() => (cur.multi ? toggle(i) : send([i]))}
      >
        {#if cur.multi}
          {#if chosen.includes(i)}
            <CheckSquare class="!text-accent" />
          {:else}
            <Square />
          {/if}
        {/if}
        <span class="min-w-0 flex-1">
          <span class="block">{option.label}</span>
          {#if option.description}
            <span class="block text-[12px] text-muted">{option.description}</span>
          {/if}
        </span>
      </button>
    {/each}
  </div>

  <div class="mt-2 flex items-center gap-2 border-t border-hair pt-3">
    <input
      bind:this={input}
      class="min-w-0 flex-1 border-0 bg-transparent py-1.5 text-[15px] text-ink outline-none placeholder:text-muted"
      placeholder="Or answer in your own words"
      bind:value={typed}
      disabled={!open}
      onkeydown={(e) => e.key === "Enter" && send(chosen)}
    />
    <button
      class="chip chip-brand chip-tall"
      disabled={!open || (chosen.length === 0 && typed.trim() === "")}
      onclick={() => send(chosen)}
    >
      {many && !last ? "Next" : "Send"}
    </button>
    <button class="round send is-stop" aria-label="Stop" onclick={() => stop()}>
      <Square weight="fill" />
    </button>
  </div>
</div>
