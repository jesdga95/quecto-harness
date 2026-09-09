<script lang="ts">
  import ChangesChip from "./ChangesChip.svelte";
  import ChangesPanel from "./ChangesPanel.svelte";
  import Composer from "./Composer.svelte";
  import ContextChip from "./ContextChip.svelte";
  import PlanPanel from "./PlanPanel.svelte";
  import Transcript from "./Transcript.svelte";
  import { activeSession, app } from "../state/app.svelte";
  import { shortPath } from "../format";

  const session = $derived(activeSession());
  const showChanges = $derived(app.changesOpen && app.changes.files.length > 0);
  // The chip draws once there is something to draw: a window to measure
  // against, or tokens counted where no window is known.
  const measured = $derived(
    app.usage.n_ctx > 0 || app.usage.prompt_tokens + app.usage.completion_tokens > 0,
  );
</script>

<div class="flex min-h-0 flex-1">
  <div class="flex min-w-0 flex-1 flex-col">
    <header class="flex h-[56px] shrink-0 items-center gap-3 px-6">
      <div class="flex min-w-0 items-baseline gap-3">
        <h1 class="max-w-[420px] truncate text-[15px] font-medium">{session?.title ?? "Session"}</h1>
        <span class="truncate text-[12.5px] text-muted">{shortPath(app.projectPath)}</span>
      </div>
      <div class="ml-auto flex shrink-0 items-center gap-2">
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
  {/if}
</div>
