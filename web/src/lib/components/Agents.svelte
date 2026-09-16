<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import Robot from "phosphor-svelte/lib/Robot";
  import type { Agent, AgentPatch } from "../api";
  import {
    addAgent,
    agentById,
    app,
    removeAgent,
    saveAgent,
  } from "../state/app.svelte";
  import { confirm } from "./../state/confirm.svelte";
  import AgentPanel from "./AgentPanel.svelte";
  import SplitDialog from "./SplitDialog.svelte";

  let { onclose }: { onclose: () => void } = $props();

  let chosen = $state("");
  let panel = $state<ReturnType<typeof AgentPanel> | null>(null);
  // What is on screen. Edits land here and reach the harness on Save, so a
  // dialog left open changes nothing.
  let draft = $state<Agent | null>(null);
  let saving = $state(false);

  const stored = $derived(
    agentById(chosen) ?? agentById(app.config?.agent ?? "") ?? app.agents[0],
  );
  const active = $derived(app.config?.agent ?? "");
  const dirty = $derived(
    draft !== null && stored !== undefined && JSON.stringify(draft) !== JSON.stringify(stored),
  );

  // The draft follows the row until something is typed into it.
  $effect(() => {
    if (stored && (draft === null || draft.id !== stored.id)) draft = { ...stored };
  });

  /** Answers whether it is alright to move off what is on screen. */
  async function letGo(): Promise<boolean> {
    panel?.flush();
    if (!dirty) return true;
    return confirm({
      title: `Discard changes to ${stored?.name}?`,
      body: "What was typed is not saved.",
      confirmLabel: "Discard",
    });
  }

  async function select(id: string) {
    if (id === stored?.id) return;
    if (!(await letGo())) return;
    draft = null;
    chosen = id;
  }

  async function leave() {
    if (!(await letGo())) return;
    onclose();
  }

  function edit(patch: AgentPatch) {
    if (draft) draft = { ...draft, ...patch };
  }

  async function save() {
    panel?.flush();
    if (!draft || saving) return;
    saving = true;
    const { id, ...rest } = draft;
    const written = await saveAgent(id, rest);
    saving = false;
    if (written) draft = { ...written };
  }

  async function create() {
    if (!(await letGo())) return;
    const made = await addAgent({ name: nextName() });
    if (made) {
      draft = null;
      chosen = made.id;
    }
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

  async function remove() {
    if (!stored) return;
    const next = app.agents.find((a) => a.id !== stored.id && a.enabled);
    const body =
      stored.id === active && next
        ? `It is the agent answering now, and ${next.name} takes over. Its address, key and settings are removed.`
        : "Its address, key and settings are removed. Sessions it ran are left alone.";
    if (!(await confirm({ title: `Delete ${stored.name}?`, body, confirmLabel: "Delete" }))) return;
    draft = null;
    chosen = "";
    await removeAgent(stored.id);
  }
</script>

<SplitDialog label="Agents" title="Agents" onclose={leave}>
  {#snippet rail()}
    <nav class="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
      {#each app.agents as agent (agent.id)}
        <button
          class="rail-row"
          class:is-active={agent.id === stored?.id}
          onclick={() => select(agent.id)}
        >
          <Robot size={15} class="shrink-0" />
          <span class="min-w-0 flex-1 truncate" class:opacity-55={!agent.enabled}>
            {agent.name}
          </span>
          {#if !agent.enabled}
            <span class="shrink-0 text-[11px] text-muted">Disabled</span>
          {/if}
        </button>
      {/each}
    </nav>

    <button class="rail-add" onclick={create}>
      <Plus size={14} />
      New agent
    </button>
  {/snippet}

  {#snippet detail()}
    {#if draft && stored}
      <header class="shrink-0 border-b border-hair px-6 pt-4 pr-14 pb-3.5">
        <div class="flex min-w-0 flex-col gap-0.5">
          <h3 class="truncate text-[17px] font-medium">
            {draft.name || "Unnamed agent"}
          </h3>
          <span class="truncate text-[12px] text-muted">
            {draft.enabled ? "" : "Disabled · "}{draft.model ||
              "No model chosen"}{draft.url ? ` · ${draft.url}` : ""}
          </span>
        </div>
      </header>

      <div class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-6 pt-4 pb-6">
        {#key stored.id}
          <AgentPanel bind:this={panel} agent={draft} onchange={edit} />
        {/key}
      </div>

      <footer
        class="flex shrink-0 items-center justify-end gap-2.5 border-t border-hair bg-panel px-6 py-3"
      >
        <button class="btn btn-danger !border-transparent" onclick={remove}>Delete</button>
        <button class="btn btn-primary" disabled={!dirty || saving} onclick={save}>
          {saving ? "Saving" : "Save"}
        </button>
      </footer>
    {:else}
      <div class="grid flex-1 place-items-center px-6 text-center text-[13px] text-muted">
        No agents yet. Add one to give the harness something to answer with.
      </div>
    {/if}
  {/snippet}
</SplitDialog>
