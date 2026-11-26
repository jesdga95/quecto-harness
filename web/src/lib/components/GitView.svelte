<script lang="ts" module>
  // The message being written outlives the section closing and opening again.
  const message = $state({ summary: "", body: "", open: false });
</script>

<script lang="ts">
  import Archive from "phosphor-svelte/lib/ArchiveIcon";
  import ArrowCounterClockwise from "phosphor-svelte/lib/ArrowCounterClockwiseIcon";
  import ArrowDown from "phosphor-svelte/lib/ArrowDownIcon";
  import ArrowSquareOut from "phosphor-svelte/lib/ArrowSquareOutIcon";
  import ArrowUp from "phosphor-svelte/lib/ArrowUpIcon";
  import ArrowUUpLeft from "phosphor-svelte/lib/ArrowUUpLeftIcon";
  import ArrowsClockwise from "phosphor-svelte/lib/ArrowsClockwiseIcon";
  import CaretDown from "phosphor-svelte/lib/CaretDownIcon";
  import CaretRight from "phosphor-svelte/lib/CaretRightIcon";
  import CaretUp from "phosphor-svelte/lib/CaretUpIcon";
  import Check from "phosphor-svelte/lib/CheckIcon";
  import ClockCounterClockwise from "phosphor-svelte/lib/ClockCounterClockwiseIcon";
  import Cloud from "phosphor-svelte/lib/CloudIcon";
  import CloudArrowDown from "phosphor-svelte/lib/CloudArrowDownIcon";
  import Copy from "phosphor-svelte/lib/CopyIcon";
  import DotsThree from "phosphor-svelte/lib/DotsThreeIcon";
  import GitBranch from "phosphor-svelte/lib/GitBranchIcon";
  import GitDiff from "phosphor-svelte/lib/GitDiffIcon";
  import Minus from "phosphor-svelte/lib/MinusIcon";
  import PencilSimple from "phosphor-svelte/lib/PencilSimpleIcon";
  import PencilSimpleLine from "phosphor-svelte/lib/PencilSimpleLineIcon";
  import Plus from "phosphor-svelte/lib/PlusIcon";
  import Sparkle from "phosphor-svelte/lib/SparkleIcon";
  import DiffBlock from "./DiffBlock.svelte";
  import Popover from "./Popover.svelte";
  import { api } from "../api";
  import type { Snippet } from "svelte";
  import type { GitBranches, GitCommit, GitCommitFile, GitFile, GitHunk, GitState } from "../api";
  import { app, gitAction, loadGit, openFile } from "../state/app.svelte";
  import { choose, confirm } from "../state/confirm.svelte";
  import { toast } from "../state/toast.svelte";
  import { copyText } from "../clipboard";
  import { tick } from "svelte";

  type Side = "staged" | "unstaged";

  let view = $state<"changes" | "history">("changes");
  // Opened rows, by side and path, so a reread after a write keeps them open.
  let opened = $state(new Set<string>());
  let menu = $state<"" | "branch" | "more" | "commit">("");
  let drafting = $state(false);
  let branches = $state<GitBranches | null>(null);
  let query = $state("");
  // A commit the next branch made in the picker starts from, set by "Branch
  // from here" in the history.
  let startFrom = $state<GitCommit | null>(null);
  let history = $state<GitCommit[]>([]);
  let openCommit = $state("");
  let commitFiles = $state<Record<string, GitCommitFile[]>>({});

  const git = $derived(app.git);
  const st = $derived(git?.status ?? null);
  const staged = $derived(st?.staged ?? []);
  const unstaged = $derived(st?.unstaged ?? []);
  const allPaths = $derived(new Set([...(st?.staged ?? []), ...(st?.unstaged ?? [])].map((f) => f.path)));
  const stagedPaths = $derived(new Set((st?.staged ?? []).map((f) => f.path)));
  const commitCount = $derived(stagedPaths.size > 0 ? stagedPaths.size : allPaths.size);
  const allFiles = $derived([...(st?.staged ?? []), ...(st?.unstaged ?? [])]);
  const totalAdded = $derived(allFiles.reduce((n, f) => n + f.added, 0));
  const totalRemoved = $derived(allFiles.reduce((n, f) => n + f.removed, 0));
  let summaryField = $state<HTMLInputElement | null>(null);
  const branchName = $derived(st ? (st.detached ? st.head || "Detached" : st.branch) : "");

  $effect(() => {
    // The history follows HEAD, so a commit or a switch made anywhere shows.
    void st?.head;
    void st?.branch;
    if (view === "history" && git?.repo) void loadHistory();
  });

  async function loadHistory() {
    try {
      history = await api.gitLog(app.projectPath);
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err));
    }
  }

  const BADGE: Record<GitState, [string, string]> = {
    changed: ["M", "is-changed"],
    new: ["A", "is-new"],
    deleted: ["D", "is-deleted"],
    untracked: ["U", "is-untracked"],
    conflict: ["C", "is-conflict"],
  };

  function split(path: string): [string, string] {
    const at = path.lastIndexOf("/");
    return [path.slice(0, at + 1), path.slice(at + 1)];
  }

  const key = (side: Side, f: GitFile) => `${side}:${f.path}`;
  const drawable = (f: GitFile) => f.hunks.length > 0;

  function toggle(side: Side, f: GitFile) {
    if (!drawable(f)) return;
    const next = new Set(opened);
    if (!next.delete(key(side, f))) next.add(key(side, f));
    opened = next;
  }

  function note(f: GitFile): string {
    if (f.state === "conflict") return "Conflict";
    if (f.link) return "Link";
    if (f.binary) return "Binary";
    if (!drawable(f) && f.state === "deleted") return "Deleted";
    return "";
  }

  function ago(seconds: number): string {
    const min = Math.round((Date.now() / 1000 - seconds) / 60);
    if (min < 1) return "Just now";
    if (min < 60) return `${min} min ago`;
    const hours = Math.round(min / 60);
    if (hours < 24) return `${hours} h ago`;
    const days = Math.round(hours / 24);
    if (days === 1) return "Yesterday";
    if (days < 30) return `${days} days ago`;
    return new Date(seconds * 1000).toLocaleDateString();
  }

  async function discard(paths: string[], untracked: boolean, title: string) {
    const ok = await confirm({
      title,
      body: untracked
        ? "Files git does not track are deleted, and the rest go back to how they were staged or committed. This cannot be undone."
        : "The changes go back to how they were staged or committed. This cannot be undone.",
      confirmLabel: untracked ? "Delete and discard" : "Discard",
    });
    if (ok) await gitAction("discard", { paths });
  }

  async function hunk(f: GitFile, h: GitHunk, side: Side, action: "stage" | "unstage" | "discard") {
    if (action === "discard") {
      const ok = await confirm({
        title: "Discard this hunk?",
        body: "Its lines go back to how they were staged or committed. This cannot be undone.",
        confirmLabel: "Discard",
      });
      if (!ok) return;
    }
    await gitAction("hunk", {
      action,
      hunk: {
        path: f.path,
        staged: side === "staged",
        old_start: h.old_start,
        old_lines: h.old_lines,
        new_start: h.new_start,
        new_lines: h.new_lines,
      },
    });
  }

  async function commit(opts: { push?: boolean; amend?: boolean } = {}) {
    menu = "";
    const text = message.body.trim() ? `${message.summary.trim()}\n\n${message.body.trim()}` : message.summary.trim();
    const result = await gitAction("commit", {
      message: text,
      amend: opts.amend,
      push: opts.push,
      all: stagedPaths.size === 0 && !opts.amend,
    });
    if (!result) return;
    message.summary = "";
    message.body = "";
    message.open = false;
    const where = branchName ? ` to ${branchName}` : "";
    if (!result.push_error) {
      const verb = opts.amend ? "Amended" : "Committed";
      toast(`${verb} ${result.hash ?? ""}${where}${opts.push ? " and pushed" : ""}`, "ok");
    }
  }

  async function draft() {
    if (!app.sessionId) return;
    message.open = true;
    drafting = true;
    try {
      const d = await api.gitDraft(app.sessionId);
      message.summary = d.summary;
      message.body = d.body;
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err));
    } finally {
      drafting = false;
    }
  }

  async function compose() {
    message.open = true;
    menu = "";
    await tick();
    summaryField?.focus();
  }

  function commitKey(event: KeyboardEvent) {
    if (event.key === "Escape" && menu === "") {
      event.preventDefault();
      message.open = false;
      return;
    }
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey) && message.summary.trim() && commitCount > 0) {
      event.preventDefault();
      void commit();
    }
  }

  async function openBranches(from: GitCommit | null = null) {
    startFrom = from;
    query = "";
    menu = "branch";
    try {
      branches = await api.gitBranches(app.projectPath);
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err));
    }
  }

  const q = $derived(query.trim());
  const localMatches = $derived((branches?.local ?? []).filter((b) => !q || b.name.toLowerCase().includes(q.toLowerCase())));
  const remoteMatches = $derived((branches?.remote ?? []).filter((b) => !q || b.toLowerCase().includes(q.toLowerCase())));
  const canCreate = $derived(q !== "" && !(branches?.local ?? []).some((b) => b.name === q));

  async function switchTo(name: string) {
    if (name === st?.branch) {
      menu = "";
      return;
    }
    menu = "";
    let stash = false;
    if (allPaths.size > 0) {
      const n = allPaths.size;
      const choice = await choose({
        title: `Switch to ${name}?`,
        body: `${n} ${n === 1 ? "file has" : "files have"} uncommitted changes. Bring them to ${name}, or stash them and switch.`,
        confirmLabel: "Bring changes",
        alternateLabel: "Stash and switch",
        destructive: false,
      });
      if (choice === "cancel") return;
      stash = choice === "alternate";
    }
    const done = await gitAction("checkout", { branch: name, stash });
    if (done) toast(stash ? `Stashed the changes and switched to ${name}` : `Switched to ${name}`, "ok");
  }

  async function create() {
    const name = q;
    if (!name) return;
    menu = "";
    const start = startFrom?.hash ?? "";
    const done = await gitAction("checkout", { branch: name, create: true, start });
    if (done) {
      toast(`Created ${name}`, "ok");
      startFrom = null;
    }
  }

  function branchKey(event: KeyboardEvent) {
    if (event.key !== "Enter") return;
    event.preventDefault();
    if (canCreate && (startFrom || localMatches.length === 0)) void create();
    else if (localMatches[0]) void switchTo(localMatches[0].name);
    else if (remoteMatches[0]) void switchTo(remoteMatches[0]);
  }

  async function remote(op: "fetch" | "pull" | "push") {
    menu = "";
    const done = await gitAction(op);
    if (done) toast(op === "fetch" ? "Fetched" : op === "pull" ? "Pulled" : `Pushed ${branchName}`, "ok");
  }

  async function stash() {
    menu = "";
    if (await gitAction("stash")) toast("Stashed every change", "ok");
  }

  async function copyDiff() {
    menu = "";
    try {
      const patch = await api.gitPatch(app.projectPath);
      if (await copyText(patch)) toast("Copied the diff", "ok");
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err));
    }
  }

  async function toggleCommit(c: GitCommit) {
    openCommit = openCommit === c.hash ? "" : c.hash;
    if (openCommit && !commitFiles[c.hash]) {
      try {
        commitFiles = { ...commitFiles, [c.hash]: await api.gitShow(app.projectPath, c.hash) };
      } catch (err) {
        toast(err instanceof Error ? err.message : String(err));
      }
    }
  }

  async function undo(c: GitCommit) {
    const ok = await confirm({
      title: "Undo this commit?",
      body: `${c.subject} is taken off ${branchName}. Its changes stay, staged.`,
      confirmLabel: "Undo commit",
      destructive: false,
    });
    if (!ok) return;
    if (await gitAction("undo")) {
      if (!message.summary) {
        message.summary = c.subject;
        message.body = c.body;
        message.open = true;
      }
      view = "changes";
    }
  }

  async function init() {
    try {
      await api.gitInit(app.projectPath);
      toast("Initialized a repository", "ok");
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err));
    }
    void loadGit();
  }

  function refresh() {
    void loadGit();
    if (view === "history") void loadHistory();
  }
