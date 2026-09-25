<script lang="ts">
  import ArrowUUpLeft from "phosphor-svelte/lib/ArrowUUpLeft";
  import ArtifactsChip from "./ArtifactsChip.svelte";
  import ArtifactsPanel from "./ArtifactsPanel.svelte";
  import ChangesChip from "./ChangesChip.svelte";
  import ChangesPanel from "./ChangesPanel.svelte";
  import Composer from "./Composer.svelte";
  import ContextChip from "./ContextChip.svelte";
  import PlanPanel from "./PlanPanel.svelte";
  import RunsChip from "./RunsChip.svelte";
  import RunsPanel from "./RunsPanel.svelte";
  import Transcript from "./Transcript.svelte";
  import { activeSession, app, artifacts, selectSession } from "../state/app.svelte";
  import { shortPath } from "../format";

  const session = $derived(activeSession());
  const showChanges = $derived(app.dock === "changes" && app.changes.files.length > 0);
  const showRuns = $derived(app.dock === "runs" && app.runs.length > 0);
  const showArtifacts = $derived(app.dock === "artifacts" && artifacts().length > 0);
  // Hidden rather than removed, so a message being typed survives the expand.
  const hideMain = $derived(showArtifacts && app.artifactsWide);
  // The chip draws once there is something to draw: a window to measure
  // against, or tokens counted where no window is known.
  const measured = $derived(
    app.usage.n_ctx > 0 || app.usage.prompt_tokens + app.usage.completion_tokens > 0,
  );
</script>

<div class="flex min-h-0 flex-1">
  <div class="flex min-w-0 flex-1 flex-col" class:hidden={hideMain}>
    <header class="flex h-[56px] shrink-0 items-center gap-3 px-6">
      {#if session?.parent}
        <button
          class="icon-btn shrink-0"
          aria-label="Back to the conversation that launched this"
          onclick={() => void selectSession(session.parent)}
        >
          <ArrowUUpLeft size={16} />
        </button>
      {/if}
      <div class="flex min-w-0 items-baseline gap-3">
        <h1 class="max-w-[420px] truncate text-[15px] font-medium">{session?.title ?? "Session"}</h1>
        {#if session?.parent}
          <span class="shrink-0 text-[12.5px] text-muted">Working for another agent</span>
        {:else}
          <span class="truncate text-[12.5px] text-muted">{shortPath(app.projectPath)}</span>
        {/if}
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2">
        <ArtifactsChip />
        <RunsChip />
        <ChangesChip />
        {#if measured}
          <ContextChip />
        {/if}
      </div>
    </header>

    <Transcript />

    <div class="shrink-0 px-6 pt-2 pb-5">
      <div class="mx-auto w-full max-w-[820px]">
        <PlanPanel />
        <Composer placeholder="Message the agent" />
      </div>
    </div>
  </div>

  {#if showChanges}
    <ChangesPanel />
  {:else if showRuns}
    <RunsPanel />
  {:else if showArtifacts}
    <ArtifactsPanel />
  {/if}
</div>
