<script lang="ts">
  import ArrowUUpLeft from "phosphor-svelte/lib/ArrowUUpLeftIcon";
  import Composer from "./Composer.svelte";
  import PanelButton from "./PanelButton.svelte";
  import SidePanel from "./SidePanel.svelte";
  import Transcript from "./Transcript.svelte";
  import { activeSession, app, NO_FOLDER, NO_FOLDER_LABEL, selectSession } from "../state/app.svelte";
  import { shortPath } from "../format";
  import { arriveComposer } from "../firstsend";

  const session = $derived(activeSession());
  const launcher = $derived(app.opened && app.opened.id === app.sessionId && !app.opened.launcherGone ? app.opened.parent : "");
  const showPanel = $derived(app.panel);
  // Below this width the conversation and the panel cannot sit side by side,
  // so an open panel takes all of it.
  const SIDE_BY_SIDE = 760;
  let width = $state(0);
  $effect(() => {
    app.dockCrowded = width > 0 && width < SIDE_BY_SIDE;
  });
  // Hidden rather than removed, so a message being typed survives the expand.
  const hideMain = $derived(showPanel && (app.artifactsWide || app.dockCrowded));
</script>

<!-- One header over the conversation and the panel. Its buttons open and
     close the panel's sections, so the panel carries no tabs of its own. -->
<div class="flex min-h-0 min-w-0 flex-1 flex-col" bind:clientWidth={width}>
  <header class="session-head">
    {#if launcher}
      <button
        class="icon-btn shrink-0"
        aria-label="Back to the conversation that launched this"
        onclick={() => void selectSession(launcher)}
      >
        <ArrowUUpLeft size={16} />
      </button>
    {/if}
    <div class="flex min-w-0 items-baseline gap-3">
      <h1 class="max-w-[420px] truncate text-[15px] font-medium">{session?.title ?? "Session"}</h1>
      {#if launcher}
        <span class="shrink-0 text-[12.5px] text-muted">Working for another agent</span>
      {:else}
        <span class="session-path truncate text-[12.5px] text-muted"
          >{app.projectPath === NO_FOLDER ? NO_FOLDER_LABEL : shortPath(app.projectPath)}</span
        >
      {/if}
    </div>
    <div class="ml-auto flex shrink-0 items-center">
      <PanelButton />
    </div>
  </header>

  <div class="flex min-h-0 flex-1">
    <div class="flex min-w-0 flex-1 flex-col" class:hidden={hideMain}>
      <Transcript />

      <div class="shrink-0 px-6 pt-2 pb-5">
        <div class="mx-auto w-full max-w-[820px]" use:arriveComposer>
          <Composer placeholder="Message the agent, @ to quote a file" compact={showPanel} />
        </div>
      </div>
    </div>

    {#if showPanel}
      <SidePanel />
    {/if}
  </div>
</div>
