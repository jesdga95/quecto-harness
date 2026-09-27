<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import Sliders from "phosphor-svelte/lib/Sliders";
  import Robot from "phosphor-svelte/lib/Robot";
  import Popover from "./Popover.svelte";
  import { activeAgent, agentReady, app, setActiveAgent } from "../state/app.svelte";
  import { probeMessage } from "../format";

  let open = $state(false);
  const current = $derived(activeAgent());
  // Only an enabled agent can be picked, so the menu holds those.
  const choices = $derived(app.agents.filter((a) => a.enabled));
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
    class="chip chip-bare"
    aria-haspopup="listbox"
    aria-expanded={open}
    title={current ? `${current.name} · ${current.model}` : undefined}
    onclick={() => (open = !open)}
  >
    {#if current}
      <Robot class={problem ? "text-crit" : app.status === "loading" ? "text-warn" : ""} />
      <span class="max-w-[190px] truncate">{current.name}</span>
      {#if current.model}
        <span class="chip-model max-w-[160px] truncate">{current.model}</span>
      {/if}
    {:else}
      <Robot class={app.agents.length > 0 ? "text-crit" : ""} />
      <span>{app.agents.length > 0 ? "No agent is enabled" : "Add an agent"}</span>
    {/if}
    <CaretDown class="chip-caret" />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" up width={300}>
      {#if problem}
        <p class="px-[9px] py-1.5 text-[12px] text-crit">{problem}</p>
      {/if}
      {#each choices as agent (agent.id)}
        <button class="popover-item" onclick={() => pick(agent.id)}>
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
