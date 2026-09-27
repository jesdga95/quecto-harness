<script lang="ts">
  import Hero from "./lib/components/Hero.svelte";
  import Onboarding from "./lib/components/Onboarding.svelte";
  import SessionView from "./lib/components/SessionView.svelte";
  import ConfirmDialog from "./lib/components/ConfirmDialog.svelte";
  import Settings from "./lib/components/Settings.svelte";
  import Agents from "./lib/components/Agents.svelte";
  import Backdrop from "./lib/components/Backdrop.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Toasts from "./lib/components/Toasts.svelte";
  import { app, init } from "./lib/state/app.svelte";

  let settings = $state(false);

  // Setup shows until there is an agent to answer with. An address that stops
  // answering later is not this screen's business: the fix is to switch agents
  // or edit one, and both are inside the app.
  const setup = $derived(app.agents.length === 0);

  void init();

  // Plan mode colours the whole page, overlays included, so it is marked on the root.
  $effect(() => {
    document.documentElement.classList.toggle("mode-plan", app.workMode === "plan");
  });
</script>

{#if app.ready && setup}
  <Onboarding />
{:else if app.ready}
  <div class="flex h-screen min-w-[900px] bg-page text-ink">
    <Sidebar onsettings={() => (settings = true)} />
    <main class="relative isolate flex min-w-0 flex-1 flex-col">
      <Backdrop />
      {#if app.items.length === 0}
        <Hero />
      {:else}
        <SessionView />
      {/if}
    </main>
  </div>

  {#if settings}
    <Settings onclose={() => (settings = false)} />
  {/if}
  {#if app.agentsOpen}
    <Agents onclose={() => (app.agentsOpen = false)} />
  {/if}
{:else}
  <div class="grid h-screen place-items-center text-[13px] text-muted">
    {app.error ? "" : "Loading"}
  </div>
{/if}

<!-- Sits over the page rather than in it, so a failure moves nothing. -->

<ConfirmDialog />
<Toasts />
