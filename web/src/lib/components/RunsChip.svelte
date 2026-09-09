<script lang="ts">
  import UsersThree from "phosphor-svelte/lib/UsersThree";
  import { app, toggleRuns } from "../state/app.svelte";

  const total = $derived(app.runs.length);
  const running = $derived(app.runs.filter((r) => r.status !== "idle").length);
</script>

{#if total > 0}
  <button
    class="chip chip-plain"
    class:chip-solid={app.runsOpen}
    onclick={toggleRuns}
    title="Agents working for this session"
  >
    <UsersThree />
    {#if running > 0}
      <span class="num text-[12px]">{running} running</span>
      <span class="spinner"></span>
    {:else}
      <span class="num text-[12px]">{total} {total === 1 ? "agent" : "agents"}</span>
    {/if}
  </button>
{/if}
