<script lang="ts">
  import Check from "phosphor-svelte/lib/CheckIcon";
  import Globe from "phosphor-svelte/lib/GlobeIcon";
  import Paperclip from "phosphor-svelte/lib/PaperclipIcon";
  import PlugsConnected from "phosphor-svelte/lib/PlugsConnectedIcon";
  import Plus from "phosphor-svelte/lib/PlusIcon";
  import Popover from "./Popover.svelte";
  import { app, setSearchPaused } from "../state/app.svelte";

  let {
    up = false,
    onattach,
    canAttach = true,
  }: { up?: boolean; onattach?: () => void; canAttach?: boolean } = $props();
  let open = $state(false);

  // Search can only be turned on here once a provider is named. Without one
  // the row opens the settings that name it.
  const search = $derived(app.search);
  const searchSet = $derived(!!search && search.provider !== "");
  const searchOn = $derived(searchSet && !search?.paused);
  const searchNote = $derived(
    !searchSet
      ? "Choose a provider in Settings"
      : search?.ready || search?.paused
        ? "Search and read web pages"
        : "Add a key in Settings to use it",
  );

  const servers = $derived(app.mcp.filter((s) => s.enabled));
  const toolCount = $derived(servers.reduce((n, s) => n + s.tools.length, 0));
  const mcpNote = $derived(
    app.mcp.length === 0
      ? "None added yet"
      : `${servers.length} of ${app.mcp.length} on, ${toolCount} ${toolCount === 1 ? "tool" : "tools"}`,
  );

  function toggleSearch() {
    if (!searchSet) {
      open = false;
      app.settingsAt = "search";
      return;
    }
    void setSearchPaused(searchOn);
  }

  function openMcp() {
    open = false;
    app.mcpOpen = true;
  }
</script>

<div class="relative">
  <button
    class="round"
    aria-label="Add"
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <Plus />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} {up} width={300}>
      <div class="popover-head">Message</div>
      <button
        class="popover-item"
        disabled={!canAttach}
        onclick={() => {
          open = false;
          onattach?.();
        }}
      >
        <span class="item-tile"><Paperclip /></span>
        <span class="min-w-0 flex-1">
          <span class="block">Attach a file</span>
          <span class="block text-[12px] text-muted">
            Up to 5 files, images or video
          </span>
        </span>
        <span class="key-pair"><kbd class="key">⌘</kbd><kbd class="key">U</kbd></span>
      </button>

      <div class="popover-sep"></div>
      <div class="popover-head">Tools</div>
      <button class="popover-item" role="menuitemcheckbox" aria-checked={searchOn} onclick={toggleSearch}>
        <span class="item-tile"><Globe /></span>
        <span class="min-w-0 flex-1">
          <span class="block">Web search</span>
          <span class="block text-[12px] text-muted">{searchNote}</span>
        </span>
        {#if searchOn}
          <Check size={14} class="text-accent" />
        {/if}
      </button>
      <button class="popover-item" onclick={openMcp}>
        <span class="item-tile"><PlugsConnected /></span>
        <span class="min-w-0 flex-1">
          <span class="block">MCP servers</span>
          <span class="block text-[12px] text-muted">{mcpNote}</span>
        </span>
      </button>
    </Popover>
  {/if}
</div>
