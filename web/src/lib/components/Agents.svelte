<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import Robot from "phosphor-svelte/lib/Robot";
  import User from "phosphor-svelte/lib/User";
  import type { Agent, AgentPatch } from "../api";
  import {
    addAgent,
    agentById,
    app,
    removeAgent,
    saveAgent,
    setActiveAgent,
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

  /** Makes this the agent a conversation opened later starts on. The ones
      already open keep the agent they answer with. */
  async function asDefault() {
    const id = draft?.id ?? "";
    if (id) await setActiveAgent(id);
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

  /** Copies what the saved record holds, under a name nothing else has. The
      id is left out, so the harness mints one, and the selection is left
      alone: a copy is not what answers next. */
  async function clone() {
    if (!stored) return;
    if (!(await letGo())) return;
    const { id, name, ...rest } = stored;
    void id;
    // Case-insensitive, the way the harness compares names.
    const taken = (candidate: string) =>
      app.agents.some((a) => a.name.toLowerCase() === candidate.toLowerCase());
    let cloneName = `${name} copy`;
    for (let n = 2; taken(cloneName); n++) cloneName = `${name} copy ${n}`;
    const made = await addAgent({ ...rest, name: cloneName });
    if (made) {
      draft = null;
      chosen = made.id;
    }
  }

  async function remove() {
    if (!stored) return;
    // The record that goes, named before anything moves the view: stored is a
    // derived, and clearing chosen would make it fall to the active agent.
    const gone = stored;
    const next = app.agents.find((a) => a.id !== gone.id && a.enabled);
    const body =
      gone.id === active && next
        ? `It is the agent answering now, and ${next.name} takes over. Its address, key and settings are removed.`
        : "Its address, key and settings are removed. Sessions it ran are left alone.";
    if (!(await confirm({ title: `Delete ${gone.name}?`, body, confirmLabel: "Delete" }))) return;
    draft = null;
    chosen = "";
    await removeAgent(gone.id);
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
          <span class="min-w-0 flex-1 truncate" class:opacity-55={!agent.enabled}>
            {agent.name}
          </span>
          {#if !agent.enabled}
            <span class="shrink-0 text-[11px] text-muted">Disabled</span>
          {:else}
            <!-- Every agent answers the person; the robot adds that at least
                 one agent names it in its list, itself included, so it may
                 take work. The label marks where new conversations start. -->
            <span class="flex shrink-0 items-center gap-1 text-muted">
              {#if agent.id === active}
                <span class="text-[11px]">New conversations</span>
              {/if}
              <User size={13} />
              {#if app.agents.some((x) => x.enabled && x.calls.includes(agent.id))}
                <Robot size={13} />
              {/if}
            </span>
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
        class="flex shrink-0 items-center gap-2.5 border-t border-hair bg-panel px-6 py-3"
      >
        <!-- What a conversation opened later starts on. The conversations
             already open keep the agent they answer with, so this moves
             nothing that is running. -->
        {#if draft.enabled}
          {#if draft.id === active}
            <span class="min-w-0 flex-1 truncate text-[12px] text-muted">
              New conversations start on this one
            </span>
          {:else}
            <button class="btn btn-ghost !border-transparent" onclick={() => void asDefault()}>
              Use for new conversations
            </button>
            <span class="min-w-0 flex-1"></span>
          {/if}
        {:else}
          <span class="min-w-0 flex-1"></span>
        {/if}
        <button class="btn btn-danger !border-transparent" onclick={remove}>Delete</button>
        <button class="btn btn-ghost !border-transparent" onclick={clone}>Clone</button>
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
