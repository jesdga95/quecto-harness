<script lang="ts">
  import type { Todo } from "../api";

  // live is set while the turn runs, so the step in hand spins only then.
  let { todos, live = false }: { todos: Todo[]; live?: boolean } = $props();
</script>

<ul class="flex flex-col gap-1">
  {#each todos as todo, i (i)}
    <li class="todo" class:is-done={todo.status === "completed"} class:is-doing={todo.status === "in_progress"}>
      {#if todo.status === "completed"}
        <span class="box is-done"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round" width="8" height="8" aria-hidden="true"><path d="M20 6 9 17l-5-5" /></svg></span>
      {:else if todo.status === "in_progress" && live}
        <span class="spinner"></span>
      {:else if todo.status === "in_progress"}
        <span class="box is-doing"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round" width="10" height="10" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg></span>
      {:else}
        <span class="box"></span>
      {/if}
      <span>{todo.content}</span>
    </li>
  {/each}
</ul>
