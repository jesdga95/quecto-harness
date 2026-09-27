<script lang="ts">
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import Check from "phosphor-svelte/lib/Check";
  import FolderPlus from "phosphor-svelte/lib/FolderPlus";
  import Popover from "./Popover.svelte";
  import { app, pickAndAddProject, selectProject } from "../state/app.svelte";

  let open = $state(false);
  const current = $derived(app.projects.find((p) => p.path === app.projectPath));

  function pick(path: string) {
    open = false;
    void selectProject(path);
  }
</script>

<!-- The empty screen's title is the project, and it is the picker for one. -->
<div class="relative inline-block">
  <button class="hero-title" aria-haspopup="menu" aria-expanded={open} onclick={() => (open = !open)}>
    <span class="hero-name">{current?.name ?? "Choose a project"}</span>
    <span class="hero-caret"><CaretDown /></span>
  </button>

  {#if open}
    <Popover onclose={() => (open = false)} width={280}>
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
