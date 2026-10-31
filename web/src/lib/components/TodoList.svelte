<script lang="ts">
  import Check from "phosphor-svelte/lib/Check";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import CircleDashed from "phosphor-svelte/lib/CircleDashed";
  import type { Todo } from "../api";

  // live is set while the turn runs, so the step in hand spins only then.
  let { todos, live = false }: { todos: Todo[]; live?: boolean } = $props();
</script>

<ul class="flex flex-col gap-1">
  {#each todos as todo, i (i)}
    <li class="todo" class:is-done={todo.status === "completed"} class:is-doing={todo.status === "in_progress"}>
      {#if todo.status === "completed"}
        <Check />
      {:else if todo.status === "in_progress" && live}
        <span class="spinner"></span>
      {:else if todo.status === "in_progress"}
        <CaretRight />
      {:else}
        <CircleDashed />
      {/if}
      <span>{todo.content}</span>
    </li>
  {/each}
</ul>
