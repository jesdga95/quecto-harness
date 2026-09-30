<script lang="ts">
  import ArrowUUpLeft from "phosphor-svelte/lib/ArrowUUpLeft";
  import Composer from "./Composer.svelte";
  import PanelButton from "./PanelButton.svelte";
  import SidePanel from "./SidePanel.svelte";
  import Transcript from "./Transcript.svelte";
  import { activeSession, app, selectSession } from "../state/app.svelte";
  import { shortPath } from "../format";
  import { arriveComposer } from "../firstsend";

  const session = $derived(activeSession());
  const showPanel = $derived(app.panel);
  // Hidden rather than removed, so a message being typed survives the expand.
  const hideMain = $derived(showPanel && app.artifactsWide);
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
      <!-- The panel's own tabs say the same once it is open. -->
      {#if !showPanel}
        <div class="ml-auto flex shrink-0 items-center gap-2">
          <PanelButton />
        </div>
      {/if}
    </header>

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
