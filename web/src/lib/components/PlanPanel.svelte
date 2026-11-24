<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRightIcon";
  import ListChecks from "phosphor-svelte/lib/ListChecksIcon";
  import TodoList from "./TodoList.svelte";
  import { app } from "../state/app.svelte";

  let open = $state(false);

  const todos = $derived(app.todos);
  const running = $derived(app.status !== "idle" && app.status !== "compacting");
  const done = $derived(todos.filter((t) => t.status === "completed").length);
  // The step in hand is what the closed row shows, since it is the one thing
  // worth reading while the turn runs.
  const current = $derived(todos.find((t) => t.status === "in_progress"));
</script>

{#if todos.length > 0}
  <div class="plan-dock">
    <button class="plan-head" onclick={() => (open = !open)}>
      {#if running && current}<span class="head-mark"><span class="spinner"></span></span>{:else}<span class="head-mark"><ListChecks /></span>{/if}
      <span class="plan-now">{current?.content ?? "Plan"}</span>
      <span class="num text-[12px] text-muted">{done}/{todos.length}</span>
      <span class="caret" class:is-open={open}><CaretRight /></span>
    </button>

    {#if open}
      <div class="px-5 pb-3">
        <TodoList {todos} live={running} />
      </div>
    {/if}
  </div>
{/if}
