<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRightIcon";
  import FolderOpen from "phosphor-svelte/lib/FolderOpenIcon";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlassIcon";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheckIcon";
  import Sliders from "phosphor-svelte/lib/SlidersIcon";
  import Wrench from "phosphor-svelte/lib/WrenchIcon";
  import { onMount, untrack } from "svelte";
  import { api, ApiError, type Config, type Memory, type SearchConfig } from "../api";
  import { app, saveConfig, saveSearch } from "../state/app.svelte";
  import { confirm } from "../state/confirm.svelte";
  import SettingsGeneral from "./SettingsGeneral.svelte";
  import SettingsPermissions from "./SettingsPermissions.svelte";
  import SettingsProjects from "./SettingsProjects.svelte";
  import SettingsSearch from "./SettingsSearch.svelte";
  import SettingsTools from "./SettingsTools.svelte";
  import SplitDialog from "./SplitDialog.svelte";

  let { start = "general", onclose }: { start?: string; onclose: () => void } = $props();

  type Pane = "general" | "permissions" | "tools" | "search" | "projects";
  type PaneRef = { flush: () => void };
  interface Draft {
    config: Config;
    search: SearchConfig;
  }

  const PANELS: { id: Pane; label: string; line: string }[] = [
    { id: "general", label: "General", line: "How the harness listens, runs commands and spends its context." },
    { id: "permissions", label: "Permissions", line: "What a tool call may do before it is shown." },
    { id: "tools", label: "Tools", line: "Which tools the model can reach for, in every session." },
    { id: "search", label: "Search", line: "Where a web search goes, and what it may cost." },
    { id: "projects", label: "Projects", line: "The folders the harness works in." },
  ];
  // The rail's order. The labels show only when the list has the dialog to itself.
  const GROUPS: { label: string; panes: Pane[] }[] = [
    { label: "Harness", panes: ["general", "tools", "search"] },
    { label: "Access", panes: ["permissions", "projects"] },
  ];

  // The pane it opens on; moving between panes afterwards is the dialog's own.
  const opening = untrack(() => start);
  let pane = $state<Pane>(PANELS.some((p) => p.id === opening) ? (opening as Pane) : "general");
  let saving = $state(false);
  let current = $state<PaneRef | null>(null);

  // One draft for the whole dialog, not one per category: a category is a
  // view of it rather than a different record, so moving off one never asks.
  function seed(): Draft {
    const config: Config = app.config ?? {
      listen: "",
      agent: "",
      permissions: "ask",
      toolset: "full",
      compact_at: 0.8,
      max_tool_calls: 50,
      shell: "/bin/sh",
      command_timeout_s: 120,
      background_after_s: 30,
      output_limit_bytes: 16384,
      projects: [],
    };
    const search: SearchConfig = app.search ?? {
      provider: "",
      paused: false,
      max_hits: 10,
      max_chars: 4000,
      timeout_s: 20,
      providers: [],
      known: [],
      ready: false,
    };
    return {
      config: { ...config, projects: config.projects.slice() },
      search: { ...search, providers: search.providers.map((p) => ({ ...p })) },
    };
  }

  let draft = $state<Draft>(seed());
  // What the harness holds, as the dialog opened. Save compares against this
  // and moves it to the answer once a write has landed.
  let stored = $state<Draft>(seed());
  let memory = $state<Memory | null>(null);
  let notes = $state("");
  let memoryError = $state("");
  let reload = $state(false);
  let loadingMemory = $state(false);
  const memoryDirty = $derived(memory !== null && notes !== memory.text);
  const canRepairMemory = $derived(memory?.warning && !memoryDirty && !reload &&
    new Blob([notes, notes && !notes.endsWith("\n") ? "\n" : ""]).size <= 4096);
  const settingsDirty = $derived(JSON.stringify(draft) !== JSON.stringify(stored));
  const dirty = $derived(memoryDirty || settingsDirty);
  const canSave = $derived(settingsDirty || (memoryDirty && !reload && !loadingMemory));

  async function loadMemory() {
    loadingMemory = true;
    reload = false;
    memoryError = "";
    try {
      memory = await api.getMemory();
      notes = memory.text;
      memoryError = memory.warning ?? "";
    } catch (err) {
      memoryError = err instanceof Error ? err.message : "Could not read memory.";
      reload = true;
    } finally {
      loadingMemory = false;
    }
  }

  onMount(() => { void loadMemory(); });

  async function saveMemory() {
    if (!memory) return;
    const text = notes;
    try {
      memory = await api.replaceMemory({ text, revision: memory.revision });
      if (notes === text) notes = memory.text;
      memoryError = "";
    } catch (err) {
      memoryError = err instanceof Error ? err.message : "Could not save memory.";
      reload = err instanceof ApiError && err.status === 409;
      pane = "general";
    }
  }

  async function repairMemory() {
    saving = true;
    try { await saveMemory(); } finally { saving = false; }
  }

  function editConfig(patch: Partial<Config>) {
    draft = { ...draft, config: { ...draft.config, ...patch } };
  }

  function editSearch(patch: Partial<SearchConfig>) {
    draft = { ...draft, search: { ...draft.search, ...patch } };
  }

  async function choose(next: Pane) {
    if (next === pane) return;
    current?.flush();
    pane = next;
  }

  /** Answers whether it is alright to leave with the draft as it stands. */
  async function keepDraft(): Promise<boolean> {
    current?.flush();
    if (!dirty) return true;
    return confirm({
      title: "Discard settings changes?",
      body: "What was typed is not saved.",
      confirmLabel: "Discard",
    });
  }

  async function leave() {
    if (!(await keepDraft())) return;
    onclose();
  }

  /** Leaves for a dialog of records, which keeps its own drafts. */
  async function manage(which: "agents" | "mcp") {
    if (!(await keepDraft())) return;
    app.backToSettings = pane;
    if (which === "agents") app.agentsOpen = true;
    else app.mcpOpen = true;
    onclose();
  }

  const CONFIG_FIELDS = [
    "listen",
    "permissions",
    "toolset",
    "compact_at",
    "shell",
    "command_timeout_s",
    "background_after_s",
    "output_limit_bytes",
    "max_tool_calls",
  ] as const;

  /** What the config pane changed, field by field. The agent and the project
      list are managed elsewhere and are not part of the draft. */
  function configPatch(): Partial<Config> {
    const patch: Record<string, unknown> = {};
    for (const field of CONFIG_FIELDS) {
      if (stored.config[field] !== draft.config[field]) patch[field] = draft.config[field];
    }
    return patch as Partial<Config>;
  }

  async function save() {
    current?.flush();
    if (!canSave || saving) return;
    saving = true;
    const changes = configPatch();
    const searchChanged = JSON.stringify(draft.search) !== JSON.stringify(stored.search);
    // The block writes whole: the providers list it carries replaces the
    // accounts, the ones the pane did not touch unchanged.
    const { known: _known, ready: _ready, ...searchBody } = draft.search;
    const [configSaved, searchSaved] = await Promise.all([
      Object.keys(changes).length > 0 ? saveConfig(changes) : Promise.resolve(true),
      searchChanged ? saveSearch(searchBody) : Promise.resolve(true),
      memoryDirty && !reload && !loadingMemory ? saveMemory() : undefined,
    ]);
    saving = false;
    // The answer is what the harness now holds, and the dialog takes it as
    // its own draft: what the write resolved, like whether a key has turned
    // the tool on, is a field the panes never type, and left in the draft it
    // would outlive the write and read as an unsaved change. A refused write
    // keeps the draft, so what was typed can be saved again.
    if (configSaved && searchSaved) draft = seed();
    stored = seed();
  }
