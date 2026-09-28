<script lang="ts">
  import Check from "phosphor-svelte/lib/Check";
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import Circle from "phosphor-svelte/lib/Circle";
  import type { Todo } from "../api";

  let { todos }: { todos: Todo[] } = $props();
</script>

<ul class="flex flex-col gap-1">
  {#each todos as todo, i (i)}
    <li class="todo" class:is-done={todo.status === "completed"} class:is-doing={todo.status === "in_progress"}>
      {#if todo.status === "completed"}
        <Check />
      {:else if todo.status === "in_progress"}
        <CaretRight />
      {:else}
        <Circle />
      {/if}
      <span>{todo.content}</span>
    </li>
  {/each}
</ul>
