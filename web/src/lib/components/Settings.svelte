<script lang="ts">
  import FolderOpen from "phosphor-svelte/lib/FolderOpen";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import ShieldCheck from "phosphor-svelte/lib/ShieldCheck";
  import Sliders from "phosphor-svelte/lib/Sliders";
  import Wrench from "phosphor-svelte/lib/Wrench";
  import type { Config, SearchConfig } from "../api";
  import { app, saveConfig, saveSearch } from "../state/app.svelte";
  import { confirm } from "../state/confirm.svelte";
  import SettingsGeneral from "./SettingsGeneral.svelte";
  import SettingsHarness from "./SettingsHarness.svelte";
  import SettingsPermissions from "./SettingsPermissions.svelte";
  import SettingsProjects from "./SettingsProjects.svelte";
  import SettingsSearch from "./SettingsSearch.svelte";
  import SplitDialog from "./SplitDialog.svelte";

  let { onclose }: { onclose: () => void } = $props();

  type Pane = "general" | "permissions" | "harness" | "search" | "projects";
  type PaneRef = { flush: () => void };
  interface Draft {
    config: Config;
    search: SearchConfig;
  }

  const PANELS: { id: Pane; label: string; line: string }[] = [
    { id: "general", label: "General", line: "How the harness listens, runs commands and spends its context." },
    { id: "permissions", label: "Permissions", line: "What a tool call may do before it is shown." },
    { id: "harness", label: "Harness", line: "Which tools the model can reach for, in every session." },
    { id: "search", label: "Search", line: "Where a web search goes, and what it may cost." },
    { id: "projects", label: "Projects", line: "The folders the harness works in." },
  ];

  let pane = $state<Pane>("general");
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
      output_limit_bytes: 16384,
      projects: [],
    };
    const search: SearchConfig = app.search ?? {
      provider: "",
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
  const dirty = $derived(JSON.stringify(draft) !== JSON.stringify(stored));

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

  async function manage() {
    if (!(await keepDraft())) return;
    app.agentsOpen = true;
    onclose();
  }

  const CONFIG_FIELDS = [
    "listen",
    "permissions",
    "toolset",
    "compact_at",
    "shell",
    "command_timeout_s",
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
    if (!dirty || saving) return;
    saving = true;
    const changes = configPatch();
    const searchChanged = JSON.stringify(draft.search) !== JSON.stringify(stored.search);
    // The block writes whole: the providers list it carries replaces the
    // accounts, the ones the pane did not touch unchanged.
    const { known: _known, ready: _ready, ...searchBody } = draft.search;
    const [configSaved, searchSaved] = await Promise.all([
      Object.keys(changes).length > 0 ? saveConfig(changes) : Promise.resolve(true),
      searchChanged ? saveSearch(searchBody) : Promise.resolve(true),
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
      {#each PANELS as panel (panel.id)}
        <button
          class="rail-row"
          class:is-active={pane === panel.id}
          onclick={() => choose(panel.id)}
        >
          {#if panel.id === "general"}
            <Sliders size={15} class="shrink-0" />
          {:else if panel.id === "permissions"}
            <ShieldCheck size={15} class="shrink-0" />
          {:else if panel.id === "harness"}
            <Wrench size={15} class="shrink-0" />
          {:else if panel.id === "search"}
            <MagnifyingGlass size={15} class="shrink-0" />
          {:else}
            <FolderOpen size={15} class="shrink-0" />
          {/if}
          <span class="min-w-0 flex-1 truncate">{panel.label}</span>
        </button>
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
        <SettingsGeneral bind:this={current} config={draft.config} onchange={editConfig} onmanage={manage} />
      {:else if pane === "permissions"}
        <SettingsPermissions bind:this={current} config={draft.config} onchange={editConfig} />
      {:else if pane === "harness"}
        <SettingsHarness bind:this={current} config={draft.config} onchange={editConfig} />
      {:else if pane === "search"}
        <SettingsSearch bind:this={current} search={draft.search} onchange={editSearch} />
      {:else}
        <SettingsProjects bind:this={current} />
      {/if}
    </div>

    <footer
      class="flex shrink-0 items-center justify-end gap-2.5 border-t border-hair bg-panel px-6 py-3"
    >
      <button class="btn btn-primary" disabled={!dirty || saving} onclick={save}>
        {saving ? "Saving" : "Save"}
      </button>
    </footer>
  {/snippet}
</SplitDialog>