</script>

<SplitDialog label="Settings" title="Settings" onclose={leave}>
  {#snippet rail()}
    <nav class="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
      {#each GROUPS as group (group.label)}
        <p class="rail-label">{group.label}</p>
        <div class="rail-group">
          {#each PANELS.filter((p) => group.panes.includes(p.id)) as panel (panel.id)}
            <button
              class="rail-row"
              class:is-active={pane === panel.id}
              onclick={() => choose(panel.id)}
            >
              <span class="rail-tile" class:is-warn={panel.id === "permissions"}>
                {#if panel.id === "general"}
                  <Sliders size={15} class="shrink-0" />
                {:else if panel.id === "permissions"}
                  <ShieldCheck size={15} class="shrink-0" />
                {:else if panel.id === "tools"}
                  <Wrench size={15} class="shrink-0" />
                {:else if panel.id === "search"}
                  <MagnifyingGlass size={15} class="shrink-0" />
                {:else}
                  <FolderOpen size={15} class="shrink-0" />
                {/if}
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate">{panel.label}</span>
                <span class="rail-sub">{panel.line}</span>
              </span>
              <CaretRight size={14} class="rail-chevron" />
            </button>
          {/each}
        </div>
      {/each}
    </nav>
  {/snippet}

  {#snippet detail()}
    <header class="shrink-0 border-b border-hair px-6 pt-4 pr-14 pb-3.5">
      <div class="flex min-w-0 flex-col gap-0.5">
        <h3 class="truncate text-[17px] font-medium">
          {PANELS.find((p) => p.id === pane)?.label}
        </h3>
        <span class="truncate text-[12px] text-muted">
          {PANELS.find((p) => p.id === pane)?.line}
        </span>
      </div>
    </header>

    <div class="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-6 pt-4 pb-6">
      {#if pane === "general"}
        <SettingsGeneral bind:this={current} config={draft.config} onchange={editConfig} onmanage={() => manage("agents")} />
        <div class="field-card">
          <label class="flex flex-col gap-1.5">
            <span class="field-head">Memory</span>
            <textarea class="input min-h-24" bind:value={notes} disabled={!memory || saving || loadingMemory}></textarea>
            <span class="text-[11.5px] text-muted">Preferences sent to the selected model when a conversation starts or compacts. Up to 4096 bytes.</span>
          </label>
          {#if memoryError}<p role="alert" class="text-[12px] text-crit">{memoryError}</p>{/if}
          {#if canRepairMemory}
            <button class="btn btn-sm btn-ghost" disabled={saving || loadingMemory} onclick={repairMemory}>Save displayed memory</button>
          {/if}
          {#if reload}
            <button disabled={saving || loadingMemory} class="btn btn-sm btn-ghost" onclick={loadMemory}>
              {memoryDirty ? "Discard memory edits and reload" : "Reload memory"}
            </button>
          {/if}
        </div>
      {:else if pane === "permissions"}
        <SettingsPermissions bind:this={current} config={draft.config} onchange={editConfig} />
      {:else if pane === "tools"}
        <SettingsTools bind:this={current} config={draft.config} onchange={editConfig} onmanage={() => manage("mcp")} />
      {:else if pane === "search"}
        <SettingsSearch bind:this={current} search={draft.search} onchange={editSearch} />
      {:else}
        <SettingsProjects bind:this={current} />
      {/if}
    </div>

    <footer
      class="flex shrink-0 items-center justify-end gap-2.5 border-t border-hair bg-panel px-6 py-3"
    >
      <button class="btn btn-primary" disabled={!canSave || saving} onclick={save}>
        {saving ? "Saving" : "Save"}
      </button>
    </footer>
  {/snippet}
</SplitDialog>
