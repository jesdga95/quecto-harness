<script lang="ts">
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import type { TranscriptItem } from "../api";
  import { PLAN_ID } from "../artifacts";
  import { answer, app, showArtifact } from "../state/app.svelte";

  let { item }: { item: Extract<TranscriptItem, { kind: "tool" }> } = $props();

  const plan = $derived(app.draft);
  // Keep planning opens a field for what to change, which is what the model
  // is handed back.
  let revising = $state(false);
  let typed = $state("");

  function approve(): void {
    answer(item, { options: [0], text: "" });
  }

  function revise(): void {
    if (typed.trim() === "") return;
    answer(item, { options: [1], text: typed.trim() });
  }

  function focus(node: HTMLInputElement) {
    node.focus();
  }
</script>

<div class="card plan-card overflow-hidden">
  <div class="flex items-center gap-2 px-4 pt-3 text-[13px]">
    <ListChecks size={15} class="text-accent" />
    <span class="font-medium">{plan?.title || "Plan"}</span>
    {#if plan}
      <span class="ml-auto text-[12px] text-muted">
        {plan.steps.length}
        {plan.steps.length === 1 ? "step" : "steps"}
      </span>
    {/if}
  </div>

  {#if plan?.goal}
    <p class="px-4 pt-2 text-[13px] text-ink-2">{plan.goal}</p>
  {/if}

  <div class="flex items-center gap-2 px-4 py-3">
    {#if revising}
      <input
        class="input input-tall !rounded-full"
        placeholder="Say what to change in the plan"
        bind:value={typed}
        onkeydown={(e) => e.key === "Enter" && revise()}
        use:focus
      />
      <button class="chip chip-tall" onclick={() => (revising = false)}>Cancel</button>
      <button class="chip chip-brand chip-tall" disabled={typed.trim() === ""} onclick={revise}>
        Send
      </button>
    {:else}
      <button class="chip chip-brand chip-tall" onclick={approve}>Approve and execute</button>
      <button class="chip chip-tall" onclick={() => (revising = true)}>Keep planning</button>
      <button class="chip chip-plain chip-tall ml-auto" onclick={() => showArtifact(PLAN_ID)}>
        Open plan
      </button>
    {/if}
  </div>
</div>
