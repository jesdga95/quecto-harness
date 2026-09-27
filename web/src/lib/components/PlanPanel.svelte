<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import TodoList from "./TodoList.svelte";
  import { app } from "../state/app.svelte";

  let open = $state(false);

  const todos = $derived(app.todos);
  const done = $derived(todos.filter((t) => t.status === "done").length);
  // The step in hand is what the closed row shows, since it is the one thing
  // worth reading while the turn runs.
  const current = $derived(todos.find((t) => t.status === "doing"));
</script>

{#if todos.length > 0}
  <div class="plan-dock">
    <button class="plan-head" onclick={() => (open = !open)}>
      <ListChecks />
      <span class="plan-now">{current?.text ?? "Plan"}</span>
      <span class="num text-[12px] text-muted">{done} of {todos.length}</span>
      <span class="caret" class:is-open={open}><CaretRight /></span>
    </button>

    {#if open}
      <div class="px-4 pb-3">
        <TodoList {todos} />
      </div>
    {/if}
  </div>
{/if}
