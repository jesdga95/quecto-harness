<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import Sliders from "phosphor-svelte/lib/Sliders";
  import Popover from "./Popover.svelte";
  import StatusDot from "./StatusDot.svelte";
  import type { ModelStatus } from "../api";
  import { activeAgent, agentReady, app, setActiveAgent } from "../state/app.svelte";
  import { probeMessage } from "../format";

  let open = $state(false);
  const current = $derived(activeAgent());
  // The harness is reading the model whatever the cached list says.
  const status = $derived<ModelStatus>(
    app.status === "loading"
      ? "loading"
      : (app.models.find((m) => m.id === current?.model)?.status ?? "unloaded"),
  );
  const problem = $derived(
    agentReady()
      ? ""
      : probeMessage(
          {
            models: app.models,
            detectError: app.detectError,
            detectKind: app.detectKind,
            modelsError: app.modelsError,
            modelsKind: app.modelsKind,
          },
          (current?.key ?? "") !== "",
        ),
  );

  function pick(id: string) {
    void setActiveAgent(id);
    open = false;
  }

  function manage() {
    open = false;
    app.agentsOpen = true;
  }
</script>

<div class="relative">
  <button
    class="chip"
    aria-haspopup="listbox"
    aria-expanded={open}
    title={current ? `${current.name} · ${current.model}` : undefined}
    onclick={() => (open = !open)}
  >
    {#if current}
      <StatusDot {status} />
      <span class="max-w-[190px] truncate">{current.name}</span>
    {:else}
      <span>Add an agent</span>
    {/if}
    <CaretDown />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" up width={300}>
      {#if problem}
        <p class="px-[9px] py-1.5 text-[12px] text-crit">{problem}</p>
      {/if}
      {#each app.agents as agent (agent.id)}
        <button class="popover-item" onclick={() => pick(agent.id)}>
          {#if agent.id === current?.id}
            <StatusDot {status} />
          {:else}
            <span class="dot"></span>
          {/if}
          <span class="min-w-0 flex-1">
            <span class="block truncate text-left">{agent.name}</span>
            <span class="block truncate text-left text-[12px] text-muted">
              {agent.model || "No model chosen"}
            </span>
          </span>
          {#if agent.id === current?.id}
            <Check size={14} class="text-accent" />
          {/if}
        </button>
      {/each}
      <button class="popover-item text-ink-2" onclick={manage}>
        <Sliders size={15} />
        Manage agents
      </button>
    </Popover>
  {/if}
</div>
