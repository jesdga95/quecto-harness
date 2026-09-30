<script lang="ts">
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import ArrowsOut from "phosphor-svelte/lib/ArrowsOut";
  import Browser from "phosphor-svelte/lib/Browser";
  import Code from "phosphor-svelte/lib/Code";
  import Eye from "phosphor-svelte/lib/Eye";
  import DownloadSimple from "phosphor-svelte/lib/DownloadSimple";
  import FileCode from "phosphor-svelte/lib/FileCode";
  import Folder from "phosphor-svelte/lib/Folder";
  import FolderOpen from "phosphor-svelte/lib/FolderOpen";
  import GitDiff from "phosphor-svelte/lib/GitDiff";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import PenNib from "phosphor-svelte/lib/PenNib";
  import Robot from "phosphor-svelte/lib/Robot";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import UsersThree from "phosphor-svelte/lib/UsersThree";
  import X from "phosphor-svelte/lib/X";
  import ArtifactPage from "./ArtifactPage.svelte";
  import ArtifactSvg from "./ArtifactSvg.svelte";
  import ChangesView from "./ChangesView.svelte";
  import CopyButton from "./CopyButton.svelte";
  import FileIcon from "./FileIcon.svelte";
  import PlanView from "./PlanView.svelte";
  import ProcessesView from "./ProcessesView.svelte";
  import Prose from "./Prose.svelte";
  import RunsView from "./RunsView.svelte";
  import type { Artifact } from "../artifacts";
  import { escapeHtml, sourceHtml } from "../highlight";
  import { Streamed } from "../stream.svelte";
  import {
    app,
    artifacts,
    browse,
    closePanel,
    closeTab,
    fileUrl,
    loadFile,
    openFile,
    openRun,
    showArtifact,
    showProcesses,
    showSection,
    toggleArtifactsWide,
    type Section,
  } from "../state/app.svelte";

  // A page over this size is shown as its source. Nothing that large is a page
  // worth running.
  const LIMIT = 512 * 1024;
  // Characters the path row holds at the narrowest dock, counting the
  // separators.
  const BUDGET = 40;

  const list = $derived(artifacts());
  // The Files section shows the folder when nothing is open in it. An id
  // that has gone, as a resend drops a reply, falls back to the newest file.
  const browsing = $derived(app.section === "files" && app.artifactId === "");
  const shown = $derived(
    app.section === "files" && !browsing
      ? (list.find((a) => a.id === app.artifactId) ?? list.at(-1))
      : undefined,
  );

  type Stacked = {
    id: string;
    label: string;
    on: boolean;
    artifact?: Artifact;
  };

  // The tabs never change: four sections, each with what is in it.
  const sections = $derived([
    { id: "files" as Section, label: "Files", count: app.tabs.length },
    { id: "changes" as Section, label: "Changes", count: app.changes.files.length },
    { id: "agents" as Section, label: "Agents", count: app.runs.length },
    { id: "processes" as Section, label: "Processes", count: app.procs.length },
  ]);

  // The stack: what is open in the section the tabs are on. Files keeps the
  // project folder before it, outside the list.
  const stack = $derived.by((): Stacked[] => {
    if (app.section === "files") {
      return app.tabs
        .map((id) => list.find((a) => a.id === id))
        .filter((a) => a !== undefined)
        .map((a) => ({ id: a.id, label: a.title, on: a.id === shown?.id, artifact: a }));
    }
    // Changes lists its files in the panel itself, so nothing stacks here.
    if (app.section === "changes") return [];
    if (app.section === "agents") {
      return app.runs.map((r) => ({
        id: r.id,
        label: r.agent || "Agent",
        on: r.id === app.runId,
      }));
    }
    return app.procs.map((p) => ({
      id: p.id,
      label: p.command,
      on: p.id === (app.procId || app.procs.at(-1)?.id),
    }));
  });

  function openSection(id: Section): void {
    showSection(id);
    if (id === "files" && app.artifactId === "") void browse("");
  }

  function openStacked(item: Stacked): void {
    if (app.section === "files") showArtifact(item.id);
    else if (app.section === "agents") void openRun(item.id);
    else showProcesses(item.id);
  }

  // A page over this size is shown as its source.
  const big = $derived(shown?.kind === "page" && shown.source.length > LIMIT);
  // A file in the project is read from disk when it is first shown. What the
  // harness said about it decides what can be drawn.
  // One still being written is drawn from the call's own text, since the file
  // may not be on disk until the call has run.
  const onDisk = $derived(!!shown?.path && !shown.fromWrite);
  const file = $derived(onDisk && shown?.path ? app.files[shown.path] : undefined);
  const fileError = $derived(onDisk && shown?.path ? app.fileErrors[shown.path] : undefined);
  const waiting = $derived(onDisk && !file && !fileError);
  const unreadable = $derived(
    onDisk && (!!fileError || (file !== undefined && file.kind !== "text" && file.kind !== "image")),
  );
  $effect(() => {
    const path = onDisk ? shown?.path : undefined;
    if (path && !app.files[path] && !app.fileErrors[path]) void loadFile(path);
  });
  $effect(() => {
    if (browsing && !app.browseDir && !app.browseError) void browse();
  });

  // How the file is drawn: rendered, for what can be, or as its code.
  let view = $state<"preview" | "code">("preview");
  const canPreview = $derived(
    !!shown &&
      !big &&
      !unreadable &&
      !waiting &&
      (shown.kind === "svg" || shown.kind === "page" || shown.kind === "plan" || shown.kind === "markdown"),
  );
  const current = $derived<"preview" | "code">(
    shown?.kind === "image" ? "preview" : canPreview ? view : "code",
  );

  // Bumped to run a page's scripts again from the start.
  let runs = $state(0);

  // A file still being written is paced like a streamed reply.
  const stream = new Streamed(
    () => shown?.source ?? "",
    () => !!shown && !shown.complete,
    () => shown?.id,
  );

  // The source is coloured once it is whole. Colouring a long file again on
  // every token of a stream costs more than the view is worth.
  const markup = $derived(
    !shown || current === "preview"
      ? ""
      : shown.complete
        ? sourceHtml(shown.source, shown.path ?? shown.name)
        : escapeHtml(stream.text),
  );

  let code = $state<HTMLElement | null>(null);
  $effect(() => {
    if (code) stream.draw(code, markup);
  });

  // One number a line, as a single run of text so a long file costs one node.
  const gutter = $derived.by(() => {
    const text = shown?.complete ? shown.source : stream.text;
    if (!shown || current === "preview" || text === "") return "";
    const lines = text.replace(/\n$/, "").split("\n").length;
    return Array.from({ length: lines }, (_, i) => i + 1).join("\n");
  });

  let sourceBox = $state<HTMLDivElement | null>(null);
  $effect(() => {
    void markup;
    if (sourceBox && shown && !shown.complete) sourceBox.scrollTop = sourceBox.scrollHeight;
  });

  // The path row: the project root, then the folders down to the file, each
  // folder opening in the browser. A deep path drops its middle segments so
  // the file at the end is still readable.
  const root = $derived((app.browseDir?.root || app.projectPath).replace(/\/$/, ""));
  const rootName = $derived(root.replace(/^\/Users\/[^/]+/, "~"));
  const pathRow = $derived.by(() => {
    const rel = browsing ? (app.browseDir?.path ?? "") : (shown?.path ?? "");
    const parts = rel ? rel.split("/") : [];
    const kept = parts.map((_, i) => i);
    const width = (base: number) => base + kept.reduce((n, i) => n + parts[i].length + 1, 0);
    const base = width(rootName.length) > BUDGET ? root.split("/").pop() ?? root : rootName;
    while (kept.length > 2 && width(base.length) > BUDGET) kept.splice(1, 1);
    return {
      label: base,
      crumbs: kept.map((i, k) => ({
        name: parts[i],
        path: parts.slice(0, i + 1).join("/"),
        last: i === parts.length - 1,
        gap: k > 0 && kept[k - 1] !== i - 1,
      })),
    };
  });

  function reload() {
    if (browsing) void browse();
    else if (shown?.path) void loadFile(shown.path, true).then(() => (runs += 1));
    else runs += 1;
  }

  // Saved as a file rather than opened in a tab: a blob address carries this
  // page's origin, so a page opened from one could call the harness with the
  // reader's session.
  function download() {
    if (!shown) return;
    const type =
      shown.kind === "svg"
        ? "image/svg+xml"
        : shown.kind === "page"
          ? "text/html"
          : shown.kind === "plan"
            ? "text/markdown"
            : "text/plain";
    const blob = new Blob([shown.source], { type });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    const given = (shown.path ?? shown.name).split("/").pop() ?? "";
    const stem = shown.title.replace(/[^\w .-]+/g, "").trim() || shown.kind;
    link.href = url;
    link.download = given || `${stem}.${shown.kind === "svg" ? "svg" : "html"}`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  function bytesLabel(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  }
</script>

{#snippet fileIcon(artifact: Artifact)}
  {#if artifact.path}
    <FileIcon path={artifact.path} size={14} />
  {:else if artifact.kind === "plan"}
    <ListChecks size={14} class="shrink-0" />
  {:else if artifact.kind === "svg"}
    <PenNib size={14} class="shrink-0" />
  {:else if artifact.kind === "page"}
    <Browser size={14} class="shrink-0" />
  {:else}
    <FileCode size={14} class="shrink-0" />
  {/if}
{/snippet}

<aside class="dock dock-wide" class:dock-full={app.artifactsWide}>
  <nav class="dock-tabs" aria-label="Dock sections">
    {#each sections as section (section.id)}
      <button
        class="tab"
        class:is-on={app.section === section.id}
        aria-current={app.section === section.id ? "true" : undefined}
        onclick={() => openSection(section.id)}
      >
        {#if section.id === "files"}
          <FolderOpen size={16} weight="fill" class="shrink-0" />
        {:else if section.id === "changes"}
          <GitDiff size={16} class="shrink-0" />
        {:else if section.id === "agents"}
          <UsersThree size={16} class="shrink-0" />
        {:else}
          <Terminal size={16} class="shrink-0" />
        {/if}
        <span class="tab-label">{section.label}</span>
        <span class="tab-count num">{section.count}</span>
      </button>
    {/each}
    <div class="ml-auto flex shrink-0 items-center gap-0.5">
      <button
        class="icon-btn"
        aria-label={app.artifactsWide ? "Show the conversation" : "Hide the conversation"}
        title={app.artifactsWide ? "Collapse" : "Expand"}
        onclick={toggleArtifactsWide}
      >
        {#if app.artifactsWide}<ArrowsIn />{:else}<ArrowsOut />{/if}
      </button>
      <button class="icon-btn changes-copy" aria-label="Close" title="Close" onclick={closePanel}>
        <X />
      </button>
    </div>
  </nav>

  <div class="panel">
    {#if app.section === "files" || stack.length}
      <div class="panel-stack">
        {#if app.section === "files"}
          <button
            class="stack-home"
            class:is-on={browsing}
            aria-label="Project"
            title="Project"
            onclick={() => void browse("")}
          >
            <Folder size={15} />
          </button>
        {/if}
        <div class="stack-tabs">
          {#each stack as item (item.id)}
            <div class="stack-tab" class:is-on={item.on}>
              <button class="stack-tab-main" title={item.artifact?.path ?? item.label} onclick={() => openStacked(item)}>
                {#if item.artifact}
                  {@render fileIcon(item.artifact)}
                {:else if app.section === "agents"}
                  <Robot size={14} class="shrink-0" />
                {:else}
                  <Terminal size={14} class="shrink-0" />
                {/if}
                <span class="truncate">{item.label}</span>
                {#if item.artifact && !item.artifact.complete}<span class="spinner shrink-0"></span>{/if}
              </button>
              {#if item.artifact}
                <button class="stack-tab-x" aria-label="Close {item.label}" onclick={() => closeTab(item.id)}>
                  <X size={12} />
                </button>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if app.section === "changes"}
      <ChangesView />
    {:else if app.section === "agents"}
      <RunsView />
    {:else if app.section === "processes"}
      <ProcessesView />
    {:else}
      <div class="panel-path">
        <div class="panel-crumbs">
          {#if browsing || shown?.path}
            <button class="crumb" class:is-last={pathRow.crumbs.length === 0} title={root} onclick={() => void browse("")}>
              {pathRow.label}
            </button>
            {#each pathRow.crumbs as step (step.path)}
              <span class="crumb-sep">/</span>
              {#if step.gap}
                <span class="crumb ellipsis" aria-hidden="true">…</span>
                <span class="crumb-sep">/</span>
              {/if}
              {#if step.last && !browsing}
                <span class="crumb is-last">{step.name}</span>
              {:else}
                <button class="crumb" class:is-last={step.last} onclick={() => void browse(step.path)}>{step.name}</button>
              {/if}
            {/each}
          {:else if shown}
            <span class="crumb">{shown.kind === "plan" ? "Plan draft" : "From a reply"}</span>
            <span class="crumb-sep">/</span>
            <span class="crumb is-last">{shown.title}</span>
          {/if}
        </div>
        <div class="flex shrink-0 items-center gap-0.5">
          {#if canPreview}
            <button
              class="icon-btn"
              aria-label={current === "preview" ? "Show the code" : "Show the preview"}
              title={current === "preview" ? "Code" : "Preview"}
              onclick={() => (view = current === "preview" ? "code" : "preview")}
            >
              {#if current === "preview"}<Code />{:else}<Eye />{/if}
            </button>
          {/if}
          {#if shown && shown.kind !== "image" && !unreadable && !waiting && current !== "preview"}
            <CopyButton text={shown.source} label="Copy source" />
            <button class="icon-btn" aria-label="Download" title="Download" onclick={download}>
              <DownloadSimple />
            </button>
          {/if}
          {#if browsing || shown?.path || (shown?.kind === "page" && shown.complete && current === "preview")}
            <button
              class="icon-btn"
              aria-label={browsing ? "List the folder again" : shown?.path ? "Read the file again" : "Run the page again"}
              title="Reload"
              onclick={reload}
            >
              <ArrowClockwise />
            </button>
          {/if}
        </div>
      </div>

      <div class="artifact-body">
        {#if browsing}
          {#if app.browseError}
            <p class="artifact-wait">{app.browseError}</p>
          {:else if !app.browseDir}
            <p class="artifact-wait">Reading the folder</p>
          {:else if app.browseDir.entries.length === 0}
            <p class="artifact-wait">This folder is empty.</p>
          {:else}
            <div class="browse-list">
              {#each app.browseDir.entries as entry (entry.name)}
                {@const path = app.browseDir.path ? `${app.browseDir.path}/${entry.name}` : entry.name}
                <button
                  class="browse-row"
                  onclick={() => (entry.dir ? void browse(path) : void openFile(path))}
                >
                  {#if entry.dir}
                    <Folder size={17} class="shrink-0 text-muted" />
                  {:else}
                    <FileIcon {path} size={17} />
                  {/if}
                  <span class="min-w-0 flex-1 truncate">{entry.name}</span>
                  {#if !entry.dir}
                    <span class="num text-[11.5px] text-muted">{bytesLabel(entry.bytes)}</span>
                  {/if}
                </button>
              {/each}
            </div>
          {/if}
        {:else if shown?.path && fileError}
          <p class="artifact-wait">{fileError}</p>
        {:else if waiting}
          <p class="artifact-wait">Reading {shown?.path}</p>
        {:else if file && file.kind === "binary"}
          <p class="artifact-wait">{file.path} is a binary file, so there is nothing to show.</p>
        {:else if file && file.kind === "large"}
          <p class="artifact-wait">{file.path} is too large to show here.</p>
        {:else if shown?.kind === "image" && shown.path}
          <div class="artifact-paper">
            {#key runs}
              <img class="artifact-img" src={fileUrl(shown.path)} alt={shown.title} />
            {/key}
          </div>
        {:else if shown}
          {#if current !== "preview"}
            {#if big}
              <p class="artifact-note">This page is too large to run, so it is shown as its source.</p>
            {/if}
            <div class="artifact-source">
              <div class="artifact-source-scroll" bind:this={sourceBox}>
                <div class="source-rows">
                  <pre class="source-gutter num" aria-hidden="true">{gutter}</pre>
                  <pre><code bind:this={code}></code></pre>
                </div>
              </div>
            </div>
          {:else if shown.kind === "plan"}
            <div class="artifact-plan">
              {#if app.draft}
                <PlanView plan={app.draft} />
              {:else}
                <Prose text={shown.source} />
              {/if}
            </div>
          {:else if shown.kind === "markdown"}
            <div class="artifact-plan">
              <Prose text={shown.source} streaming={!shown.complete} />
            </div>
          {:else if shown.kind === "svg"}
            {#key `${shown.id}:${runs}`}
              <ArtifactSvg source={shown.source} complete={shown.complete} />
            {/key}
          {:else}
            {#key `${shown.id}:${runs}`}
              <ArtifactPage source={shown.source} complete={shown.complete} />
            {/key}
          {/if}
        {/if}
      </div>
    {/if}
  </div>
</aside>
