<script lang="ts">
  import Hero from "./lib/components/Hero.svelte";
  import Onboarding from "./lib/components/Onboarding.svelte";
  import SessionView from "./lib/components/SessionView.svelte";
  import ConfirmDialog from "./lib/components/ConfirmDialog.svelte";
  import Settings from "./lib/components/Settings.svelte";
  import Agents from "./lib/components/Agents.svelte";
  import McpServers from "./lib/components/McpServers.svelte";
  import Backdrop from "./lib/components/Backdrop.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Toasts from "./lib/components/Toasts.svelte";
  import { app, init, loadGit } from "./lib/state/app.svelte";

  // Setup shows until there is an agent to answer with. An address that stops
  // answering later is not this screen's business: the fix is to switch agents
  // or edit one, and both are inside the app.
  const setup = $derived(app.agents.length === 0);

  void init();

  // Work done in an editor or a terminal while the tab was away shows when it
  // comes back.
  function refocus() {
    if (app.projectPath) void loadGit();
  }

  function closeAgents() {
    app.agentsOpen = false;
    app.backToSettings = "";
  }

  function closeMcp() {
    app.mcpOpen = false;
    app.backToSettings = "";
  }

  /** Closes a dialog Settings opened and reopens Settings where it was. */
  function backToSettings(close: () => void): () => void {
    return () => {
      const pane = app.backToSettings;
      close();
      app.settingsAt = pane;
    };
  }

  // Plan mode colours the whole page, overlays included, so it is marked on the root.
  $effect(() => {
    document.documentElement.classList.toggle("mode-plan", app.workMode === "plan");
  });
</script>

<svelte:window onfocus={refocus} />

{#if app.ready && setup}
  <Onboarding />
{:else if app.ready}
  <div class="flex h-screen min-w-[900px] bg-page text-ink">
    <Sidebar onsettings={() => (app.settingsAt = "general")} />
    <main class="relative isolate flex min-w-0 flex-1 flex-col">
      <Backdrop />
      {#if app.items.length === 0}
        <Hero />
      {:else}
        <SessionView />
      {/if}
    </main>
  </div>

  {#if app.settingsAt}
    <Settings start={app.settingsAt} onclose={() => (app.settingsAt = "")} />
  {/if}
  {#if app.agentsOpen}
    <Agents onclose={closeAgents} onback={app.backToSettings ? backToSettings(closeAgents) : undefined} />
  {/if}
  {#if app.mcpOpen}
    <McpServers onclose={closeMcp} onback={app.backToSettings ? backToSettings(closeMcp) : undefined} />
  {/if}
{:else}
  <div class="grid h-screen place-items-center text-[13px] text-muted">
    {app.error ? "" : "Loading"}
  </div>
{/if}

<!-- Sits over the page rather than in it, so a failure moves nothing. -->

<ConfirmDialog />
<Toasts />
