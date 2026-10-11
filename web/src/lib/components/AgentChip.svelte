<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import Sliders from "phosphor-svelte/lib/Sliders";
  import Robot from "phosphor-svelte/lib/Robot";
  import Popover from "./Popover.svelte";
  import {
    activeAgent,
    agentById,
    agentReady,
    app,
    conversationAgent,
    setConversationAgent,
    setActiveAgent,
  } from "../state/app.svelte";
  import { probeMessage } from "../format";

  let open = $state(false);
  // The agent this conversation answers with. Every conversation starts on the
  // one answering now and keeps it, so it is on a choice of its own once the two
  // differ.
  const current = $derived(conversationAgent());
  // A choice of its own, meaning an agent that still exists and is not the one
  // answering now. A conversation whose agent was deleted answers with the one
  // answering now, and is not marked as having chosen.
  const own = $derived(
    app.opened !== null &&
      agentById(app.opened.agent ?? "") !== undefined &&
      (app.opened.agent ?? "") !== (app.config?.agent ?? ""),
  );
  // Only an enabled agent can be picked, so the menu holds those.
  const choices = $derived(app.agents.filter((a) => a.enabled));
  // What was read of the model was read of the agent answering now, so it says
  // nothing about a conversation answering elsewhere. Nothing is claimed then.
  const onDefault = $derived(current?.id === (app.config?.agent ?? ""));
  const problem = $derived(
    onDefault && !agentReady()
      ? probeMessage(
          {
            models: app.models,
            detectError: app.detectError,
            detectKind: app.detectKind,
            modelsError: app.modelsError,
            modelsKind: app.modelsKind,
          },
          (current?.key ?? "") !== "",
        )
      : "",
  );

  // Choosing here answers this conversation with that agent, and leaves every
  // other conversation where it is.
  function pick(id: string) {
    if (app.sessionId) void setConversationAgent(id);
    else void setActiveAgent(id);
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
      {#if app.sessionId}
        <div class="popover-head">This conversation</div>
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
        <!-- The agent answering now is what a conversation opened later starts
             on, and what this one falls back to if its own agent goes away. It
             is changed in the agents dialog, not here. -->
        <div class="popover-head">New conversations</div>
        <div class="popover-item">
          <span class="min-w-0 flex-1">
            <span class="block truncate text-left">
              {activeAgent()?.name ?? "No agent is enabled"}
            </span>
            <span class="block truncate text-left text-[12px] text-muted">
              {activeAgent()?.model || "No model chosen"}{#if own} · not this conversation{/if}
            </span>
          </span>
        </div>
      {:else}
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
      {/if}
      <button class="popover-item text-ink-2" onclick={manage}>
        <Sliders size={15} />
        Manage agents
      </button>
    </Popover>
  {/if}
</div>
