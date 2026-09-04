<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import FolderOpen from "phosphor-svelte/lib/FolderOpen";
  import FolderPlus from "phosphor-svelte/lib/FolderPlus";
  import Popover from "./Popover.svelte";
  import { app, pickAndAddProject, selectProject } from "../state/app.svelte";

  let {
    up = false,
    inline = false,
    big = false,
  }: { up?: boolean; inline?: boolean; big?: boolean } = $props();
  let open = $state(false);
  const current = $derived(app.projects.find((p) => p.path === app.projectPath));

  function pick(path: string) {
    open = false;
    void selectProject(path);
  }
</script>

<div class={inline ? "relative inline-block" : "relative"}>
  <button
    class={inline ? (big ? "linkpick is-big" : "linkpick") : "chip"}
    aria-haspopup="menu"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <FolderOpen />
    {current?.name ?? "Choose a project"}
    <CaretDown />
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} {up} width={280}>
      {#each app.projects as project (project.path)}
        <button class="popover-item" onclick={() => pick(project.path)}>
          <span class="min-w-0 flex-1">
            <span class="block truncate">{project.name}</span>
            <span class="block truncate text-[12px] text-muted">{project.path}</span>
          </span>
          {#if project.path === app.projectPath}
            <Check size={14} class="text-accent" />
          {/if}
        </button>
      {/each}
      <button
        class="popover-item text-ink-2"
        onclick={() => {
          open = false;
          void pickAndAddProject();
        }}
      >
        <FolderPlus size={15} />
        Add project
      </button>
    </Popover>
  {/if}
</div>
