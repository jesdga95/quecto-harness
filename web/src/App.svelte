<script lang="ts">
  import Hero from "./lib/components/Hero.svelte";
  import Onboarding from "./lib/components/Onboarding.svelte";
  import SessionView from "./lib/components/SessionView.svelte";
  import ConfirmDialog from "./lib/components/ConfirmDialog.svelte";
  import Settings from "./lib/components/Settings.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import Toasts from "./lib/components/Toasts.svelte";
  import { app, init } from "./lib/state/app.svelte";

  let settings = $state(false);
  let skipped = $state(false);

  // Setup shows until a model server answers, which also covers a wrong
  // address later rather than only the first run.
  const setup = $derived(!skipped && (app.detectError !== "" || app.models.length === 0));

  void init();
</script>

{#if app.ready && setup}
  <Onboarding ondone={() => (skipped = true)} />
{:else if app.ready}
  <div class="flex h-screen min-w-[900px] bg-page text-ink">
    <Sidebar onsettings={() => (settings = true)} />
    <main class="flex min-w-0 flex-1 flex-col">
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
{:else}
  <div class="grid h-screen place-items-center text-[13px] text-muted">
    {app.error ? "" : "Loading"}
  </div>
{/if}

<!-- Sits over the page rather than in it, so a failure moves nothing. -->

<ConfirmDialog />
<Toasts />
