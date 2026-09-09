<script lang="ts">
  import Plus from "phosphor-svelte/lib/Plus";
  import X from "phosphor-svelte/lib/X";
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
  import { isTop, pushOverlay } from "../overlay";
  import AgentPanel from "./AgentPanel.svelte";

  let { onclose }: { onclose: () => void } = $props();

  let chosen = $state("");
  let panel = $state<ReturnType<typeof AgentPanel> | null>(null);
  let card = $state<HTMLDivElement | null>(null);
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

  const escape = () => void leave();
  $effect(() => pushOverlay(escape));

  $effect(() => {
    const before = document.activeElement as HTMLElement | null;
    card?.focus();
    return () => before?.focus();
  });

  const FOCUSABLE =
    'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

  // Tab wraps inside the dialog, unless something is open above it: a confirm
  // draws over this one from outside its markup and has to stay reachable.
  function trap(event: KeyboardEvent) {
    if (event.key !== "Tab" || !card || !isTop(escape)) return;
    const items = [...card.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
      (el) => el.offsetParent !== null,
    );
    const first = items[0];
    const last = items[items.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

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
    const next = app.agents.find((a) => a.id !== stored.id);
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

<div
  class="fixed inset-0 z-50 grid place-items-center bg-[rgb(15_23_42/0.42)] p-6"
  role="presentation"
  onclick={(e) => e.target === e.currentTarget && leave()}
>
  <div
    bind:this={card}
    class="card flex max-h-full w-[1000px] max-w-full overflow-hidden !rounded-[18px] p-0"
    style="height:720px"
    role="dialog"
    aria-modal="true"
    aria-label="Agents"
    tabindex="-1"
    onkeydown={trap}
  >
    <div class="rail w-[248px] shrink-0">
      <div class="rail-head">
        <span>Agents</span>
        <button
          class="text-[#7f918b] hover:text-white"
          aria-label="Close"
          onclick={leave}
        >
          <X size={16} />
        </button>
      </div>

      <nav class="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
        {#each app.agents as agent (agent.id)}
          <button
            class="rail-row"
            class:is-active={agent.id === stored?.id}
            onclick={() => select(agent.id)}
          >
            {#if agent.id === active}
              <span class="rail-dot is-live"></span>
            {:else}
              <span class="rail-dot"></span>
            {/if}
            <span class="min-w-0 flex-1 truncate">{agent.name}</span>
          </button>
        {/each}
      </nav>

      <button class="rail-add" onclick={create}>
        <Plus size={15} />
        New agent
      </button>
    </div>

    <div class="flex min-w-0 flex-1 flex-col">
      {#if draft && stored}
        <header class="flex shrink-0 items-start justify-between gap-5 border-b border-hair px-7 pt-[22px] pb-[18px]">
          <div class="flex min-w-0 flex-col gap-1">
            <input
              class="title-input w-[280px]"
              aria-label="Agent name"
              bind:value={draft.name}
              placeholder="Local llama.cpp"
            />
            <span class="truncate text-[13px] text-muted">
              {draft.model || "No model chosen"}{draft.url ? ` · ${draft.url}` : ""}
            </span>
          </div>
          {#if stored.id === active}
            <span class="pill pill-ok mt-1 shrink-0">Active</span>
          {:else}
            <button class="btn btn-ghost btn-sm mt-0.5 shrink-0" onclick={() => setActiveAgent(stored.id)}>
              Use this agent
            </button>
          {/if}
        </header>

        <div class="flex min-h-0 flex-1 flex-col gap-[18px] overflow-y-auto px-7 pt-5 pb-7">
          {#key stored.id}
            <AgentPanel bind:this={panel} agent={draft} onchange={edit} />
          {/key}
        </div>

        <footer class="flex shrink-0 items-center justify-end gap-2.5 border-t border-hair bg-panel px-7 py-3.5">
          <button class="btn btn-danger !border-transparent" onclick={remove}>Delete</button>
          <button class="btn btn-primary" disabled={!dirty || saving} onclick={save}>
            {saving ? "Saving" : "Save"}
          </button>
        </footer>
      {:else}
        <div class="grid flex-1 place-items-center px-7 text-center text-[13px] text-muted">
          No agents yet. Add one to give the harness something to answer with.
        </div>
      {/if}
    </div>
  </div>
</div>