</script>

<!-- Every empty state is this one block, centred in what is left below the
     toolbar, so they line up whichever one shows. -->
{#snippet emptyBlock(title: string, text: string, action: Snippet | null)}
  <div class="dock-empty">
    <div class="dock-empty-icon">
      {#if title === "Working tree clean"}
        <Check size={21} />
      {:else if title === "No commits yet"}
        <ClockCounterClockwise size={21} />
      {:else}
        <GitBranch size={21} />
      {/if}
    </div>
    <h3>{title}</h3>
    <p>{text}</p>
    {#if action}{@render action()}{/if}
  </div>
{/snippet}

{#snippet checkAgain()}
  <button class="btn btn-ghost btn-sm mt-2" onclick={() => void loadGit()}>Check again</button>
{/snippet}
{#snippet initButton()}
  <button class="btn btn-primary btn-sm mt-2" onclick={() => void init()}>Initialize repository</button>
{/snippet}
{#snippet showHistory()}
  <button class="btn btn-ghost btn-sm mt-2" onclick={() => (view = "history")}>Show history</button>
{/snippet}

{#snippet gitOnly()}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">Git</span></div>
  </div>
{/snippet}

{#snippet row(f: GitFile, side: Side)}
  {@const [dir, name] = split(f.path)}
  {@const [letter, tone] = BADGE[f.state] ?? ["M", "is-changed"]}
  {@const open = drawable(f) && opened.has(key(side, f))}
  {@const said = note(f)}
  <div
    class="changes-file git-file"
    class:is-open={open}
    class:is-flat={!drawable(f)}
    title={f.path}
    role="button"
    tabindex="0"
    onclick={() => toggle(side, f)}
    onkeydown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), toggle(side, f))}
  >
    <span class="change-badge {tone}">{letter}</span>
    <span class="file"><span><span class="dir">{dir}</span>{name}</span></span>
    <span class="git-counts num">
      {#if said}
        <span class="text-muted">{said}</span>
      {:else}
        {#if f.added}<span class="stat-add">+{f.added}</span>{/if}
        {#if f.removed}<span class="stat-del ml-1">&minus;{f.removed}</span>{/if}
      {/if}
    </span>
    <span class="git-row-acts">
      {#if f.state !== "deleted"}
        <button
          class="icon-btn"
          title="Open file"
          aria-label="Open {f.path}"
          onclick={(e) => (e.stopPropagation(), void openFile(f.path))}
        >
          <ArrowSquareOut />
        </button>
      {/if}
      {#if side === "unstaged"}
        <button
          class="icon-btn is-danger"
          title="Discard changes"
          aria-label="Discard changes to {f.path}"
          onclick={(e) => (
            e.stopPropagation(),
            void discard([f.path], f.state === "untracked", `Discard changes to ${name}?`)
          )}
        >
          <ArrowCounterClockwise />
        </button>
        <button
          class="icon-btn"
          title="Stage"
          aria-label="Stage {f.path}"
          onclick={(e) => (e.stopPropagation(), void gitAction("stage", { paths: [f.path] }))}
        >
          <Plus />
        </button>
      {:else}
        <button
          class="icon-btn"
          title="Unstage"
          aria-label="Unstage {f.path}"
          onclick={(e) => (e.stopPropagation(), void gitAction("unstage", { paths: [f.path] }))}
        >
          <Minus />
        </button>
      {/if}
    </span>
    {#if drawable(f)}
      <span class="caret"><CaretRight /></span>
    {/if}
  </div>
  {#if open}
    <div class="changes-diff">
      <DiffBlock hunks={f.hunks} omitted={f.omitted} path={f.path}>
        {#snippet actions(h)}
          {#if side === "unstaged"}
            <button class="dock-action is-danger" onclick={() => void hunk(f, h, side, "discard")}>Discard</button>
            <button class="dock-action" onclick={() => void hunk(f, h, side, "stage")}>Stage hunk</button>
          {:else}
            <button class="dock-action" onclick={() => void hunk(f, h, side, "unstage")}>Unstage hunk</button>
          {/if}
        {/snippet}
      </DiffBlock>
    </div>
  {/if}
{/snippet}

{#if !git}
  {@render gitOnly()}
{:else if !git.available}
  {@render gitOnly()}
  {@render emptyBlock(
    "Git is required",
    "Install git and this section shows the working tree, commits and branches for the project.",
    checkAgain,
  )}
{:else if !git.repo || !st}
  {@render gitOnly()}
  {@render emptyBlock(
    "Not a git repository",
    "This folder has no repository. Initialize one to track changes, commit and branch.",
    initButton,
  )}
{:else}
  <div class="panel-path">
    <div class="relative min-w-0">
      <button
        class="git-branch-btn"
        class:is-open={menu === "branch"}
        title={st.upstream ? `Tracking ${st.upstream}` : "Switch branch"}
        onclick={() => (menu === "branch" ? (menu = "") : void openBranches())}
      >
        <GitBranch size={15} />
        <span class="truncate">{branchName}</span>
        <CaretDown size={11} />
      </button>
      {#if menu === "branch"}
        <Popover onclose={() => ((menu = ""), (startFrom = null))} width={300}>
          <!-- svelte-ignore a11y_autofocus -->
          <input
            class="input mb-1"
            placeholder={startFrom ? `Name a branch from ${startFrom.short}` : "Find or create a branch"}
            bind:value={query}
            onkeydown={branchKey}
            autofocus
          />
          {#if canCreate}
            <button class="popover-item" onclick={() => void create()}>
              <Plus />
              <span class="min-w-0 truncate">
                Create <span class="num">{q}</span> from <span class="num">{startFrom?.short ?? branchName}</span>
              </span>
            </button>
            {#if !startFrom && (localMatches.length > 0 || remoteMatches.length > 0)}
              <div class="popover-sep"></div>
            {/if}
          {/if}
          {#if !startFrom}
            <div class="git-branch-list">
              {#if localMatches.length > 0}
                <div class="popover-head">Local</div>
                {#each localMatches as b (b.name)}
                  <button class="popover-item" onclick={() => void switchTo(b.name)}>
                    <GitBranch />
                    <span class="git-branch-name">{b.name}</span>
                    <span class="git-branch-side num">
                      {#if b.ahead}&uarr;{b.ahead}{/if}
                      {#if b.behind}&darr;{b.behind}{/if}
                    </span>
                    {#if b.current}<span class="qcard-tick"><Check /></span>{/if}
                  </button>
                {/each}
              {/if}
              {#if remoteMatches.length > 0}
                <div class="popover-head">Remote</div>
                {#each remoteMatches as b (b)}
                  <button class="popover-item" onclick={() => void switchTo(b)}>
                    <Cloud />
                    <span class="git-branch-name">{b}</span>
                  </button>
                {/each}
              {/if}
              {#if branches && localMatches.length === 0 && remoteMatches.length === 0 && !canCreate}
                <div class="px-[9px] py-1.5 text-[12px] text-muted">No branches match</div>
              {/if}
            </div>
          {/if}
        </Popover>
      {/if}
    </div>
    <span class="git-sync num">
      {#if st.ahead}<span title="{st.ahead} not pushed">&uarr;{st.ahead}</span>{/if}
      {#if st.behind}<span title="{st.behind} to pull">&darr;{st.behind}</span>{/if}
    </span>
    <div class="ml-auto flex shrink-0 items-center gap-0.5">
      {#if view === "changes"}
        <button class="icon-btn" title="History" aria-label="Show history" onclick={() => (view = "history")}>
          <ClockCounterClockwise />
        </button>
      {:else}
        <button class="icon-btn" title="Changes" aria-label="Show changes" onclick={() => (view = "changes")}>
          <GitDiff />
        </button>
      {/if}
      <button class="icon-btn" title="Refresh" aria-label="Refresh" onclick={refresh}>
        <ArrowsClockwise />
      </button>
      <div class="relative">
        <button class="icon-btn" title="More" aria-label="More git actions" onclick={() => (menu = menu === "more" ? "" : "more")}>
          <DotsThree />
        </button>
        {#if menu === "more"}
          <Popover onclose={() => (menu = "")} align="right" width={220}>
            <button class="popover-item" onclick={() => void remote("fetch")}><CloudArrowDown />Fetch</button>
            <button class="popover-item" onclick={() => void remote("pull")}><ArrowDown />Pull</button>
            <button class="popover-item" onclick={() => void remote("push")}>
              <ArrowUp />Push
              {#if st.ahead}<span class="num ml-auto text-[11.5px] text-muted">{st.ahead}</span>{/if}
            </button>
            <div class="popover-sep"></div>
            <button class="popover-item" disabled={allPaths.size === 0} onclick={() => void stash()}>
              <Archive />Stash changes
            </button>
            <button class="popover-item" disabled={allPaths.size === 0} onclick={() => void copyDiff()}>
              <Copy />Copy diff
            </button>
          </Popover>
        {/if}
      </div>
    </div>
  </div>

  {#if view === "history"}
    {#if history.length === 0 && allFiles.length === 0}
      {@render emptyBlock("No commits yet", `Commits on ${branchName} show here.`, null)}
    {:else}
      <div class="artifact-body">
        <div class="git-hist min-h-0 flex-1 overflow-y-auto pb-5">
          {#if allFiles.length > 0}
            <!-- Uncommitted work hangs off HEAD on a dashed amber segment. -->
            <div
              class="dock-row git-hist-row is-dirty"
              role="button"
              tabindex="0"
              onclick={() => (view = "changes")}
              onkeydown={(e) => e.key === "Enter" && (view = "changes")}
            >
              <span class="git-rail">
                <span class="git-dot"></span>
                {#if history.length > 0}<span class="git-seg is-down is-dashed"></span>{/if}
              </span>
              <div class="dock-row-main">
                <span class="dock-row-title">Uncommitted changes</span>
                <span class="dock-row-sub">
                  {allPaths.size}
                  {allPaths.size === 1 ? "file" : "files"} &middot;
                  <span class="num"><span class="stat-add">+{totalAdded}</span> <span class="stat-del">&minus;{totalRemoved}</span></span>
                </span>
              </div>
              <span class="dock-row-side">Not committed</span>
            </div>
          {/if}
          {#each history as c, i (c.hash)}
            {@const last = i === history.length - 1}
            <div
              class="dock-row git-hist-row"
              class:is-local={!c.pushed}
              class:is-head={i === 0}
              class:is-on={openCommit === c.hash}
              role="button"
              tabindex="0"
              onclick={() => void toggleCommit(c)}
              onkeydown={(e) => e.key === "Enter" && void toggleCommit(c)}
            >
              <span class="git-rail">
                {#if i > 0 || allFiles.length > 0}
                  <span class="git-seg is-up" class:is-dashed={i === 0}></span>
                {/if}
                <span class="git-dot"></span>
                {#if !last}<span class="git-seg is-down"></span>{/if}
              </span>
              <div class="dock-row-main">
                <span class="dock-row-title">{c.subject}</span>
                <span class="dock-row-sub"><span class="num">{c.short}</span> &middot; {c.author} &middot; {ago(c.when)}</span>
              </div>
              {#if st.remotes && !c.pushed}
                <span class="dock-row-side">Not pushed</span>
              {/if}
            </div>
            {#if openCommit === c.hash}
              <div class="git-commit-files" class:has-rail={!last}>
                {#if c.body}<div class="git-commit-body">{c.body}</div>{/if}
                {#each commitFiles[c.hash] ?? [] as f (f.path)}
                  {@const [dir, name] = split(f.path)}
                  {@const [letter, tone] = BADGE[f.state] ?? ["M", "is-changed"]}
                  <div class="changes-file is-flat" title={f.path}>
                    <span class="change-badge {tone}">{letter}</span>
                    <span class="file"><span><span class="dir">{dir}</span>{name}</span></span>
                    <span class="git-counts num">
                      {#if f.binary}
                        <span class="text-muted">Binary</span>
                      {:else}
                        {#if f.added}<span class="stat-add">+{f.added}</span>{/if}
                        {#if f.removed}<span class="stat-del ml-1">&minus;{f.removed}</span>{/if}
                      {/if}
                    </span>
                  </div>
                {/each}
                <div class="git-commit-acts">
                  <button class="dock-action" onclick={() => void copyText(c.hash).then((ok) => ok && toast("Copied the hash", "ok"))}>
                    <Copy />Copy hash
                  </button>
                  {#if i === 0 && !c.pushed}
                    <button class="dock-action" onclick={() => void undo(c)}><ArrowUUpLeft />Undo commit</button>
                  {/if}
                  <button class="dock-action" onclick={() => void openBranches(c)}><GitBranch />Branch from here</button>
                </div>
              </div>
            {/if}
          {/each}
        </div>
      </div>
    {/if}
  {:else}
    {#if allFiles.length === 0}
      {@render emptyBlock("Working tree clean", `Nothing to commit on ${branchName}.`, showHistory)}
    {:else}
    <div class="artifact-body">
      <div class="min-h-0 flex-1 overflow-y-auto">
        {#if staged.length > 0}
          <div class="git-group-head">
            Staged <span class="num font-normal text-muted">{staged.length}</span>
            <button class="dock-action ml-auto" onclick={() => void gitAction("unstage", { paths: staged.map((f) => f.path) })}>
              Unstage all
            </button>
          </div>
          {#each staged as f (f.path)}
            {@render row(f, "staged")}
          {/each}
        {/if}
        {#if unstaged.length > 0}
          <div class="git-group-head">
            Changes <span class="num font-normal text-muted">{unstaged.length}</span>
            <button
              class="dock-action is-danger ml-auto"
              onclick={() =>
                void discard(
                  unstaged.map((f) => f.path),
                  unstaged.some((f) => f.state === "untracked"),
                  `Discard ${unstaged.length} unstaged ${unstaged.length === 1 ? "change" : "changes"}?`,
                )}
            >
              Discard all
            </button>
            <button class="dock-action" onclick={() => void gitAction("stage", { paths: unstaged.map((f) => f.path) })}>
              Stage all
            </button>
          </div>
          {#each unstaged as f (f.path)}
            {@render row(f, "unstaged")}
          {/each}
        {/if}
        {#if st.hidden > 0}
          <div class="git-filter-note">{st.hidden} more files are not listed</div>
        {/if}
      </div>

      <div class="git-commit">
        {#if !message.open}
          <div class="relative">
            <button class="git-msg-closed" onclick={() => void compose()}>
              <PencilSimpleLine size={15} />Write a commit message
            </button>
            {#if app.sessionId}
              <button
                class="icon-btn git-draft"
                title="Draft a message from the diff"
                aria-label="Draft a commit message from the diff"
                disabled={drafting}
                onclick={() => void draft()}
              >
                <Sparkle />
              </button>
            {/if}
          </div>
        {:else}
          <!-- Summary and description are one field, with one outline lit while
               either has focus. -->
          <div class="git-msg">
            <div class="relative">
              <input
                bind:this={summaryField}
                class="git-msg-summary"
                placeholder="Summary"
                bind:value={message.summary}
                onkeydown={commitKey}
              />
              {#if app.sessionId}
                <button
                  class="icon-btn git-draft"
                  title="Draft a message from the diff"
                  aria-label="Draft a commit message from the diff"
                  disabled={drafting}
                  onclick={() => void draft()}
                >
                  {#if drafting}<span class="spinner"></span>{:else}<Sparkle />{/if}
                </button>
              {/if}
            </div>
            <div class="git-msg-sep"></div>
            <textarea class="git-msg-body" placeholder="Description" bind:value={message.body} onkeydown={commitKey}></textarea>
          </div>
          {#if stagedPaths.size === 0}
            <div class="git-commit-note">Nothing staged, so every change is committed</div>
          {/if}
          <div class="flex items-center gap-1.5">
            <button class="btn btn-ghost" onclick={() => (message.open = false)}>Cancel</button>
            <button
              class="btn btn-primary flex-1 justify-center"
              disabled={!message.summary.trim() || commitCount === 0}
              onclick={() => void commit()}
            >
              <Check />
              {stagedPaths.size > 0
                ? `Commit ${commitCount} ${commitCount === 1 ? "file" : "files"}`
                : `Commit all ${commitCount} ${commitCount === 1 ? "file" : "files"}`}
            </button>
            <div class="relative">
              <button class="icon-btn" title="Commit options" aria-label="Commit options" onclick={() => (menu = menu === "commit" ? "" : "commit")}>
                <CaretUp />
              </button>
              {#if menu === "commit"}
                <Popover onclose={() => (menu = "")} align="right" up width={220}>
                  <button
                    class="popover-item"
                    disabled={!message.summary.trim() || commitCount === 0}
                    onclick={() => void commit({ push: true })}
                  >
                    <ArrowUp />Commit and push
                  </button>
                  <button class="popover-item" disabled={!st.head} onclick={() => void commit({ amend: true })}>
                    <PencilSimple />Amend last commit
                  </button>
                </Popover>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    </div>
    {/if}
  {/if}
{/if}
