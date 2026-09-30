<script lang="ts">
  import Stop from "phosphor-svelte/lib/Stop";
  import { app, loadProcessOutput, stopProcess } from "../state/app.svelte";
  import type { Process } from "../api";
  import { bytes, clockLabel, shortPath } from "../format";

  // The tail is a window of the last lines, so the panel shows the end of it
  // and lets the reader ask for the rest of what is held.
  const SHOWN = 16;

  const running = $derived(app.procs.filter((p) => p.status === "running").length);
  // The newest command is what a reader opening the tab came for.
  const shown = $derived(app.procs.find((p) => p.id === app.procId) ?? app.procs.at(-1));

  let whole = $state(false);
  let now = $state(Date.now());
  $effect(() => {
    if (running === 0) return;
    const tick = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(tick);
  });

  /** A command that has gone is measured to its end; one still going ticks. */
  function age(proc: Process): string {
    return clockLabel(((proc.endedAt ?? now) - proc.startedAt) / 1000);
  }

  function ended(proc: Process): string {
    if (proc.status === "running") return "";
    if (proc.status === "stopped") return "Stopped";
    return proc.exitCode === null ? "Ended" : `Exit ${proc.exitCode}`;
  }

  const tail = $derived(shown ? (whole ? shown.tail : shown.tail.slice(-SHOWN)) : []);
  const held = $derived(shown?.tail.length ?? 0);
  const more = $derived(held > SHOWN);
  const output = $derived(shown ? app.procOutput[shown.id] : undefined);
  const outputError = $derived(shown ? app.procErrors[shown.id] ?? "" : "");
</script>

<div class="panel-path">
  <div class="panel-crumbs">
    <span class="crumb is-last">{running} of {app.procs.length} running</span>
  </div>
  {#if shown && shown.status === "running"}
    <button
      class="icon-btn"
      aria-label="Stop this command"
      title="Stop"
      onclick={() => void stopProcess(shown.id)}
    >
      <Stop />
    </button>
  {/if}
</div>

<div class="artifact-body">
  {#if shown}
    <div class="min-h-0 flex-1 overflow-y-auto px-2.5 py-3 flex flex-col gap-2.5">
      <div class="proc-meta">
        <span class="proc-id">{shown.id}</span>
        <span class="text-[12px] text-muted">pid {shown.pid}</span>
        <span class="text-[12px] text-muted">{age(shown)}</span>
        <span class="text-[12px] text-muted">{bytes(shown.bytes)}</span>
        {#if more}
          <button class="proc-more" onclick={() => (whole = !whole)}>
            {whole ? "Show the last lines" : `${held - SHOWN} more lines`}
          </button>
        {/if}
        {#if shown.spill}
          <button class="proc-more ml-auto" onclick={() => void loadProcessOutput(shown.id)}>
            Whole output
          </button>
        {/if}
      </div>

      <div class="term">
        <div class="term-scroll">
          <div class="term-line term-prompt">$ {shown.command}</div>
          {#if tail.length > 0}
            <div class="term-line">{tail.join("\n")}</div>
          {:else}
            <div class="term-line term-muted">Nothing printed yet</div>
          {/if}
        </div>
        <div class="term-foot">
          {#if shown.status === "running"}
            Printing
          {:else}
            {ended(shown)} after {age(shown)}
          {/if}
        </div>
      </div>

      {#if outputError}
        <p class="text-[13px] text-crit">{outputError}</p>
      {:else if output}
        <pre class="mono-block">{output.output}</pre>
        {#if output.truncated}
          <span class="text-[11.5px] text-muted">The output is longer than the panel can hold</span>
        {/if}
      {/if}

      {#if shown.spill}
        <p class="text-[11.5px] text-muted">Saved at {shortPath(shown.spill)}</p>
      {/if}
    </div>
  {:else}
    <p class="px-2.5 pt-3 text-[13px] text-muted">
      Nothing is running in the background. A bash call with background set starts one.
    </p>
  {/if}
</div>
