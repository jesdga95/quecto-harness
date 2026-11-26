<script lang="ts">
  import Folder from "phosphor-svelte/lib/FolderIcon";
  import FolderOpen from "phosphor-svelte/lib/FolderOpenIcon";
  import PlusCircle from "phosphor-svelte/lib/PlusCircleIcon";
  import RowMenu from "./RowMenu.svelte";
  import { confirm } from "../state/confirm.svelte";
  import CaretDown from "phosphor-svelte/lib/CaretDownIcon";
  import FolderDashed from "phosphor-svelte/lib/FolderDashedIcon";
  import FolderPlus from "phosphor-svelte/lib/FolderPlusIcon";
  import Gear from "phosphor-svelte/lib/GearIcon";
  import MagnifyingGlass from "phosphor-svelte/lib/MagnifyingGlassIcon";
  import SidebarSimple from "phosphor-svelte/lib/SidebarSimpleIcon";
  import X from "phosphor-svelte/lib/XIcon";
  import Logo from "./Logo.svelte";
  import ThemeButton from "./ThemeButton.svelte";
  import {
    app,
    newSession,
    pickAndAddProject,
    selectLoose,
    selectProject,
    LOOSE,
    isWorking,
    hasBackground,
    needsYou,
    turnFinished,
    selectSession,
    sessionsOf,
    removeProject,
    deleteSession,
    renameSession,
    reorderProjects,
  } from "../state/app.svelte";
  import { keyLabels, SEARCH_EVENT } from "../keys";
  import { tick } from "svelte";

  // As an overlay on a narrow window its collapse button closes it.
  let {
    onsettings,
    overlay = false,
    onclose,
  }: { onsettings: () => void; overlay?: boolean; onclose?: () => void } = $props();

  // Search is a row until pressed, then a field in its place. Escape, the
  // clear button or leaving the field empty puts the row back.
  let searching = $state(false);
  let query = $state("");
  let searchField = $state<HTMLInputElement | null>(null);
  const keys = keyLabels();

  async function openSearch(): Promise<void> {
    searching = true;
    await tick();
    searchField?.focus();
  }
  function closeSearch(): void {
    query = "";
    searching = false;
  }

  $effect(() => {
    const open = () => void openSearch();
    window.addEventListener(SEARCH_EVENT, open);
    return () => window.removeEventListener(SEARCH_EVENT, open);
  });

  function searchKey(e: KeyboardEvent): void {
    if (e.key === "Escape") closeSearch();
  }
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
  let folded = $state<string[]>(readList(FOLDED_KEY));
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

  // A project shows its first sessions and offers the rest; a project opened
  // to show them all stays open, remembered the same way as a fold.
  const SHOWN = 10;
  const EXPANDED_KEY = "quecto.expanded-projects";
  let expanded = $state<string[]>(readList(EXPANDED_KEY));
  function readList(key: string): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(key) ?? "[]");
      return Array.isArray(raw) ? raw.filter((p): p is string => typeof p === "string") : [];
    } catch {
      return [];
    }
  }
  function toggleExpanded(path: string): void {
    expanded = expanded.includes(path) ? expanded.filter((p) => p !== path) : [...expanded, path];
    try {
      localStorage.setItem(EXPANDED_KEY, JSON.stringify(expanded));
    } catch {
      // It holds for this page.
    }
  }
  // The open session always shows, even when it sits past the first few.
  function visible(path: string) {
    const list = sessions(path);
    if (query.trim() || expanded.includes(path)) return list;
    const open = list.findIndex((s) => s.id === app.sessionId);
    return list.slice(0, Math.max(SHOWN, open + 1));
  }

  // A project's row picks it as the one new sessions go to, opening it if it
  // was folded. Pressing the current project folds or opens it.
  function pressProject(path: string): void {
    if (app.loose || path !== app.projectPath) {
      void selectProject(path);
      if (folded.includes(path)) toggleFold(path);
    } else {
      toggleFold(path);
    }
  }


  // The row of the conversations without a folder works like a project's.
  function pressLoose(): void {
    if (!app.loose) {
      void selectLoose();
      if (folded.includes(LOOSE)) toggleFold(LOOSE);
    } else {
      toggleFold(LOOSE);
    }
  }

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

