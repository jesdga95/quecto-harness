<script lang="ts">
  import FolderOpen from "phosphor-svelte/lib/FolderOpen";
  import X from "phosphor-svelte/lib/X";
  import { shortPath } from "../format";
  import { app, pickAndAddProject, removeProject, sessionsOf } from "../state/app.svelte";
  import { confirm } from "../state/confirm.svelte";

  // Projects are written as they are changed, the way the sidebar does it,
  // so the pane has nothing to flush and nothing in the draft.
  export function flush() {}

  async function remove(path: string) {
    const name = app.projects.find((p) => p.path === path)?.name ?? path;
    const count = sessionsOf(path).length;
    const body =
      (count === 1
        ? "Its 1 conversation is deleted. "
        : count > 1
          ? `Its ${count} conversations are deleted. `
          : "") +
      "The folder and the files in it are left alone, and its allowlist rules are kept for when you add it back.";
    const ok = await confirm({
      title: `Remove ${name}?`,
      body,
      confirmLabel: count > 0 ? "Remove and delete" : "Remove",
    });
    if (ok) await removeProject(path);
  }
</script>

<div class="field-card">
  <div class="flex items-center justify-between gap-4">
    <h4 class="field-head">Projects</h4>
    <button class="btn btn-sm btn-ghost shrink-0" onclick={() => pickAndAddProject()}>
      Add project
    </button>
  </div>

  {#each app.projects as project (project.path)}
    <div class="flex items-center gap-2.5">
      <FolderOpen size={15} class="shrink-0 text-muted" />
      <span class="min-w-0 flex-1 truncate text-[13px]" title={project.path}>
        {shortPath(project.path)}
      </span>
      <button class="icon-btn" aria-label="Remove {project.name}" onclick={() => remove(project.path)}>
        <X />
      </button>
    </div>
  {:else}
    <p class="text-[12.5px] text-muted">No projects yet. Add one to start a conversation in it.</p>
  {/each}
</div>
