<script lang="ts">
  import CheckSquare from "phosphor-svelte/lib/CheckSquare";
  import Question from "phosphor-svelte/lib/Question";
  import Square from "phosphor-svelte/lib/Square";
  import type { TranscriptItem } from "../api";
  import { asked } from "../question";
  import { answer } from "../state/app.svelte";

  let { item }: { item: Extract<TranscriptItem, { kind: "tool" }> } = $props();

  const question = $derived(asked(item.args));
  let chosen = $state<number[]>([]);
  let typed = $state("");

  function toggle(i: number): void {
    chosen = chosen.includes(i) ? chosen.filter((n) => n !== i) : [...chosen, i].sort();
  }

  function send(options: number[]): void {
    if (options.length === 0 && typed.trim() === "") return;
    answer(item, { options, text: typed.trim() });
  }
</script>

<div class="card overflow-hidden">
  <div class="flex items-center gap-2 px-4 pt-3 text-[13px]">
    <Question size={15} class="text-accent" />
    <span class="font-medium">{question.question}</span>
  </div>

  <div class="flex flex-col px-2 pt-2">
    {#each question.options as option, i (i)}
      <button
        class="popover-item"
        aria-pressed={question.multi ? chosen.includes(i) : undefined}
        onclick={() => (question.multi ? toggle(i) : send([i]))}
      >
        {#if question.multi}
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

  <div class="flex items-center gap-2 px-4 py-3">
    <input
      class="input !rounded-full"
      placeholder="Answer in your own words"
      bind:value={typed}
      onkeydown={(e) => e.key === "Enter" && send(chosen)}
    />
    <button
      class="chip chip-brand"
      disabled={chosen.length === 0 && typed.trim() === ""}
      onclick={() => send(chosen)}
    >
      Send
    </button>
  </div>
</div>
