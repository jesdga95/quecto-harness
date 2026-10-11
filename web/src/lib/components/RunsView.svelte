<script lang="ts">
  import ArrowSquareOut from "phosphor-svelte/lib/ArrowSquareOut";
  import UsersThree from "phosphor-svelte/lib/UsersThree";
  import Prose from "./Prose.svelte";
  import StepRow from "./StepRow.svelte";
  import { app, openRun, selectSession } from "../state/app.svelte";
  import type { Child } from "../api";
  import { statusLabel } from "../format";
  import { followEnd } from "../stream.svelte";

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);

  const running = $derived(app.runs.filter((r) => r.status !== "idle").length);
  const shown = $derived(app.runs.find((r) => r.id === app.runId));

  // The sidebar's marks: working, held on the user, or at rest.
  function mark(run: Child): string {
    if (run.status === "waiting") return "is-waiting";
    return run.status === "idle" ? "" : "is-working";
  }

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
  // is what a reader opening it came for. While the list alone is asked for,
  // nothing is followed and nothing is opened again.
  $effect(() => {
    if (app.listed || app.runId || app.runs.length === 0) return;
    const first = app.runs.find((r) => r.status !== "idle") ?? app.runs[0];
    if (first) void openRun(first.id);
  });
</script>

{#if app.runs.length === 0}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">No agents</span></div>
  </div>
  <div class="dock-empty">
    <div class="dock-empty-icon"><UsersThree size={21} /></div>
    <h3>No agents yet</h3>
    <p>When the model hands a task to another agent, its work shows here while the conversation carries on.</p>
  </div>
{:else}
  <div class="panel-path">
    <div class="panel-crumbs">
      <span class="crumb" class:is-last={running > 0}>
        {running > 0 ? `${running} working, ` : ""}{app.runs.length} this session
      </span>
    </div>
  </div>

  <!-- One agent needs no list of one, until the reader asks for the list: then
       it is the only way back to it. -->
  {#if app.runs.length > 1 || app.listed}
    <div class="dock-list" class:is-alone={app.listed}>
      {#each app.runs as run (run.id)}
        <div class="dock-row" class:is-on={run.id === app.runId}>
          <span class="state-mark {mark(run)}" role="status" aria-label={statusLabel(run.status)}></span>
          <button class="dock-row-main" title={run.title} onclick={() => void openRun(run.id)}>
            <span class="dock-row-title">{run.title}</span>
            <span class="dock-row-sub">{run.agent || "Agent"}</span>
          </button>
          <span class="dock-row-side" class:text-warn={run.status === "waiting"}>
            {run.status === "waiting" ? "Pending" : statusLabel(run.status)}
          </span>
        </div>
      {/each}
    </div>
  {/if}

  <!-- The list alone leaves no room for a body: the list is the section then. -->
  {#if !app.listed}
    <div class="artifact-body">
      {#if shown}
        <div class="dock-detail-head">
          <div class="dock-detail-meta">
            {#if app.runs.length === 1}
              <span class="state-mark {mark(shown)}" role="status" aria-label={statusLabel(shown.status)}></span>
            {/if}
            <span class="truncate text-ink">{shown.agent || "Agent"}</span>
            {#if app.runs.length === 1}
              <span class:text-warn={shown.status === "waiting"}>
                {shown.status === "waiting" ? "Pending" : statusLabel(shown.status)}
              </span>
            {/if}
          </div>
          <button
            class="dock-action"
            aria-label="Open this agent's conversation"
            title="Open the conversation"
            onclick={() => void selectSession(shown.id)}
          >
            <ArrowSquareOut size={14} />Open
          </button>
        </div>
      {/if}
      <div
        bind:this={scroller}
        {onscroll}
        use:followEnd={() => atBottom}
        class="min-h-0 flex-1 overflow-y-auto px-4 pt-1 pb-5"
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
          <p class="text-[13px] text-muted">Pick an agent to see what it is doing.</p>
        {/if}
      </div>
    </div>
  {/if}
{/if}
