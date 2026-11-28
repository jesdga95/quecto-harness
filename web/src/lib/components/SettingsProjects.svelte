<script lang="ts">
  import { api, type RecoveryItem, type UnreadableSession } from "../api";
  import FolderOpen from "phosphor-svelte/lib/FolderOpenIcon";
  import X from "phosphor-svelte/lib/XIcon";
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

  let { onopen }: { onopen: (id: string) => void } = $props();
  let retained = $state<RecoveryItem[]>([]);
  let unreadable = $state<UnreadableSession[]>([]);
  let feedback = $state("");
  let checking = $state(false);

  async function checkTranscripts() {
    checking = true;
    try {
      unreadable = await api.unreadableSessions();
      retained = await api.recoveryItems();
      feedback = unreadable.length || retained.length ? "" : "No transcripts or saved output need recovery.";
    } catch (err) {
      feedback = err instanceof Error ? err.message : "Could not read the transcripts.";
    } finally {
      checking = false;
    }
  }

  async function deleteRetained(item: RecoveryItem) {
    const archive = item.kind === "archive";
    if (!(await confirm({
      title: archive ? "Delete archived transcript?" : "Delete saved output?",
      body: archive
        ? `Permanently delete ${item.name}. Saved output is kept separately.`
        : `Permanently delete ${item.name} and its contents. Archived transcripts may still refer to these files.`,
      confirmLabel: archive ? "Delete archive" : "Delete output",
    }))) return;
    try {
      await api.deleteRetained(item.name, item.generation);
      await checkTranscripts();
    } catch (err) {
      feedback = err instanceof Error ? err.message : "Could not delete the item.";
    }
  }

  async function deleteRun(id: string) {
    if (!(await confirm({
      title: "Delete recorded run?",
      body: `Any current transcript and saved output for ${id} will be deleted. Archived copies stay in the sessions folder.`,
      confirmLabel: "Delete run",
    }))) return;
    try {
      await api.deleteSession(id);
      await checkTranscripts();
    } catch (err) {
      feedback = err instanceof Error ? err.message : "Could not delete the run.";
    }
  }

  async function archive(id: string) {
    if (!(await confirm({
      title: "Archive unreadable transcript?",
      body: `The file for ${id} will be renamed in the sessions folder. The archived transcript and its saved output stay in the sessions folder.`,
      confirmLabel: "Archive",
    }))) return;
    try {
      const path = await api.archiveUnreadable(id);
      await checkTranscripts();
      if (!feedback) feedback = `Transcript archived at ${path}.`;
    } catch (err) {
      feedback = err instanceof Error ? err.message : "Could not archive the transcript.";
    }
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

<div class="field-card">
  <div class="flex items-center justify-between gap-4">
    <h4 class="field-head">Transcript recovery</h4>
    <button class="btn btn-sm btn-ghost" disabled={checking} onclick={checkTranscripts}>Check transcripts</button>
  </div>
  {#each unreadable as { id, runs } (id)}
    <div class="flex items-center justify-between gap-2">
      <code>{id}</code>
      <button class="btn btn-sm btn-ghost" disabled={runs.length > 0} onclick={() => archive(id)}>Archive</button>
    </div>
    {#if runs.length}
      <p class="text-[12.5px] text-muted">Resolve the recorded runs before archiving this transcript.</p>
      {#each runs as run (run)}
        <div class="flex items-center justify-between gap-2">
          <code>{run}</code>
          {#if unreadable.some((entry) => entry.id === run)}
            <span class="text-[12.5px] text-muted">Archive this unreadable run first.</span>
          {:else}
            <button class="btn btn-sm btn-ghost" onclick={() => deleteRun(run)}>Delete run</button>
          {/if}
        </div>
      {/each}
    {/if}
  {/each}
  {#each retained as item (item.name)}
    <div class="flex flex-wrap items-center justify-between gap-2">
      {#if item.kind === "run"}
        <span>Run without a launcher <code>{item.id}</code></span>
        <button class="btn btn-sm btn-ghost" onclick={() => onopen(item.id)}>Open run</button>
        <button class="btn btn-sm btn-ghost" onclick={() => deleteRun(item.id)}>Delete run</button>
      {:else if item.kind === "record"}
        <span>
          Inspect in the sessions directory <code>{item.name}</code>
          {#if item.repair}<br />{item.repair}{/if}
        </span>
      {:else}
        <code>{item.name}</code>
        <button class="btn btn-sm btn-ghost" disabled={checking} onclick={() => deleteRetained(item)}>
          {item.kind === "archive" ? "Delete archive" : "Delete saved output"}
        </button>
      {/if}
    </div>
  {/each}
  {#if feedback}<p class="text-[12.5px] text-muted" role="status">{feedback}</p>{/if}
</div>
