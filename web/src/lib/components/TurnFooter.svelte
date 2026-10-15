<script lang="ts">
  import Clock from "phosphor-svelte/lib/Clock";
  import Database from "phosphor-svelte/lib/Database";
  import CopyButton from "./CopyButton.svelte";
  import Popover from "./Popover.svelte";
  import type { TurnCost } from "../api";
  import { duration, full, k, timeOf } from "../format";

  // The line under the reply that ends a turn: copy, what the turn spent, how
  // long it ran and when it ended.
  let { text, cost, at }: { text: string; cost: TurnCost; at?: number } = $props();

  let open = $state(false);

  // A turn stored before input was counted knows only its output.
  const input = $derived(cost.input);
  const total = $derived((input ?? 0) + cost.tokens);
  const hit = $derived(
    input !== undefined && input > 0 && cost.cached !== undefined ? (cost.cached / input) * 100 : undefined,
  );
  const who = $derived([cost.agent, cost.model].filter(Boolean).join(" / "));

  // The card's two figures name the calls missing from its totals: unrecorded
  // is every one of them, and stopped the ones the user cut short among them.
  const uncounted = $derived(cost.unrecorded ?? 0);
  const cut = $derived(cost.stopped ?? 0);
  const counted = $derived(cost.tokens > 0 || uncounted === 0);
  const calls = (n: number) => `${n} ${n === 1 ? "call" : "calls"}`;
  const note = $derived(
    uncounted === 0
      ? ""
      : cut === uncounted
        ? `${calls(uncounted)} stopped, not counted`
        : cut > 0
          ? `${calls(uncounted)} not counted, ${calls(cut)} of them stopped`
          : `${calls(uncounted)} with no usage report`,
  );
</script>

<div class="turn-foot">
  {#if text}
    <CopyButton {text} label="Copy reply" />
    <span class="foot-sep" aria-hidden="true"></span>
  {/if}
  <div class="relative">
    <button
      class="foot-chip"
      class:is-open={open}
      aria-expanded={open}
      onclick={() => (open = !open)}
    >
      <Database />
      <span class="num">{counted ? `${k(total)} tokens` : "No usage"}</span>
    </button>
    {#if open}
      <Popover onclose={() => (open = false)} up float width={300}>
        <div class="stat-card">
          <div class="stat-title">This turn</div>
          {#if who}
            <p class="stat-who" title={who}>{who}</p>
          {/if}
          {#if counted}
            <div class="stat-tiles">
              <div><span>Tokens</span><b class="num">{k(total)}</b></div>
              {#if hit !== undefined}
                <div><span>From cache</span><b class="num">{Math.round(hit)}%</b></div>
              {/if}
              <div><span>Generated</span><b class="num">{k(cost.tokens)}</b></div>
              {#if cost.tokPerS > 0}
                <div><span>Speed</span><b class="num">{Math.round(cost.tokPerS)}<small> tok/s</small></b></div>
              {/if}
            </div>
          {/if}
          {#if cost.requests > 0}
            <p class="stat-note">
              {cost.requests} model {cost.requests === 1 ? "call" : "calls"}{#if input !== undefined}, {full(input)} prompt tokens{/if}
            </p>
          {/if}
          {#if note}
            <p class="stat-note">{note}</p>
          {/if}
        </div>
      </Popover>
    {/if}
  </div>
  <span class="foot-chip is-static" title="Wall clock from the message to the answer, tool calls included">
    <Clock />
    <span class="num">Took {duration(cost.ms)}</span>
  </span>
  {#if at}
    <span class="num foot-time" title={new Date(at).toLocaleString()}>{timeOf(at)}</span>
  {/if}
</div>
