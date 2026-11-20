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
  import PlusCircle from "phosphor-svelte/lib/PlusCircleIcon";
  import SidebarSimple from "phosphor-svelte/lib/SidebarSimpleIcon";
  import { app, init, loadGit, newSession } from "./lib/state/app.svelte";
  import { isNewSessionKey, isSearchKey, SEARCH_EVENT } from "./lib/keys";
  import { overlayOpen } from "./lib/overlay";
  import { tick } from "svelte";

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

  // Below this width the sidebar leaves the page and opens over it instead.
  const NARROW = 860;
  let width = $state(window.innerWidth);
  const narrow = $derived(width < NARROW);
  let drawer = $state(false);

  // A hidden sidebar leaves the page; the header's top left then carries its
  // toggle and New session. Narrow, the toggle opens it over the page.
  const away = $derived(narrow || !app.sidebar);
  $effect(() => {
    document.documentElement.classList.toggle("sidebar-away", away);
  });

  function openSidebar() {
    if (narrow) drawer = true;
    else app.sidebar = true;
  }
  // Picking a session or widening the window puts the drawer away.
  $effect(() => {
    void app.sessionId;
    void narrow;
    drawer = false;
  });

  // Escape puts the drawer away. New session and search answer anywhere on
  // the page while no dialog is open; search brings the sidebar out first.
  async function pageKey(event: KeyboardEvent) {
    if (drawer && event.key === "Escape") drawer = false;
    if (setup || overlayOpen() || event.defaultPrevented) return;
    if (isNewSessionKey(event)) {
      event.preventDefault();
      void newSession();
    } else if (isSearchKey(event)) {
      event.preventDefault();
      if (away) openSidebar();
      await tick();
      window.dispatchEvent(new Event(SEARCH_EVENT));
    }
  }

  // Plan mode colours the whole page, overlays included, so it is marked on the root.
  $effect(() => {
    document.documentElement.classList.toggle("mode-plan", app.workMode === "plan");
  });
</script>

<svelte:window onfocus={refocus} onkeydown={pageKey} bind:innerWidth={width} />

{#if app.ready && setup}
  <Onboarding />
{:else if app.ready}
  <div class="flex h-screen bg-page text-ink">
    {#if !away}
      <Sidebar onsettings={() => (app.settingsAt = "general")} />
    {/if}
    <main class="relative isolate flex min-w-0 flex-1 flex-col">
      <Backdrop />
      {#if app.items.length === 0}
        <Hero />
      {:else}
        <SessionView />
      {/if}
      <!-- After the page, so its no-drag region outranks the header's drag
           region in the desktop window. -->
      {#if away}
        <div class="top-tools">
          <button class="icon-btn" aria-label="Open sidebar" title="Open sidebar" onclick={openSidebar}>
            <SidebarSimple />
          </button>
          <button class="icon-btn" aria-label="New session" title="New session" onclick={() => newSession()}>
            <PlusCircle />
          </button>
        </div>
      {/if}
    </main>
  </div>

  {#if narrow && drawer}
    <div class="drawer-scrim" role="presentation" onclick={() => (drawer = false)}></div>
    <div class="drawer">
      <Sidebar overlay onclose={() => (drawer = false)} onsettings={() => (app.settingsAt = "general")} />
    </div>
  {/if}

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
