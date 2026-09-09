<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import Trash from "phosphor-svelte/lib/Trash";
  import Modal from "./Modal.svelte";
  import StatusDot from "./StatusDot.svelte";
  import AgentPanel from "./AgentPanel.svelte";
  import type { Agent, AgentPatch, ModelStatus } from "../api";
  import {
    addAgent,
    agentById,
    app,
    removeAgent,
    saveAgent,
    setActiveAgent,
  } from "../state/app.svelte";
  import { confirm } from "./../state/confirm.svelte";

  let { onclose }: { onclose: () => void } = $props();

  let chosen = $state("");
  let panel = $state<ReturnType<typeof AgentPanel> | null>(null);

  // The row being edited defaults to the one answering, and follows the list
  // when what it named is gone.
  const editing = $derived(agentById(chosen) ?? agentById(app.config?.agent ?? "") ?? app.agents[0]);
  const active = $derived(app.config?.agent ?? "");

  // The one row that can be loading is the one answering, so it is the only
  // one drawn from a live reading.
  const status = $derived<ModelStatus>(
    app.status === "loading"
      ? "loading"
      : (app.models.find((m) => m.id === agentById(active)?.model)?.status ?? "unloaded"),
  );

  function select(id: string) {
    if (id === editing?.id) return;
    panel?.flush();
    chosen = id;
  }

  function leave() {
    panel?.flush();
    onclose();
  }

  function save(patch: AgentPatch) {
    if (editing) void saveAgent(editing.id, patch);
  }

  async function create() {
    panel?.flush();
    const made = await addAgent({ name: nextName() });
    if (made) chosen = made.id;
  }

  /** A name nothing else has, since two agents may not share one. */
  function nextName(): string {
    const base = "New agent";
    if (!app.agents.some((a) => a.name === base)) return base;
    for (let n = 2; ; n++) {
      const name = `${base} ${n}`;
      if (!app.agents.some((a) => a.name === name)) return name;
    }
  }

  async function remove(agent: Agent) {
    const next = app.agents.find((a) => a.id !== agent.id);
    const body =
      agent.id === active && next
        ? `It is the agent answering now, and ${next.name} takes over. Its address, key and settings are removed.`
        : "Its address, key and settings are removed. Sessions it ran are left alone.";
    if (!(await confirm({ title: `Delete ${agent.name}?`, body, confirmLabel: "Delete" }))) return;
    if (agent.id === chosen) chosen = "";
    await removeAgent(agent.id);
  }
</script>

<Modal title="Agents" width={880} height={600} onclose={leave}>
  <div class="grid min-h-0 flex-1 grid-cols-[248px_1fr]">
    <div class="flex min-h-0 flex-col border-r border-hair">
      <p class="label px-4 pt-3 pb-1">Agents</p>
      <nav class="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {#each app.agents as agent (agent.id)}
          <div class="row-wrap">
            <button
              class="side-row"
              class:is-active={agent.id === editing?.id}
              onclick={() => select(agent.id)}
            >
              {#if agent.id === active}
                <StatusDot {status} />
              {:else}
                <span class="dot"></span>
              {/if}
              <span class="min-w-0 flex-1 truncate text-left">{agent.name}</span>
            </button>
            <div class="row-act">
              <button
                class="row-menu hover:text-crit"
                aria-label="Delete {agent.name}"
                onclick={() => remove(agent)}
              >
                <Trash />
              </button>
            </div>
          </div>
        {/each}
      </nav>
      <div class="px-3 pt-1 pb-3">
        <button class="btn btn-ghost w-full justify-center" onclick={create}>
          <Plus size={14} />
          New agent
        </button>
      </div>
    </div>

    <div class="flex min-h-0 flex-col">
      {#if editing}
        <header
          class="flex h-[46px] shrink-0 items-center gap-3 border-b border-hair bg-surface px-6"
        >
          <span class="min-w-0 flex-1 truncate text-[13px] font-medium">{editing.name}</span>
          {#if editing.id === active}
            <span class="pill pill-ok shrink-0">Active</span>
          {:else}
            <button class="btn btn-ghost btn-sm shrink-0" onclick={() => setActiveAgent(editing.id)}>
              Use this agent
            </button>
          {/if}
        </header>
        <div class="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
          {#key editing.id}
            <AgentPanel bind:this={panel} agent={editing} onchange={save} />
          {/key}
        </div>
      {:else}
        <div class="grid flex-1 place-items-center px-6 text-center text-[13px] text-muted">
          No agents yet. Add one to give the harness something to answer with.
        </div>
      {/if}
    </div>
  </div>
</Modal>
