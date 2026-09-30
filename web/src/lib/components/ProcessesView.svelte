<script lang="ts">
  import FileText from "phosphor-svelte/lib/FileText";
  import Stop from "phosphor-svelte/lib/Stop";
  import Terminal from "phosphor-svelte/lib/Terminal";
  import CopyButton from "./CopyButton.svelte";
  import { app, loadProcessOutput, showProcesses, stopProcess } from "../state/app.svelte";
  import type { Process } from "../api";
  import { bytes, clockLabel } from "../format";
  import { followEnd } from "../stream.svelte";

  const running = $derived(app.procs.filter((p) => p.status === "running").length);
  const ended = $derived(app.procs.length - running);
  // The newest command is what a reader opening the section came for.
  const shown = $derived(app.procs.find((p) => p.id === app.procId) ?? app.procs.at(-1));
  // Newest first, so a command just started is at the top of the list.
  const listed = $derived([...app.procs].reverse());

  let now = $state(Date.now());
  $effect(() => {
    if (running === 0) return;
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  // The whole saved output replaces the tail for the command it was asked for.
  let wholeFor = $state("");
  const whole = $derived(shown && wholeFor === shown.id ? app.procOutput[shown.id] : undefined);
  const wholeError = $derived(shown && wholeFor === shown.id ? (app.procErrors[shown.id] ?? "") : "");
  const text = $derived(whole ? whole.output.replace(/\n$/, "") : (shown?.tail.join("\n") ?? ""));

  function showWhole(id: string) {
    wholeFor = id;
    void loadProcessOutput(id);
  }

  let scroller = $state<HTMLDivElement | null>(null);
  let atBottom = $state(true);
  function onscroll() {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 24;
  }

  function age(proc: Process): string {
    return clockLabel(((proc.endedAt ?? now) - proc.startedAt) / 1000);
  }

  function ago(at: number): string {
    const min = Math.round((now - at) / 60000);
    if (min < 1) return "just now";
    if (min < 60) return `${min} min ago`;
    return `${Math.round(min / 60)} h ago`;
  }

  function mark(proc: Process): string {
    if (proc.status === "running") return "is-background";
    if (proc.status === "stopped") return "is-stopped";
    return proc.exitCode === 0 ? "is-ok" : "is-crit";
  }

  function end(proc: Process): string {
    if (proc.status === "stopped") return `Stopped after ${age(proc)}`;
    if (proc.exitCode === null) return `Ended after ${age(proc)}`;
    return `Exited with ${proc.exitCode} after ${age(proc)}`;
  }

  function stopAll() {
    for (const proc of app.procs) if (proc.status === "running") void stopProcess(proc.id);
  }
</script>

{#if app.procs.length === 0}
  <div class="panel-path">
    <div class="panel-crumbs"><span class="crumb">No processes</span></div>
  </div>
  <div class="dock-empty">
    <div class="dock-empty-icon"><Terminal size={21} /></div>
    <h3>Nothing in the background</h3>
    <p>Servers, watchers and long builds the agent starts keep running here, with their output.</p>
  </div>
{:else}
  <div class="panel-path">
    <div class="panel-crumbs">
      <span class="crumb" class:is-last={running > 0}>
        {running > 0 ? `${running} running` : "None running"}{ended > 0 ? `, ${ended} ended` : ""}
      </span>
    </div>
    {#if running > 1}
      <button class="dock-action is-danger" onclick={stopAll}><Stop size={14} />Stop all</button>
    {/if}
  </div>

  <div class="dock-list">
    {#each listed as proc (proc.id)}
      <div class="dock-row" class:is-on={proc.id === shown?.id}>
        <span class="state-mark {mark(proc)}" role="status" aria-label={proc.status === "running" ? "Running" : end(proc)}></span>
        <button class="dock-row-main" title={proc.command} onclick={() => showProcesses(proc.id)}>
          <span class="dock-row-title is-mono">{proc.command}</span>
          <span class="dock-row-sub">
            {proc.status === "running"
              ? `Started ${ago(proc.startedAt)}`
              : `Ran ${age(proc)}, ended ${ago(proc.endedAt ?? now)}`}
          </span>
        </button>
        <span class="dock-row-side">
          {#if proc.status === "running"}
            <span class="num">{age(proc)}</span>
          {:else if proc.status === "stopped"}
            <span class="dock-pill">Stopped</span>
          {:else if proc.exitCode !== null}
            <span class="dock-pill" class:is-ok={proc.exitCode === 0} class:is-crit={proc.exitCode !== 0}>
              Exit {proc.exitCode}
            </span>
          {/if}
        </span>
      </div>
    {/each}
  </div>

  {#if shown}
    <div class="artifact-body">
      <div class="dock-detail-head">
        <div class="dock-detail-meta">
          <span class="num">pid {shown.pid}</span>
          <span class="num">{bytes(shown.bytes)}</span>
        </div>
        {#if shown.spill}
          <button class="dock-action" onclick={() => showWhole(shown.id)}><FileText size={14} />Full log</button>
        {/if}
        <CopyButton text={text} label="Copy the output" />
        {#if shown.status === "running"}
          <button class="dock-action is-danger" onclick={() => void stopProcess(shown.id)}><Stop size={14} />Stop</button>
        {/if}
      </div>

      <div class="term dock-term">
        <div class="term-scroll" bind:this={scroller} {onscroll} use:followEnd={() => atBottom}>
          {#if shown.tailCut && shown.spill && !whole}
            <div class="term-cut">
              Earlier lines are in the log.
              <button onclick={() => showWhole(shown.id)}>Show them</button>
            </div>
          {/if}
          <div class="term-line term-prompt">$ {shown.command}</div>
          {#if wholeError}
            <div class="term-line term-bad">{wholeError}</div>
          {/if}
          {#if text}
            <div class="term-line">{text}</div>
          {:else if shown.status === "running"}
            <div class="term-line term-muted">Nothing printed yet</div>
          {/if}
          {#if whole?.truncated}
            <div class="term-line term-muted">The log is longer than the panel holds.</div>
          {/if}
          {#if shown.status !== "running"}
            <div
              class="term-line"
              class:term-bad={shown.status === "done" && shown.exitCode !== 0}
              class:term-muted={shown.status !== "done" || shown.exitCode === 0}
            >
              {end(shown)}
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
{/if}