<!-- A group's conversations: a project's, or those without a folder. -->
{#snippet tree(key: string)}
    {#if !isFolded(key) && sessions(key).length > 0}
      <div class="side-tree">
        {#each visible(key) as session (session.id)}
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
          {@const held = needsYou(session.id)}
          {@const back = hasBackground(session.id)}
          <div class="row-wrap">
            <button
              class="side-row session-row"
              class:is-active={session.id === (app.opening || app.sessionId)}
              onclick={() => selectSession(session.id)}
            >
              {#if held}
                <span class="needs-you" role="status" aria-label="Waiting on you"></span>
              {:else if back}
                <span class="background" role="status" aria-label="Running in the background"></span>
              {:else if isWorking(session.id)}
                <span class="working" role="status" aria-label="Working"></span>
              {:else if turnFinished(session.id)}
                <span class="finished" role="status" aria-label="Turn finished"></span>
              {/if}
              <span class="min-w-0 flex-1 truncate">{session.title}</span>
              {#if held}
                <span class="side-meta text-[11px] text-warn">Pending</span>
              {:else if back}
                <span class="side-meta text-[11px] text-tools">Background</span>
              {/if}
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
                    body: session.loose
                      ? "Its transcript and every file in its folder are removed from disk. This cannot be undone."
                      : "Its transcript is removed from disk. This cannot be undone.",
                    confirmLabel: "Delete",
                  });
                  if (ok) await deleteSession(session.id);
                }}
              />
            </div>
          </div>
        {/if}
      {/each}
        {#if !query.trim() && (expanded.includes(key) ? sessions(key).length > SHOWN : sessions(key).length > visible(key).length)}
          <button class="side-row session-row side-more" onclick={() => toggleExpanded(key)}>
            <span class="side-more-mark" class:is-open={expanded.includes(key)}><CaretDown /></span>
            {expanded.includes(key)
              ? "Show less"
              : `Show ${sessions(key).length - visible(key).length} more`}
          </button>
        {/if}
      </div>
    {:else}
      <div class="mb-2"></div>
    {/if}
{/snippet}

<!-- Two edges line everything up: icons, the logo and labels start 20px in,
     and icons, counts and times end 20px from the right. Row backgrounds run
     from 12px in on each side. -->
<aside class="flex w-[276px] shrink-0 flex-col border-r border-hair bg-panel">
    <div class="side-brand flex h-[56px] items-center gap-2 pr-[15px] pl-5">
      <Logo size={24} text={12} />
      <span class="chip-harness">Harness</span>
      <button
        class="icon-btn ml-auto"
        aria-label={overlay ? "Close sidebar" : "Collapse sidebar"}
        onclick={() => (overlay ? onclose?.() : (app.sidebar = false))}
      >
        <SidebarSimple />
      </button>
    </div>

    <div class="px-3">
      {#if searching}
        <label class="side-search">
          <MagnifyingGlass />
          <input
            bind:this={searchField}
            bind:value={query}
            onkeydown={searchKey}
            onblur={() => query.trim() === "" && closeSearch()}
            placeholder="Search sessions"
            aria-label="Search sessions"
          />
          <button class="side-search-clear" aria-label="Close the search" onclick={closeSearch}>
            <X />
          </button>
        </label>
      {:else}
        <button class="side-act" onclick={() => newSession()}>
          <PlusCircle /> New session
          <span class="side-kbd">{keys.newSession}</span>
        </button>
        <button class="side-act" onclick={() => void openSearch()}>
          <MagnifyingGlass /> Search
          <span class="side-kbd">{keys.search}</span>
        </button>
      {/if}
    </div>

    <div class="flex items-center gap-1 pt-3 pr-[15px] pb-1.5 pl-5">
      <span class="label flex-1">Projects</span>
      <button class="icon-btn" aria-label="Add project" onclick={() => pickAndAddProject()}>
        <FolderPlus />
      </button>
    </div>

    <!-- The top padding lets the drop line above the first row stay inside
         the scroll box, which would otherwise clip it. -->
    <nav
      class="min-h-0 flex-1 overflow-y-auto px-3 pt-1 pb-3"
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
              class:is-current={!app.loose && project.path === app.projectPath}
              draggable={canDrag}
              ondragstart={(e) => startDrag(e, { kind: "project", id: project.path })}
              ondragend={endDrag}
              onclick={() => pressProject(project.path)}
              aria-expanded={!isFolded(project.path)}
            >
              <span class="folder-glyph" class:is-open={!isFolded(project.path)}>
                {#if isFolded(project.path)}<Folder />{:else}<FolderOpen />{/if}
              </span>
              <span class="min-w-0 flex-1 truncate">{project.name}</span>
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

          {@render tree(project.path)}
          {#if canDrag && over && over.target.id === project.path && !over.before}
            <div class="drop-line is-bottom"></div>
          {/if}
        </div>
      {/each}

      <!-- Conversations without a folder, always offered, so a harness with
           no project can still be talked to. -->
      {#if !query.trim() || sessions(LOOSE).length > 0}
        <div class="project-group" role="group">
          <div class="row-wrap">
            <button
              class="side-row project-row"
              class:is-current={app.loose}
              onclick={pressLoose}
              aria-expanded={!isFolded(LOOSE)}
            >
              <span class="folder-glyph" class:is-open={!isFolded(LOOSE)}><FolderDashed /></span>
              <span class="min-w-0 flex-1 truncate">No folder</span>
            </button>
          </div>
          {@render tree(LOOSE)}
        </div>
      {/if}
    </nav>

    <!-- The list ends against a hairline, so its last row never runs into
         Settings. -->
    <div class="flex items-center gap-2 border-t border-hair pt-1.5 pr-[15px] pb-2 pl-3">
      <button class="side-row mb-0 min-w-0 flex-1" onclick={onsettings}>
        <Gear />
        Settings
      </button>
      <ThemeButton />
    </div>
  </aside>

