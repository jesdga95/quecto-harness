<script lang="ts">
  import ArrowCounterClockwise from "phosphor-svelte/lib/ArrowCounterClockwiseIcon";
  import CaretDown from "phosphor-svelte/lib/CaretDownIcon";
  import CaretLeft from "phosphor-svelte/lib/CaretLeftIcon";
  import CaretRight from "phosphor-svelte/lib/CaretRightIcon";
  import Check from "phosphor-svelte/lib/CheckIcon";
  import Sliders from "phosphor-svelte/lib/SlidersIcon";
  import Robot from "phosphor-svelte/lib/RobotIcon";
  import Popover from "./Popover.svelte";
  import type { Effort } from "../api";
  import {
    activeAgent,
    agentReady,
    app,
    effectiveEffort,
    setActiveAgent,
    setSessionEffort,
  } from "../state/app.svelte";
  import { effortHint, effortLabel, probeMessage } from "../format";

  const EFFORTS: Effort[] = ["auto", "off", "low", "medium", "high", "xhigh"];

  let open = $state(false);
  // The menu shows its two rows, or the list one of them opened in place.
  let view = $state<"menu" | "agents" | "effort">("menu");
  const current = $derived(activeAgent());
  // Only an enabled agent can be picked, so the menu holds those.
  const choices = $derived(app.agents.filter((a) => a.enabled));
  const effort = $derived(effectiveEffort());
  const overridden = $derived(app.sessionEffort !== "");
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

  function toggle() {
    open = !open;
    view = "menu";
  }

  function pick(id: string) {
    void setActiveAgent(id);
    open = false;
  }

  function pickEffort(value: Effort) {
    void setSessionEffort(value);
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
    aria-haspopup="menu"
    aria-expanded={open}
    title={current ? `${current.name} · ${current.model}` : undefined}
    onclick={toggle}
  >
    {#if current}
      <Robot class={problem ? "text-crit" : app.status === "loading" ? "text-warn" : ""} />
      <span class="max-w-[190px] truncate">{current.name}</span>
      {#if effort}
        <span class="chip-effort {overridden ? 'text-accent' : 'text-muted'}">{effortLabel(effort)}</span>
      {/if}
    {:else}
      <Robot class={app.agents.length > 0 ? "text-crit" : ""} />
      <span>{app.agents.length > 0 ? "No agent is enabled" : "Add an agent"}</span>
    {/if}
    <CaretDown class="chip-caret" />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} align="right" up width={250}>
      {#if view === "menu"}
        {#if problem}
          <p class="px-[9px] py-1.5 text-[12px] text-crit">{problem}</p>
        {/if}
        <button class="popover-item" onclick={() => (view = "agents")}>
          <span class="flex-1">Agent</span>
          <span class="max-w-[140px] truncate text-muted">{current?.name ?? "None"}</span>
          <CaretRight />
        </button>
        <button class="popover-item" disabled={!current} onclick={() => (view = "effort")}>
          <span class="flex-1">Effort</span>
          <span class={overridden ? "text-accent" : "text-muted"}>{effort ? effortLabel(effort) : "None"}</span>
          <CaretRight />
        </button>
        {#if overridden}
          <button class="popover-item text-ink-2" onclick={() => void setSessionEffort("")}>
            <span class="flex-1">Reset to the agent's effort</span>
            <ArrowCounterClockwise />
          </button>
        {/if}
        <div class="popover-sep"></div>
        <button class="popover-item text-ink-2" onclick={manage}>
          <span class="flex-1">Manage agents</span>
          <Sliders />
        </button>
      {:else}
        <button class="popover-item text-ink-2" onclick={() => (view = "menu")}>
          <CaretLeft />
          <span class="flex-1">{view === "agents" ? "Agent" : "Effort for this session"}</span>
        </button>
        <div class="popover-sep"></div>
        {#if view === "agents"}
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
        {:else}
          {#each EFFORTS as value (value)}
            <button class="popover-item" onclick={() => pickEffort(value)}>
              <span class="min-w-0 flex-1">
                <span class="block text-left">
                  {effortLabel(value)}
                  {#if value === current?.effort}
                    <span class="ml-1 text-[11px] text-muted">Default</span>
                  {/if}
                </span>
                <span class="block text-left text-[12px] text-muted">{effortHint(value)}</span>
              </span>
              {#if value === effort}
                <Check size={14} class="text-accent" />
              {/if}
            </button>
          {/each}
        {/if}
      {/if}
    </Popover>
  {/if}
</div>
