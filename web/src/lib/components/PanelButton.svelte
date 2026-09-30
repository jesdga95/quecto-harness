<script lang="ts">
  import Folder from "phosphor-svelte/lib/Folder";
  import GitDiff from "phosphor-svelte/lib/GitDiff";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import UsersThree from "phosphor-svelte/lib/UsersThree";
  import { app, artifacts, contextUse, openSection, type Section } from "../state/app.svelte";
  import { k, level } from "../format";

  // The way into the panel from the header, shown while the panel is closed:
  // one button per section, each carrying that section's news. Files shows the spinner while one is being
  // written, Changes its line counts, Agents and Processes the sidebar's marks.
  const writing = $derived(app.status !== "idle" && artifacts().some((a) => !a.complete));
  const agentMark = $derived(
    app.runs.some((r) => r.status === "waiting")
      ? "is-waiting"
      : app.runs.some((r) => r.status !== "idle")
        ? "is-working"
        : "",
  );
  const running = $derived(app.procs.some((p) => p.status === "running"));
  // Context sits apart from the four: it is the session's, not the project's.
  // It shows once there is something measured.
  const use = $derived(contextUse());
  const tone = $derived(level(use.ratio));
  const RING = 2 * Math.PI * 6.5;

  const LABELS: Record<Section, string> = {
    files: "Files",
    changes: "Diff",
    agents: "Agents",
    processes: "Shell",
    context: "Context",
  };

</script>

{#snippet button(section: Section, has: boolean)}
  <button
    class="entry-btn"
    class:has-news={has}
    onclick={() => openSection(section)}
    title={LABELS[section]}
    aria-label="Open {LABELS[section]}"
  >
    {#if section === "files"}
      <Folder size={16} />
      {#if writing}<span class="spinner"></span>{/if}
    {:else if section === "changes"}
      <GitDiff size={16} />
      {#if app.changes.files.length > 0}
        <span class="num">
          <span class="stat-add">+{app.changes.added}</span>
          <span class="stat-del">&minus;{app.changes.removed}</span>
        </span>
      {/if}
    {:else if section === "agents"}
      <UsersThree size={16} />
      {#if app.runs.length > 0}
        {#if agentMark}<span class="state-mark {agentMark}"></span>{/if}
        <span class="num">{app.runs.length}</span>
      {/if}
    {:else}
      <Terminal size={16} />
      {#if app.procs.length > 0}
        {#if running}<span class="state-mark is-background"></span>{/if}
        <span class="num">{app.procs.length}</span>
      {/if}
    {/if}
  </button>
{/snippet}

<div class="entry-group" role="group" aria-label="Panel sections">
  {@render button("files", writing)}
  {@render button("changes", app.changes.files.length > 0)}
  {@render button("agents", app.runs.length > 0)}
  {@render button("processes", app.procs.length > 0)}
  {#if use.measured}
    <span class="entry-divider"></span>
    <button
      class="entry-btn has-news"
      class:is-warn={tone === "warn"}
      class:is-crit={tone === "crit"}
      onclick={() => openSection("context")}
      title="Context"
      aria-label="Open Context"
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
</div>
