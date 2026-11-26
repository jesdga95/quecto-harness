<script lang="ts">
  import ArrowsIn from "phosphor-svelte/lib/ArrowsInIcon";
  import ArrowsOut from "phosphor-svelte/lib/ArrowsOutIcon";
  import Folder from "phosphor-svelte/lib/FolderIcon";
  import GitBranch from "phosphor-svelte/lib/GitBranchIcon";
  import Paperclip from "phosphor-svelte/lib/PaperclipIcon";
  import Terminal from "phosphor-svelte/lib/TerminalIcon";
  import UsersThree from "phosphor-svelte/lib/UsersThreeIcon";
  import {
    app,
    artifacts,
    contextUse,
    sessionAttachments,
    toggleArtifactsWide,
    toggleSection,
    type Section,
  } from "../state/app.svelte";
  import { k, level } from "../format";

  // The header's sections: a button each, carrying that section's news. The
  // open one is lit, and pressing it again closes the panel. Files shows the
  // spinner while one is being written, Git its branch and line counts, Agents
  // and Shell the sidebar's marks.
  const writing = $derived(app.status !== "idle" && artifacts().some((a) => !a.complete));
  const agentMark = $derived(
    app.runs.some((r) => r.status === "waiting")
      ? "is-waiting"
      : app.runs.some((r) => r.status !== "idle")
        ? "is-working"
        : "",
  );
  const running = $derived(app.procs.some((p) => p.status === "running"));
  // The repository's branch and the lines changed across both sides of the
  // index, when the project is one.
  const repo = $derived(app.git?.repo ? app.git.status : null);
  const gitFiles = $derived(repo ? [...repo.staged, ...repo.unstaged] : []);
  const gitAdded = $derived(gitFiles.reduce((n, f) => n + f.added, 0));
  const gitRemoved = $derived(gitFiles.reduce((n, f) => n + f.removed, 0));
  // Context and Attachments are the session's rather than the project's, so
  // they sit apart, each once there is something in it.
  const use = $derived(contextUse());
  const tone = $derived(level(use.ratio));
  const attached = $derived(sessionAttachments().reduce((n, g) => n + g.files.length, 0));
  const RING = 2 * Math.PI * 6.5;

  const LABELS: Record<Section, string> = {
    files: "Files",
    git: "Git",
    agents: "Agents",
    processes: "Shell",
    context: "Context",
    attachments: "Attachments",
  };

  const on = (section: Section) => app.panel && app.section === section;
</script>

{#snippet button(section: Section, has: boolean)}
  <button
    class="entry-btn"
    class:has-news={has}
    class:is-on={on(section)}
    onclick={() => toggleSection(section)}
    title={LABELS[section]}
    aria-label={on(section) ? `Close ${LABELS[section]}` : `Open ${LABELS[section]}`}
    aria-pressed={on(section)}
  >
    {#if section === "files"}
      <Folder size={16} />
      {#if writing}<span class="spinner"></span>{/if}
    {:else if section === "git"}
      <GitBranch size={16} />
      {#if repo}
        <span class="entry-branch">{repo.detached ? repo.head : repo.branch}</span>
        {#if gitFiles.length > 0}
          <span class="num">
            <span class="stat-add">+{gitAdded}</span>
            <span class="stat-del">&minus;{gitRemoved}</span>
          </span>
        {/if}
      {/if}
    {:else if section === "agents"}
      <UsersThree size={16} />
      {#if app.runs.length > 0}
        {#if agentMark}<span class="state-mark {agentMark}"></span>{/if}
        <span class="num">{app.runs.length}</span>
      {/if}
    {:else if section === "processes"}
      <Terminal size={16} />
      {#if app.procs.length > 0}
        {#if running}<span class="state-mark is-background"></span>{/if}
        <span class="num">{app.procs.length}</span>
      {/if}
    {:else if section === "attachments"}
      <Paperclip size={16} />
      <span class="num">{attached}</span>
    {/if}
  </button>
{/snippet}

<div class="entry-group" role="group" aria-label="Panel sections">
  <!-- Held in place while the panel is shut, so opening it moves nothing. A
       crowded window already gives the panel all of it. -->
  <button
    class="entry-btn"
    class:is-hidden={!app.panel}
    class:hidden={app.dockCrowded}
    tabindex={app.panel ? undefined : -1}
    aria-hidden={app.panel ? undefined : "true"}
    aria-label={app.artifactsWide ? "Show the conversation" : "Hide the conversation"}
    title={app.artifactsWide ? "Restore" : "Maximise"}
    onclick={toggleArtifactsWide}
  >
    {#if app.artifactsWide}<ArrowsIn size={16} />{:else}<ArrowsOut size={16} />{/if}
  </button>
  <span class="entry-divider" class:is-hidden={!app.panel} class:hidden={app.dockCrowded}></span>
  {@render button("files", writing)}
  {#if !app.loose}
    {@render button("git", !!repo)}
  {/if}
  {@render button("agents", app.runs.length > 0)}
  {@render button("processes", app.procs.length > 0)}
  {#if use.measured || attached > 0}
    <span class="entry-divider"></span>
  {/if}
  {#if use.measured}
    <button
      class="entry-btn has-news"
      class:is-warn={tone === "warn"}
      class:is-crit={tone === "crit"}
      class:is-on={on("context")}
      onclick={() => toggleSection("context")}
      title="Context"
      aria-label={on("context") ? "Close Context" : "Open Context"}
      aria-pressed={on("context")}
    >
      <svg viewBox="0 0 18 18" class="entry-ring">
        <circle cx="9" cy="9" r="6.5" fill="none" stroke="var(--border)" stroke-width="2.5" />
        <circle
          cx="9"
          cy="9"
          r="6.5"
          fill="none"
          stroke="currentColor"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-dasharray="{(use.limited ? use.percent / 100 : 0) * RING} {RING}"
        />
      </svg>
      <span class="num">{use.limited ? `${use.percent}%` : k(use.fill)}</span>
    </button>
  {/if}
  {#if attached > 0}
    {@render button("attachments", true)}
  {/if}
</div>
