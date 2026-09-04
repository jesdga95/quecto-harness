<script lang="ts">
  import Folder from "phosphor-svelte/lib/Folder";
  import RowMenu from "./RowMenu.svelte";
  import { confirm } from "../state/confirm.svelte";
  import FolderPlus from "phosphor-svelte/lib/FolderPlus";
  import Gear from "phosphor-svelte/lib/Gear";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlass";
  import Plus from "phosphor-svelte/lib/Plus";
  import SidebarSimple from "phosphor-svelte/lib/SidebarSimple";
  import Logo from "./Logo.svelte";
  import {
    app,
    newSession,
    pickAndAddProject,
    selectProject,
    selectSession,
    sessionsOf,
    removeProject,
    deleteSession,
    renameSession,
  } from "../state/app.svelte";
  import { since } from "../format";

  let { onsettings }: { onsettings: () => void } = $props();

  let searching = $state(false);
  let query = $state("");
  let renaming = $state("");
  let draft = $state("");

  function sessions(path: string) {
    const list = sessionsOf(path);
    const q = query.trim().toLowerCase();
    return q ? list.filter((s) => s.title.toLowerCase().includes(q)) : list;
  }

  function projects() {
    const q = query.trim().toLowerCase();
    if (!q) return app.projects;
    return app.projects.filter(
      (p) => p.name.toLowerCase().includes(q) || sessions(p.path).length > 0,
    );
  }
</script>

{#if app.sidebar}
  <aside class="flex w-[276px] shrink-0 flex-col border-r border-hair bg-panel">
    <div class="flex h-[64px] items-center gap-2 px-4">
      <Logo size={28} />
      <span class="chip-harness">Harness</span>
      <button
        class="icon-btn ml-auto"
        aria-label="Collapse sidebar"
        onclick={() => (app.sidebar = false)}
      >
        <SidebarSimple />
      </button>
    </div>

    <div class="px-3 pb-3">
      <button class="btn btn-ghost w-full justify-center" onclick={() => newSession()}>
        <Plus /> New session
      </button>
    </div>

    <div class="flex items-center gap-1 px-4 pb-1">
      <span class="label flex-1">Projects</span>
      <button
        class="icon-btn"
        aria-label="Search sessions"
        onclick={() => {
          searching = !searching;
          if (!searching) query = "";
        }}
      >
        <MagnifyingGlass />
      </button>
      <button class="icon-btn" aria-label="Add project" onclick={() => pickAndAddProject()}>
        <FolderPlus />
      </button>
    </div>

    {#if searching}
      <div class="px-3 pb-2">
        <input class="input" placeholder="Search" bind:value={query} />
      </div>
    {/if}

    <nav class="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
      {#each projects() as project (project.path)}
        <div class="row-wrap">
          <button class="side-row" onclick={() => selectProject(project.path)}>
            <Folder />
            <span
              class="min-w-0 flex-1 truncate"
              class:text-ink={project.path === app.projectPath}
              class:font-medium={project.path === app.projectPath}>{project.name}</span
            >
          </button>
          <div class="row-act">
            <RowMenu
              deleteLabel="Remove"
              ondelete={async () => {
                const count = sessions(project.path).length;
                const ok = await confirm({
                  title: `Remove ${project.name}?`,
                  body:
                    (count === 1
                      ? "Its 1 conversation is deleted. "
                      : count > 1
                        ? `Its ${count} conversations are deleted. `
                        : "") +
                    "The folder and the files in it are left alone, and its allowlist rules are kept for when you add it back.",
                  confirmLabel: count > 0 ? "Remove and delete" : "Remove",
                });
                if (ok) await removeProject(project.path);
              }}
            />
          </div>
        </div>

        <div class="mb-3 pl-3">
          {#each sessions(project.path) as session (session.id)}
            {#if renaming === session.id}
              <input
                class="input mb-[3px]"
                bind:value={draft}
                onkeydown={(e) => {
                  if (e.key === "Enter") {
                    renaming = "";
                    void renameSession(session.id, draft);
                  }
                  if (e.key === "Escape") renaming = "";
                }}
                onblur={() => (renaming = "")}
                aria-label="Session name"
              />
            {:else}
              <div class="row-wrap">
                <button
                  class="side-row"
                  class:is-active={session.id === app.sessionId}
                  onclick={() => selectSession(session.id)}
                >
                  <span class="min-w-0 flex-1 truncate">{session.title}</span>
                  <span class="num text-[11px] text-muted">{since(session.created)}</span>
                </button>
                <div class="row-act">
                  <RowMenu
                    onrename={() => {
                      draft = session.title;
                      renaming = session.id;
                    }}
                    ondelete={async () => {
                      const ok = await confirm({
                        title: "Delete this session?",
                        body: "Its transcript is removed from disk. This cannot be undone.",
                        confirmLabel: "Delete",
                      });
                      if (ok) await deleteSession(session.id);
                    }}
                  />
                </div>
              </div>
            {/if}
          {/each}
        </div>
      {/each}
    </nav>

    <div class="px-2 pb-3">
      <button class="side-row" onclick={onsettings}>
        <Gear />
        Settings
      </button>
    </div>
  </aside>
{:else}
  <!-- 61px, because the 1px right border is inside the width: that leaves 60px
       of content, and a centred 28px mark then starts at 16px, exactly where
       the expanded header's padding puts it. -->
  <aside class="flex w-[61px] shrink-0 flex-col items-center border-r border-hair bg-panel">
    <div class="flex h-[64px] items-center">
      <Logo size={28} wordmark={false} />
    </div>
    <button
      class="icon-btn"
      aria-label="Expand sidebar"
      onclick={() => (app.sidebar = true)}
    >
      <SidebarSimple />
    </button>
    <button class="icon-btn mt-2" aria-label="New session" onclick={() => newSession()}>
      <Plus />
    </button>
    <button class="icon-btn mt-auto mb-4" aria-label="Settings" onclick={onsettings}>
      <Gear />
    </button>
  </aside>
{/if}
