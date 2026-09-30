<script lang="ts">
  import ArrowSquareOut from "phosphor-svelte/lib/ArrowSquareOut";
  import Prose from "./Prose.svelte";
  import StepRow from "./StepRow.svelte";
  import { app, openRun, selectSession } from "../state/app.svelte";
  import { followEnd } from "../stream.svelte";

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);

  const running = $derived(app.runs.filter((r) => r.status !== "idle").length);
  const shown = $derived(app.runs.find((r) => r.id === app.runId));

  // Follows the run while the reader is at the end, and stops the moment they
  // scroll up. The tail is measured as well as the count, since a reply and a
  // command still running both grow in place.
  const tail = $derived(app.runItems.at(-1));
  const tailLength = $derived.by(() => {
    if (!tail) return 0;
    if (tail.kind === "assistant" || tail.kind === "reasoning") return tail.text.length;
    if (tail.kind === "tool") return tail.output.length;
    return 0;
  });

  $effect(() => {
    void app.runItems.length;
    void tailLength;
    if (scroller && atBottom) scroller.scrollTop = scroller.scrollHeight;
  });

  function onscroll() {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 40;
  }

  // Shown with nothing followed, the tab follows the one still working, which
  // is what a reader opening it came for.
  $effect(() => {
    if (app.runId || app.runs.length === 0) return;
    const first = app.runs.find((r) => r.status !== "idle") ?? app.runs[0];
    if (first) void openRun(first.id);
  });
</script>

<div class="panel-path">
  <div class="panel-crumbs">
    <span class="crumb is-last">{running} of {app.runs.length} working</span>
  </div>
  {#if shown}
    <button
      class="icon-btn"
      aria-label="Open this agent's conversation"
      title="Open the conversation"
      onclick={() => void selectSession(shown.id)}
    >
      <ArrowSquareOut />
    </button>
  {/if}
</div>

<div class="artifact-body">
  <div
    bind:this={scroller}
    {onscroll}
    use:followEnd={() => atBottom}
    class="min-h-0 flex-1 overflow-y-auto px-2.5 py-3"
  >
    {#if shown}
      <div class="flex flex-col gap-1.5">
        {#each app.runItems as item (item.id)}
          {#if item.kind === "user"}
            <div class="run-said">{item.text}</div>
          {:else if item.kind === "assistant"}
            <div class="my-1">
              <Prose text={item.text} streaming={item.streaming} />
            </div>
          {:else if item.kind === "permission"}
            <!-- A subagent's card is drawn in the conversation that launched
                 it, so the row here would be a second copy of one answered
                 somewhere else. -->
          {:else}
            <StepRow {item} live={item === app.runItems.at(-1) && shown.status !== "idle"} />
          {/if}
        {/each}
      </div>
    {:else}
      <p class="px-2 text-[13px] text-muted">Pick an agent to see what it is doing.</p>
    {/if}
  </div>
</div>
