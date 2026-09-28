<script lang="ts">
  import CaretRight from "phosphor-svelte/lib/CaretRight";
  import Folder from "phosphor-svelte/lib/Folder";
  import FolderOpen from "phosphor-svelte/lib/FolderOpen";
  import PlusCircle from "phosphor-svelte/lib/PlusCircle";
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
    isWorking,
    selectSession,
    sessionsOf,
    removeProject,
    deleteSession,
    renameSession,
    reorderProjects,
  } from "../state/app.svelte";
  import { since } from "../format";

  let { onsettings }: { onsettings: () => void } = $props();

  let searching = $state(false);
  let query = $state("");
  let renaming = $state("");
  let draft = $state("");

  // What a drag carries: a project moving in the project list.
  type Drag = { kind: "project"; id: string };

  let drag = $state<Drag | null>(null);
  // The row a drop would land against, and which of its edges it takes.
  let over = $state<{ target: Drag; before: boolean } | null>(null);

  // A query narrows the lists, and the order of a narrow list is not the
  // order of the whole one, so rows only carry while the lists stand whole.
  const canDrag = $derived(query.trim() === "");

  function startDrag(e: DragEvent, d: Drag): void {
    if (!canDrag) return;
    drag = d;
    // Firefox will not begin a drag without payload text.
    e.dataTransfer?.setData("text/plain", d.id);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
  }

  function passOverRow(e: DragEvent, target: Drag): void {
    if (!drag || drag.kind !== target.kind) return;
    if (drag.id === target.id) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const before = e.clientY < rect.top + rect.height / 2;
    if (!over || over.target.id !== target.id || over.before !== before) {
      over = { target, before };
    }
  }

  function leaveRow(e: DragEvent, id: string): void {
    if (!drag) return;
    const el = e.currentTarget as HTMLElement;
    const to = e.relatedTarget as Node | null;
    // Crossing into a child of the same row is not leaving it.
    if (to && el.contains(to)) return;
    if (over?.target.id === id) over = null;
  }

  function endDrag(): void {
    drag = null;
    over = null;
  }

  // A drop anywhere in the list is allowed, so a release in the gap between
  // rows lands at the edge the indicator last showed.
  function navDragOver(e: DragEvent): void {
    if (drag) e.preventDefault();
  }

  function drop(e: DragEvent): void {
    e.preventDefault();
    const from = drag;
    const place = over;
    drag = null;
    over = null;
    if (!from || !place || from.kind !== place.target.kind) return;

    const moved = app.projects.find((p) => p.path === from.id);
    if (!moved) return;
    const list = app.projects.filter((p) => p.path !== from.id);
    const at = list.findIndex((p) => p.path === place.target.id);
    if (at === -1) return;
    list.splice(at + (place.before ? 0 : 1), 0, moved);
    if (list.every((p, i) => app.projects[i]?.path === p.path)) return;
    void reorderProjects(list);
  }

  // Projects whose sessions are folded away. It is this browser's own
  // preference, so it lives in local storage and a blocked store costs only
  // the memory of it.
  const FOLDED_KEY = "quecto.folded-projects";
  let folded = $state<string[]>(readFolded());
  function readFolded(): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(FOLDED_KEY) ?? "[]");
      return Array.isArray(raw) ? raw.filter((p): p is string => typeof p === "string") : [];
    } catch {
      return [];
    }
  }
  function toggleFold(path: string): void {
    folded = folded.includes(path) ? folded.filter((p) => p !== path) : [...folded, path];
    try {
      localStorage.setItem(FOLDED_KEY, JSON.stringify(folded));
    } catch {
      // The fold holds for this page.
    }
  }
  // A search shows every match, folded or not.
  const isFolded = (path: string) => query.trim() === "" && folded.includes(path);


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
      <button class="new-session" onclick={() => newSession()}>
        <PlusCircle /> New session
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

    <!-- The top padding lets the drop line above the first row stay inside
         the scroll box, which would otherwise clip it. -->
    <nav
      class="min-h-0 flex-1 overflow-y-auto px-2 pt-1 pb-3"
      ondragover={navDragOver}
      ondrop={drop}
    >
      {#each projects() as project (project.path)}
        <!-- The whole group is the drop target, since a drop moves the project
             with its sessions, and the line marks the group's edge. -->
        <div
          class="project-group"
          class:is-dragging={drag?.id === project.path}
          role="group"
          ondragover={(e) => passOverRow(e, { kind: "project", id: project.path })}
          ondragleave={(e) => leaveRow(e, project.path)}
        >
          {#if canDrag && over && over.target.id === project.path && over.before}
            <div class="drop-line is-top"></div>
          {/if}
          <div class="row-wrap" class:is-draggable={canDrag}>
            <button
              class="side-row project-row"
              class:is-current={project.path === app.projectPath}
              draggable={canDrag}
              ondragstart={(e) => startDrag(e, { kind: "project", id: project.path })}
              ondragend={endDrag}
              onclick={() => selectProject(project.path)}
            >
              <span class="folder-glyph">
                {#if project.path === app.projectPath}<FolderOpen weight="fill" />{:else}<Folder />{/if}
              </span>
              <span class="min-w-0 flex-1 truncate">{project.name}</span>
            </button>
            {#if sessions(project.path).length > 0}
              <button
                class="fold-toggle"
                aria-label={isFolded(project.path) ? `Show the sessions in ${project.name}` : `Hide the sessions in ${project.name}`}
                aria-expanded={!isFolded(project.path)}
                onclick={() => toggleFold(project.path)}
              >
                <span class="caret" class:is-open={!isFolded(project.path)}><CaretRight /></span>
              </button>
            {/if}
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

          {#if !isFolded(project.path)}
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
                    class="side-row session-row"
                    class:is-active={session.id === app.sessionId}
                    onclick={() => selectSession(session.id)}
                  >
                    {#if isWorking(session.id)}
                      <span class="working" role="status" aria-label="Working"></span>
                    {/if}
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
          {:else}
            <div class="mb-2"></div>
          {/if}
          {#if canDrag && over && over.target.id === project.path && !over.before}
            <div class="drop-line is-bottom"></div>
          {/if}
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
