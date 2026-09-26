<script lang="ts">
  import ArrowClockwise from "phosphor-svelte/lib/ArrowClockwise";
  import ArrowsIn from "phosphor-svelte/lib/ArrowsIn";
  import ArrowsOut from "phosphor-svelte/lib/ArrowsOut";
  import Browser from "phosphor-svelte/lib/Browser";
  import Code from "phosphor-svelte/lib/Code";
  import DownloadSimple from "phosphor-svelte/lib/DownloadSimple";
  import Eye from "phosphor-svelte/lib/Eye";
  import FileCode from "phosphor-svelte/lib/FileCode";
  import ListChecks from "phosphor-svelte/lib/ListChecks";
  import PlanView from "./PlanView.svelte";
  import Prose from "./Prose.svelte";
  import PenNib from "phosphor-svelte/lib/PenNib";
  import CaretDown from "phosphor-svelte/lib/CaretDown";
  import X from "phosphor-svelte/lib/X";
  import Popover from "./Popover.svelte";
  import ArtifactPage from "./ArtifactPage.svelte";
  import ArtifactSvg from "./ArtifactSvg.svelte";
  import CopyButton from "./CopyButton.svelte";
  import { escapeHtml, sourceHtml } from "../highlight";
  import {
    app,
    artifacts,
    closeDock,
    showArtifact,
    toggleArtifactsWide,
  } from "../state/app.svelte";

  // A page over this size is shown as its source. Nothing that large is a page
  // worth running.
  const LIMIT = 512 * 1024;

  const list = $derived(artifacts());
  const shown = $derived(list.find((a) => a.id === app.artifactId) ?? list.at(-1));
  const big = $derived(shown?.kind === "page" && shown.source.length > LIMIT);
  // A file other than a drawing or a page has nothing to preview.
  const onlySource = $derived(shown?.kind === "file" || big);

  let asSource = $state(false);
  let listOpen = $state(false);
  // Bumped to run a page's scripts again from the start.
  let runs = $state(0);

  // The source is coloured once it is whole. Colouring a long file again on
  // every token of a stream costs more than the view is worth.
  const markup = $derived(
    !shown ? "" : shown.complete ? sourceHtml(shown.source, shown.name) : escapeHtml(shown.source),
  );

  let sourceBox = $state<HTMLDivElement | null>(null);
  $effect(() => {
    void markup;
    if (sourceBox && shown && !shown.complete) sourceBox.scrollTop = sourceBox.scrollHeight;
  });

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
    const given = shown.name.split("/").pop() ?? "";
    const stem = shown.title.replace(/[^\w .-]+/g, "").trim() || shown.kind;
    link.href = url;
    link.download = given || `${stem}.${shown.kind === "svg" ? "svg" : "html"}`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
</script>

<aside class="dock dock-wide" class:dock-full={app.artifactsWide}>
  <div class="dock-head">
    {#if list.length > 1}
      <div class="relative min-w-0">
        <button
          class="artifact-pick"
          class:is-open={listOpen}
          aria-haspopup="menu"
          aria-expanded={listOpen}
          onclick={() => (listOpen = !listOpen)}
        >
          <span class="truncate">{shown?.title ?? "Artifacts"}</span>
          <span class="num text-[12px] font-normal text-muted">{list.length}</span>
          <CaretDown size={12} class="shrink-0 text-muted" />
        </button>
        {#if listOpen}
          <Popover onclose={() => (listOpen = false)} width={300}>
            {#each list as artifact (artifact.id)}
              <button
                class="popover-item"
                class:is-on={artifact.id === shown?.id}
                onclick={() => {
                  listOpen = false;
                  showArtifact(artifact.id);
                }}
              >
                {#if artifact.kind === "plan"}
                  <ListChecks size={15} class="shrink-0" />
                {:else if artifact.kind === "svg"}
                  <PenNib size={15} class="shrink-0" />
                {:else if artifact.kind === "page"}
                  <Browser size={15} class="shrink-0" />
                {:else}
                  <FileCode size={15} class="shrink-0" />
                {/if}
                <span class="truncate">{artifact.title}</span>
                {#if !artifact.complete}
                  <span class="spinner ml-auto shrink-0"></span>
                {/if}
              </button>
            {/each}
          </Popover>
        {/if}
      </div>
    {:else}
      <h2 class="min-w-0 truncate text-[13px] font-medium">{shown?.title ?? "Artifacts"}</h2>
    {/if}
    <div class="ml-auto flex shrink-0 items-center gap-0.5">
      {#if shown?.kind === "page" && shown.complete && !asSource && !onlySource}
        <button class="icon-btn" aria-label="Run the page again" title="Reload" onclick={() => (runs += 1)}>
          <ArrowClockwise />
        </button>
      {/if}
      {#if shown && (asSource || onlySource)}
        <CopyButton text={shown.source} label="Copy source" />
        <button class="icon-btn" aria-label="Download" title="Download" onclick={download}>
          <DownloadSimple />
        </button>
      {/if}
      {#if shown && !onlySource}
        <button
          class="icon-btn"
          aria-label={asSource ? "Show the preview" : "Show the source"}
          title={asSource ? "Preview" : "Source"}
          onclick={() => (asSource = !asSource)}
        >
          {#if asSource}<Eye />{:else}<Code />{/if}
        </button>
      {/if}
      <button
        class="icon-btn"
        aria-label={app.artifactsWide ? "Show the conversation" : "Hide the conversation"}
        title={app.artifactsWide ? "Collapse" : "Expand"}
        onclick={toggleArtifactsWide}
      >
        {#if app.artifactsWide}<ArrowsIn />{:else}<ArrowsOut />{/if}
      </button>
      <button class="icon-btn changes-copy" aria-label="Close" title="Close" onclick={closeDock}>
        <X />
      </button>
    </div>
  </div>

  <div class="artifact-body">
    {#if shown}
      {#if asSource || onlySource}
        {#if big}
          <p class="artifact-note">This page is too large to run, so it is shown as its source.</p>
        {/if}
        <div class="artifact-source">
          <div class="artifact-source-scroll" bind:this={sourceBox}>
            <pre><code>{@html markup}</code></pre>
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
      {:else if shown.kind === "svg"}
        {#key shown.id}
          <ArtifactSvg source={shown.source} complete={shown.complete} />
        {/key}
      {:else}
        {#key `${shown.id}:${runs}`}
          <ArtifactPage source={shown.source} complete={shown.complete} />
        {/key}
      {/if}
    {/if}
  </div>
</aside>
