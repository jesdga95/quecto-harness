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
  import UsersThree from "phosphor-svelte/lib/UsersThree";
  import ChangesView from "./ChangesView.svelte";
  import RunsView from "./RunsView.svelte";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import PenNib from "phosphor-svelte/lib/PenNib";
  import Plus from "phosphor-svelte/lib/Plus";
  import X from "phosphor-svelte/lib/X";
  import ArtifactPage from "./ArtifactPage.svelte";
  import ArtifactSvg from "./ArtifactSvg.svelte";
  import CopyButton from "./CopyButton.svelte";
  import FileIcon from "./FileIcon.svelte";
  import PlanView from "./PlanView.svelte";
  import Popover from "./Popover.svelte";
  import Prose from "./Prose.svelte";
  import type { Artifact } from "../artifacts";
  import { escapeHtml, sourceHtml } from "../highlight";
  import {
    app,
    artifacts,
    browse,
    BROWSE_ID,
    CHANGES_ID,
    closePanel,
    closeTab,
    RUNS_ID,
    showTab,
    fileUrl,
    loadFile,
    openFile,
    showArtifact,
    toggleArtifactsWide,
  } from "../state/app.svelte";

  // A page over this size is shown as its source. Nothing that large is a page
  // worth running.
  const LIMIT = 512 * 1024;

  const list = $derived(artifacts());
  const FIXED = [BROWSE_ID, CHANGES_ID, RUNS_ID];
  const fixed = $derived(FIXED.includes(app.artifactId) ? app.artifactId : "");
  const browsing = $derived(fixed === BROWSE_ID);
  const shown = $derived(
    fixed ? undefined : (list.find((a) => a.id === app.artifactId) ?? list.at(-1)),
  );
  // A tab is a fixed one or an artifact. An artifact can go, as a resend
  // drops a reply, so tabs are read through the list.
  type Tab = { id: string; label: string; artifact?: Artifact };
  const LABELS: Record<string, string> = { [BROWSE_ID]: "Files", [CHANGES_ID]: "Changes", [RUNS_ID]: "Agents" };
  const tabs = $derived(
    app.tabs.flatMap<Tab>((id) => {
      if (LABELS[id]) return [{ id, label: LABELS[id] }];
      const artifact = list.find((a) => a.id === id);
      return artifact ? [{ id, label: artifact.title, artifact }] : [];
    }),
  );
  const activeTab = $derived(fixed || shown?.id);
  // What "+" offers: the fixed tabs that have something in them, and the
  // artifacts not open yet.
  let addOpen = $state(false);
  const closed = $derived(list.filter((a) => !app.tabs.includes(a.id)));

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

  // How the tab is drawn: rendered, for what can be, or as its code.
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

  // The source is coloured once it is whole. Colouring a long file again on
  // every token of a stream costs more than the view is worth.
  const markup = $derived(
    !shown || current === "preview"
      ? ""
      : shown.complete
        ? sourceHtml(shown.source, shown.path ?? shown.name)
        : escapeHtml(shown.source),
  );

  // One number a line, as a single run of text so a long file costs one node.
  const gutter = $derived.by(() => {
    if (!shown || current === "preview" || shown.source === "") return "";
    const lines = shown.source.replace(/\n$/, "").split("\n").length;
    return Array.from({ length: lines }, (_, i) => i + 1).join("\n");
  });

  let sourceBox = $state<HTMLDivElement | null>(null);
  $effect(() => {
    void markup;
    if (sourceBox && shown && !shown.complete) sourceBox.scrollTop = sourceBox.scrollHeight;
  });

  // The path row: the project root, then the folders down to the file, each
  // folder opening in the browser.
  const root = $derived((app.browseDir?.root || app.projectPath).replace(/\/$/, ""));
  const trail = $derived.by(() => {
    const rel = browsing ? (app.browseDir?.path ?? "") : (shown?.path ?? "");
    const parts = rel ? rel.split("/") : [];
    return parts.map((name, i) => ({
      name,
      path: parts.slice(0, i + 1).join("/"),
      last: i === parts.length - 1,
    }));
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

{#snippet tabIcon(artifact: Artifact)}
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
  <div class="panel-tabs">
    <div class="panel-tab-strip" role="tablist">
      {#each tabs as tab (tab.id)}
        <div class="panel-tab" class:is-on={tab.id === activeTab}>
          <button
            class="panel-tab-main"
            role="tab"
            aria-selected={tab.id === activeTab}
            title={tab.artifact?.path ?? tab.label}
            onclick={() => (tab.id === BROWSE_ID ? void browse() : showTab(tab.id))}
          >
            {#if tab.id === BROWSE_ID}
              <FolderOpen size={14} weight="fill" class="shrink-0 text-accent" />
            {:else if tab.id === CHANGES_ID}
              <GitDiff size={14} class="shrink-0" />
            {:else if tab.id === RUNS_ID}
              <UsersThree size={14} class="shrink-0" />
            {:else if tab.artifact}
              {@render tabIcon(tab.artifact)}
            {/if}
            <span class="truncate">{tab.label}</span>
            {#if tab.artifact && !tab.artifact.complete}<span class="spinner shrink-0"></span>{/if}
            {#if tab.id === RUNS_ID && app.runs.some((r) => r.status !== "idle")}<span class="spinner shrink-0"></span>{/if}
          </button>
          <button class="panel-tab-x" aria-label="Close {tab.label}" onclick={() => closeTab(tab.id)}>
            <X size={12} />
          </button>
        </div>
      {/each}
      <div class="relative shrink-0">
        <button
          class="icon-btn"
          aria-label="Open a tab"
          title="Open a tab"
          aria-expanded={addOpen}
          onclick={() => (addOpen = !addOpen)}
        >
          <Plus />
        </button>
        {#if addOpen}
          <Popover onclose={() => (addOpen = false)} float width={260}>
            <button class="popover-item" onclick={() => ((addOpen = false), void browse())}>
              <FolderOpen size={15} class="shrink-0" /> Files
            </button>
            {#if app.changes.files.length > 0}
              <button class="popover-item" onclick={() => ((addOpen = false), showTab(CHANGES_ID))}>
                <GitDiff size={15} class="shrink-0" /> Changes
                <span class="num ml-auto text-[12px]">
                  <span class="stat-add">+{app.changes.added}</span>
                  <span class="stat-del">&minus;{app.changes.removed}</span>
                </span>
              </button>
            {/if}
            {#if app.runs.length > 0}
              <button class="popover-item" onclick={() => ((addOpen = false), showTab(RUNS_ID))}>
                <UsersThree size={15} class="shrink-0" /> Agents
              </button>
            {/if}
            {#each closed as artifact (artifact.id)}
              <button class="popover-item" onclick={() => ((addOpen = false), showArtifact(artifact.id))}>
                {@render tabIcon(artifact)}
                <span class="truncate">{artifact.title}</span>
              </button>
            {/each}
          </Popover>
        {/if}
      </div>
    </div>
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
  </div>

  {#if fixed === CHANGES_ID}
    <ChangesView />
  {:else if fixed === RUNS_ID}
    <RunsView />
  {:else}
    <div class="panel-path">
      <div class="panel-crumbs">
        {#if browsing || shown?.path}
          <button class="crumb" class:is-last={trail.length === 0} onclick={() => void browse("")}>
            {root.replace(/^\/Users\/[^/]+/, "~")}
          </button>
          {#each trail as step (step.path)}
            <span class="crumb-sep">/</span>
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
                <pre><code>{@html markup}</code></pre>
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
</aside>
