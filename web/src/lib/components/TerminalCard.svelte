<script lang="ts">
  import { app } from "../state/app.svelte";

  let {
    command,
    output,
    exitCode,
    truncated,
  }: {
    command: string;
    output: string;
    exitCode: number | null;
    truncated: boolean;
  } = $props();

  // What a command prints is read line by line, so a long run is cut and the
  // footer says how much is left rather than the card growing past the page.
  const CLAMP = 14;

  let whole = $state(false);

  const lines = $derived(output.replace(/\s+$/, "").split("\n"));
  const held = $derived(output.trim() === "" ? 0 : lines.length);
  const more = $derived(held > CLAMP);
  const shown = $derived(whole ? lines : lines.slice(0, CLAMP));

  const ending = $derived(exitCode === null ? "" : `Exit ${exitCode}`);
  const hidden = $derived(
    !more ? "" : whole ? "Show less" : `${held - CLAMP} more lines`,
  );
  const limit = $derived(app.config?.output_limit_bytes ?? 16384);
  const cut = $derived(
    truncated ? `Truncated to ${Math.round(limit / 1024)} kb, head and tail kept` : "",
  );
  const footer = $derived([ending, cut, hidden].filter(Boolean).join(" · "));
</script>

<div class="term">
  <div class="term-scroll">
    {#if command}
      <div class="term-line term-prompt">$ {command}</div>
    {/if}
    {#if held > 0}
      <div class="term-line" class:term-bad={exitCode !== null && exitCode !== 0}>
        {shown.join("\n")}
      </div>
    {/if}
  </div>

  {#if footer}
    {#if more}
      <button class="term-foot" onclick={() => (whole = !whole)}>{footer}</button>
    {:else}
      <div class="term-foot">{footer}</div>
    {/if}
  {/if}
</div>
